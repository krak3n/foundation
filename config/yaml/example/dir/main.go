// This example reads a directory of YAML files, the way a conf.d directory is
// read, and a file chosen by the operator through a variable, above it.
//
//	cd config/yaml/example/dir
//	go run .
//
// yaml.Dir reads the files ending in .yaml or .yml in the order of their names,
// byte by byte, so 00-base.yaml comes before 10-site.yml, and each overrides the
// ones before it. Hidden files, backups (name~, #name#), other extensions and
// subdirectories are never opened, so an editor's swap file or a README is no
// problem; but a name that matches and can't be read, such as a link that points
// nowhere, is an error, as is more than 256 files or 32 MiB, and then nothing is
// read from the directory. yaml.FileFromEnv reads the file whose path is in a
// variable the program names; the operator gives the value, and errors say
// $SHOP_CONFIG, never the path.
//
// Like every file provider, they refuse a file or a directory that the group or
// others can write, unless the program says config.WithoutPermissionCheck() in code.
// The example writes its files with mode 0600 in a private directory.
//
// The documents are the subset of YAML that says what it means: anchors, aliases,
// merge keys, tags and more than one document in a file are errors. See the package
// documentation.
package main

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/yaml"
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
		"00-base.yaml": "shop:\n  api:\n    addr: \":8080\"\n    workers: 2\n    region: eu\n",
		"10-site.yml":  "shop:\n  api:\n    workers: 4\n    debug: true\n",

		// Not read: none of these is a .yaml or .yml file that is not hidden.
		".10-site.yml.swp": "an editor's swap file",
		"99-draft.yaml~":   "a backup",
		"README":           "about this directory",
	}

	for name, data := range files {
		if err := os.WriteFile(filepath.Join(confd, name), []byte(data), 0o600); err != nil {
			return err
		}
	}

	own := filepath.Join(dir, "own.yaml")
	if err := os.WriteFile(own, []byte("shop:\n  api:\n    region: us\n    debug: false\n"), 0o600); err != nil {
		return err
	}

	// Whoever runs a service sets the variable. It is set here so that the example
	// needs nothing set up. It is read once, when the provider is made.
	if err := os.Setenv("SHOP_CONFIG", own); err != nil {
		return err
	}

	vars := map[string]string{"SHOP_API_ADDR": ":9090"}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{}

			f.Load(&cfg)

			// addr comes from the environment (the top), workers from 10-site.yml,
			// region from the file SHOP_CONFIG names, and debug from that file too,
			// which overrides 10-site.yml and may set a field to false.
			f.Log().Info("loaded",
				slog.String("addr", cfg.Addr),
				slog.Int("workers", cfg.Workers),
				slog.String("region", cfg.Region),
				slog.Bool("debug", cfg.Debug),
			)
		})
	},
		foundation.WithDocumentConfig(
			yaml.Dir(confd),
			yaml.FileFromEnv("SHOP_CONFIG"),
		),

		// A real service uses the process environment, which is the default; a map
		// keeps the example from depending on yours.
		foundation.SetEnvironmentConfig(env.New(env.WithMap(vars))),

		// Strict mode checks every file of the directory, so a typo in any of them is
		// reported with its name.
		foundation.WithStrictConfig(),
	)

	return nil
}
