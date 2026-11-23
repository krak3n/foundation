//go:build unix

package yaml_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config/yaml"
)

// Opening a named pipe for reading waits for a writer. A file path that is one
// must be an error and not a hung startup.
func TestAFIFODoesNotBlockStartup(t *testing.T) {
	t.Parallel()

	fifo := filepath.Join(t.TempDir(), "fifo.yaml")

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}

	for _, check := range []bool{false, true} {
		done := make(chan error, 1)

		go func() {
			p := yaml.File(fifo)

			if check {
				done <- p.Check([]string{"svc"})

				return
			}

			done <- p.Unmarshal([]string{"svc"}, &fileConfig{})
		}()

		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("check=%v: err = %v", check, err)
			}
		case <-time.After(3 * time.Second):
			if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
				w.Close()
			}

			<-done

			t.Fatalf("check=%v: loading blocked on a FIFO", check)
		}
	}
}

func TestADeviceIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}

	if err := yaml.File("/dev/null").Unmarshal([]string{"svc"}, &fileConfig{}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
}
