//go:build unix

package foundation_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The program in the README is run as a real process, because what a reader does
// with it is run it: it serves, it stops in order when it is interrupted, it
// refuses a mistake, and its configuration is what the README says.

var (
	quickstartOnce sync.Once
	quickstartBin  string
	quickstartErr  error
)

// quickstart builds the example once and returns the program.
func quickstart(t *testing.T) string {
	t.Helper()

	quickstartOnce.Do(func() {
		dir, err := os.MkdirTemp("", "quickstart")
		if err != nil {
			quickstartErr = err

			return
		}

		quickstartBin = filepath.Join(dir, "hello")

		out, err := exec.Command("go", "build", "-o", quickstartBin, "./examples/quickstart").CombinedOutput()
		if err != nil {
			quickstartErr = &buildError{string(out)}
		}
	})

	if quickstartErr != nil {
		t.Fatalf("the quick start does not build: %v", quickstartErr)
	}

	return quickstartBin
}

type buildError struct{ out string }

func (e *buildError) Error() string { return e.out }

// process is a running quick start and what it has logged.
type process struct {
	cmd    *exec.Cmd
	lines  chan string
	stdout *strings.Builder
	done   chan error
}

// start runs the program with only what the test gives it in its environment, so
// nothing of the developer's machine reaches it.
func start(t *testing.T, env []string, args ...string) *process {
	t.Helper()

	cmd := exec.Command(quickstart(t), args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	p := &process{cmd: cmd, lines: make(chan string, 1024), stdout: &strings.Builder{}, done: make(chan error, 1)}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})

	var out sync.WaitGroup

	out.Go(func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
	})

	var mu sync.Mutex

	out.Go(func() {
		b, _ := io.ReadAll(stdout)

		mu.Lock()
		p.stdout.Write(b)
		mu.Unlock()
	})

	go func() {
		out.Wait()

		p.done <- cmd.Wait()

		close(p.lines)
	}()

	return p
}

// waitFor reads the log until a line matches, or fails.
func (p *process) waitFor(t *testing.T, re *regexp.Regexp) []string {
	t.Helper()

	var seen []string

	timeout := time.After(15 * time.Second)

	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("the program ended before %q was logged:\n%s", re, strings.Join(seen, "\n"))
			}

			seen = append(seen, line)

			if re.MatchString(line) {
				return seen
			}
		case <-timeout:
			t.Fatalf("no line matched %q in 15s:\n%s", re, strings.Join(seen, "\n"))
		}
	}
}

// exit waits for the program to end and returns its status, with everything it
// logged after the point the caller had read to.
func (p *process) exit(t *testing.T) (int, []string) {
	t.Helper()

	var rest []string

	timeout := time.After(15 * time.Second)

	for {
		select {
		case line, ok := <-p.lines:
			if ok {
				rest = append(rest, line)

				continue
			}

			err := <-p.done

			if err == nil {
				return 0, rest
			}

			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), rest
			}

			t.Fatal(err)
		case <-timeout:
			t.Fatalf("the program did not end in 15s:\n%s", strings.Join(rest, "\n"))
		}
	}
}

var servingAddr = regexp.MustCompile(`serving http server.* addr=(\S+)`)

func TestTheQuickStartServesAndStopsInOrder(t *testing.T) {
	p := start(t, []string{"HELLO_HTTP_SERVER_ADDR=127.0.0.1:0"})

	seen := p.waitFor(t, servingAddr)
	addr := servingAddr.FindStringSubmatch(seen[len(seen)-1])[1]

	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("it listens on %s, want the loopback address that was configured", addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hello") {
		t.Fatalf("GET / = %d %q, want 200 and a greeting", resp.StatusCode, body)
	}

	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	code, rest := p.exit(t)

	if code != 0 {
		t.Errorf("exit status %d after Ctrl-C, want 0:\n%s", code, strings.Join(rest, "\n"))
	}

	log := strings.Join(rest, "\n")

	if !strings.Contains(log, "signal: interrupt") {
		t.Errorf("the log does not say it was asked to stop by the signal:\n%s", log)
	}

	// The connection is closed once it has stopped.
	if c, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/"); err == nil {
		c.Body.Close()

		t.Error("it still answers after it stopped")
	}
}

// A mistake in what it was given stops it before it serves, with a status and a
// reason that does not repeat what was given.
func TestTheQuickStartRefusesABadAddress(t *testing.T) {
	const secret = "canary-9d2e-not-an-address"

	p := start(t, []string{"HELLO_HTTP_SERVER_ADDR=" + secret})

	code, rest := p.exit(t)

	log := strings.Join(rest, "\n")

	if code != 1 {
		t.Errorf("exit status %d, want 1:\n%s", code, log)
	}

	if strings.Contains(log, "serving http server") {
		t.Error("it served with a bad address")
	}

	if strings.Contains(log, secret) {
		t.Errorf("the log repeats the value it was given:\n%s", log)
	}
}

// What it would do can be checked without doing it.
func TestTheQuickStartHasADryRunAndHelp(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		p := start(t, nil, "--dry-run")

		code, rest := p.exit(t)

		log := strings.Join(rest, "\n")

		if code != 0 {
			t.Errorf("exit status %d, want 0:\n%s", code, log)
		}

		if !strings.Contains(log, "dry run complete") {
			t.Errorf("the log does not say the dry run completed:\n%s", log)
		}

		if strings.Contains(log, "serving http server") {
			t.Error("a dry run started the server")
		}
	})

	t.Run("help", func(t *testing.T) {
		p := start(t, nil, "--help")

		code, _ := p.exit(t)

		if code != 0 {
			t.Errorf("exit status %d, want 0", code)
		}

		// The program has ended and its pipes are drained, so this is all of it.
		if out := p.stdout.String(); !strings.Contains(out, "--addr") {
			t.Errorf("the help does not list the address flag:\n%s", out)
		}
	})

	t.Run("a flag it does not have", func(t *testing.T) {
		p := start(t, nil, "--no-such-flag")

		if code, _ := p.exit(t); code != 1 {
			t.Errorf("exit status %d, want 1", code)
		}
	})
}
