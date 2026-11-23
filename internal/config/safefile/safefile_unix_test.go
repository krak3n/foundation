//go:build unix

package safefile_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/internal/config/safefile"
)

// Opening a FIFO for reading waits for a writer, so a FIFO must be refused before
// it is opened, not after.
func TestAFIFOIsRejectedWithoutBlocking(t *testing.T) {
	t.Parallel()

	fifo := filepath.Join(t.TempDir(), "fifo")

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("can't make a FIFO here: %v", err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := safefile.ReadFile(fifo, limit)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, safefile.ErrNotRegular) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		// Let the blocked read go, so the test doesn't leak it.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}

		<-done

		t.Fatal("ReadFile blocked on a FIFO")
	}
}

// A device is not a file with contents. /dev/null is harmless to read, which is
// why it, and not /dev/zero, is what tests a refusal.
func TestADeviceIsRejected(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}

	if _, err := safefile.ReadFile("/dev/null", limit); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}
}
