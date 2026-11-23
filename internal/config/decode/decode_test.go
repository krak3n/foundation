package decode_test

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/internal/config/decode"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		value any
		want  decode.Category
	}{
		{"", decode.String},
		{true, decode.Bool},
		{int8(0), decode.Int},
		{int64(0), decode.Int},
		{uint16(0), decode.Uint},
		{float32(0), decode.Float},
		{time.Duration(0), decode.Duration},
		{url.URL{}, decode.URL},
		{time.Time{}, decode.Text},
		{net.IP{}, decode.Text},
		{[]string{}, decode.None},
		{map[string]string{}, decode.None},
		{uintptr(0), decode.None},
		{complex64(0), decode.None},
		{struct{ A int }{}, decode.None},
		{new(int), decode.None},
	}

	for _, tt := range tests {
		if got := decode.Classify(reflect.TypeOf(tt.value)); got != tt.want {
			t.Errorf("Classify(%T) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	type group struct{ A int }

	supported := []any{
		"", 0, uint8(0), time.Duration(0), time.Time{}, url.URL{},
		new(int), new(time.Time), []string{}, []net.IP{}, map[string]int{}, map[int]time.Duration{},
	}

	unsupported := []any{
		make(chan int), func() {}, complex128(0), [2]string{}, group{}, new(group), [][]string{}, new([]string),
		[]*int{}, map[string][]string{}, map[string]*int{}, uintptr(0),
	}

	for _, v := range supported {
		if err := decode.Check(reflect.TypeOf(v)); err != nil {
			t.Errorf("expected %T to be supported: %v", v, err)
		}
	}

	for _, v := range unsupported {
		if err := decode.Check(reflect.TypeOf(v)); err == nil {
			t.Errorf("expected %T to be unsupported", v)
		}
	}
}

func TestSetStringLeavesTheOriginalOfAPointerAlone(t *testing.T) {
	shared := 1

	dst := struct{ P *int }{P: &shared}

	if err := decode.SetString(reflect.ValueOf(&dst).Elem().Field(0), "2"); err != nil {
		t.Fatal(err)
	}

	if shared != 1 || *dst.P != 2 {
		t.Fatalf("expected a new value (shared=%d, set=%d)", shared, *dst.P)
	}
}

func TestSetStringIgnoresATrailingCommaInAMap(t *testing.T) {
	dst := struct{ M map[string]string }{}

	if err := decode.SetString(reflect.ValueOf(&dst).Elem().Field(0), "a:1,b:2,"); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(dst.M, map[string]string{"a": "1", "b": "2"}) {
		t.Fatalf("unexpected map %v", dst.M)
	}
}

func TestSetStringErrorsNameTheItem(t *testing.T) {
	dst := struct{ L []int }{}

	err := decode.SetString(reflect.ValueOf(&dst).Elem().Field(0), "1,x,3")
	if err == nil || !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("expected the error to name the bad item, got %v", err)
	}
}

func TestIsPlainString(t *testing.T) {
	type name string

	tests := []struct {
		value any
		want  bool
	}{
		{"", true}, {new(string), true}, {name(""), true}, {0, false}, {[]string{}, false}, {time.Time{}, false},
	}

	for _, tt := range tests {
		if got := decode.IsPlainString(reflect.TypeOf(tt.value)); got != tt.want {
			t.Errorf("IsPlainString(%T) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func tagName(tag string) (decode.Tag, error) {
	return decode.Tag{Name: tag}, nil
}

func TestFieldsListsTheFieldsToLoad(t *testing.T) {
	type embedded struct {
		Region string `x:"region"`
	}

	dst := struct {
		embedded
		Name    string `x:"name"`
		Skipped string
		Dash    string `x:"-"`
		Group   struct {
			A int `x:"a"`
		} `x:"group"`
	}{}

	fields, problems := decode.Fields(reflect.ValueOf(&dst).Elem(), "x", func(tag string) (decode.Tag, error) {
		if tag == "-" {
			return decode.Tag{Skip: true}, nil
		}

		return decode.Tag{Name: tag}, nil
	})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	type summary struct {
		GoName, Name string
		Group        bool
	}

	var got []summary

	for _, f := range fields {
		got = append(got, summary{f.GoName, f.Name, f.Group})
	}

	want := []summary{{"Region", "region", false}, {"Name", "name", false}, {"Group", "group", true}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected fields\n got: %v\nwant: %v", got, want)
	}
}

func TestFieldsReportsEveryProblemAndKeepsTheGoodFields(t *testing.T) {
	type embedded struct {
		Bad func() `x:"embedded_bad"`
	}

	dst := struct {
		Fine string   `x:"fine"`
		Bad  chan int `x:"bad"`
		embedded
		Last any `x:"last"`
	}{}

	fields, problems := decode.Fields(reflect.ValueOf(&dst).Elem(), "x", tagName)

	if len(fields) != 1 || fields[0].GoName != "Fine" {
		t.Fatalf("expected only the good field to be returned, got %v", fields)
	}

	var got []string

	for _, p := range problems {
		if p.Err == nil {
			t.Fatalf("expected every problem to carry an error: %+v", p)
		}

		got = append(got, p.GoName+"="+p.Name)
	}

	// In declaration order, including the embedded struct's field, which is
	// named as if it were declared in the parent. The tag name is known.
	if want := []string{"Bad=bad", "Bad=embedded_bad", "Last=last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected problems\n got: %v\nwant: %v", got, want)
	}
}

func TestFieldsKeepsTheNameWhenATagIsRejected(t *testing.T) {
	dst := struct {
		Opt  string `x:"OPT,required"`
		None string `x:""`
	}{}

	_, problems := decode.Fields(reflect.ValueOf(&dst).Elem(), "x", func(tag string) (decode.Tag, error) {
		name, _, _ := strings.Cut(tag, ",")

		if name == "" {
			return decode.Tag{}, errors.New("no name")
		}

		return decode.Tag{Name: name}, errors.New("unknown option")
	})

	if len(problems) != 2 {
		t.Fatalf("expected two problems, got %v", problems)
	}

	// The name is still known when the tag has one, so the problem can be traced
	// to the variable or key it was meant to read.
	if problems[0].GoName != "Opt" || problems[0].Name != "OPT" || problems[1].GoName != "None" || problems[1].Name != "" {
		t.Fatalf("unexpected problems: %+v", problems)
	}
}

// A provider's own tag options are handed back with the field, so it can act on
// them without Fields knowing what they mean.
func TestFieldsPassesTagOptionsThrough(t *testing.T) {
	type tls struct {
		Cert string `x:"CERT,file"`
	}

	type cfg struct {
		Password string `x:"PASSWORD,file,trim"`
		Plain    string `x:"PLAIN"`
		TLS      tls    `x:"TLS"`
	}

	var dst cfg

	fields, problems := decode.Fields(reflect.ValueOf(&dst).Elem(), "x", func(tag string) (decode.Tag, error) {
		name, options, _ := strings.Cut(tag, ",")

		var opts []string
		if options != "" {
			opts = strings.Split(options, ",")
		}

		return decode.Tag{Name: name, Options: opts}, nil
	})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(fields))
	}

	if !slices.Equal(fields[0].Options, []string{"file", "trim"}) {
		t.Errorf("Password options = %v", fields[0].Options)
	}

	if len(fields[1].Options) != 0 {
		t.Errorf("Plain options = %v, want none", fields[1].Options)
	}

	// Options on a group are kept too, so a provider can reject ones that make
	// no sense there.
	if !fields[2].Group || len(fields[2].Options) != 0 {
		t.Errorf("TLS = %+v", fields[2])
	}
}

func TestFieldsKeepsOptionsOnAGroup(t *testing.T) {
	type tls struct {
		Cert string `x:"CERT"`
	}

	type cfg struct {
		TLS tls `x:"TLS,file"`
	}

	var dst cfg

	fields, _ := decode.Fields(reflect.ValueOf(&dst).Elem(), "x", func(tag string) (decode.Tag, error) {
		name, option, _ := strings.Cut(tag, ",")

		return decode.Tag{Name: name, Options: []string{option}}, nil
	})

	if len(fields) != 1 || !fields[0].Group || !slices.Equal(fields[0].Options, []string{"file"}) {
		t.Fatalf("got %+v", fields)
	}
}

// secret is what a value is: this must never come back in an error, which is
// logged.
const secret = "hunter2-SECRET"

type picky string

func (p *picky) UnmarshalText(b []byte) error {
	// A custom type's error often quotes its input, as the standard library's do.
	return fmt.Errorf("unknown picky value %q", b)
}

func setErr(t *testing.T, dst any, text string) error {
	t.Helper()

	return decode.SetString(reflect.ValueOf(dst).Elem(), text)
}

// A value in a configuration source is not something an error may repeat: it
// could be a password that went into the wrong field, or a URL with credentials
// in it, and errors end up in logs. The field and key say where to look.
func TestErrorsNeverContainTheValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		dst    any
		text   string
		reason string
	}{
		{"int", new(int), secret, "not a valid integer"},
		{"int8", new(int8), secret, "not a valid integer"},
		{"uint", new(uint16), secret, "not a valid integer"},
		{"negative uint", new(uint), "-" + secret, "not a valid integer"},
		{"float", new(float64), secret, "not a valid number"},
		{"bool", new(bool), secret, "not a valid bool"},
		{"duration", new(time.Duration), secret, "not a valid duration"},
		{"duration with a unit", new(time.Duration), "5" + secret, "not a valid duration"},
		{"url", new(url.URL), "https://admin:" + secret + "@[::1", "not a valid URL"},
		{"url escape", new(url.URL), "http://user:p%zz" + secret + "@host/", "not a valid URL"},
		{"url control character", new(url.URL), "http://host/\x00" + secret, "not a valid URL"},
		{"time", new(time.Time), secret, "not a valid time.Time"},
		{"ip", new(net.IP), secret, "not a valid net.IP"},
		{"a custom type", new(picky), secret, "not a valid decode_test.picky"},
		{"a pointer", new(*int), secret, "not a valid integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := setErr(t, tt.dst, tt.text)
			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("the error repeats the value: %q", err)
			}

			if !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("expected %q in %q", tt.reason, err)
			}
		})
	}
}

// What kind of mistake it was survives: a number that is too big is not the same
// as text that is not a number, and a program can tell.
func TestTheReasonForANumberErrorIsKept(t *testing.T) {
	t.Parallel()

	err := setErr(t, new(int8), "300")

	if !errors.Is(err, strconv.ErrRange) || !strings.Contains(err.Error(), "value out of range") || strings.Contains(err.Error(), "300") {
		t.Fatalf("got %q", err)
	}

	err = setErr(t, new(int8), "abc")

	if !errors.Is(err, strconv.ErrSyntax) || !strings.Contains(err.Error(), "invalid syntax") {
		t.Fatalf("got %q", err)
	}

	err = setErr(t, new(float32), "1e999")

	if !errors.Is(err, strconv.ErrRange) || strings.Contains(err.Error(), "1e999") {
		t.Fatalf("got %q", err)
	}
}

func TestSliceAndMapErrorsNameTheItemButNotTheValue(t *testing.T) {
	t.Parallel()

	err := setErr(t, new([]int), "1,"+secret+",3")

	if err == nil || !strings.Contains(err.Error(), "item 1") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}

	err = setErr(t, new(map[string]int), "a:1,b:"+secret)

	if err == nil || !strings.Contains(err.Error(), "item 1") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}

	err = setErr(t, new(map[int]string), secret+":x")

	if err == nil || !strings.Contains(err.Error(), "item 0") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}

	// An item with no colon is a mistake in the shape, not the value.
	err = setErr(t, new(map[string]string), secret)

	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
}

func TestSetLeafErrorsNeverContainTheValueEither(t *testing.T) {
	t.Parallel()

	var n int

	err := decode.SetLeaf(reflect.ValueOf(&n).Elem(), secret)

	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
}

// Nothing is lost for a value that is fine.
func TestValidValuesStillLoad(t *testing.T) {
	t.Parallel()

	var (
		n int
		d time.Duration
		u url.URL
	)

	for dst, text := range map[any]string{&n: "42", &d: "1m30s", &u: "https://example.com/x"} {
		if err := setErr(t, dst, text); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}

	if n != 42 || d != 90*time.Second || u.Host != "example.com" {
		t.Fatalf("n=%d d=%v u=%v", n, d, u)
	}
}
