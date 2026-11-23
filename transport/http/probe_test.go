package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// inF runs fn inside a foundation function, as a probe needs one.
func inF(t *testing.T, fn func(f *foundation.F)) {
	t.Helper()

	foundationtest.NewExitRecorder().Install(t)

	foundation.Run("probe-test", fn, foundation.WithConfigProviders())
}

func statusOf(t *testing.T, url string) (monitor.Status, error) {
	t.Helper()

	var (
		status monitor.Status
		err    error
	)

	inF(t, func(f *foundation.F) {
		status, err = transporthttp.NewProbe(f, url).Status(context.Background())
	})

	return status, err
}

func TestProbeIsHealthyOnA2xx(t *testing.T) {
	for _, code := range []int{200, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))

		status, err := statusOf(t, srv.URL+"/_probe")

		srv.Close()

		if status != monitor.StatusHealthy || err != nil {
			t.Fatalf("%d: status=%v err=%v", code, status, err)
		}
	}
}

// Getting an answer is not the same as being healthy: only a 2xx is. A redirect
// isn't followed, as the probe path should answer for itself.
func TestProbeIsUnhealthyOnAnyOtherStatus(t *testing.T) {
	for _, code := range []int{301, 302, 400, 401, 404, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code/100 == 3 {
				w.Header().Set("Location", "/elsewhere")
			}

			w.WriteHeader(code)
		}))

		status, err := statusOf(t, srv.URL+"/_probe")

		srv.Close()

		if status != monitor.StatusUnhealthy {
			t.Fatalf("%d: status = %v, want unhealthy", code, status)
		}

		if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) && !strings.Contains(err.Error(), "status") {
			t.Fatalf("%d: expected an error naming the status, got %v", code, err)
		}
	}
}

func TestProbeIsUnhealthyWhenTheServerIsDown(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL

	srv.Close()

	status, err := statusOf(t, url)

	if status != monitor.StatusUnhealthy || err == nil {
		t.Fatalf("status=%v err=%v", status, err)
	}
}

// The error is shown to whoever reads the health endpoint, so it doesn't repeat
// the address that was probed.
func TestProbeErrorsDoNotRepeatTheURL(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL

	srv.Close()

	_, err := statusOf(t, url+"/_probe")

	if err == nil || strings.Contains(err.Error(), url) || strings.Contains(err.Error(), "/_probe") {
		t.Fatalf("the error includes the URL: %v", err)
	}
}

// The context bounds the probe, which previously ignored it.
func TestProbeStopsWhenItsContextIsCancelled(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))

	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan monitor.Status, 1)

	inF(t, func(f *foundation.F) {
		go func() {
			status, _ := transporthttp.NewProbe(f, srv.URL).Status(ctx)
			done <- status
		}()

		select {
		case status := <-done:
			if status != monitor.StatusUnhealthy {
				t.Errorf("status = %v, want unhealthy", status)
			}
		case <-time.After(3 * time.Second):
			t.Error("the probe was still blocked 3s after its context expired")
		}
	})
}

// A TLS server answers a plain http request with an error, which is not healthy.
func TestProbeOfATLSServerOverPlainHTTPIsUnhealthy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	defer srv.Close()

	status, err := statusOf(t, "http://"+srv.Listener.Addr().String()+"/_probe")

	if status != monitor.StatusUnhealthy || err == nil {
		t.Fatalf("status=%v err=%v", status, err)
	}
}

// Over https the certificate is checked like any other, so a self-signed one
// that isn't trusted fails.
func TestProbeVerifiesCertificatesByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	defer srv.Close()

	status, err := statusOf(t, srv.URL+"/_probe")

	if status != monitor.StatusUnhealthy || err == nil {
		t.Fatalf("status=%v err=%v", status, err)
	}
}
