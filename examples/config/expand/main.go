// This example shows a configuration file that is a template. The file is
// checked in, but some of what it needs only exists at deploy time, under names
// the platform chooses: Kubernetes secrets, the postgres image's variables, the
// PORT that Heroku and Cloud Run set. A field tagged config:"expand" has
// ${NAME} references in its value replaced from the environment:
//
//	"dsn":  "postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST:-localhost}:5432/shop"
//	"addr": ":${PORT:-8080}"
//
// ${NAME:-default} uses the default if NAME is unset or empty, and $$ is a
// literal $. A variable that is unset with no default is an error, not an empty
// string, so a missing password can't slip through as a blank one.
//
// Only fields with the tag are templates, so a password that contains a $ is
// never touched, and only values from providers are expanded, not defaults.
//
// Values are substituted exactly as they are, so a password with characters that
// mean something in a URL (@ / : ?) has to be escaped by whoever provides it. And
// the DSN now holds a secret: never log it as it is, and note that url.Parse's
// own error quotes the whole string, so this example doesn't pass it on.
//
// Try removing DB_PASSWORD from the variables below.
package main

import (
	"errors"
	"log/slog"
	"net/url"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/json"
)

// Config is loaded by the "api" function below.
type Config struct {
	DSN  string `json:"dsn" config:"required,expand"`
	Addr string `json:"addr" config:"expand"`
}

func main() {
	// A real service would use the default providers, which read the environment and any file
	// named with WithDocumentConfig.
	file := json.Raw([]byte(`{
		"shop": {
			"api": {
				"dsn": "postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST:-localhost}:5432/shop",
				"addr": ":${PORT:-8080}"
			}
		}
	}`))

	// What the platform provides. A real service reads the process environment,
	// which is the default; this is injected so the example needs nothing set.
	vars := map[string]string{
		"DB_USER":     "app",
		"DB_PASSWORD": "s3cr3t-pw",
		"PORT":        "9000",
	}

	lookup := func(name string) (string, bool) {
		v, ok := vars[name]

		return v, ok
	}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			var cfg Config

			f.Load(&cfg)

			// The DSN now holds the password, so don't log it as it is.
			// url.URL.Redacted hides it.
			dsn, err := url.Parse(cfg.DSN)
			if err != nil {
				// Not err itself: it would quote the DSN, password included.
				f.Fail(errors.New("the dsn is not a valid URL"))
			}

			f.Log().Info("configuration loaded", slog.String("dsn", dsn.Redacted()), slog.String("addr", cfg.Addr))
		})
	}, foundation.WithConfigProviders(file), foundation.WithLookupEnv(lookup))
}
