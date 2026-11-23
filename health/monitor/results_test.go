package monitor_test

import (
	"errors"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/health/monitor"
)

func TestKubernetesResultsMarshalerReportsEachStatus(t *testing.T) {
	t.Parallel()

	m := monitor.KubernetesResultsMarshaler()

	got, err := m.MarshalResults([]monitor.Result{
		{Name: "db", Status: monitor.StatusHealthy},
		{Name: "cache", Status: monitor.StatusUnhealthy, Error: errors.New("connection refused")},
		{Name: "queue", Status: monitor.StatusUnhealthy},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "[+]db ok\n[-]cache failed: connection refused\n[-]queue failed\n"

	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A status that nothing has decided is not a pass: the handler fails closed, and
// a report that says ok for it would disagree with the response code.
func TestKubernetesResultsMarshalerDoesNotCallAnUnknownStatusOK(t *testing.T) {
	t.Parallel()

	got, err := monitor.KubernetesResultsMarshaler().MarshalResults([]monitor.Result{
		{Name: "db", Status: monitor.StatusUnknown},
		{Name: "odd", Status: monitor.Status(99)},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
		if !strings.HasPrefix(line, "[-]") || strings.Contains(line, " ok") {
			t.Errorf("line %q reports something that is not healthy as a pass", line)
		}
	}
}

// Names and errors can carry text from outside (an error from a database driver
// repeats what it was given), and the verbose output is a list of lines, so a
// newline in either must not start a line of its own.
func TestKubernetesResultsMarshalerEscapesHostileText(t *testing.T) {
	t.Parallel()

	got, err := monitor.KubernetesResultsMarshaler().MarshalResults([]monitor.Result{
		{Name: "db\n[+]forged ok", Status: monitor.StatusHealthy},
		{Name: "cache", Status: monitor.StatusUnhealthy, Error: errors.New("refused\n[+]forged ok\r\x1b[2J\u202e\u2028")},
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")

	if len(lines) != 2 {
		t.Fatalf("two results made %d lines: %q", len(lines), got)
	}

	for _, line := range lines {
		if strings.ContainsAny(line, "\r\x1b\u202e\u2028") {
			t.Errorf("line %q has a character that is not shown escaped", line)
		}
	}

	if strings.HasPrefix(lines[1], "[+]") || !strings.HasPrefix(lines[1], "[-]cache failed") {
		t.Errorf("the second line is %q", lines[1])
	}
}

func TestStatusString(t *testing.T) {
	t.Parallel()

	for status, want := range map[monitor.Status]string{
		monitor.StatusUnknown:   "unknown",
		monitor.StatusHealthy:   "healthy",
		monitor.StatusUnhealthy: "unhealthy",
		monitor.Status(99):      "unknown",
	} {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", status, got, want)
		}
	}
}
