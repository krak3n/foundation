package health_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/health/monitor"
)

// A probe fails with whatever its dependency said, and that can carry text an
// outsider chose (a database error that quotes a query, an upstream's reply). It
// reaches the log, so it is escaped there and cut short; the response is only a
// verdict and is not affected.
func TestTheReasonAProbeFailedIsEscapedInTheLog(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(registry{fakeProbe{name: "db", fn: func(context.Context) (monitor.Status, error) {
		return monitor.StatusUnhealthy, errors.New("query failed\nlevel=ERROR msg=forged\x1b[2J\u202e canary")
	}}})

	get(h, "/healthz")

	failed := logsOf(recorder, "health probe failed")
	if len(failed) != 1 {
		t.Fatalf("%d failures were logged, want 1", len(failed))
	}

	got := failed[0]["err"]

	if !utf8.ValidString(got) {
		t.Errorf("err %q is not valid UTF-8", got)
	}

	for _, r := range got {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Errorf("err %q has %U in it", got, r)

			break
		}
	}

	for _, want := range []string{`query failed\nlevel=ERROR msg=forged`, `\x1b[2J`, `\u202e canary`} {
		if !strings.Contains(got, want) {
			t.Errorf("err = %q does not read %q", got, want)
		}
	}
}

func TestTheReasonAProbeFailedIsCutShortInTheLog(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(registry{fakeProbe{name: "db", fn: func(context.Context) (monitor.Status, error) {
		return monitor.StatusUnhealthy, errors.New(strings.Repeat("a", 1<<20))
	}}})

	get(h, "/healthz")

	failed := logsOf(recorder, "health probe failed")
	if len(failed) != 1 {
		t.Fatalf("%d failures were logged, want 1", len(failed))
	}

	if want := strings.Repeat("a", 8<<10) + "...(cut)"; failed[0]["err"] != want {
		t.Errorf("err is %d bytes, want the first %d and the mark", len(failed[0]["err"]), 8<<10)
	}
}

// An ordinary reason is logged as it is.
func TestAnOrdinaryProbeFailureIsLoggedAsItIs(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	h := health.Handler(registry{fakeProbe{name: "db", fn: func(context.Context) (monitor.Status, error) {
		return monitor.StatusUnhealthy, errors.New(`dial tcp 10.0.0.1:5432: connect: connection refused ("db")`)
	}}})

	get(h, "/healthz")

	failed := logsOf(recorder, "health probe failed")

	if len(failed) != 1 || failed[0]["err"] != `dial tcp 10.0.0.1:5432: connect: connection refused ("db")` {
		t.Errorf("unexpected log: %v", failed)
	}
}
