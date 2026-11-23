//go:build unix

package env_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/internal/config/safefile"
)

// A named pipe used to block startup for as long as nothing wrote to it.
func TestASecretFileThatIsAFIFODoesNotBlockStartup(t *testing.T) {
	t.Parallel()

	fifo := filepath.Join(t.TempDir(), "fifo")

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := passwordFrom(t, fifo)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, safefile.ErrNotRegular) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}

		<-done

		t.Fatal("loading blocked on a FIFO")
	}
}

func TestASecretFileThatIsADeviceIsRejected(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}

	if _, err := passwordFrom(t, "/dev/null"); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}
}
