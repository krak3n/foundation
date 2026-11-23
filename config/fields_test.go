package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// The `config` tag gives a field one name for every provider that reads names:
// the environment uppercases it, and a file uses it as the key. A provider's own
// tag still wins, and a provider that has none reads the common name.

var commonPath = []string{"svc"}

// taggedWith returns a pointer to a struct with one string field, F, whose config
// tag is exactly tag, whatever bytes it holds.
func taggedWith(tag string) any {
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "F",
		Type: reflect.TypeFor[string](),
		Tag:  reflect.StructTag("config:" + strconv.Quote(tag)),
	}})

	return reflect.New(typ).Interface()
}

func fieldF(dst any) string {
	return reflect.ValueOf(dst).Elem().Field(0).String()
}

func problemFields(err error) []string {
	var out []string

	var walk func(error)

	walk = func(e error) {
		if e == nil {
			return
		}

		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range j.Unwrap() {
				walk(c)
			}

			return
		}

		var fe *config.FieldError
		if errors.As(e, &fe) {
			out = append(out, fe.Field)
		}
	}

	walk(err)

	return out
}

type commonTLS struct {
	CertFile string `config:"cert_file"`
}

type commonShared struct {
	Region string `config:"region"`
}

type commonConfig struct {
	commonShared

	Addr        string        `config:"addr"`
	ReadTimeout time.Duration `config:"readTimeout"`
	Retries     *int          `config:"retries"`
	Tags        []string      `config:"tags"`
	TLS         commonTLS     `config:"tls"`
}

// One tag, and the environment and a file agree on what it means.
func TestCommonNameIsReadByEveryProvider(t *testing.T) {
	t.Parallel()

	vars := map[string]string{
		"SVC_ADDR":          ":1",
		"SVC_READTIMEOUT":   "30s",
		"SVC_RETRIES":       "3",
		"SVC_TAGS":          "a,b",
		"SVC_TLS_CERT_FILE": "c.pem",
		"SVC_REGION":        "eu",
	}

	doc := `{"svc": {"addr": ":1", "readTimeout": "30s", "retries": 3, "tags": ["a", "b"],
		"tls": {"cert_file": "c.pem"}, "region": "eu"}}`

	var fromEnv, fromJSON commonConfig

	if err := env.New(lookupMap(vars)).Unmarshal(commonPath, &fromEnv); err != nil {
		t.Fatalf("env: %v", err)
	}

	if err := json.Raw([]byte(doc)).Unmarshal(commonPath, &fromJSON); err != nil {
		t.Fatalf("json: %v", err)
	}

	// Guard against both being empty.
	if fromEnv.Addr != ":1" || fromEnv.TLS.CertFile != "c.pem" || fromEnv.Region != "eu" || fromEnv.Retries == nil {
		t.Fatalf("env did not read the common names: %+v", fromEnv)
	}

	if !reflect.DeepEqual(fromEnv, fromJSON) {
		t.Fatalf("providers disagree\n env: %+v\njson: %+v", fromEnv, fromJSON)
	}
}

// A struct with a common name is a group for the providers that have no tag of
// their own, and so is an embedded struct that has one; one without is flattened.
func TestCommonNameOfAGroupAndAnEmbeddedStruct(t *testing.T) {
	t.Parallel()

	type named struct {
		commonShared `config:"shared"`

		TLS commonTLS `config:"tls"`
	}

	var fromEnv, fromJSON named

	vars := map[string]string{"SVC_SHARED_REGION": "eu", "SVC_TLS_CERT_FILE": "c.pem"}

	if err := env.New(lookupMap(vars)).Unmarshal(commonPath, &fromEnv); err != nil {
		t.Fatalf("env: %v", err)
	}

	doc := `{"svc": {"shared": {"region": "eu"}, "tls": {"cert_file": "c.pem"}}}`

	if err := json.Raw([]byte(doc)).Unmarshal(commonPath, &fromJSON); err != nil {
		t.Fatalf("json: %v", err)
	}

	if fromEnv.Region != "eu" || fromEnv.TLS.CertFile != "c.pem" || !reflect.DeepEqual(fromEnv, fromJSON) {
		t.Fatalf("env %+v, json %+v", fromEnv, fromJSON)
	}
}

// A provider's own tag wins, and "-" leaves a field out of that provider only.
func TestAProviderTagWinsOverTheCommonName(t *testing.T) {
	t.Parallel()

	t.Run("env tag", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Host string `config:"host" env:"HOSTNAME"`
		}

		var fromEnv, fromJSON cfg

		vars := map[string]string{"SVC_HOSTNAME": "named", "SVC_HOST": "common"}

		if err := env.New(lookupMap(vars)).Unmarshal(commonPath, &fromEnv); err != nil {
			t.Fatal(err)
		}

		if err := json.Raw([]byte(`{"svc": {"host": "file"}}`)).Unmarshal(commonPath, &fromJSON); err != nil {
			t.Fatal(err)
		}

		if fromEnv.Host != "named" || fromJSON.Host != "file" {
			t.Fatalf("env read %q, json read %q", fromEnv.Host, fromJSON.Host)
		}
	})

	t.Run("json tag", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Host string `config:"host" json:"hostName"`
		}

		var fromEnv, fromJSON cfg

		if err := env.New(lookupMap(map[string]string{"SVC_HOST": "env"})).Unmarshal(commonPath, &fromEnv); err != nil {
			t.Fatal(err)
		}

		if err := json.Raw([]byte(`{"svc": {"hostName": "named", "host": "common"}}`)).Unmarshal(commonPath, &fromJSON); err != nil {
			t.Fatal(err)
		}

		if fromEnv.Host != "env" || fromJSON.Host != "named" {
			t.Fatalf("env read %q, json read %q", fromEnv.Host, fromJSON.Host)
		}
	})

	t.Run("env skip", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Host string `config:"host" env:"-"`
		}

		var fromEnv, fromJSON cfg

		if err := env.New(lookupMap(map[string]string{"SVC_HOST": "env"})).Unmarshal(commonPath, &fromEnv); err != nil {
			t.Fatal(err)
		}

		if err := json.Raw([]byte(`{"svc": {"host": "file"}}`)).Unmarshal(commonPath, &fromJSON); err != nil {
			t.Fatal(err)
		}

		if fromEnv.Host != "" || fromJSON.Host != "file" {
			t.Fatalf("env read %q, json read %q", fromEnv.Host, fromJSON.Host)
		}
	})

	t.Run("json skip", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Host string `config:"host" json:"-"`
		}

		var fromEnv, fromJSON cfg

		if err := env.New(lookupMap(map[string]string{"SVC_HOST": "env"})).Unmarshal(commonPath, &fromEnv); err != nil {
			t.Fatal(err)
		}

		if err := json.Raw([]byte(`{"svc": {"host": "file"}}`)).Unmarshal(commonPath, &fromJSON); err != nil {
			t.Fatal(err)
		}

		if fromEnv.Host != "env" || fromJSON.Host != "" {
			t.Fatalf("env read %q, json read %q", fromEnv.Host, fromJSON.Host)
		}
	})
}

// A provider tag that is only options takes the name from the common one.
func TestAProviderTagWithOnlyOptionsTakesTheCommonName(t *testing.T) {
	t.Parallel()

	t.Run("alias", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Port int `config:"port" env:",alias=PORT"`
		}

		var got cfg

		if err := env.New(lookupMap(map[string]string{"PORT": "9"})).Unmarshal(commonPath, &got); err != nil {
			t.Fatal(err)
		}

		if got.Port != 9 {
			t.Fatalf("the alias was not read: %+v", got)
		}

		// The prefixed name still wins over the alias.
		got = cfg{}

		if err := env.New(lookupMap(map[string]string{"PORT": "9", "SVC_PORT": "7"})).Unmarshal(commonPath, &got); err != nil {
			t.Fatal(err)
		}

		if got.Port != 7 {
			t.Fatalf("the prefixed variable should win: %+v", got)
		}
	})

	t.Run("file", func(t *testing.T) {
		t.Parallel()

		type cfg struct {
			Password string `config:"password" env:",file" json:"-"`
		}

		path := filepath.Join(t.TempDir(), "password")

		if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		var fromEnv, fromJSON cfg

		if err := env.New(lookupMap(map[string]string{"SVC_PASSWORD_FILE": path})).Unmarshal(commonPath, &fromEnv); err != nil {
			t.Fatal(err)
		}

		// A secret that is kept out of files is not read from one.
		if err := json.Raw([]byte(`{"svc": {"password": "from-file"}}`)).Unmarshal(commonPath, &fromJSON); err != nil {
			t.Fatal(err)
		}

		if fromEnv.Password != "s3cret" || fromJSON.Password != "" {
			t.Fatalf("env read %q, json read %q", fromEnv.Password, fromJSON.Password)
		}
	})
}

// With no name from anywhere there is nothing to read, and that is a mistake.
func TestAnOptionsOnlyProviderTagWithoutAnyNameIsAProblem(t *testing.T) {
	t.Parallel()

	for _, dst := range []any{
		&struct {
			Password string `env:",file"`
		}{},
		&struct {
			Port int `env:",alias=PORT"`
		}{},
	} {
		err := env.New(lookupMap(nil)).Unmarshal(commonPath, dst)

		if got := problemFields(err); len(got) != 1 {
			t.Errorf("%T: expected one problem, got %v", dst, err)
		}
	}
}

// A field that only has a config tag of options, as before, is not read by any
// provider: there is no name to read it by.
func TestConfigOptionsAloneGiveNoName(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{"required", ",required", "required,expand", "expand,required", ",expand"} {
		dst := taggedWith(tag)

		vars := map[string]string{"SVC_F": "x", "SVC_REQUIRED": "x", "SVC_EXPAND": "x"}

		if err := env.New(lookupMap(vars)).Unmarshal(commonPath, dst); err != nil {
			t.Errorf("env, %q: %v", tag, err)
		}

		if err := json.Raw([]byte(`{"svc": {"f": "x", "required": "x", "expand": "x"}}`)).Unmarshal(commonPath, dst); err != nil {
			t.Errorf("json, %q: %v", tag, err)
		}

		if got := fieldF(dst); got != "" {
			t.Errorf("%q gave a name, and the field was read: %q", tag, got)
		}
	}
}

// A name with options after it is still a name.
func TestANameWithOptions(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{"addr", "addr,required", "addr,expand", "addr,required,expand", "addr,expand,required"} {
		dst := taggedWith(tag)

		if err := env.New(lookupMap(map[string]string{"SVC_ADDR": "x"})).Unmarshal(commonPath, dst); err != nil {
			t.Errorf("env, %q: %v", tag, err)
		}

		if got := fieldF(dst); got != "x" {
			t.Errorf("env, %q: read %q", tag, got)
		}

		dst = taggedWith(tag)

		if err := json.Raw([]byte(`{"svc": {"addr": "x"}}`)).Unmarshal(commonPath, dst); err != nil {
			t.Errorf("json, %q: %v", tag, err)
		}

		if got := fieldF(dst); got != "x" {
			t.Errorf("json, %q: read %q", tag, got)
		}
	}
}

// Names are code, but they end up in variable names, keys and logs, so what a name
// may hold is narrow, and a mistake is shown at startup, never read around.
func TestHostileCommonNamesAreRefused(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"space":              "a b",
		"dot":                "a.b",
		"hyphen":             "a-b",
		"colon":              "a:b",
		"slash":              "a/b",
		"equals":             "a=b",
		"comma and more":     "a,b",
		"leading digit":      "1a",
		"leading underscore": "_a",
		"newline":            "a\nb",
		"NUL":                "a\x00b",
		"escape sequence":    "a\x1b[31m",
		"non ASCII":          "ünï",
		"lookalike":          "аddr", // the first letter is Cyrillic
		"too long":           strings.Repeat("a", 65),
		"invalid UTF-8":      "a\xffb",
		"percent":            "a%sb",
		"dollar":             "${A}",
	}

	for name, tag := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// "a,b" is two names, which is also refused, so it belongs here.
			for provider, p := range map[string]config.Provider{
				"env":  env.New(lookupMap(map[string]string{"SVC_F": "x"})),
				"json": json.Raw([]byte(`{"svc": {"f": "x"}}`)),
			} {
				dst := taggedWith(tag)

				err := p.Unmarshal(commonPath, dst)
				if err == nil {
					t.Fatalf("%s: %q was accepted", provider, tag)
				}

				if got := problemFields(err); len(got) != 1 || got[0] != "F" {
					t.Errorf("%s: expected one problem with F, got %v (%v)", provider, got, err)
				}

				// The name is escaped, whatever it holds, so it can't forge a log line.
				//lint:ignore SA1011 the invalid byte is the point: it must not get through
				if strings.ContainsAny(err.Error(), "\x00\x1b\n\xff") {
					t.Errorf("%s: the error repeats the name unescaped: %q", provider, err.Error())
				}

				if got := fieldF(dst); got != "" {
					t.Errorf("%s: the field was set to %q", provider, got)
				}
			}
		})
	}
}

func TestValidCommonNamesAreRead(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"addr", "Addr", "cert_file", "a1", "a_", "readTimeout", strings.Repeat("a", 64)} {
		dst := taggedWith(name)

		if err := env.New(lookupMap(map[string]string{"SVC_" + strings.ToUpper(name): "x"})).Unmarshal(commonPath, dst); err != nil || fieldF(dst) != "x" {
			t.Errorf("env, %q: %v, read %q", name, err, fieldF(dst))
		}

		dst = taggedWith(name)

		if err := json.Raw([]byte(`{"svc": {`+strconv.Quote(name)+`: "x"}}`)).Unmarshal(commonPath, dst); err != nil || fieldF(dst) != "x" {
			t.Errorf("json, %q: %v, read %q", name, err, fieldF(dst))
		}
	}
}

// Close misspellings of an option are refused as names: "requried" would
// otherwise be read as a name, and the field would quietly stop being required.
func TestMisspeltOptionsAreNotNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"requried", "requird", "reqiured", "Required", "REQUIRED", "requiredd",
		"expnad", "expan", "expands", "Expand", "EXPAND", "expandd",
	} {
		for provider, p := range map[string]config.Provider{
			"env":  env.New(lookupMap(nil)),
			"json": json.Raw([]byte(`{}`)),
		} {
			if err := p.Unmarshal(commonPath, taggedWith(name)); err == nil {
				t.Errorf("%s: %q was accepted as a name", provider, name)
			}
		}
	}

	// Words that merely resemble them are fine.
	for _, name := range []string{"expiry", "export", "request", "region", "expire"} {
		for provider, p := range map[string]config.Provider{
			"env":  env.New(lookupMap(nil)),
			"json": json.Raw([]byte(`{}`)),
		} {
			if err := p.Unmarshal(commonPath, taggedWith(name)); err != nil {
				t.Errorf("%s: %q was refused: %v", provider, name, err)
			}
		}
	}
}

// After the name there are only options, and there is one name.
func TestBadCommonTagShapesAreProblems(t *testing.T) {
	t.Parallel()

	for name, tag := range map[string]string{
		"two names":              "a,b",
		"unknown option":         "addr,optional",
		"a name after an option": "required,addr",
		"a name after options":   "addr,required,other",
	} {
		for provider, p := range map[string]config.Provider{
			"env":  env.New(lookupMap(nil)),
			"json": json.Raw([]byte(`{}`)),
		} {
			if err := p.Unmarshal(commonPath, taggedWith(tag)); err == nil {
				t.Errorf("%s, %s: %q was accepted", name, provider, tag)
			}
		}
	}
}

// A mistake in the common name shows even when a provider's own tag would have
// been used instead, so it can't wait for the day someone removes that tag.
func TestACommonNameMistakeShowsWhateverTheProviderTagSays(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `config:"bad name" env:"ADDR" json:"addr"`
	}

	if err := env.New(lookupMap(nil)).Unmarshal(commonPath, &cfg{}); err == nil {
		t.Error("env accepted a bad common name")
	}

	if err := json.Raw([]byte(`{}`)).Unmarshal(commonPath, &cfg{}); err == nil {
		t.Error("json accepted a bad common name")
	}
}

// Two fields can't have the same name for one provider. Names from the common tag
// are compared without regard to case, as the environment uppercases them and a
// file's keys are matched that way.
func TestCommonNamesThatCollideAreProblems(t *testing.T) {
	t.Parallel()

	envProvider := func() config.Provider { return env.New(lookupMap(map[string]string{"SVC_HOST": "x"})) }
	jsonProvider := func() config.Provider { return json.Raw([]byte(`{"svc": {"host": "x"}}`)) }

	tests := []struct {
		name      string
		dst       func() any
		env, json bool // whether the provider reports a problem with B
	}{
		{"two common names that differ in case", func() any {
			return &struct {
				A string `config:"host"`
				B string `config:"HOST"`
			}{}
		}, true, true},
		{"two common names that differ in case and an underscore", func() any {
			return &struct {
				A string `config:"a_b"`
				B string `config:"A_B"`
			}{}
		}, true, true},
		{"a common name and an explicit env name", func() any {
			return &struct {
				A string `config:"host"`
				B string `env:"HOST"`
			}{}
		}, true, false},
		{"a common name and an explicit json key", func() any {
			return &struct {
				A string `config:"host"`
				B string `json:"Host"`
			}{}
		}, false, true},
		{"a field skipped by one provider does not count for it", func() any {
			return &struct {
				A string `config:"host" env:"-"`
				B string `config:"host"`
			}{}
		}, false, true},
		{"different names", func() any {
			return &struct {
				A string `config:"host"`
				B string `config:"port"`
			}{}
		}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for provider, c := range map[string]struct {
				p    config.Provider
				want bool
			}{
				"env":  {envProvider(), tt.env},
				"json": {jsonProvider(), tt.json},
			} {
				err := c.p.Unmarshal(commonPath, tt.dst())

				got := problemFields(err)

				if c.want && !reflect.DeepEqual(got, []string{"B"}) {
					t.Errorf("%s: expected a problem with B, got %v (%v)", provider, got, err)
				}

				if !c.want && err != nil {
					t.Errorf("%s: unexpected error: %v", provider, err)
				}
			}
		})
	}
}

// Providers say where they look using the name they would actually read.
func TestDescribeUsesTheEffectiveNames(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr    string `config:"addr"`
		Port    int    `config:"port" env:"PORT_NUMBER" json:"portNo"`
		Skipped string `config:"x" env:"-"`
		Plain   string
	}

	path := []string{"svc"}

	tests := []struct {
		name     string
		provider config.Describer
		want     map[string]string
	}{
		{"env", env.New(), map[string]string{"Addr": "SVC_ADDR", "Port": "SVC_PORT_NUMBER"}},
		{"json", json.Raw(nil), map[string]string{"Addr": "svc.addr", "Port": "svc.portNo", "Skipped": "svc.x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys, err := tt.provider.Describe(path, &cfg{})
			if err != nil {
				t.Fatal(err)
			}

			got := map[string]string{}

			for _, k := range keys {
				got[k.Field] = k.Key
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// config.Fields is what a provider written elsewhere (TOML, YAML) uses, so it
// has to follow the same rule as the built in ones: its own tag, then the common
// name.

// parseTomlish reads a tag as `name,option,option`, with "-" to skip.
func parseTomlish(tag string) (config.Tag, error) {
	name, options, _ := strings.Cut(tag, ",")

	if name == "-" {
		return config.Tag{Skip: true}, nil
	}

	var opts []string

	if options != "" {
		opts = strings.Split(options, ",")
	}

	return config.Tag{Name: name, Options: opts}, nil
}

// fromCommon reports Field.Common, which says the name came from the config tag
// and not the provider's own. It is read by reflection so that this file compiles
// before the field exists and the tests fail one by one.
func fromCommon(t *testing.T, f config.Field) bool {
	t.Helper()

	v := reflect.ValueOf(f).FieldByName("Common")
	if !v.IsValid() {
		t.Fatal("config.Field has no Common field")
	}

	return v.Bool()
}

func TestFieldsFallBackToTheCommonName(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Alpha   string `config:"alpha"`
		Beta    string `config:"beta" toml:"bee"`
		Gamma   string `config:"gamma" toml:"-"`
		Delta   string `config:"delta" toml:",inline"`
		Epsilon string `toml:"epsilon"`
		Zeta    string `config:"required"`
		Eta     string `config:"eta,required"`
		Theta   string
	}

	fields, problems := config.Fields(reflect.ValueOf(&cfg{}).Elem(), "toml", parseTomlish)

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	type want struct {
		name    string
		common  bool
		options []string
	}

	wants := map[string]want{
		"Alpha":   {"alpha", true, nil},
		"Beta":    {"bee", false, nil},
		"Delta":   {"delta", true, []string{"inline"}},
		"Epsilon": {"epsilon", false, nil},
		"Eta":     {"eta", true, nil},
	}

	got := map[string]want{}

	for _, f := range fields {
		got[f.GoName] = want{f.Name, fromCommon(t, f), f.Options}
	}

	if !reflect.DeepEqual(got, wants) {
		t.Fatalf("got %+v\nwant %+v", got, wants)
	}

	// In declaration order.
	var order []string

	for _, f := range fields {
		order = append(order, f.GoName)
	}

	if want := []string{"Alpha", "Beta", "Delta", "Epsilon", "Eta"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// A common name that is a group or an embedded struct works like a provider's own.
func TestFieldsWalkGroupsByTheCommonName(t *testing.T) {
	t.Parallel()

	type inner struct {
		Host string `config:"host"`
	}

	type flat struct {
		Region string `config:"region"`
	}

	type cfg struct {
		flat

		DB inner `config:"db"`
	}

	fields, problems := config.Fields(reflect.ValueOf(&cfg{}).Elem(), "toml", parseTomlish)

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	var got []string

	for _, f := range fields {
		got = append(got, f.GoName+":"+f.Name+":"+strconv.FormatBool(f.Group))
	}

	if want := []string{"Region:region:false", "DB:db:true"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A mistake in the common name is a problem for the field, even if the provider's
// own tag would have been used instead.
func TestFieldsReportMistakesInTheCommonName(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Good  string `config:"good"`
		Spare string `toml:"spare" config:"bad name"`
		Typo  string `config:"requried"`
		Two   string `config:"a,b"`
	}

	fields, problems := config.Fields(reflect.ValueOf(&cfg{}).Elem(), "toml", parseTomlish)

	var bad []string

	for _, p := range problems {
		bad = append(bad, p.GoName)
	}

	if want := []string{"Spare", "Typo", "Two"}; !slices.Equal(bad, want) {
		t.Fatalf("problems with %v, want %v", bad, want)
	}

	if len(fields) != 1 || fields[0].GoName != "Good" {
		t.Fatalf("fields = %+v, want only Good", fields)
	}
}

var validName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// Whatever is in a config tag, Fields doesn't panic, and a name it accepts is
// one that is safe to use in a variable name and a key.
func FuzzCommonName(f *testing.F) {
	for _, seed := range []string{
		"addr", "required", ",required", "addr,required", "a,b", "", ",", ",,", "a b", "a.b", "a\x00b",
		"requried", "Required", "expnad", "\xff", strings.Repeat("a", 64), strings.Repeat("a", 65),
		"ünï", "a\nb", "${A}", "addr,", "addr,required,expand,required",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, tag string) {
		typ := reflect.StructOf([]reflect.StructField{{
			Name: "F",
			Type: reflect.TypeFor[string](),
			Tag:  reflect.StructTag("config:" + strconv.Quote(tag)),
		}})

		fields, _ := config.Fields(reflect.New(typ).Elem(), "toml", parseTomlish)

		for _, field := range fields {
			if !validName.MatchString(field.Name) {
				t.Fatalf("tag %q gave the name %q", tag, field.Name)
			}

			switch strings.ToLower(field.Name) {
			case "required", "expand":
				t.Fatalf("tag %q gave the option %q as a name", tag, field.Name)
			}
		}
	})
}
