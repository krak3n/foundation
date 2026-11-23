package config_test

import (
	"errors"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
)

// The built in providers can say where they look for each field.
var (
	_ config.Describer = (*env.Provider)(nil)
	_ config.Describer = (*json.FileProvider)(nil)
	_ config.Describer = (*json.RawProvider)(nil)
)

func TestFieldKeyShape(t *testing.T) {
	t.Parallel()

	k := config.FieldKey{Field: "TLS.CertFile", Key: "SVC_TLS_CERT_FILE"}

	if k.Field != "TLS.CertFile" || k.Key != "SVC_TLS_CERT_FILE" {
		t.Fatalf("unexpected key: %+v", k)
	}
}

// Providers agree on which fields they would read, and in what order, each
// naming the key in its own format.
func TestProvidersAgreeOnDescribingFields(t *testing.T) {
	t.Parallel()

	type tls struct {
		CertFile string `env:"CERT_FILE" json:"cert_file"`
	}

	type shared struct {
		Region string `env:"REGION" json:"region"`
	}

	type cfg struct {
		shared

		Addr    string `env:"ADDR" json:"addr"`
		Skipped string `env:"-" json:"-"`
		Plain   string
		TLS     tls `env:"TLS" json:"tls"`
		Port    int `env:"PORT" json:"port"`
	}

	path := []string{"svc", "http.server"}

	wantFields := []string{"Region", "Addr", "TLS.CertFile", "Port"}

	tests := []struct {
		name     string
		provider config.Describer
		wantKeys []string
	}{
		{
			"env",
			env.New(),
			[]string{"SVC_HTTP_SERVER_REGION", "SVC_HTTP_SERVER_ADDR", "SVC_HTTP_SERVER_TLS_CERT_FILE", "SVC_HTTP_SERVER_PORT"},
		},
		{
			"json raw",
			json.Raw(nil),
			[]string{"svc.http.server.region", "svc.http.server.addr", "svc.http.server.tls.cert_file", "svc.http.server.port"},
		},
		{
			"json file",
			json.File("does-not-exist.json"),
			[]string{"svc.http.server.region", "svc.http.server.addr", "svc.http.server.tls.cert_file", "svc.http.server.port"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys, err := tt.provider.Describe(path, &cfg{})
			if err != nil {
				t.Fatal(err)
			}

			var fields, names []string

			for _, k := range keys {
				fields = append(fields, k.Field)
				names = append(names, k.Key)
			}

			if !slices.Equal(fields, wantFields) {
				t.Errorf("fields = %v, want %v", fields, wantFields)
			}

			if !slices.Equal(names, tt.wantKeys) {
				t.Errorf("keys = %v, want %v", names, tt.wantKeys)
			}
		})
	}
}

// What help prints is what the providers describe: every provider says, for each
// field, what it is called in the provider's own format and what the field is
// for (the help tag) and is (its type), the same way for all of them.
type helpFields struct {
	Addr    string `env:"ADDR" json:"addr" flag:"addr" help:"Listen address."`
	Port    int    `env:"PORT" json:"port" flag:"port,short=p" help:"Listen port."`
	Verbose bool   `env:"VERBOSE" json:"verbose" flag:"verbose"`
}

func helpDescribers() map[string]config.Describer {
	return map[string]config.Describer{
		"env":   env.New(env.WithMap(nil)),
		"json":  json.Raw([]byte(`{}`)),
		"file":  json.File("does-not-exist.json"),
		"flags": flags.New(flags.WithArgs(nil)),
	}
}

func TestEveryDescriberSaysWhatAFieldIsFor(t *testing.T) {
	t.Parallel()

	for name, d := range helpDescribers() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			keys, err := d.Describe([]string{"svc"}, &helpFields{})
			if err != nil {
				t.Fatal(err)
			}

			byField := map[string]config.FieldKey{}

			for _, k := range keys {
				if prev, ok := byField[k.Field]; ok && (prev.Help != k.Help || prev.Type != k.Type) {
					t.Errorf("%s: two keys of one field disagree: %+v and %+v", k.Field, prev, k)
				}

				byField[k.Field] = k
			}

			if got := byField["Addr"]; got.Help != "Listen address." || got.Type != "string" {
				t.Errorf("Addr = %+v", got)
			}

			if got := byField["Port"]; got.Help != "Listen port." || got.Type != "int" {
				t.Errorf("Port = %+v", got)
			}

			// A field with no help tag has no help, and a bool says so.
			if got := byField["Verbose"]; got.Help != "" || got.Type != "bool" {
				t.Errorf("Verbose = %+v", got)
			}
		})
	}
}

// Every key says which provider it is for, by the name its problems use, so help
// can label where a field is set without knowing the provider.
func TestEveryKeyNamesItsProvider(t *testing.T) {
	t.Parallel()

	want := map[string]string{"env": "env", "json": "json", "file": "json", "flags": "flags"}

	for name, d := range helpDescribers() {
		keys, err := d.Describe([]string{"svc"}, &helpFields{})
		if err != nil || len(keys) == 0 {
			t.Fatalf("%s: %v, %v", name, keys, err)
		}

		for _, k := range keys {
			if k.Provider != want[name] {
				t.Errorf("%s: key %+v, want provider %q", name, k, want[name])
			}
		}
	}
}

// The names of types, as someone reading --help would expect to see them.
func TestTypeNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  any
		want string
	}{
		{"string", &struct {
			F string `env:"F" json:"f" flag:"f"`
		}{}, "string"},
		{"int", &struct {
			F int `env:"F" json:"f" flag:"f"`
		}{}, "int"},
		{"sized int", &struct {
			F int64 `env:"F" json:"f" flag:"f"`
		}{}, "int64"},
		{"uint", &struct {
			F uint16 `env:"F" json:"f" flag:"f"`
		}{}, "uint16"},
		{"float", &struct {
			F float64 `env:"F" json:"f" flag:"f"`
		}{}, "float64"},
		{"bool", &struct {
			F bool `env:"F" json:"f" flag:"f"`
		}{}, "bool"},
		{"duration", &struct {
			F time.Duration `env:"F" json:"f" flag:"f"`
		}{}, "duration"},
		{"url", &struct {
			F url.URL `env:"F" json:"f" flag:"f"`
		}{}, "url"},
		{"pointer to url", &struct {
			F *url.URL `env:"F" json:"f" flag:"f"`
		}{}, "url"},
		{"pointer", &struct {
			F *int `env:"F" json:"f" flag:"f"`
		}{}, "int"},
		{"list", &struct {
			F []string `env:"F" json:"f" flag:"f"`
		}{}, "list of string"},
		{"list of durations", &struct {
			F []time.Duration `env:"F" json:"f" flag:"f"`
		}{}, "list of duration"},
		{"map", &struct {
			F map[string]int `env:"F" json:"f" flag:"f"`
		}{}, "map of string to int"},
		{"text type", &struct {
			F net.IP `env:"F" json:"f" flag:"f"`
		}{}, "IP"},
		{"list of text types", &struct {
			F []net.IP `env:"F" json:"f" flag:"f"`
		}{}, "list of IP"},
	}

	for _, tt := range tests {
		for name, d := range helpDescribers() {
			keys, err := d.Describe([]string{"svc"}, tt.dst)
			if err != nil || len(keys) == 0 {
				t.Errorf("%s/%s: %v, %v", tt.name, name, keys, err)

				continue
			}

			for _, k := range keys {
				if k.Type != tt.want {
					t.Errorf("%s/%s: Type = %q, want %q", tt.name, name, k.Type, tt.want)
				}
			}
		}
	}
}

// Help text is data the provider hands over as it is: escaping is for whoever
// prints it, which can't be done earlier without making it wrong for others.
func TestHelpTextIsHandedOverAsItIs(t *testing.T) {
	t.Parallel()

	type cfg struct {
		F string `env:"F" json:"f" flag:"f" help:"a \x1b[31mb\nc"`
	}

	for name, d := range helpDescribers() {
		keys, err := d.Describe([]string{"svc"}, &cfg{})
		if err != nil || len(keys) == 0 {
			t.Fatalf("%s: %v, %v", name, keys, err)
		}

		if keys[0].Help != "a \x1b[31mb\nc" {
			t.Errorf("%s: Help = %q", name, keys[0].Help)
		}
	}
}

// A help tag is bounded, so a mistake or a generated tag can't fill a screen, and
// the field that has it is a problem like any other, with the rest described.
func TestAHelpTagIsBounded(t *testing.T) {
	t.Parallel()

	const max = 1024

	for name, d := range helpDescribers() {
		for _, tt := range []struct {
			n    int
			fail bool
		}{{max, false}, {max + 1, true}} {
			// A tag is a literal, so the struct is built with reflect for each size.
			dst := bigHelp(tt.n)

			keys, err := d.Describe([]string{"svc"}, dst)

			var fe *config.FieldError

			if tt.fail {
				if !errors.As(err, &fe) || fe.Field != "Big" {
					t.Errorf("%s: %d bytes: got %v, want a problem with Big", name, tt.n, err)
				}

				if err != nil && strings.Contains(err.Error(), strings.Repeat("x", 100)) {
					t.Errorf("%s: the problem repeats the help text", name)
				}

				if len(keys) == 0 {
					t.Errorf("%s: the other fields should still be described", name)
				}

				continue
			}

			if err != nil {
				t.Errorf("%s: %d bytes: %v", name, tt.n, err)
			}
		}
	}
}

// bigHelp is a pointer to a struct with a fine field and one whose help tag is n
// bytes long.
func bigHelp(n int) any {
	tag := func(name, help string) reflect.StructTag {
		return reflect.StructTag(`env:"` + strings.ToUpper(name) + `" json:"` + name + `" flag:"` + name + `" help:"` + help + `"`)
	}

	typ := reflect.StructOf([]reflect.StructField{
		{Name: "Fine", Type: reflect.TypeOf(""), Tag: tag("fine", "ok")},
		{Name: "Big", Type: reflect.TypeOf(""), Tag: tag("big", strings.Repeat("x", n))},
	})

	return reflect.New(typ).Interface()
}

// Describing never changes what it describes and never reads anything.
func TestDescribingLeavesTheStructAlone(t *testing.T) {
	t.Parallel()

	for name, d := range helpDescribers() {
		got := helpFields{Addr: "default", Port: 7}

		if _, err := d.Describe([]string{"svc"}, &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if got != (helpFields{Addr: "default", Port: 7}) {
			t.Errorf("%s: Describe changed the struct: %+v", name, got)
		}
	}
}
