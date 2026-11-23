package safefile_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/internal/config/safefile"
)

const limit = 1 << 10

func write(t *testing.T, contents []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file")

	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// sparse makes a file of size bytes that takes no space on disk.
func sparse(t *testing.T, size int64) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sparse")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	if err := f.Truncate(size); err != nil {
		t.Skipf("can't make a sparse file here: %v", err)
	}

	return path
}

func TestReadsARegularFile(t *testing.T) {
	t.Parallel()

	got, err := safefile.ReadFile(write(t, []byte("s3cret\n")), limit)
	if err != nil || string(got) != "s3cret\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadsAnEmptyFile(t *testing.T) {
	t.Parallel()

	got, err := safefile.ReadFile(write(t, nil), limit)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAFileOfExactlyTheLimitIsRead(t *testing.T) {
	t.Parallel()

	want := bytes.Repeat([]byte("a"), limit)

	got, err := safefile.ReadFile(write(t, want), limit)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
}

func TestAFileOneByteOverTheLimitIsRejected(t *testing.T) {
	t.Parallel()

	got, err := safefile.ReadFile(write(t, bytes.Repeat([]byte("a"), limit+1)), limit)

	if !errors.Is(err, safefile.ErrTooLarge) || got != nil {
		t.Fatalf("got %d bytes, err %v", len(got), err)
	}
}

// The size is known before anything is read, so a huge file is refused at once.
func TestAHugeFileIsRejectedWithoutReadingIt(t *testing.T) {
	t.Parallel()

	path := sparse(t, 1<<40)

	start := time.Now()

	_, err := safefile.ReadFile(path, limit)

	if !errors.Is(err, safefile.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v", took)
	}
}

func TestADirectoryIsNotAFile(t *testing.T) {
	t.Parallel()

	_, err := safefile.ReadFile(t.TempDir(), limit)

	if !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}
}

func TestAMissingFileIsNotExist(t *testing.T) {
	t.Parallel()

	_, err := safefile.ReadFile(filepath.Join(t.TempDir(), "nope"), limit)

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNameThePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if _, err := safefile.ReadFile(dir, limit); err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("expected the path in %v", err)
	}
}

// A Kubernetes secret volume is a chain of symlinks: the file is a link to
// ..data/file, and ..data is a link to a timestamped directory. They are followed.
func TestSymlinksAreFollowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	real := filepath.Join(dir, "..2026_01_02")

	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(real, "password"), []byte("from-k8s"), 0o600); err != nil {
		t.Fatal(err)
	}

	for link, target := range map[string]string{"..data": "..2026_01_02", "password": "..data/password"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Skipf("symlinks aren't available: %v", err)
		}
	}

	got, err := safefile.ReadFile(filepath.Join(dir, "password"), limit)
	if err != nil || string(got) != "from-k8s" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestASymlinkToADirectoryIsRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	link := filepath.Join(dir, "link")

	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks aren't available: %v", err)
	}

	if _, err := safefile.ReadFile(link, limit); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}
}

func TestABrokenSymlinkIsNotExist(t *testing.T) {
	t.Parallel()

	link := filepath.Join(t.TempDir(), "link")

	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), link); err != nil {
		t.Skipf("symlinks aren't available: %v", err)
	}

	if _, err := safefile.ReadFile(link, limit); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// A configuration file, or the directory it is in, that anyone but its owner can
// write lets them change the service: the group-write and other-write bits are
// what TooOpen looks at. A sticky directory (like /tmp) only lets an owner
// remove or rename their own entries, so it passes.
func TestTooOpen(t *testing.T) {
	tests := map[string]struct {
		mode fs.FileMode
		want bool
	}{
		"0600":                  {0o600, false},
		"0640":                  {0o640, false},
		"0644":                  {0o644, false},
		"0444":                  {0o444, false},
		"0660 group":            {0o660, true},
		"0620 group":            {0o620, true},
		"0602 others":           {0o602, true},
		"0666":                  {0o666, true},
		"0777":                  {0o777, true},
		"dir 0700":              {fs.ModeDir | 0o700, false},
		"dir 0755":              {fs.ModeDir | 0o755, false},
		"dir 0775":              {fs.ModeDir | 0o775, true},
		"dir 0757":              {fs.ModeDir | 0o757, true},
		"dir 0777":              {fs.ModeDir | 0o777, true},
		"dir 1777 sticky":       {fs.ModeDir | fs.ModeSticky | 0o777, false},
		"dir 1775 sticky":       {fs.ModeDir | fs.ModeSticky | 0o775, false},
		"file 0666 with sticky": {fs.ModeSticky | 0o666, true},
		"setgid dir 2775":       {fs.ModeDir | fs.ModeSetgid | 0o775, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := safefile.TooOpen(tt.mode); got != tt.want {
				t.Fatalf("TooOpen(%v) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
