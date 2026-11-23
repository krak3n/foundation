// Package safefile reads small files that configuration points at, such as a
// secret mounted by Docker or Kubernetes, without trusting what is at the path.
//
// A path in an environment variable can name anything: a named pipe, which blocks
// whoever opens it until something writes to it, or a device such as /dev/zero,
// which never ends. ReadFile reads regular files only, up to a limit.
package safefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	// ErrNotRegular is the cause of the error for a path that is not a regular
	// file: a directory, a named pipe, a device or a socket.
	ErrNotRegular = errors.New("not a regular file")

	// ErrTooLarge is the cause of the error for a file over the limit.
	ErrTooLarge = errors.New("file is too large")

	// ErrTooOpen is the cause of the error for a file or directory that the group
	// or others can write, which [ReadConfigFile] and [CheckDir] refuse.
	ErrTooOpen = errors.New("writable by group or others")
)

// ReadFile returns the contents of the file at path, which must be a regular file
// of at most max bytes. Symbolic links are followed, as they must be for a
// Kubernetes secret volume, where each file is a link to a link.
//
// The type is checked before the file is opened, because opening a named pipe
// waits for a writer, and again on what was opened, so that a path swapped for
// something else in between is caught. The size is checked before reading, and
// again while reading, as some files report a size of zero.
//
// Errors are [*fs.PathError]s. A path that doesn't exist satisfies
// errors.Is(err, fs.ErrNotExist), and the others errors.Is(err, ErrNotRegular) or
// errors.Is(err, ErrTooLarge).
func ReadFile(path string, max int64) ([]byte, error) {
	return read(path, max, false)
}

// ReadConfigFile is [ReadFile] for a file of configuration, which whoever can
// write to it can change the service by. Unless modes is false it also refuses a
// file that the group or others can write, and a directory that holds it that
// they can write (see [TooOpen]), with an error that is [ErrTooOpen]. The mode is
// read from the file that was opened, so it can't be a different one from the one
// that is read; a file that doesn't exist is not a mode problem. Where the
// platform has no mode bits (anything but Unix) the modes are not looked at.
func ReadConfigFile(path string, max int64, modes bool) ([]byte, error) {
	return read(path, max, modes && checkModes)
}

func read(path string, max int64, modes bool) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if err := check(path, info, max); err != nil {
		return nil, err
	}

	if modes {
		if err := CheckDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer f.Close()

	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if err := check(path, opened, max); err != nil {
		return nil, err
	}

	if modes && TooOpen(opened.Mode()) {
		return nil, tooOpen(path, opened.Mode())
	}

	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > max {
		return nil, tooLarge(path, max)
	}

	return data, nil
}

// TooOpen reports whether a file or directory with this mode can be written by
// the group or by others, which is more than a file of configuration should allow.
// A sticky directory (such as /tmp) only lets an owner remove or rename their own
// entries, so it passes; the sticky bit means nothing on a file.
func TooOpen(mode fs.FileMode) bool {
	if mode.IsDir() && mode&fs.ModeSticky != 0 {
		return false
	}

	return mode.Perm()&0o022 != 0
}

// CheckDir returns an error that is [ErrTooOpen] if the directory at path, to
// which symbolic links are followed, can be written by the group or by others.
// Where the platform has no mode bits it checks nothing.
func CheckDir(path string) error {
	if !checkModes {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if TooOpen(info.Mode()) {
		return tooOpen(path, info.Mode())
	}

	return nil
}

// CheckMode returns an error that is [ErrTooOpen] if mode can be written by the
// group or by others. Where the platform has no mode bits it checks nothing.
func CheckMode(path string, mode fs.FileMode) error {
	if checkModes && TooOpen(mode) {
		return tooOpen(path, mode)
	}

	return nil
}

func tooOpen(path string, mode fs.FileMode) error {
	return &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("%w (mode %04o); run chmod go-w on it, or use config.WithoutPermissionCheck()", ErrTooOpen, mode.Perm())}
}

func check(path string, info fs.FileInfo, max int64) error {
	if !info.Mode().IsRegular() {
		return &fs.PathError{Op: "read", Path: path, Err: ErrNotRegular}
	}

	if info.Size() > max {
		return tooLarge(path, max)
	}

	return nil
}

func tooLarge(path string, max int64) error {
	return &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("%w (the limit is %s)", ErrTooLarge, size(max))}
}

// size writes a number of bytes the way a person would.
func size(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}

	return fmt.Sprintf("%d bytes", n)
}
