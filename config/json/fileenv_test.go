package json_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/internal/config/confdir"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// FileFromEnv is for a program that lets whoever runs it choose the file: the
// program names the variable, in code, and the framework reads no other. An unset
// variable means no file, which is the one thing File("") can't say, since an
// empty path there is a mistake.

type fromEnvConfig struct {
	Addr string `config:"addr"`
}

// doc is a document that sets svc.addr to value.
func fromEnvDoc(value string) string {
	return `{"svc": {"addr": "` + value + `"}}`
}

func writeFromEnv(t *testing.T, dir, data string) string {
	t.Helper()

	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestFileFromEnvReadsTheFileTheVariableNames(t *testing.T) {
	path := writeFromEnv(t, t.TempDir(), fromEnvDoc("from-file"))

	t.Setenv("SHOP_CONFIG", path)

	p := json.FileFromEnv("SHOP_CONFIG")

	var got fromEnvConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "from-file" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if p.Path() != path {
		t.Errorf("Path() = %q, want %q", p.Path(), path)
	}
}

// The variable is read when the provider is made, so the provider doesn't change
// under a running program, and the same file is not read twice by accident.
func TestFileFromEnvReadsTheVariableOnce(t *testing.T) {
	dir := t.TempDir()

	first := writeFromEnv(t, dir, fromEnvDoc("first"))

	t.Setenv("SHOP_CONFIG", first)

	p := json.FileFromEnv("SHOP_CONFIG")

	t.Setenv("SHOP_CONFIG", filepath.Join(dir, "elsewhere.json"))

	var got fromEnvConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "first" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAnUnsetOrEmptyVariableMeansNoFile(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv("SHOP_CONFIG", "")
			}

			p := json.FileFromEnv("SHOP_CONFIG")

			got := fromEnvConfig{Addr: "default"}

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "default" {
				t.Fatalf("got %+v, %v", got, err)
			}

			if p.Path() != "" {
				t.Errorf("Path() = %q, want none", p.Path())
			}

			if err := p.Check([]string{"svc"}); err != nil {
				t.Errorf("Check: %v", err)
			}
		})
	}
}

// Saying the file is required means one must be named: an unset variable is a
// mistake that stops startup, naming the variable.
func TestAnUnsetVariableIsAnErrorWhenTheFileIsRequired(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv("SHOP_CONFIG", "")
			}

			err := json.FileFromEnv("SHOP_CONFIG", config.WithRequired()).Unmarshal([]string{"svc"}, &fromEnvConfig{})
			if err == nil {
				t.Fatal("expected an error")
			}

			if !strings.Contains(err.Error(), "SHOP_CONFIG") {
				t.Errorf("the error should name the variable: %v", err)
			}
		})
	}
}

func TestARequiredFileFromEnvMustExist(t *testing.T) {
	t.Setenv("SHOP_CONFIG", filepath.Join(t.TempDir(), "missing.json"))

	var got fromEnvConfig

	if err := json.FileFromEnv("SHOP_CONFIG").Unmarshal([]string{"svc"}, &got); err != nil {
		t.Errorf("an optional file that is missing should be skipped: %v", err)
	}

	if err := json.FileFromEnv("SHOP_CONFIG", config.WithRequired()).Unmarshal([]string{"svc"}, &got); err == nil {
		t.Error("a required file that is missing should be an error")
	}
}

// An empty path given in code is a bug, not an optional file: an unset variable
// passed through os.Getenv would otherwise run without its configuration.
func TestAnEmptyPathIsAnError(t *testing.T) {
	for name, p := range map[string]*json.FileProvider{
		"optional": json.File(""),
		"required": json.File("", config.WithRequired()),
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Unmarshal([]string{"svc"}, &fromEnvConfig{}); err == nil {
				t.Fatal("expected an error")
			}

			if err := p.Check([]string{"svc"}); err == nil {
				t.Fatal("expected Check to refuse it too")
			}
		})
	}
}

// The value of the variable is a path from the environment. Errors name the
// variable, not what it holds, so pointing the helper at the wrong variable can't
// put its value in a log.
func TestFileFromEnvErrorsNameTheVariableNeverItsValue(t *testing.T) {
	const canary = "canary-value-7f3a"

	tests := map[string]struct {
		prepare  func(t *testing.T, dir string) string
		required bool
		run      func(p *json.FileProvider) error
		names    bool // the error says where the problem is, so it names the variable
	}{
		"missing": {
			prepare:  func(t *testing.T, dir string) string { return filepath.Join(dir, "missing.json") },
			required: true,
			run:      func(p *json.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
			names:    true,
		},
		"a directory": {
			prepare: func(t *testing.T, dir string) string { return dir },
			run:     func(p *json.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
			names:   true,
		},
		"not a document": {
			prepare: func(t *testing.T, dir string) string { return writeFromEnv(t, dir, `{"svc": `) },
			run:     func(p *json.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
		},
		"a key nothing reads": {
			prepare: func(t *testing.T, dir string) string {
				return writeFromEnv(t, dir, `{"svc": {"addr": "x", "typo": 1}}`)
			},
			run: func(p *json.FileProvider) error {
				if err := p.Unmarshal([]string{"svc"}, &fromEnvConfig{}); err != nil {
					return err
				}

				return p.Check([]string{"svc"})
			},
			names: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), canary)

			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			t.Setenv("SHOP_CONFIG", tt.prepare(t, dir))

			var opts []config.FileOption
			if tt.required {
				opts = append(opts, config.WithRequired())
			}

			err := tt.run(json.FileFromEnv("SHOP_CONFIG", opts...))
			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error contains the value of the variable: %v", err)
			}

			if tt.names && !strings.Contains(err.Error(), "$SHOP_CONFIG") {
				t.Errorf("the error should name the variable as $SHOP_CONFIG: %v", err)
			}
		})
	}
}

// A file named in code still shows its path, as that is the program's own text.
func TestFileErrorsStillNameAFileNamedInCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	err := json.File(path, config.WithRequired()).Unmarshal([]string{"svc"}, &fromEnvConfig{})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("got %v", err)
	}
}

// The name is checked as an alias is, and whatever the value holds is never
// looked at when it is refused.
func TestFileFromEnvRefusesBadVariableNames(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "canary-secret-9c1d")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "canary-secret-9c1d")

	tests := map[string]string{
		"empty":        "",
		"lower case":   "shop_config",
		"space":        "SHOP CONFIG",
		"equals":       "A=B",
		"NUL":          "A\x00B",
		"newline":      "A\nB",
		"too long":     strings.Repeat("A", 129),
		"PATH":         "PATH",
		"HOME":         "HOME",
		"credentials":  "AWS_SECRET_ACCESS_KEY",
		"google":       "GOOGLE_APPLICATION_CREDENTIALS",
		"LD_":          "LD_PRELOAD",
		"SSH_":         "SSH_AUTH_SOCK",
		"starts digit": "1CONFIG",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			for _, opts := range [][]config.FileOption{nil, {config.WithRequired()}} {
				p := json.FileFromEnv(in, opts...)

				for what, err := range map[string]error{
					"Unmarshal": p.Unmarshal([]string{"svc"}, &fromEnvConfig{}),
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
			}
		})
	}
}

func TestFileFromEnvAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"SHOP_CONFIG", "CONFIG_FILE", "A", "X1_Y2"} {
		t.Run(name, func(t *testing.T) {
			if err := json.FileFromEnv(name).Unmarshal([]string{"svc"}, &fromEnvConfig{}); err != nil {
				t.Fatalf("an unset variable should be no file: %v", err)
			}
		})
	}
}

const documentLimit = 8 << 20

type limitConfig struct {
	Addr string `json:"addr"`
}

func writeDoc(t *testing.T, contents []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")

	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// A configuration file is a few kilobytes. One over 8 MiB is refused instead of
// being read, and decoded, in full.
func TestAConfigFileOverTheLimitIsRejected(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "big.json")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Truncate(documentLimit + 1); err != nil {
		t.Skip(err)
	}

	f.Close()

	p := json.File(path)

	err = p.Unmarshal([]string{"svc"}, &limitConfig{})

	if !errors.Is(err, safefile.ErrTooLarge) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("err = %v", err)
	}

	// Check reads the file too, and has the same limit.
	if err := p.Check([]string{"svc"}); !errors.Is(err, safefile.ErrTooLarge) {
		t.Fatalf("Check: err = %v", err)
	}
}

func TestAConfigFileOfExactlyTheLimitIsRead(t *testing.T) {
	t.Parallel()

	head, tail := `{"svc": {"addr": "`, `"}}`

	doc := head + strings.Repeat("a", documentLimit-len(head)-len(tail)) + tail

	var got limitConfig

	if err := json.File(writeDoc(t, []byte(doc))).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Addr) != documentLimit-len(head)-len(tail) {
		t.Fatalf("read %d bytes of the value", len(got.Addr))
	}
}

func TestAConfigFileThatIsADirectoryIsAnError(t *testing.T) {
	t.Parallel()

	p := json.File(t.TempDir())

	if err := p.Unmarshal([]string{"svc"}, &limitConfig{}); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("err = %v", err)
	}

	if err := p.Check([]string{"svc"}); !errors.Is(err, safefile.ErrNotRegular) {
		t.Fatalf("Check: err = %v", err)
	}
}

// A file that isn't there is skipped unless it is required, as it always was.
func TestAMissingConfigFileIsStillSkippedUnlessRequired(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nope.json")

	if err := json.File(missing).Unmarshal([]string{"svc"}, &limitConfig{}); err != nil {
		t.Fatalf("a missing file should be skipped, got %v", err)
	}

	if err := json.File(missing).Check([]string{"svc"}); err != nil {
		t.Fatalf("Check of a missing file should find nothing, got %v", err)
	}

	err := json.File(missing, config.WithRequired()).Unmarshal([]string{"svc"}, &limitConfig{})

	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a required file that is missing: err = %v", err)
	}
}

func TestAConfigFileThatIsASymlinkIsRead(t *testing.T) {
	t.Parallel()

	real := writeDoc(t, []byte(`{"svc": {"addr": ":1"}}`))
	link := filepath.Join(t.TempDir(), "link.json")

	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}

	var got limitConfig

	if err := json.File(link).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != ":1" {
		t.Fatalf("got %q, %v", got.Addr, err)
	}
}

// A provider that was asked to restart the program when what it reads changes
// (config.WithRestartOnChange) says what that is, as data, and the framework does
// the watching. One that was not asked says nothing, so it is never watched.

func sourcesOf(t *testing.T, p config.Provider) []config.Source {
	t.Helper()

	r, ok := p.(config.Restartable)
	if !ok {
		t.Fatalf("%T is not a config.Restartable", p)
	}

	return r.RestartSources()
}

func TestAFileThatAsksToRestartSaysWhatItReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc"+dirExts[0])

	got := sourcesOf(t, json.File(path, config.WithRestartOnChange()))

	want := []config.Source{{Path: path}}
	if len(got) != 1 || got[0].Path != want[0].Path || got[0].Label != "" || got[0].Dir || len(got[0].Extensions) != 0 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestADirectoryThatAsksToRestartSaysWhatItReads(t *testing.T) {
	dir := t.TempDir()

	got := sourcesOf(t, json.Dir(dir, config.WithRestartOnChange()))

	if len(got) != 1 || got[0].Path != dir || got[0].Label != "" || !got[0].Dir || !slices.Equal(got[0].Extensions, dirExts) {
		t.Fatalf("got %+v, want the directory with %v", got, dirExts)
	}
}

// A path that came from the environment is not for a log: the source carries the
// name of the variable to use instead, and the path itself is only data for the
// watch.
func TestAnEnvironmentPathIsLabelledWithTheVariable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "svc"+dirExts[0])

	t.Setenv("SHOP_CONFIG", file)
	t.Setenv("SHOP_CONFIGS", dir)

	f := sourcesOf(t, json.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange()))
	if len(f) != 1 || f[0].Path != file || f[0].Label != "$SHOP_CONFIG" || f[0].Dir {
		t.Errorf("FileFromEnv: got %+v", f)
	}

	d := sourcesOf(t, json.DirFromEnv("SHOP_CONFIGS", config.WithRestartOnChange()))
	if len(d) != 1 || d[0].Path != dir || d[0].Label != "$SHOP_CONFIGS" || !d[0].Dir || !slices.Equal(d[0].Extensions, dirExts) {
		t.Errorf("DirFromEnv: got %+v", d)
	}
}

// A variable that isn't set means there is no file. There is nothing to watch, and
// no path to make up.
func TestNothingIsWatchedForAVariableThatIsNotSet(t *testing.T) {
	t.Setenv("SHOP_CONFIG", "")

	if got := sourcesOf(t, json.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange())); len(got) != 0 {
		t.Errorf("an empty variable: got %+v", got)
	}

	if got := sourcesOf(t, json.DirFromEnv("SHOP_CONFIGS_NOT_SET", config.WithRestartOnChange())); len(got) != 0 {
		t.Errorf("a variable that is not set: got %+v", got)
	}

	if got := sourcesOf(t, json.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange(), config.WithRequired())); len(got) != 0 {
		t.Errorf("required, and not given: got %+v", got)
	}
}

// A provider that is in error reads nothing, so it watches nothing: a name that
// is not allowed for a variable never gives a path.
func TestAProviderInErrorWatchesNothing(t *testing.T) {
	t.Setenv("PATH_TO_BE_IGNORED", "/tmp/x"+dirExts[0])

	for _, name := range []string{"PATH", "HOME", "lower", "", "A B", "AWS_SECRET_ACCESS_KEY"} {
		if got := sourcesOf(t, json.FileFromEnv(name, config.WithRestartOnChange())); len(got) != 0 {
			t.Errorf("FileFromEnv(%q): got %+v", name, got)
		}

		if got := sourcesOf(t, json.DirFromEnv(name, config.WithRestartOnChange())); len(got) != 0 {
			t.Errorf("DirFromEnv(%q): got %+v", name, got)
		}
	}
}

// Not asked for means not watched, for every constructor, however else it is set up.
func TestAProviderThatDidNotAskToRestartSaysNothing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "svc"+dirExts[0])

	t.Setenv("SHOP_CONFIG", file)
	t.Setenv("SHOP_CONFIGS", dir)

	for name, p := range map[string]config.Provider{
		"File":                     json.File(file),
		"File with options":        json.File(file, config.WithRequired(), config.WithoutPermissionCheck()),
		"FileFromEnv":              json.FileFromEnv("SHOP_CONFIG"),
		"FileFromEnv with options": json.FileFromEnv("SHOP_CONFIG", config.WithRequired()),
		"Dir":                      json.Dir(dir),
		"Dir with options":         json.Dir(dir, config.WithRequired(), config.WithoutPermissionCheck()),
		"DirFromEnv":               json.DirFromEnv("SHOP_CONFIGS"),
		"DirFromEnv with options":  json.DirFromEnv("SHOP_CONFIGS", config.WithRequired()),
	} {
		if got := sourcesOf(t, p); len(got) != 0 {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// Asking to restart does not change what the provider does with its other options.
func TestRestartOnChangeIsOneOptionAmongOthers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "svc"+dirExts[0])

	got := sourcesOf(t, json.File(file, config.WithRequired(), config.WithRestartOnChange(), config.WithoutPermissionCheck()))
	if len(got) != 1 || got[0].Path != file {
		t.Errorf("got %+v", got)
	}

	// A file that must exist and isn't there is still an error: asking to watch it
	// does not make that a state the program is happy with.
	if err := json.File(file, config.WithRequired(), config.WithRestartOnChange()).Unmarshal([]string{"svc"}, &struct{}{}); err == nil {
		t.Error("a required file that is missing was not an error")
	}

	// And the permission check is still on.
	if got := config.ResolveFile(config.WithRestartOnChange()); got.NoPermissionCheck {
		t.Errorf("the permission check was turned off: %+v", got)
	}
}

// What a provider returns is its own copy: whoever changes it can't change what
// the provider reports next time.
func TestTheSourcesAreACopy(t *testing.T) {
	dir := t.TempDir()
	p := json.Dir(dir, config.WithRestartOnChange())

	first := sourcesOf(t, p)

	first[0].Path = "/changed"
	first[0].Label = "changed"
	first[0].Extensions[0] = ".changed"

	_ = append(first, config.Source{Path: "/more"})

	again := sourcesOf(t, p)
	if len(again) != 1 || again[0].Path != dir || again[0].Label != "" || !slices.Equal(again[0].Extensions, dirExts) {
		t.Errorf("the provider's report was changed from outside: %+v", again)
	}
}

// Bytes in memory can't change under the program, and have no option: there is
// nothing to say.
func TestRawIsNeverWatched(t *testing.T) {
	if _, ok := any(json.Raw([]byte("x"))).(config.Restartable); ok {
		t.Error("Raw says it can be restarted on: it holds bytes, not a file")
	}
}

// A directory is watched by the rule that reads it: every name the provider reads is
// one the filter built from the source accepts (or an edit to it would be missed),
// and the leftovers of editors, hidden files and names that can't be shown safely
// are neither read nor watched (or saving a file in an editor would restart the
// program). Checked against what the provider really does.
func TestADirectoryIsWatchedByTheRuleThatReadsIt(t *testing.T) {
	ext := dirExts[0]

	names := []string{
		"a" + ext, "00-base" + ext, "A" + ext, "a.b" + ext, "é" + ext,
		".hidden" + ext, "x" + ext + "~", "#x" + ext + "#", "x" + ext + ".swp", "README", "notes.txt",
		"x.JSON", "a\n" + ext, "a\x1b[2J" + ext, "a\u202e" + ext, " x" + ext, "x" + ext + " ",
		strings.Repeat("a", 255-len(ext)) + ext,
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			if err := os.WriteFile(filepath.Join(dir, name), []byte(dirDoc("addr", dirQ("a"))), 0o600); err != nil {
				t.Skipf("this system can't create that name: %v", err)
			}

			p := json.Dir(dir, config.WithRestartOnChange())
			src := sourcesOf(t, p)[0]

			var got struct {
				Addr string `config:"addr"`
			}

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
				// A name the provider refuses outright is not one it reads.
				got.Addr = ""
			}

			read := got.Addr == "a"
			watched := confdir.Match(name, src.Extensions...)

			if read && !watched {
				t.Errorf("%q is read by the provider and would not be watched: an edit to it would be missed", name)
			}

			if watched && !read {
				t.Errorf("%q is watched and not read: it would restart the program for nothing", name)
			}
		})
	}
}
