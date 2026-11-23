// This example loads configuration from a YAML file, then from the environment,
// which wins over the file.
//
//	cd config/yaml/example
//	go run .                          # reads config.yaml
//	SHOP_API_ADDR=:7070 go run .      # the environment wins
//
// The YAML provider is a module of its own, go.krak3n.dev/foundation/config/yaml,
// so that the core has no dependencies: a program that wants YAML names it with
// foundation.WithDocumentConfig, which adds it to the document layer (empty by
// default), and one that doesn't never imports it. The environment and flags
// layers are untouched, so the environment still wins over the file. To let the
// operator choose the file, use yaml.FileFromEnv("SHOP_CONFIG").
//
// A field is named once, with a config tag, and the file's key is that name. The
// file is read as a subset of YAML that says what it means: anchors, aliases,
// merge keys, tags and more than one document are errors, a key that appears
// twice is an error, and every scalar is the text as written (see the package
// documentation). WithStrictConfig turns a key nothing reads, such as a typo, into
// an error.
package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/yaml"
)

// Config is what the "api" function loads.
type Config struct {
	Addr    string        `config:"addr"`
	Timeout time.Duration `config:"timeout"`
	Debug   bool          `config:"debug"`
	Hosts   []string      `config:"hosts"`
	Region  string        `config:"region"`

	TLS struct {
		CertFile string `config:"cert_file"`
	} `config:"tls"`
}

func main() {
	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{Addr: ":8080", Timeout: 30 * time.Second}

			f.Load(&cfg)

			f.Log().Info("loaded",
				slog.String("addr", cfg.Addr),
				slog.Duration("timeout", cfg.Timeout),
				slog.Bool("debug", cfg.Debug),
				slog.Any("hosts", cfg.Hosts),
				slog.String("region", cfg.Region),
				slog.String("cert_file", cfg.TLS.CertFile),
			)
		})
	},
		foundation.WithDocumentConfig(yaml.File("config.yaml")),
		foundation.WithStrictConfig(),
	)
}
