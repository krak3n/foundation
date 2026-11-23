package http

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
)

func inF(t *testing.T, opts []foundation.Option, fn func(f *foundation.F)) {
	t.Helper()

	foundationtest.NewExitRecorder().Install(t)

	foundation.Run("test", fn, append([]foundation.Option{foundation.WithConfigProviders()}, opts...)...)
}

func TestDefaultServerConfigIsSafe(t *testing.T) {
	t.Parallel()

	cfg := DefaultServerConfig()

	if cfg.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", cfg.ReadHeaderTimeout)
	}

	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", cfg.IdleTimeout)
	}

	// Left at no limit on purpose: uploads and streaming need them.
	if cfg.ReadTimeout != 0 || cfg.WriteTimeout != 0 {
		t.Errorf("ReadTimeout = %v and WriteTimeout = %v, want 0", cfg.ReadTimeout, cfg.WriteTimeout)
	}

	if cfg.Addr != "" || cfg.BlockTimeout != 15*time.Second || cfg.ProbePath != "/_probe" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestNewServerUsesTheTimeouts(t *testing.T) {
	t.Parallel()

	cfg := DefaultServerConfig()
	cfg.ReadTimeout = 3 * time.Second
	cfg.WriteTimeout = 4 * time.Second

	srv := newServer(cfg, http.NotFoundHandler())

	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 3*time.Second ||
		srv.WriteTimeout != 4*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("unexpected server: header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}

	// The probe is injected in front of the handler.
	rec := httptest.NewRecorder()

	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_probe", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("probe path answered %d", rec.Code)
	}
}

// The defaults are set before configuration is loaded, so configuration can
// change them, and turn them off with 0.
func TestTimeoutsCanBeConfigured(t *testing.T) {
	loaded := func(t *testing.T, provider foundation.Option) ServerConfig {
		t.Helper()

		var cfg ServerConfig

		inF(t, []foundation.Option{provider}, func(f *foundation.F) {
			f.Run("http.server", func(f *foundation.F) {
				cfg = DefaultServerConfig()

				f.Load(&cfg)
			})
		})

		return cfg
	}

	t.Run("env", func(t *testing.T) {
		cfg := loaded(t, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
			"TEST_HTTP_SERVER_READ_HEADER_TIMEOUT": "2s",
			"TEST_HTTP_SERVER_IDLE_TIMEOUT":        "0",
		}))))

		if cfg.ReadHeaderTimeout != 2*time.Second || cfg.IdleTimeout != 0 {
			t.Fatalf("ReadHeaderTimeout = %v, IdleTimeout = %v", cfg.ReadHeaderTimeout, cfg.IdleTimeout)
		}
	})

	t.Run("json", func(t *testing.T) {
		cfg := loaded(t, foundation.WithConfigProviders(json.Raw([]byte(`{"test": {"http": {"server": {"read_header_timeout": "0"}}}}`))))

		if cfg.ReadHeaderTimeout != 0 || cfg.IdleTimeout != 120*time.Second {
			t.Fatalf("ReadHeaderTimeout = %v, IdleTimeout = %v", cfg.ReadHeaderTimeout, cfg.IdleTimeout)
		}
	})

	t.Run("not configured", func(t *testing.T) {
		cfg := loaded(t, foundation.WithConfigProviders(env.New(env.WithMap(nil))))

		if cfg.ReadHeaderTimeout != 10*time.Second {
			t.Fatalf("ReadHeaderTimeout = %v", cfg.ReadHeaderTimeout)
		}
	})
}

func TestProbeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr net.Addr
		tls  bool
		path string
		want string
	}{
		{"ipv6 wildcard is probed on loopback", &net.TCPAddr{IP: net.IPv6unspecified, Port: 8080}, false, "/_probe", "http://[::1]:8080/_probe"},
		{"ipv4 wildcard is probed on loopback", &net.TCPAddr{IP: net.IPv4zero, Port: 80}, false, "/_probe", "http://127.0.0.1:80/_probe"},
		{"no ip at all", &net.TCPAddr{Port: 9}, false, "/_probe", "http://127.0.0.1:9/_probe"},
		{"loopback is kept", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, false, "/_probe", "http://127.0.0.1:9/_probe"},
		{"a specific address is kept", &net.TCPAddr{IP: net.IPv4(10, 1, 2, 3), Port: 8080}, false, "/_probe", "http://10.1.2.3:8080/_probe"},
		{"a specific ipv6 address is kept", &net.TCPAddr{IP: net.ParseIP("fd00::5"), Port: 8080}, false, "/healthy", "http://[fd00::5]:8080/healthy"},
		{"tls", &net.TCPAddr{IP: net.IPv6unspecified, Port: 8443}, true, "/_probe", "https://[::1]:8443/_probe"},
	}

	for _, tt := range tests {
		if got := probeURL(tt.addr, tt.tls, tt.path); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// The probe talks to its own server, so it has no business going through a
// proxy, following redirects or keeping connections open.
func TestProbeClientIsLocked(t *testing.T) {
	inF(t, nil, func(f *foundation.F) {
		p := newProbe(f, "http://127.0.0.1:1/_probe", nil, time.Second)

		transport, ok := p.client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("transport is %T", p.client.Transport)
		}

		if transport.Proxy != nil {
			t.Error("the probe must not use a proxy")
		}

		if !transport.DisableKeepAlives {
			t.Error("the probe should not keep connections open")
		}

		if p.client.CheckRedirect == nil {
			t.Error("the probe must not follow redirects")
		}

		if p.client.Timeout != time.Second {
			t.Errorf("timeout = %v", p.client.Timeout)
		}
	})
}

func TestProbeHasItsOwnTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))

	defer srv.Close()
	defer close(block)

	inF(t, nil, func(f *foundation.F) {
		start := time.Now()

		status, err := newProbe(f, srv.URL, nil, 200*time.Millisecond).Status(context.Background())

		if status != monitor.StatusUnhealthy || err == nil {
			t.Errorf("status=%v err=%v", status, err)
		}

		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("took %v with a timeout of 200ms", took)
		}
	})
}

// newCert makes a self-signed certificate for localhost.
func newCert(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// With TLS on, the probe accepts the one certificate the server was configured
// with, whatever name it is for, and nothing else.
func TestProbePinsTheServersCertificate(t *testing.T) {
	pair, served := newCert(t)
	_, other := newCert(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}

	srv.StartTLS()

	defer srv.Close()

	probe := func(f *foundation.F, pin *x509.Certificate) (monitor.Status, error) {
		return newProbe(f, srv.URL+"/_probe", pin, 2*time.Second).Status(context.Background())
	}

	inF(t, nil, func(f *foundation.F) {
		if status, err := probe(f, served); status != monitor.StatusHealthy || err != nil {
			t.Errorf("the right certificate: status=%v err=%v", status, err)
		}

		if status, err := probe(f, other); status != monitor.StatusUnhealthy || err == nil {
			t.Errorf("a different certificate: status=%v err=%v", status, err)
		}

		// With nothing pinned the ordinary rules apply, and a self-signed
		// certificate isn't trusted.
		if status, err := probe(f, nil); status != monitor.StatusUnhealthy || err == nil {
			t.Errorf("no pin: status=%v err=%v", status, err)
		}
	})
}

var (
	_ monitor.Probe = (*probe)(nil)
	_               = env.New
)

func TestValidateProbePath(t *testing.T) {
	t.Parallel()

	valid := []string{"/_probe", "/healthz", "/a/b", "/a-b_c.d", "/v1/ready", "/ü"}

	for _, path := range valid {
		if err := validateProbePath(path); err != nil {
			t.Errorf("%q should be valid: %v", path, err)
		}
	}

	invalid := []string{
		"", "/", "a", "/a b", "/a\tb", "/a\nb", "/{x}", "/a/{$}", "/a?b", "/a#b",
		"/a/", "/a/../b", "//a", "/a/./b", "GET /a", "/a\x00b",
	}

	for _, path := range invalid {
		err := validateProbePath(path)
		if err == nil {
			t.Errorf("%q should be invalid", path)

			continue
		}

		// The message says what is wrong with it.
		if !strings.Contains(err.Error(), "probe path") {
			t.Errorf("%q: the error should be about the probe path: %v", path, err)
		}
	}
}
