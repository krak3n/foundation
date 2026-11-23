package decode_test

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/internal/config/decode"
)

func required(t *testing.T, dst any) ([]string, []decode.Problem) {
	t.Helper()

	return decode.Required(reflect.ValueOf(dst).Elem())
}

func TestRequiredListsUnsetFieldsInOrder(t *testing.T) {
	type cfg struct {
		Addr   string `config:"required"`
		Port   int    `config:"required"`
		Name   string `config:"required"`
		Ignore string
	}

	missing, problems := required(t, &cfg{Name: "set"})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"Addr", "Port"}; !slices.Equal(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

func TestRequiredIsSatisfiedByAnyNonZeroValue(t *testing.T) {
	zero := 0

	type cfg struct {
		Name    string            `config:"required"`
		Port    int               `config:"required"`
		On      bool              `config:"required"`
		Ratio   float64           `config:"required"`
		Wait    time.Duration     `config:"required"`
		When    time.Time         `config:"required"`
		Home    url.URL           `config:"required"`
		Tags    []string          `config:"required"`
		Labels  map[string]string `config:"required"`
		Retries *int              `config:"required"`
	}

	missing, problems := required(t, &cfg{
		Name:    "x",
		Port:    1,
		On:      true,
		Ratio:   0.5,
		Wait:    time.Second,
		When:    time.Unix(1, 0),
		Home:    url.URL{Host: "example.com"},
		Tags:    []string{"a"},
		Labels:  map[string]string{"a": "b"},
		Retries: &zero, // an explicit zero, through a pointer, is a value
	})

	if len(problems) != 0 || len(missing) != 0 {
		t.Fatalf("expected nothing missing, got missing=%v problems=%v", missing, problems)
	}
}

func TestRequiredTreatsZeroEmptyAndNilAsUnset(t *testing.T) {
	type cfg struct {
		Name    string            `config:"required"`
		Port    int               `config:"required"`
		On      bool              `config:"required"`
		Wait    time.Duration     `config:"required"`
		When    time.Time         `config:"required"`
		Home    url.URL           `config:"required"`
		Nil     []string          `config:"required"`
		Empty   []string          `config:"required"`
		NilMap  map[string]string `config:"required"`
		EmptyM  map[string]string `config:"required"`
		Retries *int              `config:"required"`
	}

	missing, _ := required(t, &cfg{Empty: []string{}, EmptyM: map[string]string{}})

	want := []string{"Name", "Port", "On", "Wait", "When", "Home", "Nil", "Empty", "NilMap", "EmptyM", "Retries"}

	if !slices.Equal(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

func TestRequiredWalksGroupsAndEmbeddedStructs(t *testing.T) {
	type tls struct {
		CertFile string `config:"required"`
		KeyFile  string `config:"required"`
	}

	type shared struct {
		Region string `config:"required"`
	}

	type cfg struct {
		shared

		Addr string `config:"required"`
		TLS  tls
		Sub  struct {
			Deep struct {
				Name string `config:"required"`
			}
		}
	}

	c := cfg{}
	c.TLS.KeyFile = "k.pem"

	missing, problems := required(t, &c)

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	// An embedded struct's fields are named as if declared in the parent, as
	// providers name them.
	want := []string{"Region", "Addr", "TLS.CertFile", "Sub.Deep.Name"}

	if !slices.Equal(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

func TestRequiredDoesNotWalkIntoValueStructs(t *testing.T) {
	type cfg struct {
		When time.Time
		Home url.URL
	}

	missing, problems := required(t, &cfg{})

	if len(missing) != 0 || len(problems) != 0 {
		t.Fatalf("time.Time and url.URL are values, not groups: missing=%v problems=%v", missing, problems)
	}
}

func TestRequiredSkipsUnexportedFields(t *testing.T) {
	type cfg struct {
		hidden string `config:"required"` //lint:ignore U1000 never read: it is here to show that an unexported field is left alone
	}

	missing, problems := required(t, &cfg{})

	if len(missing) != 0 || len(problems) != 0 {
		t.Fatalf("missing=%v problems=%v", missing, problems)
	}
}

func TestRequiredIgnoresOtherTagsAndEmptyConfigTag(t *testing.T) {
	type cfg struct {
		A string `env:"A" json:"a"`
		B string `config:""`
	}

	missing, problems := required(t, &cfg{})

	if len(missing) != 0 || len(problems) != 0 {
		t.Fatalf("missing=%v problems=%v", missing, problems)
	}
}

func TestRequiredReportsMistakesInTheTag(t *testing.T) {
	type inner struct {
		Port int `config:"port,mandatory"`
	}

	type cfg struct {
		Addr  string `config:"required,secret"`
		Inner inner
		Group inner  `config:"required"`
		Wrong string `config:"Required"`
		Also  string `config:"required"`
	}

	missing, problems := required(t, &cfg{})

	var got []string

	for _, p := range problems {
		if p.Err == nil {
			t.Errorf("problem for %s has no error", p.GoName)
		}

		got = append(got, p.GoName)
	}

	// Every mistake is reported, in declaration order, with the full path.
	if want := []string{"Addr", "Inner.Port", "Group", "Wrong"}; !slices.Equal(got, want) {
		t.Fatalf("problems for %v, want %v", got, want)
	}

	// The other required field is still checked.
	if !slices.Equal(missing, []string{"Also"}) {
		t.Fatalf("missing = %v, want [Also]", missing)
	}

	if !strings.Contains(problems[0].Err.Error(), `"secret"`) {
		t.Errorf("expected the unknown option to be named, got %q", problems[0].Err)
	}

	if !strings.Contains(problems[2].Err.Error(), "struct") {
		t.Errorf("expected required on a struct to be explained, got %q", problems[2].Err)
	}
}
