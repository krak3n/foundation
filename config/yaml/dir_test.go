// These tests are the same for config/json and config/yaml, which behave the same:
// keep the two files in step.

package yaml_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
	"go.krak3n.dev/foundation/internal/config/confdir"
)

// Dir reads every document in a directory, the way a conf.d directory is read:
// files of the provider's format, in name order, each overriding the ones before
// it. The program names the directory in code, or by a variable (DirFromEnv).

var dirExts = []string{".yaml", ".yml"}

// dirDoc is a document that sets svc.<key> to each value, given as key, value
// pairs; values are written as the format would write them.
func dirDoc(pairs ...string) string {
	var b strings.Builder

	b.WriteString("svc:\n")

	for i := 0; i < len(pairs); i += 2 {
		b.WriteString("  " + pairs[i] + ": " + pairs[i+1] + "\n")
	}

	return b.String()
}

// dirBadDoc is a document that doesn't parse, with a secret in it that no error
// may repeat.
func dirBadDoc(secret string) string {
	return "svc: {addr: " + strconv.Quote(secret) + "\n"
}

type dirConfig struct {
	Addr    string `config:"addr"`
	Debug   bool   `config:"debug"`
	Workers int    `config:"workers"`
}

func dirQ(s string) string { return strconv.Quote(s) }

// dirWrite writes a file of 0600 in dir and returns its path.
func dirWrite(t *testing.T, dir, name, data string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func dirUnmarshal(p config.Provider, dst any) error {
	return p.Unmarshal([]string{"svc"}, dst)
}

// The dir provider is a document, and a path of the directory, so it is in the
// document layer and the same directory given twice is noticed.
func TestDirIsADocumentWithAPath(t *testing.T) {
	var (
		_ config.DocumentLayer = (*yaml.DirProvider)(nil)
		_ config.Pathed        = (*yaml.DirProvider)(nil)
	)

	dir := t.TempDir()

	if got := yaml.Dir(dir).Path(); got != dir {
		t.Errorf("Path() = %q, want %q", got, dir)
	}
}

// Files are read in the order of their names, byte by byte, so that 00- and 10-
// prefixes mean what people expect, and later files win. Capitals sort before
// lower case.
func TestDirReadsTheFilesInNameOrder(t *testing.T) {
	dir := t.TempDir()

	// Written in an order that is not the name order.
	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("a")))
	dirWrite(t, dir, "B"+dirExts[0], dirDoc("addr", dirQ("B")))
	dirWrite(t, dir, "10"+dirExts[0], dirDoc("addr", dirQ("10")))

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil || got.Addr != "a" {
		t.Fatalf("got %+v, %v: want the last by name, a, to win", got, err)
	}
}

func TestDirMergesWhatEachFileHas(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "00-base"+dirExts[0], dirDoc("addr", dirQ(":80"), "workers", "2"))
	dirWrite(t, dir, "10-more"+dirExts[0], dirDoc("debug", "true"))

	got := dirConfig{Addr: "default"}

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil {
		t.Fatal(err)
	}

	if want := (dirConfig{Addr: ":80", Debug: true, Workers: 2}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Unlike a rule that only non-empty values override, a later file can turn a
// setting off or set it to zero.
func TestALaterFileCanSetFalseAndZero(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "00"+dirExts[0], dirDoc("debug", "true", "workers", "5", "addr", dirQ(":80")))
	dirWrite(t, dir, "10"+dirExts[0], dirDoc("debug", "false", "workers", "0", "addr", dirQ("")))

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil {
		t.Fatal(err)
	}

	if want := (dirConfig{Addr: "", Debug: false, Workers: 0}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Every extension of the format is read, so a program doesn't care which one a
// file was given.
func TestDirReadsEveryExtensionOfTheFormat(t *testing.T) {
	dir := t.TempDir()

	for i, ext := range dirExts {
		pairs := [][]string{{"addr", dirQ("set")}, {"workers", "7"}}

		if i < len(pairs) {
			dirWrite(t, dir, fmt.Sprintf("f%d%s", i, ext), dirDoc(pairs[i]...))
		}
	}

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil {
		t.Fatal(err)
	}

	if got.Addr != "set" || (len(dirExts) > 1 && got.Workers != 7) {
		t.Fatalf("got %+v: a file was not read", got)
	}
}

// Names that are not documents of the format are never opened, let alone parsed:
// swap files, backups, hidden files, other formats and the README. A directory
// that looks like a file is not read into.
func TestDirIgnoresWhatIsNotAConfigurationFile(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "real"+dirExts[0], dirDoc("addr", dirQ("real")))

	// Each of these would fail to parse, so reading one is an error.
	bad := dirBadDoc("canary-secret-1")

	for _, name := range []string{
		".hidden" + dirExts[0],
		"x" + dirExts[0] + "~",
		"#x" + dirExts[0] + "#",
		"x" + dirExts[0] + ".swp",
		"x.txt",
		"README",
		"x" + strings.ToUpper(dirExts[0]),
	} {
		dirWrite(t, dir, name, bad)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub"+dirExts[0]), 0o700); err != nil {
		t.Fatal(err)
	}

	dirWrite(t, filepath.Join(dir, "sub"+dirExts[0]), "inner"+dirExts[0], dirDoc("addr", dirQ("canary")))

	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	p := yaml.Dir(dir)

	var got dirConfig

	if err := dirUnmarshal(p, &got); err != nil || got.Addr != "real" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestOnlyDocumentsCountAgainstTheLimit(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "real"+dirExts[0], dirDoc("addr", dirQ("real")))

	for i := range confdir.MaxFiles + 5 {
		dirWrite(t, dir, fmt.Sprintf("note-%d.txt", i), "x")
	}

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil || got.Addr != "real" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestMissingAndEmptyDirectories(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	got := dirConfig{Addr: "default"}

	p := yaml.Dir(missing)

	if err := dirUnmarshal(p, &got); err != nil || got.Addr != "default" {
		t.Fatalf("an optional directory that is missing should be skipped: %+v, %v", got, err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Errorf("Check of a missing directory: %v", err)
	}

	if err := dirUnmarshal(yaml.Dir(missing, config.WithRequired()), &got); err == nil {
		t.Error("a required directory that is missing should be an error")
	}

	// A directory with nothing in it is a directory: there is just nothing to read,
	// even when it is required.
	empty := t.TempDir()

	for _, opts := range [][]config.FileOption{nil, {config.WithRequired()}} {
		if err := dirUnmarshal(yaml.Dir(empty, opts...), &got); err != nil || got.Addr != "default" {
			t.Fatalf("an empty directory: %+v, %v", got, err)
		}
	}
}

// An empty path is a bug, not a directory to skip: as for File, an unset variable
// passed through os.Getenv would otherwise run without its configuration.
func TestAnEmptyDirectoryPathIsAnError(t *testing.T) {
	for name, p := range map[string]*yaml.DirProvider{
		"optional": yaml.Dir(""),
		"required": yaml.Dir("", config.WithRequired()),
	} {
		t.Run(name, func(t *testing.T) {
			if err := dirUnmarshal(p, &dirConfig{}); err == nil {
				t.Fatal("expected an error")
			}

			if err := p.Check([]string{"svc"}); err == nil {
				t.Fatal("expected Check to refuse it too")
			}
		})
	}
}

func TestAPathThatIsAFileIsNotADirectory(t *testing.T) {
	path := dirWrite(t, t.TempDir(), "file"+dirExts[0], dirDoc("addr", dirQ("x")))

	if err := dirUnmarshal(yaml.Dir(path), &dirConfig{}); err == nil {
		t.Fatal("expected an error")
	}
}

// The directory is bounded: more files than the limit is an error, not a quiet
// truncation, and so is more bytes in all than the limit.
func TestDirRefusesTooManyFiles(t *testing.T) {
	dir := t.TempDir()

	for i := range confdir.MaxFiles {
		dirWrite(t, dir, fmt.Sprintf("%04d%s", i, dirExts[0]), dirDoc("workers", strconv.Itoa(i)))
	}

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err != nil {
		t.Fatalf("exactly the limit should be fine: %v", err)
	}

	dirWrite(t, dir, "zzzz"+dirExts[0], dirDoc("workers", "1"))

	got = dirConfig{}

	if err := dirUnmarshal(yaml.Dir(dir), &got); err == nil {
		t.Fatal("one more than the limit should be an error")
	}

	if got != (dirConfig{}) {
		t.Errorf("nothing should be read from a directory that is over the limit, got %+v", got)
	}

	if err := yaml.Dir(dir).Check([]string{"svc"}); err == nil {
		t.Error("Check should refuse it too")
	}
}

func TestDirRefusesTooManyBytes(t *testing.T) {
	dir := t.TempDir()

	const each = 7 << 20

	doc := dirDoc("addr", dirQ("x"))
	pad := strings.Repeat("\n", each-len(doc))

	for i := range confdir.MaxTotalBytes/each + 1 {
		dirWrite(t, dir, fmt.Sprintf("%d%s", i, dirExts[0]), doc+pad)
	}

	var got dirConfig

	if err := dirUnmarshal(yaml.Dir(dir), &got); err == nil {
		t.Fatal("expected an error for a directory over the total limit")
	}

	if got != (dirConfig{}) {
		t.Errorf("nothing should be read from a directory that is over the limit, got %+v", got)
	}
}

// A problem in one file is reported with its name, and what the other files say is
// still read. The error never repeats what the file holds.
func TestAProblemInOneFileNamesTheFile(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("a")))
	dirWrite(t, dir, "b-broken"+dirExts[0], dirBadDoc("canary-secret-2"))
	dirWrite(t, dir, "c"+dirExts[0], dirDoc("workers", "3"))

	var got dirConfig

	err := dirUnmarshal(yaml.Dir(dir), &got)
	if err == nil {
		t.Fatal("expected an error")
	}

	if !strings.Contains(err.Error(), "b-broken"+dirExts[0]) {
		t.Errorf("the error should name the file: %v", err)
	}

	if strings.Contains(err.Error(), "canary-secret-2") {
		t.Errorf("the error contains what the file holds: %v", err)
	}

	if got.Addr != "a" || got.Workers != 3 {
		t.Errorf("the other files should still be read, got %+v", got)
	}
}

// Check is the strict check: a key nothing asked for is reported, with the file it
// is in, and a key some file's reader asked for is known in every file.
func TestDirCheckNamesTheFileOfAnUnknownKey(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("a"), "typo", "1"))
	dirWrite(t, dir, "b"+dirExts[0], dirDoc("workers", "2", "oops", "1"))

	p := yaml.Dir(dir)

	var got dirConfig

	if err := dirUnmarshal(p, &got); err != nil {
		t.Fatal(err)
	}

	err := p.Check([]string{"svc"})
	if !errors.Is(err, config.ErrUnknown) {
		t.Fatalf("expected unknown keys, got %v", err)
	}

	for _, want := range []string{"typo", "a" + dirExts[0], "oops", "b" + dirExts[0]} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q: %v", want, err)
		}
	}

	for _, dontWant := range []string{"workers", "addr"} {
		if strings.Contains(err.Error(), dontWant) {
			t.Errorf("the error mentions %q, which is read: %v", dontWant, err)
		}
	}
}

// Describe says what would be read and reads nothing, so the directory need not
// exist.
func TestDirDescribeReadsNothing(t *testing.T) {
	d, ok := any(yaml.Dir(filepath.Join(t.TempDir(), "missing"))).(config.Describer)
	if !ok {
		t.Fatal("a directory provider should implement config.Describer")
	}

	keys, err := d.Describe([]string{"svc"}, &dirConfig{})
	if err != nil || len(keys) != 3 {
		t.Fatalf("got %v, %v", keys, err)
	}
}

// --- from the environment ---

func TestDirFromEnvReadsTheDirectoryTheVariableNames(t *testing.T) {
	dir := t.TempDir()

	dirWrite(t, dir, "a"+dirExts[0], dirDoc("addr", dirQ("from-dir")))

	t.Setenv("SHOP_CONFIGS", dir)

	p := yaml.DirFromEnv("SHOP_CONFIGS")

	// The variable is read once, when the provider is made.
	t.Setenv("SHOP_CONFIGS", filepath.Join(dir, "elsewhere"))

	var got dirConfig

	if err := dirUnmarshal(p, &got); err != nil || got.Addr != "from-dir" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if p.Path() != dir {
		t.Errorf("Path() = %q, want %q", p.Path(), dir)
	}
}

func TestAnUnsetOrEmptyDirectoryVariableMeansNoDirectory(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv("SHOP_CONFIGS", "")
			}

			p := yaml.DirFromEnv("SHOP_CONFIGS")

			got := dirConfig{Addr: "default"}

			if err := dirUnmarshal(p, &got); err != nil || got.Addr != "default" {
				t.Fatalf("got %+v, %v", got, err)
			}

			if p.Path() != "" {
				t.Errorf("Path() = %q, want none", p.Path())
			}

			if err := p.Check([]string{"svc"}); err != nil {
				t.Errorf("Check: %v", err)
			}

			err := dirUnmarshal(yaml.DirFromEnv("SHOP_CONFIGS", config.WithRequired()), &got)
			if err == nil || !strings.Contains(err.Error(), "SHOP_CONFIGS") {
				t.Errorf("a required directory should be an error naming the variable: %v", err)
			}
		})
	}
}

// What the variable holds is a path from the environment: errors name the
// variable, and the file inside it by its own name.
func TestDirFromEnvErrorsNameTheVariableNeverItsValue(t *testing.T) {
	const canary = "canary-value-5e2b"

	tests := map[string]struct {
		prepare  func(t *testing.T, dir string) string
		required bool
		run      func(p *yaml.DirProvider) error
		names    []string
	}{
		"missing": {
			prepare:  func(t *testing.T, dir string) string { return filepath.Join(dir, "missing") },
			required: true,
			run:      func(p *yaml.DirProvider) error { return dirUnmarshal(p, &dirConfig{}) },
			names:    []string{"$SHOP_CONFIGS"},
		},
		"a file": {
			prepare: func(t *testing.T, dir string) string {
				return dirWrite(t, dir, "file"+dirExts[0], dirDoc("addr", dirQ("x")))
			},
			run:   func(p *yaml.DirProvider) error { return dirUnmarshal(p, &dirConfig{}) },
			names: []string{"$SHOP_CONFIGS"},
		},
		"a broken document": {
			prepare: func(t *testing.T, dir string) string {
				dirWrite(t, dir, "broken"+dirExts[0], dirBadDoc("canary-secret-3"))

				return dir
			},
			run:   func(p *yaml.DirProvider) error { return dirUnmarshal(p, &dirConfig{}) },
			names: []string{"$SHOP_CONFIGS", "broken" + dirExts[0]},
		},
		"a key nothing reads": {
			prepare: func(t *testing.T, dir string) string {
				dirWrite(t, dir, "typo"+dirExts[0], dirDoc("addr", dirQ("x"), "typo", "1"))

				return dir
			},
			run: func(p *yaml.DirProvider) error {
				if err := dirUnmarshal(p, &dirConfig{}); err != nil {
					return err
				}

				return p.Check([]string{"svc"})
			},
			names: []string{"$SHOP_CONFIGS", "typo" + dirExts[0]},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), canary)

			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			t.Setenv("SHOP_CONFIGS", tt.prepare(t, dir))

			var opts []config.FileOption
			if tt.required {
				opts = append(opts, config.WithRequired())
			}

			err := tt.run(yaml.DirFromEnv("SHOP_CONFIGS", opts...))
			if err == nil {
				t.Fatal("expected an error")
			}

			for _, bad := range []string{canary, "canary-secret-3"} {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("the error contains %q: %v", bad, err)
				}
			}

			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error should contain %q: %v", want, err)
				}
			}
		})
	}
}

func TestDirFromEnvRefusesBadVariableNames(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "canary-secret-4")

	for name, in := range map[string]string{
		"empty":       "",
		"lower case":  "shop_configs",
		"equals":      "A=B",
		"NUL":         "A\x00B",
		"newline":     "A\nB",
		"too long":    strings.Repeat("A", 129),
		"PATH":        "PATH",
		"credentials": "AWS_SECRET_ACCESS_KEY",
		"LD_":         "LD_PRELOAD",
		"SSH_":        "SSH_AUTH_SOCK",
	} {
		t.Run(name, func(t *testing.T) {
			p := yaml.DirFromEnv(in)

			for what, err := range map[string]error{
				"Unmarshal": dirUnmarshal(p, &dirConfig{}),
				"Check":     p.Check([]string{"svc"}),
			} {
				if err == nil {
					t.Errorf("%s: %q was accepted", what, in)

					continue
				}

				if strings.Contains(err.Error(), "canary") || len(err.Error()) > 300 {
					t.Errorf("%s: unsafe error: %q", what, err.Error())
				}

				for _, r := range err.Error() {
					if r < 0x20 || r > 0x7e {
						t.Errorf("%s: the error contains %q", what, r)

						break
					}
				}
			}
		})
	}
}
