// This example prints where a service looks for each of its settings: the
// environment variables it reads and the keys in its config file. It runs
// nothing, reads nothing and needs no variables set, so it is safe to run in CI
// or to generate documentation from, for example to keep a .env.example in step
// with the struct (compare this output with the file, and fail if they differ).
//
// A field is named once, with a config tag: config:"cert_file" is the variable
// SHOP_API_TLS_CERT_FILE (the name in capitals) and the key cert_file in a file.
// A tag of a provider's own, env:"NAME" or json:"name", takes precedence for that
// provider alone, and env:",alias=PORT" adds options to the common name.
//
// The names depend on the path of the function that loads the config. The
// function below is called "api" inside "shop", so the path is shop > api; inside
// a running service, f.Path() gives it.
package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// Config is what the "api" function loads.
type Config struct {
	Addr string `config:"addr"`

	// A platform such as Cloud Run sets PORT, not SHOP_API_PORT: the field reads
	// both, and the one with the prefix wins.
	Port int `config:"port" env:",alias=PORT"`

	Timeout time.Duration `config:"timeout"`

	TLS struct {
		CertFile string `config:"cert_file"`
		KeyFile  string `config:"key_file"`
	} `config:"tls"`
}

func main() {
	path := []string{"shop", "api"}

	// Providers that can say where they look implement config.Describer. The
	// built in ones do; these are the same ones a service uses by default.
	for _, p := range []struct {
		name     string
		provider config.Describer
	}{
		{"environment variables", env.New()},
		{"config.json", json.File("config.json")},
	} {
		keys, err := p.provider.Describe(path, &Config{})
		if err != nil {
			// A mistake in the struct, such as a field of an unsupported type.
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		fmt.Println(p.name)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		for _, k := range keys {
			fmt.Fprintf(w, "  %s\t%s\n", k.Key, k.Field)
		}

		w.Flush()
		fmt.Println()
	}
}
