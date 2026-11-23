//go:build unix

// These tests are the same for config/json and config/yaml, which behave the same:
// keep the two files in step.

package json_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
)

// A configuration file, or the directory it is in, that others can write lets
// them change the service. By default a provider refuses one whose mode has the
// group-write or other-write bit, and says how to fix it. The check is Unix only:
// without mode bits there is nothing to look at.

const dirSecret = "canary-secret-perm"

// dirMode sets the mode of path, whatever the umask was.
func dirMode(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// dirTooOpen asserts err refuses a file or directory for being writable by
// others, helpfully and without anything the file holds.
func dirTooOpen(t *testing.T, what string, err error) {
	t.Helper()

	if err == nil {
		t.Errorf("%s: expected a refusal", what)

		return
	}

	msg := err.Error()

	if !strings.Contains(msg, "writable") {
		t.Errorf("%s: the error should say why: %v", what, err)
	}

	if !strings.Contains(msg, "chmod") || !strings.Contains(msg, "WithoutPermissionCheck") {
		t.Errorf("%s: the error should say how to fix it: %v", what, err)
	}

	if strings.Contains(msg, dirSecret) {
		t.Errorf("%s: the error contains what the file holds: %v", what, err)
	}
}

func dirSecretDoc() string { return dirDoc("addr", dirQ(dirSecret)) }

func TestAFileWritableByOthersIsRefused(t *testing.T) {
	for name, tt := range map[string]struct {
		mode    fs.FileMode
		refused bool
	}{
		"0600": {0o600, false},
		"0640": {0o640, false},
		"0644": {0o644, false},
		"0444": {0o444, false},
		"0660": {0o660, true},
		"0620": {0o620, true},
		"0602": {0o602, true},
		"0666": {0o666, true},
	} {
		t.Run(name, func(t *testing.T) {
			path := dirWrite(t, t.TempDir(), "config"+dirExts[0], dirSecretDoc())

			dirMode(t, path, tt.mode)

			p := json.File(path)

			var got dirConfig

			uerr := dirUnmarshal(p, &got)
			cerr := p.Check([]string{"svc"})

			if !tt.refused {
				if uerr != nil || cerr != nil || got.Addr != dirSecret {
					t.Fatalf("got %+v, %v, %v", got, uerr, cerr)
				}

				return
			}

			dirTooOpen(t, "Unmarshal", uerr)
			dirTooOpen(t, "Check", cerr)

			if got.Addr != "" {
				t.Errorf("nothing should be read from a file that is refused, got %+v", got)
			}
		})
	}
}

// The directory a file is in matters as much as the file: whoever can write there
// can swap the file for another. A sticky directory such as /tmp only lets owners
// remove their own entries.
func TestAFileInADirectoryWritableByOthersIsRefused(t *testing.T) {
	for name, tt := range map[string]struct {
		mode    fs.FileMode
		refused bool
	}{
		"0700": {0o700, false},
		"0755": {0o755, false},
		"0775": {0o775, true},
		"0757": {0o757, true},
		"0777": {0o777, true},
		"1777": {fs.ModeSticky | 0o777, false},
		"2775": {fs.ModeSetgid | 0o775, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := dirWrite(t, dir, "config"+dirExts[0], dirSecretDoc())

			dirMode(t, dir, tt.mode)

			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			p := json.File(path)

			var got dirConfig

			uerr := dirUnmarshal(p, &got)

			if !tt.refused {
				if uerr != nil || got.Addr != dirSecret {
					t.Fatalf("got %+v, %v", got, uerr)
				}

				return
			}

			dirTooOpen(t, "Unmarshal", uerr)
			dirTooOpen(t, "Check", p.Check([]string{"svc"}))
		})
	}
}

func TestAMissingFileIsNotAPermissionProblem(t *testing.T) {
	dir := t.TempDir()

	dirMode(t, dir, 0o777)

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := dirUnmarshal(json.File(filepath.Join(dir, "missing"+dirExts[0])), &dirConfig{}); err != nil {
		t.Fatalf("an optional file that is missing should be skipped: %v", err)
	}
}

// A link is followed to the file it points to, which is the one that is checked;
// the directory that holds the link is checked too, as whoever can write there
// can point it elsewhere.
func TestSymlinksAreCheckedWhereTheyPoint(t *testing.T) {
	target := dirWrite(t, t.TempDir(), "target"+dirExts[0], dirSecretDoc())

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "config"+dirExts[0])

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var got dirConfig

	if err := dirUnmarshal(json.File(link), &got); err != nil || got.Addr != dirSecret {
		t.Fatalf("a link to a safe file in a safe directory: %+v, %v", got, err)
	}

	dirMode(t, target, 0o666)

	dirTooOpen(t, "a link to a writable file", dirUnmarshal(json.File(link), &dirConfig{}))

	dirMode(t, target, 0o600)
	dirMode(t, linkDir, 0o777)

	t.Cleanup(func() { _ = os.Chmod(linkDir, 0o700) })

	dirTooOpen(t, "a link in a writable directory", dirUnmarshal(json.File(link), &dirConfig{}))
}

func TestWithoutPermissionCheckAcceptsWhatIsOtherwiseRefused(t *testing.T) {
	dir := t.TempDir()
	path := dirWrite(t, dir, "config"+dirExts[0], dirSecretDoc())

	dirMode(t, path, 0o666)
	dirMode(t, dir, 0o777)

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var got dirConfig

	p := json.File(path, config.WithoutPermissionCheck())

	if err := dirUnmarshal(p, &got); err != nil || got.Addr != dirSecret {
		t.Fatalf("File: %+v, %v", got, err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Errorf("File Check: %v", err)
	}

	got = dirConfig{}

	if err := dirUnmarshal(json.Dir(dir, config.WithoutPermissionCheck()), &got); err != nil || got.Addr != dirSecret {
		t.Fatalf("Dir: %+v, %v", got, err)
	}

	// It is an option of the provider, in code: it is not an environment variable
	// or anything a document can say.
	for _, name := range []string{"WITHOUT_PERMISSION_CHECK", "INSECURE", "ALLOW_WRITABLE", "SHOP_CONFIG_INSECURE", "CONFIG_PERMISSIONS"} {
		t.Setenv(name, "1")
	}

	dirTooOpen(t, "File", dirUnmarshal(json.File(path), &dirConfig{}))
	dirTooOpen(t, "Dir", dirUnmarshal(json.Dir(dir), &dirConfig{}))
}

func TestADirectoryWritableByOthersIsRefused(t *testing.T) {
	for name, tt := range map[string]struct {
		mode    fs.FileMode
		refused bool
	}{
		"0700": {0o700, false},
		"0755": {0o755, false},
		"0775": {0o775, true},
		"0757": {0o757, true},
		"0777": {0o777, true},
		"1777": {fs.ModeSticky | 0o777, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			dirWrite(t, dir, "a"+dirExts[0], dirSecretDoc())
			dirMode(t, dir, tt.mode)

			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			p := json.Dir(dir)

			var got dirConfig

			uerr := dirUnmarshal(p, &got)

			if !tt.refused {
				if uerr != nil || got.Addr != dirSecret {
					t.Fatalf("got %+v, %v", got, uerr)
				}

				return
			}

			dirTooOpen(t, "Unmarshal", uerr)
			dirTooOpen(t, "Check", p.Check([]string{"svc"}))

			if got.Addr != "" {
				t.Errorf("nothing should be read from a directory that is refused, got %+v", got)
			}
		})
	}
}

// One file that others can write refuses the whole directory: which of the
// settings can be trusted is not something to guess.
func TestAFileInADirectoryThatIsWritableIsRefused(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("fine")))
	loose := dirWrite(t, dir, "b"+dirExts[0], dirSecretDoc())

	dirMode(t, loose, 0o666)

	var got dirConfig

	dirTooOpen(t, "Unmarshal", dirUnmarshal(json.Dir(dir), &got))

	if got.Addr != "" {
		t.Errorf("nothing should be read from a directory with a file that is refused, got %+v", got)
	}
}

// A link in the directory is followed, and what it points to is checked.
func TestDirFollowsLinksAndChecksTheirTargets(t *testing.T) {
	dir := t.TempDir()
	target := dirWrite(t, t.TempDir(), "target"+dirExts[0], dirDoc("addr", dirQ("linked")))

	if err := os.Symlink(target, filepath.Join(dir, "a"+dirExts[0])); err != nil {
		t.Fatal(err)
	}

	var got dirConfig

	if err := dirUnmarshal(json.Dir(dir), &got); err != nil || got.Addr != "linked" {
		t.Fatalf("got %+v, %v", got, err)
	}

	dirMode(t, target, 0o666)

	dirTooOpen(t, "a link to a writable file", dirUnmarshal(json.Dir(dir), &dirConfig{}))
}

func TestFromEnvProvidersCheckPermissionsToo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "canary-dir-value")

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := dirWrite(t, dir, "config"+dirExts[0], dirSecretDoc())

	dirMode(t, path, 0o666)

	t.Setenv("SHOP_CONFIG", path)
	t.Setenv("SHOP_CONFIGS", dir)

	for what, err := range map[string]error{
		"FileFromEnv": dirUnmarshal(json.FileFromEnv("SHOP_CONFIG"), &dirConfig{}),
		"DirFromEnv":  dirUnmarshal(json.DirFromEnv("SHOP_CONFIGS"), &dirConfig{}),
	} {
		dirTooOpen(t, what, err)

		if err != nil && strings.Contains(err.Error(), "canary-dir-value") {
			t.Errorf("%s: the error contains the value of the variable: %v", what, err)
		}
	}

	if err := dirUnmarshal(json.FileFromEnv("SHOP_CONFIG"), &dirConfig{}); err == nil || !strings.Contains(err.Error(), "$SHOP_CONFIG") {
		t.Errorf("the error should name the variable: %v", err)
	}

	if err := dirUnmarshal(json.DirFromEnv("SHOP_CONFIGS"), &dirConfig{}); err == nil || !strings.Contains(err.Error(), "$SHOP_CONFIGS") {
		t.Errorf("the error should name the variable: %v", err)
	}

	for _, opt := range []config.FileOption{config.WithoutPermissionCheck()} {
		if err := dirUnmarshal(json.FileFromEnv("SHOP_CONFIG", opt), &dirConfig{}); err != nil {
			t.Errorf("FileFromEnv with the opt-out: %v", err)
		}

		if err := dirUnmarshal(json.DirFromEnv("SHOP_CONFIGS", opt), &dirConfig{}); err != nil {
			t.Errorf("DirFromEnv with the opt-out: %v", err)
		}
	}
}

// --- what is in a directory that is not an ordinary file ---

// A name that matches but is a broken link is a mistake, not a file to skip: the
// service would run on less configuration than was put there.
func TestDirRefusesABrokenLink(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("a")))

	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "b"+dirExts[0])); err != nil {
		t.Fatal(err)
	}

	err := dirUnmarshal(json.Dir(dir), &dirConfig{})
	if err == nil {
		t.Fatal("expected an error")
	}

	if !strings.Contains(err.Error(), "b"+dirExts[0]) {
		t.Errorf("the error should name the file: %v", err)
	}
}

// A link to a directory with a name that looks like a file is a directory, which
// is not read into.
func TestDirIgnoresALinkToADirectory(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()

	dirWrite(t, other, "inner"+dirExts[0], dirDoc("addr", dirQ("canary")))
	dirWrite(t, dir, "real"+dirExts[0], dirDoc("addr", dirQ("real")))

	if err := os.Symlink(other, filepath.Join(dir, "linked"+dirExts[0])); err != nil {
		t.Fatal(err)
	}

	var got dirConfig

	if err := dirUnmarshal(json.Dir(dir), &got); err != nil || got.Addr != "real" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Opening a FIFO waits for a writer, so a service would hang at startup. It is
// refused instead, without being opened.
func TestDirRefusesAFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()

	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"+dirExts[0]), 0o600); err != nil {
		t.Skipf("can't make a FIFO here: %v", err)
	}

	done := make(chan error, 1)

	go func() { done <- dirUnmarshal(json.Dir(dir), &dirConfig{}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading the directory blocked on a FIFO")
	}
}

func TestDirRefusesAFileItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}

	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("a")))

	unreadable := dirWrite(t, dir, "b"+dirExts[0], dirDoc("addr", dirQ("b")))

	dirMode(t, unreadable, 0o000)

	if err := dirUnmarshal(json.Dir(dir), &dirConfig{}); err == nil {
		t.Fatal("expected an error: the service would run on less configuration than was put there")
	}
}

// The options are the framework's, in package config, so a provider that was made
// without any of them must not be a weaker one: every constructor checks by
// default, whatever other option it was given, and only the opt-out turns the
// check off. A refactor that lost the default would be a silent fail-open for every
// provider at once.
func TestEveryConstructorChecksPermissionsByDefault(t *testing.T) {
	makers := map[string]func(t *testing.T, dir, file string, opts ...config.FileOption) config.Provider{
		"File": func(t *testing.T, dir, file string, opts ...config.FileOption) config.Provider {
			return json.File(file, opts...)
		},
		"FileFromEnv": func(t *testing.T, dir, file string, opts ...config.FileOption) config.Provider {
			t.Setenv("SHOP_CONFIG", file)

			return json.FileFromEnv("SHOP_CONFIG", opts...)
		},
		"Dir": func(t *testing.T, dir, file string, opts ...config.FileOption) config.Provider {
			return json.Dir(dir, opts...)
		},
		"DirFromEnv": func(t *testing.T, dir, file string, opts ...config.FileOption) config.Provider {
			t.Setenv("SHOP_CONFIGS", dir)

			return json.DirFromEnv("SHOP_CONFIGS", opts...)
		},
	}

	for name, build := range makers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := dirWrite(t, dir, "config"+dirExts[0], dirSecretDoc())

			dirMode(t, file, 0o666)

			for what, opts := range map[string][]config.FileOption{
				"no options":                nil,
				"a nil option":              {nil},
				"only WithRequired":         {config.WithRequired()},
				"options that are a slice":  {config.FileOptions{config.WithRequired()}},
				"an empty slice of options": {config.FileOptions{}},
				"a slice that holds a nil":  {config.FileOptions{nil, config.WithRequired()}},
				"WithRequired given twice":  {config.WithRequired(), config.WithRequired()},
			} {
				var got dirConfig

				dirTooOpen(t, what, dirUnmarshal(build(t, dir, file, opts...), &got))

				if got.Addr != "" {
					t.Errorf("%s: nothing should be read from a file that is refused, got %+v", what, got)
				}
			}

			// And the opt-out turns it off however it is given.
			for what, opts := range map[string][]config.FileOption{
				"alone":                {config.WithoutPermissionCheck()},
				"after another":        {config.WithRequired(), config.WithoutPermissionCheck()},
				"before another":       {config.WithoutPermissionCheck(), config.WithRequired()},
				"inside a slice":       {config.FileOptions{config.WithoutPermissionCheck()}},
				"beside a nil":         {nil, config.WithoutPermissionCheck()},
				"given more than once": {config.WithoutPermissionCheck(), config.WithoutPermissionCheck()},
			} {
				var got dirConfig

				if err := dirUnmarshal(build(t, dir, file, opts...), &got); err != nil || got.Addr != dirSecret {
					t.Errorf("%s: got %+v, %v", what, got, err)
				}
			}
		})
	}
}

// The opt-out belongs to the provider it was given to. There is no package level
// switch: a second provider made without it still refuses, before and after.
func TestTheOptOutIsLocalToTheProviderItWasGivenTo(t *testing.T) {
	dir := t.TempDir()
	file := dirWrite(t, dir, "config"+dirExts[0], dirSecretDoc())

	dirMode(t, file, 0o666)

	before := json.File(file)
	relaxed := json.File(file, config.WithoutPermissionCheck())
	after := json.File(file)

	if err := dirUnmarshal(relaxed, &dirConfig{}); err != nil {
		t.Fatalf("the provider with the opt-out should read the file: %v", err)
	}

	for what, p := range map[string]config.Provider{"made before": before, "made after": after, "a new one": json.File(file)} {
		dirTooOpen(t, what, dirUnmarshal(p, &dirConfig{}))
	}

	// Options are values: reusing one for a second provider does not change the
	// first, and the one that was not given it is still strict.
	opt := config.WithoutPermissionCheck()

	a := json.File(file, opt)
	b := json.File(file)

	if err := dirUnmarshal(a, &dirConfig{}); err != nil {
		t.Errorf("a: %v", err)
	}

	dirTooOpen(t, "b", dirUnmarshal(b, &dirConfig{}))
}

// The refusal names the option that would turn it off, as the program has to say
// it in code.
func TestTheRefusalNamesTheOptionInPackageConfig(t *testing.T) {
	file := dirWrite(t, t.TempDir(), "config"+dirExts[0], dirSecretDoc())

	dirMode(t, file, 0o666)

	err := dirUnmarshal(json.File(file), &dirConfig{})
	if err == nil || !strings.Contains(err.Error(), "config.WithoutPermissionCheck()") {
		t.Errorf("the error should name config.WithoutPermissionCheck(): %v", err)
	}
}
