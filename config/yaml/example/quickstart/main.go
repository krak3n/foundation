package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
)

// Config is what the "api" function loads. A field is named once, in its config tag,
// and that is the key in the file.
type Config struct {
	Addr    string        `config:"addr"`
	Timeout time.Duration `config:"timeout"`
}

func main() {
	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			// These are the defaults: the file changes them, and the environment
			// (SHOP_API_ADDR) wins over the file.
			cfg := Config{Addr: "127.0.0.1:8080", Timeout: 30 * time.Second}

			f.Load(&cfg)

			f.Log().Info("loaded", slog.String("addr", cfg.Addr), slog.Duration("timeout", cfg.Timeout))
		})
		// The operator chooses the file by naming it in SHOP_CONFIG, and it must exist.
	}, foundation.WithDocumentConfig(yaml.FileFromEnv("SHOP_CONFIG", config.WithRequired())))
}
