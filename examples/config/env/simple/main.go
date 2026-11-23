package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
)

type BazConfig struct {
	String string  `env:"STRING"`
	Num    int     `env:"NUM"`
	Bool   bool    `env:"BOOL"`
	Float  float64 `env:"FLOAT"`
}

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	os.Setenv("FOO_BAZ_STRING", "foobar")
	os.Setenv("FOO_BAZ_NUM", "12")
	os.Setenv("FOO_BAZ_BOOL", "true")
	os.Setenv("FOO_BAZ_FLOAT", "1.43")

	foundation.Run("foo", func(f *foundation.F) {
		f.Run("baz", func(f *foundation.F) {
			var cfg BazConfig

			f.Load(&cfg, env.New())

			f.Log().Debug("baz config:", slog.Any("values", cfg))
		})
	})
}
