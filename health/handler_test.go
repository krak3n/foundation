package health_test

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/health/monitor"
)

type fakeProbe struct {
	name string
	mode monitor.Mode
	fn   func(ctx context.Context) (monitor.Status, error)
}

func (p fakeProbe) Name() string { return p.name }

func (p fakeProbe) Mode() monitor.Mode {
	if p.mode == 0 {
		return monitor.AllModes
	}

	return p.mode
}

func (p fakeProbe) Status(ctx context.Context) (monitor.Status, error) { return p.fn(ctx) }

type registry []monitor.Probe

func (r registry) Seq() iter.Seq[monitor.Probe] { return slices.Values(r) }

// verboseHandler is a handler that shows its working, which most of these tests
// read: verbose output is off unless asked for.
func verboseHandler(r registry, opts ...health.HandlerOption) http.Handler {
	return health.Handler(r, append([]health.HandlerOption{health.WithVerbose()}, opts...)...)
}

func returns(status monitor.Status, err error) func(context.Context) (monitor.Status, error) {
	return func(context.Context) (monitor.Status, error) { return status, err }
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	return rec
}

func TestHandlerHealthyWhenEveryProbeIs(t *testing.T) {
	t.Parallel()

	h := verboseHandler(registry{
		fakeProbe{name: "b", fn: returns(monitor.StatusHealthy, nil)},
		fakeProbe{name: "a", fn: returns(monitor.StatusHealthy, nil)},
	})

	rec := get(h, "/healthz")

	if rec.Code != http.StatusOK || rec.Body.String() != "healthz check passed\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}

	verbose := get(h, "/healthz?verbose").Body.String()

	if want := "[+]a ok\n[+]b ok\nhealthz check passed\n"; verbose != want {
		t.Fatalf("got %q, want %q", verbose, want)
	}
}

func TestHandlerUnhealthyOutputIsUnchanged(t *testing.T) {
	t.Parallel()

	h := verboseHandler(registry{
		fakeProbe{name: "a", fn: returns(monitor.StatusUnhealthy, errors.New("connection refused"))},
		fakeProbe{name: "b", fn: returns(monitor.StatusUnhealthy, nil)},
	})

	rec := get(h, "/healthz?verbose")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}

	if want := "[-]a failed: connection refused\n[-]b failed\nhealthz check failed\n"; rec.Body.String() != want {
		t.Fatalf("got %q, want %q", rec.Body.String(), want)
	}
}

// A probe has to say it is healthy. Anything else, including an error alongside
// a healthy status and the zero Status, fails.
func TestHandlerFailsClosed(t *testing.T) {
	t.Parallel()

	tests := map[string]func(context.Context) (monitor.Status, error){
		"unknown status":              returns(monitor.StatusUnknown, nil),
		"unknown status with error":   returns(monitor.StatusUnknown, errors.New("boom")),
		"healthy status with error":   returns(monitor.StatusHealthy, errors.New("boom")),
		"a status that doesn't exist": returns(monitor.Status(99), nil),
	}

	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := verboseHandler(registry{fakeProbe{name: "p", fn: fn}})

			rec := get(h, "/healthz?verbose")

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("code = %d, want 500", rec.Code)
			}

			if !strings.Contains(rec.Body.String(), "[-]p failed") {
				t.Fatalf("expected the probe to be shown as failed, got %q", rec.Body.String())
			}
		})
	}
}

// A probe that panics is a failed probe. It must not take the process down, as
// panics in goroutines other than the handler's are not recovered by net/http.
func TestHandlerRecoversFromAPanickingProbe(t *testing.T) {
	t.Parallel()

	h := verboseHandler(registry{
		fakeProbe{name: "bad", fn: func(context.Context) (monitor.Status, error) { panic("boom") }},
		fakeProbe{name: "good", fn: returns(monitor.StatusHealthy, nil)},
	})

	rec := get(h, "/healthz?verbose")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}

	body := rec.Body.String()

	if !strings.Contains(body, "[-]bad failed") || !strings.Contains(body, "panic") || !strings.Contains(body, "[+]good ok") {
		t.Fatalf("unexpected body %q", body)
	}
}

func TestHandlerRunsOnlyTheProbesOfTheMode(t *testing.T) {
	t.Parallel()

	var live, ready atomic.Int32

	h := verboseHandler(registry{
		fakeProbe{name: "l", mode: monitor.LivenessMode, fn: func(context.Context) (monitor.Status, error) { live.Add(1); return monitor.StatusHealthy, nil }},
		fakeProbe{name: "r", mode: monitor.ReadinessMode, fn: func(context.Context) (monitor.Status, error) { ready.Add(1); return monitor.StatusHealthy, nil }},
	})

	get(h, "/livez")

	if live.Load() != 1 || ready.Load() != 0 {
		t.Fatalf("live=%d ready=%d", live.Load(), ready.Load())
	}
}

// A probe that never returns can't hold a request for ever: it is given a
// deadline, and when that passes the request fails.
func TestHandlerStopsWaitingForAHungProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		h := verboseHandler(registry{fakeProbe{name: "hung", fn: func(context.Context) (monitor.Status, error) {
			<-release // ignores its context, like a badly written probe

			return monitor.StatusHealthy, nil
		}}})

		rec := httptest.NewRecorder()
		done := make(chan struct{})

		go func() {
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz?verbose", nil))
			close(done)
		}()

		synctest.Wait()

		select {
		case <-done:
			t.Fatal("returned before the probe timeout")
		default:
		}

		time.Sleep(5*time.Second + time.Millisecond)
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("still waiting after the default probe timeout of 5s")
		}

		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "[-]hung failed") || !strings.Contains(rec.Body.String(), "timed out") {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}

		close(release)
	})
}

func TestWithProbeTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		h := verboseHandler(registry{fakeProbe{name: "slow", fn: func(context.Context) (monitor.Status, error) {
			<-release

			return monitor.StatusHealthy, nil
		}}}, health.WithProbeTimeout(time.Second))

		done := make(chan struct{})

		go func() {
			get(h, "/healthz")
			close(done)
		}()

		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("still waiting after a probe timeout of 1s")
		}

		close(release)
	})
}

// A probe that does look at its context is told when it has run out of time.
func TestHandlerGivesProbesADeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var deadline atomic.Int64

		h := verboseHandler(registry{fakeProbe{name: "p", fn: func(ctx context.Context) (monitor.Status, error) {
			d, ok := ctx.Deadline()
			if !ok {
				return monitor.StatusUnhealthy, errors.New("no deadline")
			}

			deadline.Store(int64(time.Until(d)))

			<-ctx.Done()

			return monitor.StatusUnhealthy, ctx.Err()
		}}}, health.WithProbeTimeout(2*time.Second))

		rec := get(h, "/healthz?verbose")

		if got := time.Duration(deadline.Load()); got != 2*time.Second {
			t.Fatalf("deadline = %v, want 2s", got)
		}

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d", rec.Code)
		}
	})
}

// A hundred requests arriving while a probe is running share that run, instead
// of each starting another.
func TestConcurrentRequestsShareOneProbeRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32

		release := make(chan struct{})

		h := verboseHandler(registry{fakeProbe{name: "db", fn: func(context.Context) (monitor.Status, error) {
			calls.Add(1)

			<-release

			return monitor.StatusHealthy, nil
		}}})

		var (
			wg    sync.WaitGroup
			codes = make([]int, 100)
		)

		for i := range codes {
			wg.Go(func() { codes[i] = get(h, "/healthz").Code })
		}

		synctest.Wait()

		if calls.Load() != 1 {
			t.Fatalf("the probe was started %d times while it was running, want 1", calls.Load())
		}

		close(release)
		wg.Wait()

		for i, code := range codes {
			if code != http.StatusOK {
				t.Fatalf("request %d got %d", i, code)
			}
		}

		// Once it has finished, the next request runs it again.
		release = make(chan struct{})

		close(release)

		get(h, "/healthz")

		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}
	})
}

// A probe that never returns is not started again for each request, which would
// leave a goroutine behind for every one of them.
func TestAHungProbeIsNotStartedAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32

		release := make(chan struct{})

		h := verboseHandler(registry{fakeProbe{name: "hung", fn: func(context.Context) (monitor.Status, error) {
			calls.Add(1)

			<-release

			return monitor.StatusHealthy, nil
		}}})

		for range 10 {
			if rec := get(h, "/healthz"); rec.Code != http.StatusInternalServerError {
				t.Fatalf("code = %d", rec.Code)
			}
		}

		if calls.Load() != 1 {
			t.Fatalf("the probe was started %d times, want 1", calls.Load())
		}

		close(release)
		synctest.Wait()

		// And when it does finish, it is run afresh.
		if rec := get(h, "/healthz"); rec.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("code = %d calls = %d, want 200 and 2", rec.Code, calls.Load())
		}
	})
}

// A client that has gone doesn't keep its request waiting for the probe.
func TestHandlerReturnsWhenTheRequestIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		h := verboseHandler(registry{fakeProbe{name: "slow", fn: func(context.Context) (monitor.Status, error) {
			<-release

			return monitor.StatusHealthy, nil
		}}})

		ctx, cancel := context.WithCancel(context.Background())

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx)
		done := make(chan struct{})

		go func() {
			h.ServeHTTP(httptest.NewRecorder(), req)
			close(done)
		}()

		synctest.Wait()

		cancel()
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("the request is still waiting after its context was cancelled")
		}

		close(release)
	})
}

// HandlerFunc is exported on its own, and has the same protections.
func TestHandlerFuncFailsClosed(t *testing.T) {
	t.Parallel()

	h := health.HandlerFunc(registry{fakeProbe{name: "p", fn: returns(monitor.StatusUnknown, nil)}}, monitor.KubernetesResultsMarshaler(), monitor.AllModes)

	if rec := get(h, "/healthz"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
}

var endpoints = []string{"/healthz", "/livez", "/readyz", "/startup"}

func failingRegistry() registry {
	return registry{
		fakeProbe{name: "shop/db.pool", fn: returns(monitor.StatusUnhealthy, errors.New("dial tcp 10.0.3.7:5432: connect: connection refused"))},
	}
}

// What a probe is called and why it failed is for whoever runs the service, not
// for whoever can reach the port, so the response is only the verdict unless
// verbose output is switched on.
func TestVerboseOutputIsOffByDefault(t *testing.T) {
	t.Parallel()

	h := health.Handler(failingRegistry())

	for _, endpoint := range endpoints {
		rec := get(h, endpoint+"?verbose")

		want := endpoint[1:] + " check failed\n"

		if rec.Code != http.StatusInternalServerError || rec.Body.String() != want {
			t.Errorf("%s: got %d %q, want 500 %q", endpoint, rec.Code, rec.Body.String(), want)
		}
	}

	healthy := health.Handler(registry{fakeProbe{name: "p", fn: returns(monitor.StatusHealthy, nil)}})

	if rec := get(healthy, "/healthz?verbose"); rec.Code != http.StatusOK || rec.Body.String() != "healthz check passed\n" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestWithVerboseShowsTheDetailWhenAskedFor(t *testing.T) {
	t.Parallel()

	h := health.Handler(failingRegistry(), health.WithVerbose())

	want := "[-]shop/db.pool failed: dial tcp 10.0.3.7:5432: connect: connection refused\nhealthz check failed\n"

	if rec := get(h, "/healthz?verbose"); rec.Body.String() != want {
		t.Fatalf("got %q, want %q", rec.Body.String(), want)
	}

	// It is still something a client asks for, not the default response.
	if rec := get(h, "/healthz"); rec.Body.String() != "healthz check failed\n" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestHandlerFuncTakesOptions(t *testing.T) {
	t.Parallel()

	reg := failingRegistry()

	quiet := health.HandlerFunc(reg, monitor.KubernetesResultsMarshaler(), monitor.AllModes)
	loud := health.HandlerFunc(reg, monitor.KubernetesResultsMarshaler(), monitor.AllModes, health.WithVerbose())

	if rec := get(quiet, "/healthz?verbose"); rec.Body.String() != "healthz check failed\n" {
		t.Fatalf("default: got %q", rec.Body.String())
	}

	if rec := get(loud, "/healthz?verbose"); rec.Body.String() == "healthz check failed\n" {
		t.Fatalf("with WithVerbose: got %q", rec.Body.String())
	}
}

// A response is plain text and says so, so a browser can't be tricked into
// treating an error message as markup, and nothing caches a stale verdict.
func TestResponsesHaveSafeHeaders(t *testing.T) {
	t.Parallel()

	registries := map[string]registry{
		"healthy": {fakeProbe{name: "p", fn: returns(monitor.StatusHealthy, nil)}},
		"failing": failingRegistry(),
	}

	for name, reg := range registries {
		for _, verbose := range []bool{false, true} {
			var opts []health.HandlerOption
			if verbose {
				opts = append(opts, health.WithVerbose())
			}

			h := health.Handler(reg, opts...)

			for _, endpoint := range endpoints {
				rec := get(h, endpoint+"?verbose")

				for header, want := range map[string]string{
					"Content-Type":           "text/plain; charset=utf-8",
					"X-Content-Type-Options": "nosniff",
					"Cache-Control":          "no-store",
				} {
					if got := rec.Header().Get(header); got != want {
						t.Errorf("%s %s verbose=%v: %s = %q, want %q", name, endpoint, verbose, header, got, want)
					}
				}
			}
		}
	}
}

// logged is a log entry reduced to what these tests look at.
type logged struct {
	msg   string
	attrs map[string]string
}

func loggedEntries(r *foundationtest.LogRecorder) []logged {
	var out []logged

	for _, e := range r.Entries() {
		l := logged{msg: e.Record.Message, attrs: map[string]string{}}

		for _, a := range e.Attrs {
			l.attrs[a.Key] = a.Value.String()
		}

		e.Record.Attrs(func(a slog.Attr) bool {
			l.attrs[a.Key] = a.Value.String()

			return true
		})

		out = append(out, l)
	}

	return out
}

func logsOf(r *foundationtest.LogRecorder, msg string) []map[string]string {
	var out []map[string]string

	for _, l := range loggedEntries(r) {
		if l.msg == msg {
			out = append(out, l.attrs)
		}
	}

	return out
}

// With the endpoint quiet, the reason a probe fails goes to the log, once when
// it starts failing and once when it recovers, not on every check.
func TestProbeFailuresAreLoggedOnTransitions(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var failing bool

	h := health.Handler(registry{fakeProbe{name: "shop/db.pool", fn: func(context.Context) (monitor.Status, error) {
		if failing {
			return monitor.StatusUnhealthy, errors.New("connection refused")
		}

		return monitor.StatusHealthy, nil
	}}})

	get(h, "/healthz")
	get(h, "/healthz")

	if n := len(recorder.Entries()); n != 0 {
		t.Fatalf("a healthy probe should be silent, got %d log entries", n)
	}

	failing = true

	for range 3 {
		get(h, "/healthz")
	}

	failed := logsOf(recorder, "health probe failed")

	if len(failed) != 1 {
		t.Fatalf("expected one failure logged for 3 failing checks, got %d", len(failed))
	}

	if failed[0]["probe"] != "shop/db.pool" || failed[0]["err"] != "connection refused" {
		t.Fatalf("unexpected attributes: %v", failed[0])
	}

	failing = false

	get(h, "/healthz")
	get(h, "/healthz")

	if recovered := logsOf(recorder, "health probe recovered"); len(recovered) != 1 || recovered[0]["probe"] != "shop/db.pool" {
		t.Fatalf("expected one recovery logged, got %v", recovered)
	}

	failing = true

	get(h, "/healthz")

	if failed := logsOf(recorder, "health probe failed"); len(failed) != 2 {
		t.Fatalf("a second failure should be logged again, got %d", len(failed))
	}
}

func TestAProbeThatFailsClosedIsLogged(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(registry{
		fakeProbe{name: "unknown", fn: returns(monitor.StatusUnknown, nil)},
		fakeProbe{name: "panics", fn: func(context.Context) (monitor.Status, error) { panic("boom") }},
	})

	get(h, "/healthz")

	got := map[string]string{}

	for _, a := range logsOf(recorder, "health probe failed") {
		got[a["probe"]] = a["err"]
	}

	if got["unknown"] != "status unknown" || got["panics"] != "probe panicked: boom" {
		t.Fatalf("unexpected failures logged: %v", got)
	}
}

func TestATimedOutProbeIsLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		release := make(chan struct{})

		h := health.Handler(registry{fakeProbe{name: "hung", fn: func(context.Context) (monitor.Status, error) {
			<-release

			return monitor.StatusHealthy, nil
		}}})

		for range 3 {
			get(h, "/healthz")
		}

		failed := logsOf(recorder, "health probe failed")

		if len(failed) != 1 || failed[0]["probe"] != "hung" || failed[0]["err"] != "probe timed out after 5s" {
			t.Fatalf("expected one timeout logged, got %v", failed)
		}

		close(release)
		synctest.Wait()
	})
}

func explodingProbe(context.Context) (monitor.Status, error) {
	panic("boom")
}

// A probe that panics is logged with the stack of the panic, so whoever runs the
// service can find the bug, but the stack never reaches the response, which is
// reachable by anyone who can reach the port.
func TestAPanickingProbeIsLoggedWithItsStack(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(registry{fakeProbe{name: "panics", fn: explodingProbe}})

	rec := get(h, "/healthz?verbose")

	failed := logsOf(recorder, "health probe failed")

	if len(failed) != 1 {
		t.Fatalf("expected one failure logged, got %d", len(failed))
	}

	if !strings.Contains(failed[0]["stack"], "explodingProbe") {
		t.Fatalf("stack should name the panicking function, got %q", failed[0]["stack"])
	}

	if body := rec.Body.String(); strings.Contains(body, "explodingProbe") || strings.Contains(body, "goroutine") {
		t.Fatalf("the response must not contain the stack, got %q", body)
	}
}

// Only a panic has a stack: an ordinary failure logs none.
func TestAnOrdinaryFailureHasNoStack(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(failingRegistry())

	get(h, "/healthz")

	failed := logsOf(recorder, "health probe failed")

	if len(failed) != 1 {
		t.Fatalf("expected one failure logged, got %d", len(failed))
	}

	if _, ok := failed[0]["stack"]; ok {
		t.Fatalf("an error return should not log a stack, got %q", failed[0]["stack"])
	}
}
