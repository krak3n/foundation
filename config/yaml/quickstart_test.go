package yaml_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The program in the README is run as a real process, because what a reader does with it
// is run it: it reads the file the operator names, the environment wins over the file,
// and a mistake stops it without showing a path.

var (
	quickstartOnce sync.Once
	quickstartBin  string
	quickstartErr  error
)

// quickstart builds the example once and returns the program.
func quickstart(t *testing.T) string {
	t.Helper()

	quickstartOnce.Do(func() {
		dir, err := os.MkdirTemp("", "yaml-quickstart")
		if err != nil {
			quickstartErr = err

			return
		}

		quickstartBin = filepath.Join(dir, "quickstart")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		if out, err := exec.CommandContext(ctx, "go", "build", "-o", quickstartBin, "./example/quickstart").CombinedOutput(); err != nil {
			quickstartErr = &buildError{out: string(out)}
		}
	})

	if quickstartErr != nil {
		t.Fatalf("the quick start does not build: %v", quickstartErr)
	}

	return quickstartBin
}

type buildError struct{ out string }

func (e *buildError) Error() string { return e.out }

// runQuickstart runs the program with only the given environment and returns what it
// printed and whether it succeeded.
func runQuickstart(t *testing.T, env ...string) (string, bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, quickstart(t))
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)

	var out bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &out

	err := cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("the quick start did not finish: %s", out.String())
	}

	return out.String(), err == nil
}

func configFile(t *testing.T) string {
	t.Helper()

	abs, err := filepath.Abs("example/quickstart/config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	return abs
}

func TestTheQuickStartReadsTheFileTheOperatorNames(t *testing.T) {
	out, ok := runQuickstart(t, "SHOP_CONFIG="+configFile(t))
	if !ok {
		t.Fatalf("it failed: %s", out)
	}

	if !regexp.MustCompile(`addr["=:\s]+"?127\.0\.0\.1:9090`).MatchString(out) {
		t.Errorf("it did not use the address in the file: %s", out)
	}
}

func TestInTheQuickStartTheEnvironmentWinsOverTheFile(t *testing.T) {
	out, ok := runQuickstart(t, "SHOP_CONFIG="+configFile(t), "SHOP_API_ADDR=127.0.0.1:7070")
	if !ok {
		t.Fatalf("it failed: %s", out)
	}

	if !regexp.MustCompile(`addr["=:\s]+"?127\.0\.0\.1:7070`).MatchString(out) {
		t.Errorf("the environment did not win: %s", out)
	}
}

func TestTheQuickStartRefusesAMissingFileWithoutShowingItsPath(t *testing.T) {
	const missing = "/nonexistent-5b2c1f/secret-place/config.yaml"

	out, ok := runQuickstart(t, "SHOP_CONFIG="+missing)
	if ok {
		t.Fatalf("it started without the file it was told to read: %s", out)
	}

	if strings.Contains(out, missing) || strings.Contains(out, "secret-place") {
		t.Errorf("the output shows the path: %s", out)
	}

	if !strings.Contains(out, "$SHOP_CONFIG") {
		t.Errorf("the output does not name the variable: %s", out)
	}
}

func TestTheQuickStartListensOnNothing(t *testing.T) {
	src := read(t, "example/quickstart/main.go")

	if strings.Contains(src, "ListenAndServe") || strings.Contains(src, "net.Listen") {
		t.Error("the example opens a listener; it only loads configuration")
	}
}
