package config_test

import (
	"errors"
	"net"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
)

// lookupMap is an env lookup that reads from a map instead of the process
// environment, so tests can run in parallel.
func lookupMap(m map[string]string) env.Option {
	return env.WithLookup(func(key string) (string, bool) {
		v, ok := m[key]

		return v, ok
	})
}

type tlsConfig struct {
	CertFile string `env:"CERT_FILE" json:"cert_file"`
}

type conformanceConfig struct {
	Addr string    `env:"ADDR" json:"addr"`
	TLS  tlsConfig `env:"TLS" json:"tls"`
}

// Every provider must find the same value for the same function path. Each case
// gives the path and, written out by hand, where an equivalent value lives in
// each provider's format, so this also pins the rule itself: node names are
// split into words on anything that isn't a letter, digit or underscore, which
// the env provider joins with underscores and the JSON provider nests.
//
// A new provider (YAML, TOML) should be added here.
func TestProvidersAgreeOnWhereToLook(t *testing.T) {
	tests := []struct {
		name string
		path []string
		env  map[string]string
		json string
	}{
		{
			name: "single segment",
			path: []string{"svc"},
			env:  map[string]string{"SVC_ADDR": ":1", "SVC_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}`,
		},
		{
			name: "nested segments",
			path: []string{"svc", "http", "server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
		},
		{
			name: "dot in a name",
			path: []string{"svc", "http.server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
		},
		{
			name: "slash in a name",
			path: []string{"svc", "http/server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1"},
			json: `{"svc": {"http": {"server": {"addr": ":1"}}}}`,
		},
		{
			name: "hyphen in a name",
			path: []string{"svc", "http-server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1"},
			json: `{"svc": {"http": {"server": {"addr": ":1"}}}}`,
		},
		{
			name: "dots in several names",
			path: []string{"a.b", "c.d"},
			env:  map[string]string{"A_B_C_D_ADDR": ":1"},
			json: `{"a": {"b": {"c": {"d": {"addr": ":1"}}}}}`,
		},
		{
			name: "mixed case",
			path: []string{"Svc", "HTTP.Server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1"},
			json: `{"svc": {"http": {"server": {"addr": ":1"}}}}`,
		},
		{
			name: "digits",
			path: []string{"svc", "v2.api"},
			env:  map[string]string{"SVC_V2_API_ADDR": ":1"},
			json: `{"svc": {"v2": {"api": {"addr": ":1"}}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			providers := map[string]config.Provider{
				"env":  env.New(lookupMap(tt.env)),
				"json": json.Raw([]byte(tt.json)),
			}

			results := make(map[string]conformanceConfig)

			for name, p := range providers {
				var cfg conformanceConfig

				if err := p.Unmarshal(tt.path, &cfg); err != nil {
					t.Fatalf("%s: %v", name, err)
				}

				results[name] = cfg
			}

			if results["env"].Addr != ":1" {
				t.Fatalf("env provider didn't find the value for %q: %+v", tt.path, results["env"])
			}

			if results["env"] != results["json"] {
				t.Fatalf("providers disagree for path %q\n env: %+v\njson: %+v", tt.path, results["env"], results["json"])
			}
		})
	}
}

type sharedConfig struct {
	Region string `env:"REGION" json:"region"`
}

type typesConfig struct {
	sharedConfig

	Port     uint16            `env:"PORT" json:"port"`
	Count    int64             `env:"COUNT" json:"count"`
	Ratio    float64           `env:"RATIO" json:"ratio"`
	Debug    bool              `env:"DEBUG" json:"debug"`
	Timeout  time.Duration     `env:"TIMEOUT" json:"timeout"`
	When     time.Time         `env:"WHEN" json:"when"`
	IP       net.IP            `env:"IP" json:"ip"`
	Home     url.URL           `env:"HOME" json:"home"`
	Retries  *int              `env:"RETRIES" json:"retries"`
	Tags     []string          `env:"TAGS" json:"tags"`
	Weights  []int             `env:"WEIGHTS" json:"weights"`
	Limits   map[string]int    `env:"LIMITS" json:"limits"`
	Labels   map[string]string `env:"LABELS" json:"labels"`
	Unset    *string           `env:"UNSET" json:"unset"`
	Defaults string            `env:"DEFAULTS" json:"defaults"`
}

// The same logical value must come out the same whichever provider supplies it,
// for every supported type. Each provider's own format is written out by hand.
func TestProvidersAgreeOnTypes(t *testing.T) {
	path := []string{"svc"}

	vars := map[string]string{
		"SVC_REGION":  "eu",
		"SVC_PORT":    "8080",
		"SVC_COUNT":   "9007199254740993",
		"SVC_RATIO":   "0.25",
		"SVC_DEBUG":   "true",
		"SVC_TIMEOUT": "1m30s",
		"SVC_WHEN":    "2026-01-02T03:04:05Z",
		"SVC_IP":      "10.0.0.1",
		"SVC_HOME":    "https://example.com:8443/home",
		"SVC_RETRIES": "3",
		"SVC_TAGS":    "a,b,c",
		"SVC_WEIGHTS": "1,2,3",
		"SVC_LIMITS":  "cpu:2,mem:512",
		"SVC_LABELS":  "team:core,tier:backend",
	}

	t.Parallel()

	data := `{"svc": {
		"region": "eu", "port": 8080, "count": 9007199254740993, "ratio": 0.25, "debug": true,
		"timeout": "1m30s", "when": "2026-01-02T03:04:05Z", "ip": "10.0.0.1",
		"home": "https://example.com:8443/home", "retries": 3,
		"tags": ["a", "b", "c"], "weights": [1, 2, 3],
		"limits": {"cpu": 2, "mem": 512}, "labels": {"team": "core", "tier": "backend"}
	}}`

	fromEnv := typesConfig{Defaults: "kept"}
	fromJSON := typesConfig{Defaults: "kept"}

	if err := env.New(lookupMap(vars)).Unmarshal(path, &fromEnv); err != nil {
		t.Fatalf("env: %v", err)
	}

	if err := json.Raw([]byte(data)).Unmarshal(path, &fromJSON); err != nil {
		t.Fatalf("json: %v", err)
	}

	// Guard against both being empty.
	if fromEnv.Port != 8080 || fromEnv.Count != 9007199254740993 || len(fromEnv.Limits) != 2 || fromEnv.Retries == nil {
		t.Fatalf("env provider didn't load the values: %+v", fromEnv)
	}

	if !reflect.DeepEqual(fromEnv, fromJSON) {
		t.Fatalf("providers disagree\n env: %+v\njson: %+v", fromEnv, fromJSON)
	}
}

// Values that don't fit the field are rejected by every provider, never
// silently wrapped or truncated.
func TestProvidersAgreeOnRejectingBadValues(t *testing.T) {
	type cfg struct {
		Small int8      `env:"SMALL" json:"small"`
		Byte  uint8     `env:"BYTE" json:"byte"`
		When  time.Time `env:"WHEN" json:"when"`
		Nums  []int     `env:"NUMS" json:"nums"`
	}

	tests := []struct {
		name string
		env  map[string]string
		json string
	}{
		{"signed overflow", map[string]string{"SVC_SMALL": "300"}, `{"svc": {"small": 300}}`},
		{"negative unsigned", map[string]string{"SVC_BYTE": "-1"}, `{"svc": {"byte": -1}}`},
		{"unsigned overflow", map[string]string{"SVC_BYTE": "256"}, `{"svc": {"byte": 256}}`},
		{"bad time", map[string]string{"SVC_WHEN": "yesterday"}, `{"svc": {"when": "yesterday"}}`},
		{"bad slice item", map[string]string{"SVC_NUMS": "1,x"}, `{"svc": {"nums": [1, "x"]}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := env.New(lookupMap(tt.env)).Unmarshal([]string{"svc"}, &cfg{}); err == nil {
				t.Error("env: expected an error")
			}

			if err := json.Raw([]byte(tt.json)).Unmarshal([]string{"svc"}, &cfg{}); err == nil {
				t.Error("json: expected an error")
			}
		})
	}
}

// fieldsThatFailed lists the Go field paths named in err, in order.
func fieldsThatFailed(err error) []string {
	var out []string

	var walk func(error)

	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range j.Unwrap() {
				walk(c)
			}

			return
		}

		var fe *config.FieldError

		if errors.As(e, &fe) {
			out = append(out, fe.Field)
		}
	}

	walk(err)

	return out
}

// Every provider reports every problem, naming the same fields in the same
// order, so a failure looks the same whichever format the bad value was in.
func TestProvidersAgreeOnWhichFieldsFail(t *testing.T) {
	t.Parallel()

	type tls struct {
		Port int `env:"PORT" json:"port"`
	}

	type cfg struct {
		sharedConfig

		Name    string        `env:"NAME" json:"name"`
		Port    uint8         `env:"PORT" json:"port"`
		Timeout time.Duration `env:"TIMEOUT" json:"timeout"`
		When    time.Time     `env:"WHEN" json:"when"`
		Tags    []int         `env:"TAGS" json:"tags"`
		TLS     tls           `env:"TLS" json:"tls"`
		Ok      int           `env:"OK" json:"ok"`
	}

	path := []string{"svc"}

	envErr := env.New(lookupMap(map[string]string{
		"SVC_NAME": "fine", "SVC_PORT": "300", "SVC_TIMEOUT": "soon", "SVC_WHEN": "yesterday",
		"SVC_TAGS": "1,x", "SVC_TLS_PORT": "x", "SVC_OK": "1",
	})).Unmarshal(path, &cfg{})

	jsonErr := json.Raw([]byte(`{"svc": {
		"name": "fine", "port": 300, "timeout": "soon", "when": "yesterday",
		"tags": [1, "x"], "tls": {"port": "x"}, "ok": 1
	}}`)).Unmarshal(path, &cfg{})

	want := []string{"Port", "Timeout", "When", "Tags", "TLS.Port"}

	if got := fieldsThatFailed(envErr); !slices.Equal(got, want) {
		t.Fatalf("env reported %v, want %v", got, want)
	}

	if got := fieldsThatFailed(jsonErr); !slices.Equal(got, want) {
		t.Fatalf("json reported %v, want %v", got, want)
	}
}

// keysOfUnknown lists the keys named in err, in the order reported.
func keysOfUnknown(err error) []string {
	var out []string

	var walk func(error)

	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range j.Unwrap() {
				walk(c)
			}

			return
		}

		var fe *config.FieldError

		if errors.As(e, &fe) && errors.Is(fe, config.ErrUnknown) {
			out = append(out, fe.Key)
		}
	}

	walk(err)

	return out
}

// Strict mode catches the same mistakes whichever format they are made in. Each
// provider names the key as it would be written in its own format. (A whole
// object nothing asked for is one problem in a document but one per variable in
// the environment, so these cases are single keys.)
func TestProvidersAgreeOnUnknownKeys(t *testing.T) {
	t.Parallel()

	type tls struct {
		CertFile string `env:"CERT_FILE" json:"cert_file"`
	}

	type cfg struct {
		Addr string `env:"ADDR" json:"addr"`
		TLS  tls    `env:"TLS" json:"tls"`
	}

	tests := []struct {
		name     string
		path     []string
		env      map[string]string
		json     string
		wantEnv  []string
		wantJSON []string
	}{
		{
			name: "nothing unknown",
			path: []string{"svc"},
			env:  map[string]string{"SVC_ADDR": ":1", "SVC_TLS_CERT_FILE": "c"},
			json: `{"svc": {"addr": ":1", "tls": {"cert_file": "c"}}}`,
		},
		{
			name:     "a typo at the root",
			path:     []string{"svc"},
			env:      map[string]string{"SVC_ADDR": ":1", "SVC_ADRR": ":2"},
			json:     `{"svc": {"addr": ":1", "adrr": ":2"}}`,
			wantEnv:  []string{"SVC_ADRR"},
			wantJSON: []string{"svc.adrr"},
		},
		{
			name:     "a typo in a nested struct",
			path:     []string{"svc"},
			env:      map[string]string{"SVC_TLS_CERT_FLIE": "c"},
			json:     `{"svc": {"tls": {"cert_flie": "c"}}}`,
			wantEnv:  []string{"SVC_TLS_CERT_FLIE"},
			wantJSON: []string{"svc.tls.cert_flie"},
		},
		{
			name:     "a typo under a name with a dot",
			path:     []string{"svc", "http.server"},
			env:      map[string]string{"SVC_HTTP_SERVER_ADRR": ":2"},
			json:     `{"svc": {"http": {"server": {"adrr": ":2"}}}}`,
			wantEnv:  []string{"SVC_HTTP_SERVER_ADRR"},
			wantJSON: []string{"svc.http.server.adrr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			providers := map[string]config.Checker{
				"env":  env.New(env.WithMap(tt.env)),
				"json": json.Raw([]byte(tt.json)),
			}

			want := map[string][]string{"env": tt.wantEnv, "json": tt.wantJSON}

			for name, p := range providers {
				if err := p.Unmarshal(tt.path, &cfg{}); err != nil {
					t.Fatalf("%s: %v", name, err)
				}

				if got := keysOfUnknown(p.Check(tt.path)); !slices.Equal(got, want[name]) {
					t.Errorf("%s reported %v, want %v", name, got, want[name])
				}
			}
		})
	}
}

type flagsTLS struct {
	CertFile string `flag:"cert-file"`
}

type flagsConfig struct {
	Addr string   `flag:"addr"`
	TLS  flagsTLS `flag:"tls"`
}

// Flags look in the same place as the other providers for a path, in their own
// format: the keys of the path, after the root function's, written as markers.
// The root function's own flags have none, as its name is the program's.
//
// Paths that give the same keys must give the same markers, so a function
// named "http.server" is --http --server, however the name is written.
func TestFlagsAgreeOnWhereToLook(t *testing.T) {
	tests := []struct {
		name  string
		paths [][]string
		args  []string
	}{
		{
			name:  "the root function",
			paths: [][]string{{"svc"}, {"other"}},
			args:  []string{"--addr=:1", "--tls-cert-file=c.pem"},
		},
		{
			name: "two words",
			paths: [][]string{
				{"svc", "http", "server"},
				{"svc", "http.server"},
				{"svc", "http/server"},
				{"svc", "http-server"},
				{"svc", "HTTP.Server"},
			},
			args: []string{"--http", "--server", "--addr=:1", "--tls-cert-file=c.pem"},
		},
		{
			name:  "digits",
			paths: [][]string{{"svc", "v2.api"}, {"svc", "v2", "api"}},
			args:  []string{"--v2", "--api", "--addr=:1", "--tls-cert-file=c.pem"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, path := range tt.paths {
				var got flagsConfig

				p := flags.New(flags.WithArgs(tt.args))

				if err := p.Unmarshal(path, &got); err != nil {
					t.Fatalf("%v: %v", path, err)
				}

				if got.Addr != ":1" || got.TLS.CertFile != "c.pem" {
					t.Errorf("%v: got %+v", path, got)
				}

				if err := p.Check(path[:1]); err != nil {
					t.Errorf("%v: Check reported %v", path, err)
				}
			}
		})
	}
}

// A provider is made once and used for as long as the program runs: by every
// instance of a program that restarts, and by the trial run of a restart beside
// the live instance. So it must answer the same each time it is asked, and when
// it is asked at the same time (the race detector says if not).
//
// A new provider should be added here.
func TestProvidersCanBeReused(t *testing.T) {
	type cfg struct {
		Addr string `config:"addr" flag:"addr"`
	}

	providers := map[string]func() config.Provider{
		"env": func() config.Provider {
			return env.New(lookupMap(map[string]string{"SVC_ADDR": ":1"}))
		},
		"json": func() config.Provider { return json.Raw([]byte(`{"svc": {"addr": ":1"}}`)) },
		"flags": func() config.Provider {
			return flags.New(flags.WithArgs([]string{"--addr", ":1"}))
		},
	}

	path := []string{"svc"}

	for name, fresh := range providers {
		t.Run(name+" one after another", func(t *testing.T) {
			p := fresh()

			for i := range 5 {
				var got cfg

				if err := p.Unmarshal(path, &got); err != nil {
					t.Fatalf("load %d: %v", i+1, err)
				}

				if got.Addr != ":1" {
					t.Fatalf("load %d: Addr = %q, want :1", i+1, got.Addr)
				}
			}
		})

		t.Run(name+" at the same time", func(t *testing.T) {
			p := fresh()

			var wg sync.WaitGroup

			results := make([]error, 32)
			values := make([]string, 32)

			for i := range results {
				wg.Go(func() {
					var got cfg

					results[i] = p.Unmarshal(path, &got)
					values[i] = got.Addr
				})
			}

			wg.Wait()

			for i := range results {
				if results[i] != nil || values[i] != ":1" {
					t.Errorf("load %d: Addr = %q, error %v", i+1, values[i], results[i])
				}
			}
		})
	}
}
