package yaml_test

import (
	"fmt"
	"sync"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/config/yaml"
)

type conformanceConfig struct {
	Addr string `config:"addr"`
	TLS  struct {
		CertFile string `config:"cert_file"`
	} `config:"tls"`
}

// Every provider must find the same value for the same function path. This is
// the YAML provider's case of the table in the config package's own conformance
// test, which can't import it: the core has no dependencies.
func TestProvidersAgreeOnWhereToLook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path []string
		env  map[string]string
		json string
		yaml string
	}{
		{
			name: "single segment",
			path: []string{"svc"},
			env:  map[string]string{"SVC_ADDR": ":1", "SVC_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}`,
			yaml: "svc:\n  addr: ':1'\n  tls:\n    cert_file: c.pem\n",
		},
		{
			name: "nested segments",
			path: []string{"svc", "http", "server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
			yaml: "svc:\n  http:\n    server:\n      addr: ':1'\n      tls:\n        cert_file: c.pem\n",
		},
		{
			name: "dot in a name",
			path: []string{"svc", "http.server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
			yaml: "svc:\n  http:\n    server:\n      addr: ':1'\n      tls:\n        cert_file: c.pem\n",
		},
		{
			name: "slash and hyphen in a name",
			path: []string{"svc", "http/server-x"},
			env:  map[string]string{"SVC_HTTP_SERVER_X_ADDR": ":1"},
			json: `{"svc": {"http": {"server": {"x": {"addr": ":1"}}}}}`,
			yaml: "svc:\n  http:\n    server:\n      x:\n        addr: ':1'\n",
		},
		{
			name: "underscore in a name",
			path: []string{"svc", "http_server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1"},
			json: `{"svc": {"http_server": {"addr": ":1"}}}`,
			yaml: "svc:\n  http_server:\n    addr: ':1'\n",
		},
		{
			name: "case",
			path: []string{"Svc", "HTTP"},
			env:  map[string]string{"SVC_HTTP_ADDR": ":1"},
			json: `{"svc": {"http": {"addr": ":1"}}}`,
			yaml: "svc:\n  http:\n    addr: ':1'\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var fromEnv, fromJSON, fromYAML conformanceConfig

			providers := []struct {
				name string
				p    config.Provider
				dst  *conformanceConfig
			}{
				{"env", env.New(env.WithMap(tt.env)), &fromEnv},
				{"json", json.Raw([]byte(tt.json)), &fromJSON},
				{"yaml", yaml.Raw([]byte(tt.yaml)), &fromYAML},
			}

			for _, p := range providers {
				if err := p.p.Unmarshal(tt.path, p.dst); err != nil {
					t.Fatalf("%s: %v", p.name, err)
				}
			}

			if fromEnv.Addr != ":1" {
				t.Fatalf("the case doesn't test anything: env read %+v", fromEnv)
			}

			if fromJSON != fromEnv || fromYAML != fromEnv {
				t.Fatalf("providers disagree:\n env:  %+v\n json: %+v\n yaml: %+v", fromEnv, fromJSON, fromYAML)
			}
		})
	}
}

// And the keys they say they look for are the same, apart from the format.
func TestProvidersDescribeTheSameKeys(t *testing.T) {
	t.Parallel()

	path := []string{"svc", "http.server"}

	jsonKeys, err := json.Raw(nil).Describe(path, &conformanceConfig{})
	if err != nil {
		t.Fatal(err)
	}

	yamlKeys, err := yaml.Raw(nil).Describe(path, &conformanceConfig{})
	if err != nil {
		t.Fatal(err)
	}

	if len(jsonKeys) != len(yamlKeys) {
		t.Fatalf("json: %v, yaml: %v", jsonKeys, yamlKeys)
	}

	for i := range jsonKeys {
		if jsonKeys[i].Field != yamlKeys[i].Field || jsonKeys[i].Key != yamlKeys[i].Key {
			t.Errorf("field %d: json %+v, yaml %+v", i, jsonKeys[i], yamlKeys[i])
		}
	}
}

// What a file can't say, JSON and YAML refuse alike.
func TestProvidersRefuseTheSameAmbiguities(t *testing.T) {
	t.Parallel()

	var fromJSON, fromYAML conformanceConfig

	jerr := json.Raw([]byte(`{"svc": {"addr": "a", "ADDR": "b"}}`)).Unmarshal([]string{"svc"}, &fromJSON)
	yerr := yaml.Raw([]byte("svc:\n  addr: a\n  ADDR: b\n")).Unmarshal([]string{"svc"}, &fromYAML)

	jf, yf := fieldErrors(jerr), fieldErrors(yerr)

	if len(jf) != 1 || len(yf) != 1 || jf[0].Field != yf[0].Field || jf[0].Key != yf[0].Key {
		t.Fatalf("json: %v, yaml: %v", jerr, yerr)
	}
}

// A provider is made once and used for as long as the program runs: by every
// instance of a program that restarts, and by the trial run of a restart beside
// the live instance. So it must answer the same each time, and when it is asked at
// the same time. This is the YAML case of the core's TestProvidersCanBeReused.
func TestTheProviderCanBeReused(t *testing.T) {
	t.Parallel()

	type loaded struct {
		Addr string `config:"addr"`
	}

	path := []string{"svc"}
	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n"))

	for i := range 5 {
		var got loaded

		if err := p.Unmarshal(path, &got); err != nil || got.Addr != ":1" {
			t.Fatalf("load %d: Addr = %q, error %v", i+1, got.Addr, err)
		}
	}

	var wg sync.WaitGroup

	errs := make(chan string, 32)

	for range 32 {
		wg.Go(func() {
			var got loaded

			if err := p.Unmarshal(path, &got); err != nil || got.Addr != ":1" {
				errs <- fmt.Sprintf("Addr = %q, error %v", got.Addr, err)
			}
		})
	}

	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}
