// This example shows a service reading a secret from a file instead of from an
// environment variable, which is how Docker, Compose and Kubernetes hand secrets
// to a container: the secret is mounted as a file, such as
// /run/secrets/db_password, and a variable names it.
//
// A field opts in with the file option in its tag:
//
//	Password string `env:"PASSWORD,file"`
//
// The tag is the environment's own, env and not config, on purpose: a config
// name would let a file hold the secret too. Whether a secret may be in a file is
// for the author to decide, and the default is no.
//
// With the path shop > api, SHOP_API_PASSWORD still works, and so does
// SHOP_API_PASSWORD_FILE=/run/secrets/db_password. Setting both is an error, and
// the file's trailing newline is removed. The secret is kept out of the process
// environment, so it doesn't show in docker inspect or /proc/<pid>/environ.
//
// Try pointing SHOP_API_PASSWORD_FILE at a file that doesn't exist, or setting
// SHOP_API_PASSWORD as well, in the code below.
package main

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
)

// Config is loaded by the "api" function below.
type Config struct {
	Addr string `env:"ADDR"`

	// Password has no sensible default and is a secret, so it must be given, and
	// may come from a file.
	Password string `env:"PASSWORD,file" config:"required"`
}

func main() {
	// A real service gets this file from its platform. Here one is written, the
	// way a mounted secret would look: a single line with a trailing newline.
	dir, err := os.MkdirTemp("", "secrets")
	if err != nil {
		log.Fatal(err)
	}

	defer os.RemoveAll(dir)

	file := filepath.Join(dir, "db_password")

	if err := os.WriteFile(file, []byte("correct horse battery staple\n"), 0o400); err != nil {
		log.Fatal(err)
	}

	vars := map[string]string{
		"SHOP_API_ADDR":          ":8080",
		"SHOP_API_PASSWORD_FILE": file,
	}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{}

			f.Load(&cfg)

			// Never log a secret: say that it is there, not what it is.
			f.Log().Info("configuration loaded",
				slog.String("addr", cfg.Addr),
				slog.Int("password_length", len(cfg.Password)),
			)
		})
	}, foundation.WithConfigProviders(env.New(env.WithMap(vars))))
}
