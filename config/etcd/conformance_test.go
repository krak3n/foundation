package etcd_test

import (
	"sync"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/etcd"
	"go.krak3n.dev/foundation/config/json"
)

type conformanceConfig struct {
	Addr string `config:"addr"`
	TLS  struct {
		CertFile string `config:"cert_file"`
	} `config:"tls"`
}

// Every provider must find the same value for the same function path. This is the
// etcd provider's case of the table in the config package's own conformance test,
// which can't import it: the core has no dependencies. The etcd provider lowercases
// the words of the path, as the JSON provider matches them without regard to case.
func TestProvidersAgreeOnWhereToLook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path []string
		env  map[string]string
		json string
		etcd map[string]string
	}{
		{
			name: "single segment",
			path: []string{"svc"},
			env:  map[string]string{"SVC_ADDR": ":1", "SVC_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}`,
			etcd: map[string]string{"/p/svc/addr": ":1", "/p/svc/tls/cert_file": "c.pem"},
		},
		{
			name: "nested segments",
			path: []string{"svc", "http", "server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
			etcd: map[string]string{"/p/svc/http/server/addr": ":1", "/p/svc/http/server/tls/cert_file": "c.pem"},
		},
		{
			name: "dot in a name",
			path: []string{"svc", "http.server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1", "SVC_HTTP_SERVER_TLS_CERT_FILE": "c.pem"},
			json: `{"svc": {"http": {"server": {"addr": ":1", "tls": {"cert_file": "c.pem"}}}}}`,
			etcd: map[string]string{"/p/svc/http/server/addr": ":1", "/p/svc/http/server/tls/cert_file": "c.pem"},
		},
		{
			name: "slash and hyphen in a name",
			path: []string{"svc", "http/server-x"},
			env:  map[string]string{"SVC_HTTP_SERVER_X_ADDR": ":1"},
			json: `{"svc": {"http": {"server": {"x": {"addr": ":1"}}}}}`,
			etcd: map[string]string{"/p/svc/http/server/x/addr": ":1"},
		},
		{
			name: "underscore in a name",
			path: []string{"svc", "http_server"},
			env:  map[string]string{"SVC_HTTP_SERVER_ADDR": ":1"},
			json: `{"svc": {"http_server": {"addr": ":1"}}}`,
			etcd: map[string]string{"/p/svc/http_server/addr": ":1"},
		},
		{
			name: "case",
			path: []string{"Svc", "HTTP"},
			env:  map[string]string{"SVC_HTTP_ADDR": ":1"},
			json: `{"svc": {"http": {"addr": ":1"}}}`,
			etcd: map[string]string{"/p/svc/http/addr": ":1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, _ := provider(t, tt.etcd, "/p")

			var fromEnv, fromJSON, fromEtcd conformanceConfig

			for _, c := range []struct {
				name string
				p    config.Provider
				dst  *conformanceConfig
			}{
				{"env", env.New(env.WithMap(tt.env)), &fromEnv},
				{"json", json.Raw([]byte(tt.json)), &fromJSON},
				{"etcd", p, &fromEtcd},
			} {
				if err := c.p.Unmarshal(tt.path, c.dst); err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
			}

			if fromEnv != fromJSON || fromEnv != fromEtcd || fromEnv.Addr != ":1" {
				t.Errorf("env %+v, json %+v, etcd %+v", fromEnv, fromJSON, fromEtcd)
			}
		})
	}
}

// Providers are shared by every instance of a program and by the trial of a
// restart, so one that is asked again, from several goroutines, gives one answer.
func TestProvidersCanBeReused(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/p/svc/addr": ":1"}, "/p")

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			var got conformanceConfig
			if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != ":1" {
				t.Errorf("got %+v, %v", got, err)
			}
		}()
	}

	wg.Wait()
}

var _ = etcd.New
