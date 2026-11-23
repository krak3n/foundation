// This example shows command line flags: reading configuration from arguments,
// scoping them to the function they are for, and what the framework does with the
// ones nothing read.
//
//	go run ./examples/config/flags --verbose
//	go run ./examples/config/flags --log-level debug --api --addr :9090 -w 8
//	go run ./examples/config/flags --api --timeout 2s --tags a,b --labels env:prod,team:shop
//	go run ./examples/config/flags --api --db-host db.internal --db-port 5433
//	go run ./examples/config/flags --http --server --addr 127.0.0.1:9000 --idle-timeout 30s
//	go run ./examples/config/flags --worker --queue --depth 50
//	go run ./examples/config/flags --help
//	go run ./examples/config/flags --api --help
//	go run ./examples/config/flags --http --server --help
//	go run ./examples/config/flags --health --addr 127.0.0.1:9001 --verbose
//	go run ./examples/config/flags --health --addr 127.0.0.1:9001 --http --server --addr 127.0.0.1:9000
//	go run ./examples/config/flags --health --help
//
// The HTTP server is the real one from transport/http, so it listens and runs
// until Ctrl-C. Put --dry-run first to check the arguments and start nothing:
//
//	go run ./examples/config/flags --dry-run --http --server --addr 127.0.0.1:9000
//
// And the mistakes, each of which fails the run (exit status 1) instead of being
// ignored:
//
//	go run ./examples/config/flags --api --adr :9090        # a misspelt flag
//	go run ./examples/config/flags --addr :9090             # api's flag before --api
//	go run ./examples/config/flags --api -w many            # not a number
//	go run ./examples/config/flags --api --workers          # no value
//	go run ./examples/config/flags --api --db               # --db is not a flag, --db-host is
//	go run ./examples/config/flags stray                    # a word nothing reads
//
// The rules, in short:
//
//   - A field opts in with a flag tag: flag:"name" or flag:"name,short=p". Without
//     one it can't be set from the command line.
//   - --name=value and --name value are the same, as are -p=8080 and -p 8080. A bool
//     is bare (--verbose) or --verbose=false, and never takes the next argument.
//     A value that starts with a dash needs the equals sign (--offset=-1).
//   - There are no clusters (-vw), attached values (-p8080), "--", abbreviations
//     or case folding. An argument is a flag exactly as written, or an error.
//   - The root function's flags come first. A function's flags follow its marker:
//     its name as a flag. "api" is --api, and "http.server" is --http --server, so
//     the same words work as variables (SHOP_HTTP_SERVER_PORT) and in a file
//     (shop.http.server.port).
//   - Flags win over the environment, which wins over the file, because the
//     providers are applied in that order.
//   - Every argument has to be read by something. A mistake is found when startup
//     ends, so a service that starts things earlier has already started them: run
//     --dry-run first (see examples/dry-run).
//
// A secret should never be a flag: arguments are visible to every user of the
// machine (ps, /proc) and end up in shell history. A field that reads a secret
// from a file (env:"TOKEN,file") is refused as a flag.
package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/interrupt"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// RootConfig is the root function's, so its flags have no marker and come first.
type RootConfig struct {
	Verbose  bool   `flag:"verbose,short=v" help:"Log at debug level."`
	LogLevel string `config:"log_level" flag:"log-level" help:"One of debug, info, warn, error."`
}

// DB is a group: tagged on the field that holds it, so its names are joined
// with a hyphen (--db-host).
type DB struct {
	Host string `config:"host" flag:"host" help:"Database host."`
	Port int    `config:"port" flag:"port" help:"Database port."`
}

// Limits is embedded with no tag, so its fields are the parent's: --max-body.
type Limits struct {
	MaxBody int64 `flag:"max-body" help:"Largest request body, in bytes."`
}

// APIConfig is loaded by the "api" function. Types are the same as for variables
// and files: strings, bools, numbers, durations, URLs, lists and maps.
type APIConfig struct {
	Limits

	Addr     string            `config:"addr" flag:"addr" help:"Address to listen on."`
	Workers  int               `config:"workers" flag:"workers,short=w" help:"Requests handled at once."`
	Timeout  time.Duration     `config:"timeout" flag:"timeout" help:"Time allowed for a request."`
	Upstream url.URL           `flag:"upstream" help:"Where to forward requests."`
	Tags     []string          `flag:"tags" help:"Comma separated."`
	Labels   map[string]string `flag:"labels" help:"Comma separated key:value pairs."`
	Debug    bool              `flag:"debug" help:"Extra logging in the API."`
	DB       DB                `flag:"db"`

	// Not a flag, whatever else it is: the tag says so.
	Internal string `flag:"-"`

	// No flag tag, so only the environment and the file can set it.
	Region string `config:"region"`
}

// QueueConfig is loaded by "queue", a child of "worker": --worker --queue.
type QueueConfig struct {
	Depth int `flag:"depth" help:"How many jobs may wait."`
}

func main() {
	args := os.Args[1:]

	// A real service would use the defaults, which with WithArgs are the environment
	// and then flags, plus any file named with WithDocumentConfig. The providers are
	// spelled out here, with some values in the file and the environment, so that
	// the example needs nothing set up and shows the order: for the API's workers the file says 2,
	// the environment 4, and a flag has the last word.
	file := json.Raw([]byte(`{"shop": {"api": {"workers": 2, "region": "eu", "addr": ":8080"}, "http": {"server": {"addr": "127.0.0.1:8080"}}, "health": {"addr": "127.0.0.1:3417"}}}`))
	vars := env.New(env.WithMap(map[string]string{"SHOP_API_WORKERS": "4"}))

	foundation.Run("shop", func(f *foundation.F) {
		f.Help("Shows how configuration is read from the command line.")

		interrupt.Signals(f)

		// The health server takes an HTTP server's settings as its own: --health
		// --addr, SHOP_HEALTH_ADDR, shop.health.addr. Its default (:3417) is set before
		// configuration is loaded, so the file, a variable or a flag can change it.
		health.Server(f)

		var root RootConfig

		f.Load(&root)

		if root.Verbose {
			slog.SetLogLoggerLevel(slog.LevelDebug)
		}

		f.Log().Info("root", slog.Bool("verbose", root.Verbose), slog.String("log_level", root.LogLevel))

		f.Run("api", func(f *foundation.F) {
			f.Help("Runs the API.\nEvery type that a variable can be, a flag can be too.")

			cfg := APIConfig{Workers: 1, Timeout: 30 * time.Second, Addr: ":80"}

			f.Load(&cfg)

			// Log the fields you need, not the whole struct, as a struct that grows a
			// secret one day would be logged with it.
			f.Log().Info("api",
				slog.String("addr", cfg.Addr),
				slog.Int("workers", cfg.Workers),
				slog.Duration("timeout", cfg.Timeout),
				slog.String("upstream", cfg.Upstream.String()),
				slog.Any("tags", cfg.Tags),
				slog.Any("labels", cfg.Labels),
				slog.String("db", cfg.DB.Host),
				slog.Int("db_port", cfg.DB.Port),
				slog.Int64("max_body", cfg.MaxBody),
				slog.String("region", cfg.Region),
			)
		})

		// The real server. Its function is "http.server", a name with a dot, so its
		// flags follow two words: --http --server --addr. They are tags on its own
		// config, and --http --server --help lists them. The address comes from the
		// file above unless a flag or variable says otherwise.
		transporthttp.Server(f, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("shop\n"))
		}))

		// A function can have functions of its own, and their markers follow its
		// own: --worker --queue --depth 50. A parent's flags come before its child's.
		f.Run("worker", func(f *foundation.F) {
			f.Help("Runs background jobs.")

			f.Run("queue", func(f *foundation.F) {
				f.Help("The job queue.")

				cfg := QueueConfig{Depth: 10}

				f.Load(&cfg)

				f.Log().Info("worker queue", slog.Int("depth", cfg.Depth))
			})
		})
	},
		// Naming the flags provider is what puts flags in the list when providers
		// are chosen. foundation.WithArgs(args) does the same for the defaults.
		foundation.WithConfigProviders(file, vars, flags.New(flags.WithArgs(args))),
	)
}
