package http_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// A client that never finishes its request headers is cut off, instead of being
// held for as long as it likes.
func TestSlowHeadersAreClosed(t *testing.T) {
	addr, shutdown := startOn(t, withReadHeaderTimeout(300*time.Millisecond))

	defer shutdown()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\n") // never finished

	start := time.Now()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	b, _ := io.ReadAll(conn)

	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("the server kept the connection for %v with a read header timeout of 300ms", took)
	}

	if strings.Contains(string(b), "200 OK") {
		t.Fatalf("the unfinished request was served: %q", b)
	}
}

// startOn starts a server on a free loopback address and returns it, with a
// function to stop it.
func startOn(t *testing.T, opts ...opt) (addr string, shutdown func()) {
	t.Helper()

	baseURL, stop, _ := startServer(t, hello, opts)

	return strings.TrimPrefix(baseURL, "http://"), stop
}

// keyPair writes a self-signed certificate and its key for localhost to files.
func keyPair(t *testing.T) (certFile, keyFile string, cert *x509.Certificate) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")

	write := func(path, kind string, der []byte) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(certFile, "CERTIFICATE", der)
	write(keyFile, "PRIVATE KEY", keyDER)

	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return certFile, keyFile, cert
}

// A server with TLS becomes ready, which needs its probe to reach it over https,
// and serves https.
func TestServerWithTLSIsReadyAndServes(t *testing.T) {
	certFile, keyFile, cert := keyPair(t)

	var probes rootProbes

	baseURL, shutdown, done := startServer(t, hello,
		[]opt{withTLS(certFile, keyFile), withBlock()},
		probes.option())

	defer shutdown()

	var probe monitor.Probe

	for deadline := time.Now().Add(3 * time.Second); probe == nil && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for p := range probes.seq() {
			if p.Name() == "test/http.server" {
				probe = p
			}
		}
	}

	if probe == nil {
		t.Fatal("the server's probe was not registered")
	}

	if status, err := probe.Status(context.Background()); status != monitor.StatusHealthy || err != nil {
		t.Fatalf("the probe of a TLS server: status=%v err=%v", status, err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}

	resp, err := client.Get("https://" + strings.TrimPrefix(baseURL, "http://") + "/")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}

	shutdown()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}
}

// A certificate that can't be loaded fails the server at startup, with a message
// that says so, instead of failing later or serving without it.
func TestServerFailsAtStartupWithABadTLSPair(t *testing.T) {
	certFile, _, _ := keyPair(t)
	_, otherKey, _ := keyPair(t)

	tests := map[string][2]string{
		"missing files":        {filepath.Join(t.TempDir(), "no-cert.pem"), filepath.Join(t.TempDir(), "no-key.pem")},
		"a key that isn't for": {certFile, otherKey},
	}

	for name, pair := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			defer ln.Close()

			foundation.Run("test", func(f *foundation.F) {
				server(f, hello, withListener(ln), withTLS(pair[0], pair[1]))
			}, foundation.WithConfigProviders())

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			var logged bool

			for _, e := range recorder.Entries() {
				e.Record.Attrs(func(a slog.Attr) bool {
					if a.Key == "err" && strings.Contains(a.Value.String(), "TLS") {
						logged = true
					}

					return true
				})

				for _, a := range e.Attrs {
					if a.Key == "err" && strings.Contains(a.Value.String(), "TLS") {
						logged = true
					}
				}
			}

			if !logged {
				t.Fatal("expected the failure to say it was about the TLS certificate")
			}
		})
	}
}

func unhealthyProbe(calls *int, results ...func() (monitor.Status, error)) monitor.Probe {
	return monitor.NewProbe("p", monitor.AllModes, monitor.StatusCheckerFunc(func(context.Context) (monitor.Status, error) {
		defer func() { *calls++ }()

		if *calls < len(results) {
			return results[*calls]()
		}

		return results[len(results)-1]()
	}))
}

var (
	refused = func() (monitor.Status, error) { return monitor.StatusUnhealthy, errors.New("connection refused") }
	notYet  = func() (monitor.Status, error) { return monitor.StatusUnhealthy, nil }
	healthy = func() (monitor.Status, error) { return monitor.StatusHealthy, nil }
)

// A probe error while the server is still starting is not the end of waiting:
// Ready keeps trying until the server is healthy.
func TestReadyKeepsTryingAfterAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := foundationtest.NewExitRecorder()
		exit.Install(t)

		var calls int

		probe := unhealthyProbe(&calls, refused, notYet, healthy)

		foundation.Run("test", func(f *foundation.F) {
			transporthttp.Ready(f, 10*time.Second, probe)
		}, foundation.WithConfigProviders())

		if calls != 3 {
			t.Fatalf("the probe was called %d times, want 3: an error, not yet healthy, then healthy", calls)
		}

		if len(exit.Codes()) != 0 {
			t.Fatalf("exit codes = %v, want none", exit.Codes())
		}
	})
}

// If it never becomes healthy Ready fails the run, as it does for a timeout
// today, and it has tried more than once.
func TestReadyFailsIfTheServerNeverBecomesHealthy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := foundationtest.NewExitRecorder()
		exit.Install(t)

		var (
			calls   int
			reached bool
		)

		probe := unhealthyProbe(&calls, refused)

		foundation.Run("test", func(f *foundation.F) {
			transporthttp.Ready(f, time.Second, probe)

			reached = true
		}, foundation.WithConfigProviders())

		if reached {
			t.Fatal("Ready returned normally for a server that never became healthy")
		}

		if len(exit.Codes()) != 1 || exit.Codes()[0] != 1 {
			t.Fatalf("exit codes = %v, want [1]", exit.Codes())
		}

		if calls < 3 {
			t.Fatalf("the probe was only tried %d times", calls)
		}
	})
}

// Shutting down while waiting ends the wait quietly.
func TestReadyReturnsOnShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := foundationtest.NewExitRecorder()
		exit.Install(t)

		var (
			calls    int
			returned bool
		)

		probe := unhealthyProbe(&calls, notYet)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("stopper", func(f *foundation.F) {
				f.Parallel()

				time.Sleep(300 * time.Millisecond)
				f.Shutdown("stop")
			})

			f.Run("waiter", func(f *foundation.F) {
				transporthttp.Ready(f, time.Hour, probe)

				returned = true
			})
		}, foundation.WithConfigProviders())

		if !returned || len(exit.Codes()) != 0 {
			t.Fatalf("returned=%v exit codes=%v", returned, exit.Codes())
		}
	})
}

// errsLogged lists every err attribute the recorder saw.
func errsLogged(r *foundationtest.LogRecorder) []string {
	var out []string

	for _, e := range r.Entries() {
		e.Record.Attrs(func(a slog.Attr) bool {
			if a.Key == "err" {
				out = append(out, a.Value.String())
			}

			return true
		})

		for _, a := range e.Attrs {
			if a.Key == "err" {
				out = append(out, a.Value.String())
			}
		}
	}

	return out
}

// startupFailure runs a server that is expected to fail while starting, and
// returns the exit codes and the errors logged. It fails the test if the server
// starts instead.
func startupFailure(t *testing.T, opts []opt, fopts ...foundation.Option) (codes []int, errs []string) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			server(f, hello, append([]opt{withListener(ln)}, opts...)...)
		}, append([]foundation.Option{foundation.WithConfigProviders()}, fopts...)...)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server started, but was expected to fail while starting")
	}

	return exit.Codes(), errsLogged(recorder)
}

func mentions(errs []string, words ...string) bool {
	for _, e := range errs {
		ok := true

		for _, w := range words {
			ok = ok && strings.Contains(strings.ToLower(e), strings.ToLower(w))
		}

		if ok {
			return true
		}
	}

	return false
}

// A certificate with no key, or a key with no certificate, is a mistake. It must
// not quietly leave the server without TLS.
func TestTLSNeedsBothFiles(t *testing.T) {
	certFile, keyFile, _ := keyPair(t)

	tests := map[string]struct {
		cert, key string
		missing   string
	}{
		"a key and no certificate": {cert: "", key: keyFile, missing: "certificate"},
		"a certificate and no key": {cert: certFile, key: "", missing: "key"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			codes, errs := startupFailure(t, []opt{withTLS(tt.cert, tt.key)})

			if len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			if !mentions(errs, "tls", tt.missing) {
				t.Fatalf("expected the error to say the %s is missing, got %v", tt.missing, errs)
			}
		})
	}
}

// The same when it is configured, which is where a misspelt variable happens.
func TestTLSNeedsBothFilesWhenConfigured(t *testing.T) {
	_, keyFile, _ := keyPair(t)

	codes, errs := startupFailure(t, nil, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
		"TEST_HTTP_SERVER_TLS_KEY_FILE": keyFile,
	}))))

	if len(codes) != 1 || codes[0] != 1 || !mentions(errs, "tls", "certificate") {
		t.Fatalf("codes=%v errs=%v", codes, errs)
	}
}

// Serve is exported, and has the same rule: given a key and no certificate it
// doesn't fall back to plain HTTP.
func TestServeWithOnlyAKeyDoesNotServePlainHTTP(t *testing.T) {
	_, keyFile, _ := keyPair(t)

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			transporthttp.Serve(f, &http.Server{Handler: hello}, ln, "", keyFile)

			<-f.Context().Done()
		}, foundation.WithConfigProviders())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not fail")
	}

	if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}

	if resp, err := client.Get("http://" + ln.Addr().String() + "/"); err == nil {
		resp.Body.Close()

		t.Fatal("the server answered a plain http request")
	}
}

func TestNoTLSFilesMeansPlainHTTPAndSaysSo(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	baseURL, shutdown, _ := startServer(t, hello, []opt{withTLS("", "")})

	defer shutdown()

	if body := get(t, strings.TrimPrefix(baseURL, "http://")); body != "hello" {
		t.Fatalf("body = %q", body)
	}

	if got := servingTLS(recorder); got != "false" {
		t.Fatalf("tls attribute = %q, want false", got)
	}
}

func TestTLSIsLoggedWhenOn(t *testing.T) {
	certFile, keyFile, _ := keyPair(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	_, shutdown, _ := startServer(t, hello, []opt{withTLS(certFile, keyFile)})

	defer shutdown()

	for deadline := time.Now().Add(2 * time.Second); servingTLS(recorder) == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}

	if got := servingTLS(recorder); got != "true" {
		t.Fatalf("tls attribute = %q, want true", got)
	}
}

// servingTLS is the tls attribute of the "serving http server" log line.
func servingTLS(r *foundationtest.LogRecorder) string {
	for _, e := range r.Entries() {
		if e.Record.Message != "serving http server" {
			continue
		}

		var tlsValue string

		e.Record.Attrs(func(a slog.Attr) bool {
			if a.Key == "tls" {
				tlsValue = a.Value.String()
			}

			return true
		})

		return tlsValue
	}

	return ""
}

// A probe path that is not a path to a probe is refused when the server starts,
// not left to panic in the mux or to answer for every request.
func TestInvalidProbePathsFailAtStartup(t *testing.T) {
	for _, path := range []string{"", "/", "no-slash", "/a b", "/a\tb", "/{x}", "/a/{$}", "/a?b", "/a#b", "/a/", "/a/../b", "//a", "/a/./b", "GET /a"} {
		t.Run(path, func(t *testing.T) {
			codes, errs := startupFailure(t, []opt{withProbePath(path)})

			if len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			if !mentions(errs, "probe path") {
				t.Fatalf("expected the error to be about the probe path, got %v", errs)
			}
		})
	}
}

// An empty value in the environment is a real, empty string for this field.
func TestInvalidProbePathsFromConfigurationFailAtStartup(t *testing.T) {
	for _, value := range []string{"/", ""} {
		codes, errs := startupFailure(t, nil, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
			"TEST_HTTP_SERVER_PROBE_PATH": value,
		}))))

		if len(codes) != 1 || codes[0] != 1 || !mentions(errs, "probe path") {
			t.Fatalf("PROBE_PATH=%q: codes=%v errs=%v", value, codes, errs)
		}
	}
}

func TestACustomProbePath(t *testing.T) {
	var probes rootProbes

	baseURL, shutdown, _ := startServer(t, hello, []opt{withProbePath("/ready-check")}, probes.option())

	defer shutdown()

	addr := strings.TrimPrefix(baseURL, "http://")

	read := func(path string) (int, string) {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(b)
	}

	if code, body := read("/ready-check"); code != http.StatusOK || body != "" {
		t.Fatalf("the probe path answered %d %q", code, body)
	}

	// Everything else reaches the application, including GET /, and the default
	// probe path is just another route.
	if _, body := read("/"); body != "hello" {
		t.Fatalf("GET / was answered with %q: the probe shadows the application", body)
	}

	if _, body := read("/_probe"); body != "hello" {
		t.Fatalf("GET /_probe was answered with %q", body)
	}

	// And the server's own probe asks the path it was configured with.
	var probe monitor.Probe

	for deadline := time.Now().Add(3 * time.Second); probe == nil && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for p := range probes.seq() {
			if p.Name() == "test/http.server" {
				probe = p
			}
		}
	}

	if probe == nil {
		t.Fatal("the server's probe was not registered")
	}

	if status, err := probe.Status(context.Background()); status != monitor.StatusHealthy || err != nil {
		t.Fatalf("status=%v err=%v", status, err)
	}
}
