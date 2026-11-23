package env_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// secretFile writes contents to a new file in a directory of its own, as a
// mounted secret would be, and returns its path.
func secretFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "secret")

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// noFile is the path of a file that doesn't exist. Reading it is an error, so a
// test that expects none shows it was never read.
func noFile(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "nope")
}

type secrets struct {
	Password string        `env:"PASSWORD,file"`
	Port     int           `env:"PORT,file"`
	Hosts    []string      `env:"HOSTS,file"`
	Wait     time.Duration `env:"WAIT,file"`
	Token    string        `env:"TOKEN"`
}

func TestAFileFieldIsLoadedFromTheFileNamedByTheFileVariable(t *testing.T) {
	t.Parallel()

	p := env.New(env.WithMap(map[string]string{
		"SVC_PASSWORD_FILE": secretFile(t, "s3cret\n"),
		"SVC_PORT_FILE":     secretFile(t, "8080\n"),
		"SVC_HOSTS_FILE":    secretFile(t, "a,b,c\n"),
		"SVC_WAIT_FILE":     secretFile(t, "1m30s"),
	}))

	var got secrets

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	want := secrets{Password: "s3cret", Port: 8080, Hosts: []string{"a", "b", "c"}, Wait: 90 * time.Second}

	if got.Password != want.Password || got.Port != want.Port || !slices.Equal(got.Hosts, want.Hosts) || got.Wait != want.Wait {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTrailingNewlinesAreRemovedFromAFile(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct{ contents, want string }{
		"none":            {"s3cret", "s3cret"},
		"unix newline":    {"s3cret\n", "s3cret"},
		"windows newline": {"s3cret\r\n", "s3cret"},
		"several":         {"s3cret\n\n\r\n", "s3cret"},
		"spaces are kept": {" s3 cret \n", " s3 cret "},
		"inner newlines":  {"line1\nline2\n", "line1\nline2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": secretFile(t, tt.contents)}))

			var got secrets

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
				t.Fatal(err)
			}

			if got.Password != tt.want {
				t.Fatalf("got %q, want %q", got.Password, tt.want)
			}
		})
	}
}

func TestAFileFieldStillReadsItsPlainVariable(t *testing.T) {
	t.Parallel()

	p := env.New(env.WithMap(map[string]string{"SVC_PASSWORD": "from-env"}))

	var got secrets

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Password != "from-env" {
		t.Fatalf("got %q", got.Password)
	}
}

func TestAnUnsetOrEmptyFileVariableIsIgnored(t *testing.T) {
	t.Parallel()

	// Reading an empty path is an error, so none means it wasn't read.
	p := env.New(env.WithMap(map[string]string{"SVC_PORT_FILE": ""}))

	got := secrets{Password: "default", Port: 80}

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Password != "default" || got.Port != 80 {
		t.Fatalf("defaults should be kept, got %+v", got)
	}
}

func TestAFileFieldWithBothVariablesSetIsAnError(t *testing.T) {
	t.Parallel()

	// The file doesn't exist, so the only error being the clash shows it was not
	// read.
	p := env.New(env.WithMap(map[string]string{"SVC_PASSWORD": "a", "SVC_PASSWORD_FILE": noFile(t)}))

	got := secrets{Password: "default"}

	errs := fieldErrors(p.Unmarshal([]string{"svc"}, &got))

	if len(errs) != 1 || errs[0].Field != "Password" || errs[0].Provider != "env" {
		t.Fatalf("expected one error for Password, got %v", errs)
	}

	if errors.Is(errs[0], fs.ErrNotExist) {
		t.Fatalf("the file should not have been read, got %v", errs[0])
	}

	msg := errs[0].Err.Error()

	if !strings.Contains(msg, "SVC_PASSWORD") || !strings.Contains(msg, "SVC_PASSWORD_FILE") {
		t.Fatalf("expected the error to name both variables, got %q", msg)
	}

	// Neither is used.
	if got.Password != "default" {
		t.Fatalf("the field should be left alone, got %q", got.Password)
	}
}

func TestAMissingFileIsAFieldErrorNamingTheFileVariable(t *testing.T) {
	t.Parallel()

	missing := noFile(t)

	p := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": missing}))

	got := secrets{Password: "default"}

	err := p.Unmarshal([]string{"svc"}, &got)

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "Password" || errs[0].Key != "SVC_PASSWORD_FILE" {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected errors.Is to find the cause, got %v", err)
	}

	if strings.Contains(err.Error(), missing) || !strings.Contains(errs[0].Err.Error(), "$SVC_PASSWORD_FILE") {
		t.Errorf("expected the message to name the variable and not the path, got %q", errs[0].Err)
	}

	if got.Password != "default" {
		t.Errorf("the field should be left alone, got %q", got.Password)
	}
}

// A directory can't be read as a file, on any platform, whoever runs the test.
func TestAnUnreadableFileIsAFieldError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	p := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": dir}))

	errs := fieldErrors(p.Unmarshal([]string{"svc"}, &secrets{}))

	if len(errs) != 1 || errs[0].Field != "Password" || errs[0].Key != "SVC_PASSWORD_FILE" {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if strings.Contains(errs[0].Error(), dir) {
		t.Fatalf("the error shows the path: %q", errs[0])
	}
}

func TestAnEmptyFileIsAValueOnlyForAPlainString(t *testing.T) {
	t.Parallel()

	p := env.New(env.WithMap(map[string]string{
		"SVC_PASSWORD_FILE": secretFile(t, "\n"),
		"SVC_PORT_FILE":     secretFile(t, ""),
	}))

	got := secrets{Password: "default", Port: 80}

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	// The same rule as for variables: empty is unset, except for a plain string.
	if got.Password != "" || got.Port != 80 {
		t.Fatalf("got %+v", got)
	}
}

// A value read from a file is a secret: a problem with it must not put it in an
// error, which will be logged.
func TestABadValueInAFileDoesNotRevealTheContents(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{"port": "hunter2", "wait": "hunter2"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := secretFile(t, contents)

			p := env.New(env.WithMap(map[string]string{"SVC_" + strings.ToUpper(name) + "_FILE": path}))

			errs := fieldErrors(p.Unmarshal([]string{"svc"}, &secrets{}))

			if len(errs) != 1 {
				t.Fatalf("expected one error, got %v", errs)
			}

			if strings.Contains(errs[0].Error(), "hunter2") {
				t.Fatalf("the error reveals the contents: %q", errs[0])
			}

			if strings.Contains(errs[0].Error(), path) || !strings.Contains(errs[0].Err.Error(), "$SVC_"+strings.ToUpper(name)+"_FILE") {
				t.Fatalf("expected the error to name the variable and not the path, got %q", errs[0].Err)
			}
		})
	}
}

func TestFileOptionRules(t *testing.T) {
	t.Parallel()

	type tls struct {
		Cert string `env:"CERT"`
	}

	type group struct {
		TLS tls `env:"TLS,file"`
	}

	errs := fieldErrors(env.New(env.WithMap(nil)).Unmarshal([]string{"svc"}, &group{}))

	if len(errs) != 1 || errs[0].Field != "TLS" || !strings.Contains(errs[0].Err.Error(), "struct") {
		t.Fatalf("expected file on a struct to be a mistake, got %v", errs)
	}

	type unknown struct {
		A string `env:"A,file,secret"`
	}

	errs = fieldErrors(env.New(env.WithMap(nil)).Unmarshal([]string{"svc"}, &unknown{}))

	if len(errs) != 1 || errs[0].Field != "A" || !strings.Contains(errs[0].Err.Error(), `"secret"`) {
		t.Fatalf("expected an unknown option to be a mistake, got %v", errs)
	}

	// Spaces around options are fine, as around names.
	type spaced struct {
		A string `env:"A, file"`
	}

	p := env.New(env.WithMap(map[string]string{"SVC_A_FILE": secretFile(t, "x")}))

	var got spaced

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.A != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestFileVariablesAreKnownToStrictMode(t *testing.T) {
	t.Parallel()

	p := env.New(env.WithMap(map[string]string{
		"SVC_PASSWORD_FILE": secretFile(t, "x"),
		"SVC_TOKEN_FILE":    "/t", // Token has no file option, so this is a typo
		"SVC_OTHER_FILE":    "/o",
	}))

	if err := p.Unmarshal([]string{"svc"}, &secrets{}); err != nil {
		t.Fatal(err)
	}

	got := unknownKeys(t, p.Check([]string{"svc"}))

	if want := []string{"SVC_OTHER_FILE", "SVC_TOKEN_FILE"}; !slices.Equal(got, want) {
		t.Fatalf("unknown = %v, want %v", got, want)
	}
}

func TestDescribeListsBothVariablesOfAFileField(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr     string `env:"ADDR"`
		Password string `env:"PASSWORD,file"`
		Port     int    `env:"PORT"`
	}

	keys, err := env.New().Describe([]string{"svc"}, &cfg{})
	if err != nil {
		t.Fatal(err)
	}

	want := []config.FieldKey{
		{Field: "Addr", Key: "SVC_ADDR", Provider: "env", Type: "string"},
		{Field: "Password", Key: "SVC_PASSWORD", Provider: "env", Type: "string"},
		{Field: "Password", Key: "SVC_PASSWORD_FILE", Provider: "env", Type: "string"},
		{Field: "Port", Key: "SVC_PORT", Provider: "env", Type: "int"},
	}

	if !slices.Equal(keys, want) {
		t.Fatalf("got %v\nwant %v", keys, want)
	}
}

// Two fields that read the same variable can't both be right, and with files it
// is easy to do by accident: CERT with the file option reads CERT_FILE, which is
// also what a field called CertFile would read. It is a mistake in the struct,
// reported on the later field, and the user must rename one.
func TestTwoFieldsReadingTheSameVariableIsAMistake(t *testing.T) {
	t.Parallel()

	type tls struct {
		CertFile string `env:"CERT_FILE"`
	}

	tests := []struct {
		name      string
		dst       any
		wantField string
		wantKey   string
		wantOther string
	}{
		{
			name: "the same name twice",
			dst: &struct {
				A string `env:"NAME"`
				B string `env:"NAME"`
			}{},
			wantField: "B", wantKey: "SVC_NAME", wantOther: "A",
		},
		{
			name: "a file variable against a plain one",
			dst: &struct {
				Cert     string `env:"CERT,file"`
				CertFile string `env:"CERT_FILE"`
			}{},
			wantField: "CertFile", wantKey: "SVC_CERT_FILE", wantOther: "Cert",
		},
		{
			name: "a plain variable against a file one, the other way round",
			dst: &struct {
				CertFile string `env:"CERT_FILE"`
				Cert     string `env:"CERT,file"`
			}{},
			wantField: "Cert", wantKey: "SVC_CERT_FILE", wantOther: "CertFile",
		},
		{
			name: "across a group",
			dst: &struct {
				TLS     tls    `env:"TLS"`
				TLSCert string `env:"TLS_CERT_FILE"`
			}{},
			wantField: "TLSCert", wantKey: "SVC_TLS_CERT_FILE", wantOther: "TLS.CertFile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := fieldErrors(env.New(env.WithMap(nil)).Unmarshal([]string{"svc"}, tt.dst))

			if len(errs) != 1 {
				t.Fatalf("expected one error, got %v", errs)
			}

			e := errs[0]

			if e.Field != tt.wantField || e.Key != tt.wantKey || e.Provider != "env" {
				t.Fatalf("got %+v", e)
			}

			msg := e.Err.Error()

			if !strings.Contains(msg, tt.wantOther) || !strings.Contains(msg, "rename") {
				t.Fatalf("expected the message to name the other field and say to rename one, got %q", msg)
			}
		})
	}
}

func TestAFieldInAConflictIsNotLoadedOrDescribed(t *testing.T) {
	t.Parallel()

	type cfg struct {
		First  string `env:"NAME"`
		Second string `env:"NAME"`
	}

	p := env.New(env.WithMap(map[string]string{"SVC_NAME": "x"}))

	var got cfg

	_ = p.Unmarshal([]string{"svc"}, &got)

	// The first keeps the variable, as the second is the mistake.
	if got.First != "x" || got.Second != "" {
		t.Fatalf("got %+v", got)
	}

	keys, err := p.Describe([]string{"svc"}, &cfg{})

	if len(fieldErrors(err)) != 1 {
		t.Fatalf("expected Describe to report the conflict too, got %v", err)
	}

	if len(keys) != 1 || keys[0].Field != "First" {
		t.Fatalf("expected only the first field to be listed, got %v", keys)
	}
}

func TestSameNamesInDifferentGroupsDoNotConflict(t *testing.T) {
	t.Parallel()

	type endpoint struct {
		Port int `env:"PORT"`
	}

	type cfg struct {
		HTTP endpoint `env:"HTTP"`
		GRPC endpoint `env:"GRPC"`
		Port int      `env:"PORT"`
	}

	if err := env.New(env.WithMap(nil)).Unmarshal([]string{"svc"}, &cfg{}); err != nil {
		t.Fatal(err)
	}
}

func passwordFrom(t *testing.T, path string) (secrets, error) {
	t.Helper()

	var got secrets

	err := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": path})).Unmarshal([]string{"svc"}, &got)

	return got, err
}

// A secret is a line or a certificate, not megabytes. A file over 1 MiB is refused
// instead of being read into memory.
func TestASecretFileOverTheLimitIsRejected(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "big")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Truncate(1<<20 + 1); err != nil {
		t.Skip(err)
	}

	f.Close()

	got, err := passwordFrom(t, path)

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "Password" || errs[0].Key != "SVC_PASSWORD_FILE" || !errors.Is(err, safefile.ErrTooLarge) {
		t.Fatalf("unexpected errors: %v", err)
	}

	if strings.Contains(errs[0].Err.Error(), path) || !strings.Contains(errs[0].Err.Error(), "$SVC_PASSWORD_FILE") || !strings.Contains(errs[0].Err.Error(), "1 MiB") {
		t.Fatalf("expected the variable and the limit, and not the path, in %q", errs[0].Err)
	}

	if got.Password != "" {
		t.Fatalf("nothing should have been loaded, got %d bytes", len(got.Password))
	}
}

func TestASecretFileOfExactlyTheLimitIsLoaded(t *testing.T) {
	t.Parallel()

	want := strings.Repeat("a", 1<<20)

	path := filepath.Join(t.TempDir(), "max")

	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := passwordFrom(t, path)
	if err != nil || got.Password != want {
		t.Fatalf("loaded %d bytes, err %v", len(got.Password), err)
	}
}

func TestASecretFileThatIsADirectoryIsRejectedAsNotAFile(t *testing.T) {
	t.Parallel()

	_, err := passwordFrom(t, t.TempDir())

	if !errors.Is(err, safefile.ErrNotRegular) || len(fieldErrors(err)) != 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretsMountedAsSymlinksAreRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	real := filepath.Join(dir, "..2026_01_02")

	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(real, "password"), []byte("from-k8s\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for link, target := range map[string]string{"..data": "..2026_01_02", "password": "..data/password"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Skip(err)
		}
	}

	got, err := passwordFrom(t, filepath.Join(dir, "password"))
	if err != nil || got.Password != "from-k8s" {
		t.Fatalf("got %q, %v", got.Password, err)
	}
}

// A variable that should hold a path can hold something else, such as the secret
// itself put in the wrong variable, and what it holds is configuration: it must
// not reach an error, which is logged. The variable's name is what an operator
// needs, and it is enough.
func TestNoErrorShowsWhatAFileVariableHolds(t *testing.T) {
	t.Parallel()

	const canary = "CANARY-hunter2"

	dir := t.TempDir()

	notAFile := filepath.Join(dir, "sub", canary)

	badValue := filepath.Join(dir, canary+"-value")
	if err := os.WriteFile(badValue, []byte("not a number"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := map[string]string{
		"a secret put in the variable": canary,
		"a path that is missing":       notAFile,
		"a directory":                  filepath.Join(dir),
		"a file with a bad value":      badValue,
		"a path with control chars":    "/run/\n" + canary + "\x1b[2J",
		"a very long path":             "/" + strings.Repeat(canary, 2000),
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := env.New(env.WithMap(map[string]string{"SVC_PORT_FILE": value, "SVC_PASSWORD_FILE": value}))

			err := p.Unmarshal([]string{"svc"}, &secrets{})
			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), dir) {
				t.Fatalf("the error shows what the variable holds: %q", err)
			}

			for _, fe := range fieldErrors(err) {
				if strings.Contains(fe.Error(), canary) || strings.Contains(fe.Key, canary) {
					t.Fatalf("the field error shows what the variable holds: %q", fe)
				}
			}
		})
	}
}
