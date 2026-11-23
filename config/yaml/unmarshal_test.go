package yaml_test

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
)

// A field is named once, with a config tag, and the YAML provider reads that
// name. A yaml tag of its own wins for this provider alone, as env and json do.

// taggedWith returns a pointer to a struct with one string field, F, whose tag is
// exactly tag, whatever bytes it holds.
func taggedWith(tag string) any {
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "F",
		Type: reflect.TypeFor[string](),
		Tag:  reflect.StructTag(tag),
	}})

	return reflect.New(typ).Interface()
}

func fieldF(dst any) string {
	return reflect.ValueOf(dst).Elem().Field(0).String()
}

func TestTheCommonNameIsTheKey(t *testing.T) {
	t.Parallel()

	var got struct {
		Addr     string `config:"addr"`
		CertFile string `config:"cert_file"`
		Port     int    `config:"port,required"`
		Dsn      string `config:"dsn,expand"`
		Both     string `config:"both,required,expand"`
	}

	doc := "svc:\n  addr: a\n  cert_file: c\n  port: 1\n  dsn: d\n  both: b\n"

	if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Addr != "a" || got.CertFile != "c" || got.Port != 1 || got.Dsn != "d" || got.Both != "b" {
		t.Fatalf("got %+v", got)
	}
}

func TestAYAMLTagWinsOverTheCommonName(t *testing.T) {
	t.Parallel()

	var got struct {
		Port int `config:"port" yaml:"portNo"`
	}

	// "port" is the common name that the yaml tag replaced, so it isn't read.
	if err := yaml.Raw([]byte("svc:\n  port: 1\n  portNo: 2\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.Port != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}

	got.Port = 0

	if err := yaml.Raw([]byte("svc:\n  port: 1\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.Port != 0 {
		t.Fatalf("the common name was read: %+v, %v", got, err)
	}
}

func TestAYAMLTagThatSkipsSkipsOnlyYAML(t *testing.T) {
	t.Parallel()

	got := struct {
		Secret string `config:"secret" yaml:"-"`
	}{Secret: "default"}

	p := yaml.Raw([]byte("svc:\n  secret: from-the-file\n"))

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Secret != "default" {
		t.Fatalf("a skipped field was read: %+v, %v", got, err)
	}

	// And a skipped field is not asked for, so strict mode would call it unknown.
	if errs := fieldErrors(p.Check([]string{"svc"})); len(errs) != 1 || errs[0].Key != "svc.secret" {
		t.Fatalf("got %v", errs)
	}

	keys, err := p.Describe([]string{"svc"}, &got)
	if err != nil || len(keys) != 0 {
		t.Fatalf("Describe: %v, %v", keys, err)
	}
}

func TestAYAMLTagWithOnlyOptionsTakesTheCommonName(t *testing.T) {
	t.Parallel()

	got := taggedWith(`config:"password" yaml:",omitempty"`)

	if err := yaml.Raw([]byte("svc:\n  password: x\n")).Unmarshal([]string{"svc"}, got); err != nil || fieldF(got) != "x" {
		t.Fatalf("got %q, %v", fieldF(got), err)
	}

	// With no name from either tag it is a mistake, not a field to be skipped.
	bad := taggedWith(`yaml:",omitempty"`)

	err := yaml.Raw([]byte("svc:\n  f: x\n")).Unmarshal([]string{"svc"}, bad)

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "F" || !strings.Contains(errs[0].Err.Error(), "no name") || fieldF(bad) != "" {
		t.Fatalf("got %q, %v", fieldF(bad), err)
	}
}

func TestOptionsInTheConfigTagAreNotNames(t *testing.T) {
	t.Parallel()

	// `config:"required"` with a yaml tag is read by the yaml tag's name, as
	// the option always meant.
	for _, tag := range []string{`yaml:"addr" config:"required"`, `yaml:"addr" config:",required"`, `yaml:"addr" config:"required,expand"`, `yaml:"addr" config:"expand,required"`} {
		got := taggedWith(tag)

		if err := yaml.Raw([]byte("svc:\n  addr: x\n")).Unmarshal([]string{"svc"}, got); err != nil || fieldF(got) != "x" {
			t.Errorf("%s: got %q, %v", tag, fieldF(got), err)
		}
	}

	// And on its own it gives no name at all.
	for _, tag := range []string{`config:"required"`, `config:",required"`, `config:"required,expand"`, `config:""`} {
		got := taggedWith(tag)

		if err := yaml.Raw([]byte("svc:\n  f: x\n  required: x\n")).Unmarshal([]string{"svc"}, got); err != nil || fieldF(got) != "" {
			t.Errorf("%s: an untagged field was read: %q, %v", tag, fieldF(got), err)
		}
	}
}

func TestGroupsAndEmbeddedStructs(t *testing.T) {
	t.Parallel()

	type shared struct {
		Region string `config:"region"`
	}

	type named struct {
		Zone string `config:"zone"`
	}

	var got struct {
		shared

		Named named `config:"named"`
		TLS   struct {
			CertFile string `config:"cert_file"`
		} `config:"tls"`
		Tagged named `config:"inner"`
	}

	doc := "svc:\n  region: eu\n  named: {zone: z}\n  tls: {cert_file: c}\n  inner: {zone: i}\n"

	if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Region != "eu" || got.Named.Zone != "z" || got.TLS.CertFile != "c" || got.Tagged.Zone != "i" {
		t.Fatalf("got %+v", got)
	}
}

func TestNamesAreMatchedWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	var got struct {
		CertFile string `config:"cert_file"`
		Port     int    `config:"PORT" yaml:"Port"`
	}

	if err := yaml.Raw([]byte("svc:\n  CERT_FILE: c\n  port: 3\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.CertFile != "c" || got.Port != 3 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Nothing is read from the Go name: a field with no tag is not in the file.
func TestAFieldWithoutATagIsNotRead(t *testing.T) {
	t.Parallel()

	got := struct {
		Plain string
		Named string `config:"named"`
	}{Plain: "default"}

	if err := yaml.Raw([]byte("svc:\n  plain: x\n  Plain: y\n  named: n\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.Plain != "default" || got.Named != "n" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Names that could be mistaken for something else, or that a file couldn't
// spell, are refused instead of read: with a problem against the field, in a form
// that is safe to log, and nothing is set.
func TestHostileNamesAreRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"requried", "Required", "REQUIRED", "requred", "require", "requires", "expnad", "expand_", "Expand",
		"a b", "a.b", "a-b", "a/b", "a:b", "a#b", "a\nb", "a\x00b", "a\x1b[31mb", "\u202eevil", "é", "1a", "_a", "a$", "{a}", "a'b", "a\"b",
		strings.Repeat("a", 65),
	} {
		for _, tag := range []string{`config:` + strconv.Quote(name), `config:` + strconv.Quote("addr,"+name)} {
			got := taggedWith(tag)

			err := yaml.Raw([]byte("svc:\n  f: x\n  addr: x\n")).Unmarshal([]string{"svc"}, got)

			errs := fieldErrors(err)

			if len(errs) == 0 || errs[0].Field != "F" {
				t.Errorf("%s: expected a problem with F, got %v", tag, err)

				continue
			}

			if fieldF(got) != "" {
				t.Errorf("%s: the field was set", tag)
			}

			msg := errs[0].Error()

			if strings.ContainsAny(msg, "\n\r\x00\x1b\u202e") || len(msg) > 400 {
				t.Errorf("%s: the problem is not safe to log: %q", tag, msg)
			}
		}
	}
}

func TestNamesThatAreValidAreRead(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a", "addr", "Addr", "cert_file", "a1", "A_1_b", "requirement", "expansion", "exp", strings.Repeat("a", 64)} {
		got := taggedWith(`config:` + strconv.Quote(name))

		if err := yaml.Raw([]byte("svc:\n  "+name+": x\n")).Unmarshal([]string{"svc"}, got); err != nil || fieldF(got) != "x" {
			t.Errorf("%s: got %q, %v", name, fieldF(got), err)
		}
	}
}

func TestAMistakeInTheConfigTagIsAProblemWhateverTheYAMLTagSays(t *testing.T) {
	t.Parallel()

	got := taggedWith(`config:"requried" yaml:"addr"`)

	err := yaml.Raw([]byte("svc:\n  addr: x\n")).Unmarshal([]string{"svc"}, got)

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "F" || fieldF(got) != "" {
		t.Fatalf("got %q, %v", fieldF(got), err)
	}
}

// Two fields that read the same key can't both be right, whatever the case,
// and the later one is the mistake.
func TestFieldsThatReadTheSameKeyAreAProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  any
	}{
		{"the same name", &struct {
			A string `config:"addr"`
			B string `config:"addr"`
		}{}},
		{"another case", &struct {
			A string `config:"addr"`
			B string `config:"ADDR"`
		}{}},
		{"a yaml tag and a common name", &struct {
			A string `yaml:"addr"`
			B string `config:"Addr"`
		}{}},
		{"two yaml tags", &struct {
			A string `yaml:"addr"`
			B string `yaml:"ADDR"`
		}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := yaml.Raw([]byte("svc:\n  addr: x\n")).Unmarshal([]string{"svc"}, tt.dst)

			errs := fieldErrors(err)

			if len(errs) != 1 || errs[0].Field != "B" || !strings.Contains(errs[0].Err.Error(), "A") || errs[0].Key == "" {
				t.Fatalf("expected one problem with B naming A, got %v", err)
			}

			if v := reflect.ValueOf(tt.dst).Elem(); v.Field(1).String() != "" {
				t.Fatalf("the later field was set")
			}
		})
	}
}

// A field that another provider skips can still collide here: the files are read
// by name whatever the environment does.
func TestAFieldOtherProvidersSkipStillCollidesInYAML(t *testing.T) {
	t.Parallel()

	err := yaml.Raw(nil).Unmarshal([]string{"svc"}, &struct {
		A string `config:"addr"`
		B string `config:"addr" env:"-"`
	}{})

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "B" {
		t.Fatalf("got %v", err)
	}
}

type described struct {
	Addr    string `config:"addr"`
	Skipped string `config:"skipped" yaml:"-"`
	Plain   string
	TLS     struct {
		CertFile string `config:"cert_file"`
	} `config:"tls"`
	Port int `config:"port" yaml:"portNo"`
	Opt  int `config:"opt" yaml:",omitempty"`
}

func TestDescribeUsesTheNamesTheFieldsAreReadBy(t *testing.T) {
	t.Parallel()

	keys, err := yaml.Raw(nil).Describe([]string{"svc", "http.server"}, &described{})
	if err != nil {
		t.Fatal(err)
	}

	var fields, names []string

	for _, k := range keys {
		fields = append(fields, k.Field)
		names = append(names, k.Key)

		if k.Provider != "yaml" {
			t.Errorf("%s: Provider = %q", k.Field, k.Provider)
		}
	}

	if want := []string{"Addr", "TLS.CertFile", "Port", "Opt"}; !slices.Equal(fields, want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}

	if want := []string{"svc.http.server.addr", "svc.http.server.tls.cert_file", "svc.http.server.portNo", "svc.http.server.opt"}; !slices.Equal(names, want) {
		t.Errorf("keys = %v, want %v", names, want)
	}
}

func TestDescribeReadsNothingAndNeedsNoFile(t *testing.T) {
	t.Parallel()

	// The document is garbage and the file doesn't exist, and neither matters.
	for _, p := range []interface {
		Describe([]string, any) ([]config.FieldKey, error)
	}{yaml.Raw([]byte("&a *a !x [")), yaml.File("/nonexistent/config.yaml")} {
		if _, err := p.Describe([]string{"svc"}, &described{}); err != nil {
			t.Fatalf("%T: %v", p, err)
		}
	}
}

func TestDescribeReportsMistakesInTheStruct(t *testing.T) {
	t.Parallel()

	_, err := yaml.Raw(nil).Describe([]string{"svc"}, &struct {
		C chan int `config:"c"`
		D string   `config:"requried"`
	}{})

	if !slices.Equal(fieldsOf(fieldErrors(err)), []string{"C", "D"}) {
		t.Fatalf("got %v", err)
	}
}

func TestDescribeCarriesTheHelpTag(t *testing.T) {
	t.Parallel()

	keys, err := yaml.Raw(nil).Describe([]string{"svc"}, &struct {
		Addr string `config:"addr" flag:"addr" help:"Where to listen."`
	}{})
	if err != nil || len(keys) != 1 || keys[0].Help != "Where to listen." || keys[0].Type == "" {
		t.Fatalf("got %+v, %v", keys, err)
	}
}
