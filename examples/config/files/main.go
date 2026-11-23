// This example shows the ways a program reads files, and how they stack. The
// document layer is empty by default, so a program names every file it reads, in
// code, with the format it wants:
//
//   - json.File("config.json") reads one file,
//   - json.Dir("conf.d") reads every .json file in a directory, in name order,
//     as a conf.d directory is read, and
//   - json.FileFromEnv("SHOP_CONFIG") reads the file whose path is in a variable,
//     so whoever runs the program chooses it. The program writes the name of the
//     variable, the operator gives the value.
//
// Each later provider overrides the earlier ones, and the environment is above all
// the documents. Here the directory is read first, then the file the variable
// names, then the environment.
//
//	go run .
//
// The directory holds names that are not configuration (a swap file, a backup, a
// README): only names ending in .json are read, and hidden files and backups are
// never opened, so the swap file, which would not parse, is no problem. Anything
// that matches but can't be read is an error instead, such as a link that points
// nowhere, because the service would otherwise run on less than was put there.
//
// A file, or a directory, that the group or others can write is refused: whoever
// can write to it can change what the service does. The example writes its files
// with mode 0600 in a private directory. Where a deployment can't give the files
// tighter modes (a Kubernetes volume with a loose defaultMode), a program turns
// the check off for that provider, in code: json.Dir(path, config.WithoutPermissionCheck()).
package main

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// Config is what the "api" function loads.
type Config struct {
	Addr    string `config:"addr"`
	Workers int    `config:"workers"`
	Region  string `config:"region"`
	Debug   bool   `config:"debug"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// A private directory for the example's files. A real service has its own, such
	// as /etc/shop/conf.d, owned by root and not writable by anyone else.
	dir, err := os.MkdirTemp("", "shop-config-")
	if err != nil {
		return err
	}

	defer os.RemoveAll(dir)

	confd := filepath.Join(dir, "conf.d")
	if err := os.Mkdir(confd, 0o700); err != nil {
		return err
	}

	files := map[string]string{
		// Read, in this order: names are compared byte by byte.
		"00-base.json": `{"shop": {"api": {"addr": ":8080", "workers": 2, "region": "eu"}}}`,
		"10-site.json": `{"shop": {"api": {"workers": 4, "debug": true}}}`,

		// Not read: none of these is a .json file that is not hidden.
		".10-site.json.swp": "an editor's swap file, which would not parse",
		"99-draft.json~":    "a backup",
		"README":            "about this directory",
	}

	for name, data := range files {
		if err := os.WriteFile(filepath.Join(confd, name), []byte(data), 0o600); err != nil {
			return err
		}
	}

	own := filepath.Join(dir, "own.json")
	if err := os.WriteFile(own, []byte(`{"shop": {"api": {"region": "us", "debug": false}}}`), 0o600); err != nil {
		return err
	}

	// Whoever runs a service sets the variable. It is set here so that the example
	// needs nothing set up. FileFromEnv reads it once, when it is made, and an error
	// about the file says $SHOP_CONFIG, never the path it holds.
	if err := os.Setenv("SHOP_CONFIG", own); err != nil {
		return err
	}

	vars := map[string]string{"SHOP_API_ADDR": ":9090"}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{}

			f.Load(&cfg)

			// addr comes from the environment (the top), workers from 10-site.json,
			// region from the file SHOP_CONFIG names, and debug from that file too,
			// which overrides 10-site.json and may set a field to false.
			f.Log().Info("loaded",
				slog.String("addr", cfg.Addr),
				slog.Int("workers", cfg.Workers),
				slog.String("region", cfg.Region),
				slog.Bool("debug", cfg.Debug),
			)
		})
	},
		foundation.WithDocumentConfig(
			json.Dir(confd),
			json.FileFromEnv("SHOP_CONFIG"),
		),

		// A real service uses the process environment, which is the default; a map
		// keeps the example from depending on yours.
		foundation.SetEnvironmentConfig(env.New(env.WithMap(vars))),

		// Strict mode checks every file of the directory too, so a typo in any of
		// them is reported with its name.
		foundation.WithStrictConfig(),
	)

	return nil
}
