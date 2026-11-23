package json_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
)

type describedTLS struct {
	CertFile string `json:"cert_file"`
}

type describedShared struct {
	Region string `json:"region"`
}

type described struct {
	describedShared

	Addr    string `json:"addr"`
	Skipped string `json:"-"`
	Plain   string
	TLS     describedTLS `json:"tls"`
	Port    int          `json:"port,omitempty"`
}

type describer interface {
	Describe(path []string, dst any) ([]config.FieldKey, error)
}

func describe(t *testing.T, p describer, path []string, dst any) ([]string, []string) {
	t.Helper()

	keys, err := p.Describe(path, dst)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	var fields, names []string

	for _, k := range keys {
		fields = append(fields, k.Field)
		names = append(names, k.Key)
	}

	return fields, names
}

func TestDescribeListsThePathsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	fields, names := describe(t, json.Raw(nil), []string{"svc"}, &described{})

	wantFields := []string{"Region", "Addr", "TLS.CertFile", "Port"}
	wantNames := []string{"svc.region", "svc.addr", "svc.tls.cert_file", "svc.port"}

	if !slices.Equal(fields, wantFields) || !slices.Equal(names, wantNames) {
		t.Fatalf("got fields %v keys %v\nwant fields %v keys %v", fields, names, wantFields, wantNames)
	}
}

func TestDescribeUsesTheKeysOfThePath(t *testing.T) {
	t.Parallel()

	for _, path := range [][]string{
		{"svc", "http", "server"},
		{"svc", "http.server"},
		{"svc", "http-server"},
	} {
		_, names := describe(t, json.Raw(nil), path, &describedTLS{})

		if want := []string{"svc.http.server.cert_file"}; !slices.Equal(names, want) {
			t.Errorf("path %q: got %v, want %v", path, names, want)
		}
	}
}

// Describing is about the struct and path alone: it doesn't need the document
// to exist, or to be valid.
func TestDescribeNeedsNoDocument(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]describer{
		"raw nil":         json.Raw(nil),
		"raw not json":    json.Raw([]byte("{not json")),
		"file missing":    json.File(filepath.Join(t.TempDir(), "missing.json")),
		"required absent": json.File(filepath.Join(t.TempDir(), "missing.json"), config.WithRequired()),
	} {
		_, names := describe(t, p, []string{"svc"}, &described{})

		if len(names) != 4 {
			t.Errorf("%s: got %v", name, names)
		}
	}
}

func TestDescribeDoesNotReadTheDocumentOrChangeTheStruct(t *testing.T) {
	t.Parallel()

	dst := described{Addr: "default"}

	describe(t, json.Raw([]byte(`{"svc": {"addr": ":1", "port": 9}}`)), []string{"svc"}, &dst)

	if dst.Addr != "default" || dst.Port != 0 {
		t.Fatalf("Describe changed the struct: %+v", dst)
	}
}

func TestDescribeDoesNotCountAsAskingForTheKeys(t *testing.T) {
	t.Parallel()

	p := json.Raw([]byte(`{"svc": {"addr": ":1"}}`))

	describe(t, p, []string{"svc"}, &described{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.addr"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestDescribeDoesNotOpenTheFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "config.json")

	if err := os.WriteFile(file, []byte(`{"svc": {"addr": ":1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := json.File(file)

	describe(t, p, []string{"svc"}, &described{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.addr"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestDescribeReportsMistakesInTheStructAlongsideTheFieldsThatAreFine(t *testing.T) {
	t.Parallel()

	type nested struct {
		Ch chan int `json:"ch"`
	}

	type cfg struct {
		Bad    chan int `json:"bad"`
		Nested nested   `json:"nested"`
		Ok     string   `json:"ok"`
	}

	keys, err := json.Raw(nil).Describe([]string{"svc"}, &cfg{})
	if err == nil {
		t.Fatal("expected the mistakes to be reported")
	}

	if want := []string{"Bad", "Nested.Ch"}; !slices.Equal(fieldsOf(fieldErrors(err)), want) {
		t.Fatalf("reported %v, want %v", fieldsOf(fieldErrors(err)), want)
	}

	if len(keys) != 1 || keys[0].Field != "Ok" || keys[0].Key != "svc.ok" {
		t.Fatalf("expected only the valid field, got %+v", keys)
	}
}

func TestDescribeRejectsAnInvalidDestination(t *testing.T) {
	t.Parallel()

	var nilPtr *described

	for name, dst := range map[string]any{
		"nil":         nil,
		"non pointer": described{},
		"nil pointer": nilPtr,
		"not struct":  new(int),
	} {
		_, err := json.Raw(nil).Describe([]string{"svc"}, dst)

		var invalid *json.InvalidUnmarshalError

		if !errors.As(err, &invalid) {
			t.Errorf("%s: expected an InvalidUnmarshalError, got %v", name, err)
		}
	}
}

// What Describe lists is what Unmarshal reads: a document with every listed path
// loads cleanly and leaves nothing unknown.
func TestDescribeListsExactlyWhatUnmarshalReads(t *testing.T) {
	t.Parallel()

	path := []string{"svc", "http.server"}

	type cfg struct {
		describedShared

		Name string       `json:"name"`
		TLS  describedTLS `json:"tls"`
	}

	data := `{"svc": {"http": {"server": {"region": "eu", "name": "n", "tls": {"cert_file": "c"}}}}}`

	_, names := describe(t, json.Raw([]byte(data)), path, &cfg{})

	want := []string{"svc.http.server.region", "svc.http.server.name", "svc.http.server.tls.cert_file"}

	if !slices.Equal(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}

	p := json.Raw([]byte(data))

	var loaded cfg

	if err := p.Unmarshal(path, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Region != "eu" || loaded.Name != "n" || loaded.TLS.CertFile != "c" {
		t.Fatalf("not every listed key was read: %+v", loaded)
	}

	if err := p.Check(path); err != nil {
		t.Fatalf("expected every key to be known, got %v", err)
	}
}
