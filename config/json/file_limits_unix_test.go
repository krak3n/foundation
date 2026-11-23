//go:build unix

package json_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

func TestAConfigFileThatIsAFIFODoesNotBlockStartup(t *testing.T) {
	t.Parallel()

	fifo := filepath.Join(t.TempDir(), "fifo.json")

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}

	done := make(chan error, 1)

	go func() {
		done <- json.File(fifo).Unmarshal([]string{"svc"}, &limitConfig{})
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

func TestAConfigFileThatIsADeviceIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}

	if err := json.File("/dev/null").Unmarshal([]string{"svc"}, &limitConfig{}); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}
}
