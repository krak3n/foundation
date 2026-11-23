package flags_test

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
)

// Arguments come from whoever starts the process, so what is wrong with them is
// reported without repeating anything that could be a value.
func TestUnknownArgumentsNameTheFlagNeverTheValue(t *testing.T) {
	t.Parallel()

	p := newProvider("--nope="+canary, "--nada", canary, "-z="+canary, canary)

	mustUnmarshal(t, p, root, &rootConfig{})

	err := p.Check(root)
	if err == nil {
		t.Fatal("expected Check to report the unknown arguments")
	}

	assertNoCanary(t, err)

	// A value is never a key, and a stray argument is named by its position.
	assertKeys(t, keysOf(fieldErrors(err)), "--nope", "--nada", "argument 3", "-z", "argument 5")
}

func TestMalformedShortFlagsAreRefused(t *testing.T) {
	t.Parallel()

	args := []string{
		"-p" + canary,   // an attached value
		"-pd",           // a cluster
		"-",             // nothing at all
		"-=x",           // no flag
		"-é",            // not ASCII
		"-1",            // a number is not a flag
		"-P=x",          // a flag nothing has
		"---x",          // too many dashes
		"--=x",          // no name
		"--",            // the end of options is not supported
		"--a b=c",       // a name with a space
		"--Port=x",      // case
		"",              // an empty argument
		" ",             // a blank one
		"--addr\n=x",    // a control character in the name
		"--\x1b[31mred", // an escape sequence
	}

	for _, arg := range args {
		var got rootConfig

		p := newProvider(arg)

		if err := p.Unmarshal(root, &got); err != nil {
			t.Errorf("%q: Unmarshal reported %v, want it to leave a bad argument to Check", arg, err)
		}

		if !reflectZero(got) {
			t.Errorf("%q set a field: %+v", arg, got)
		}

		err := p.Check(root)
		if err == nil {
			t.Errorf("%q: Check reported nothing", arg)

			continue
		}

		assertNoCanary(t, err)

		for _, fe := range fieldErrors(err) {
			if !utf8.ValidString(fe.Key) {
				t.Errorf("%q: key %q is not valid UTF-8", arg, fe.Key)
			}

			if len(fe.Key) > flags.MaxNameLength+8 {
				t.Errorf("%q: key is %d bytes", arg, len(fe.Key))
			}
		}
	}
}

func reflectZero(c rootConfig) bool {
	return c.Addr == "" && c.Port == 0 && !c.Debug && c.Timeout == 0 && c.Ratio == 0 &&
		c.IP == nil && c.Tags == nil && c.Labels == nil && c.Level == nil
}

// Invalid UTF-8 and huge names are reported in a form that is safe to log.
func TestHugeAndInvalidNamesAreBoundedAndValid(t *testing.T) {
	t.Parallel()

	long := "--" + strings.Repeat("a", flags.MaxArgLength-10)
	bad := "--\xff\xfe\xfd"

	p := newProvider(long, bad)

	err := p.Check(root)

	for _, fe := range fieldErrors(err) {
		if !utf8.ValidString(fe.Key) || len(fe.Key) > flags.MaxNameLength+8 {
			t.Errorf("key %q (%d bytes) is not bounded valid text", fe.Key, len(fe.Key))
		}
	}

	if err == nil {
		t.Fatal("expected problems")
	}
}

func TestTooManyArgumentsFailClosed(t *testing.T) {
	t.Parallel()

	args := slices.Repeat([]string{"--addr=x"}, flags.MaxArgs+1)

	p := newProvider(args...)

	var got rootConfig

	err := p.Unmarshal(root, &got)

	if err == nil || got.Addr != "" {
		t.Fatalf("got %+v, %v; want a problem and nothing set", got, err)
	}

	if len(err.Error()) > 300 {
		t.Fatalf("the error is %d bytes: it must not repeat the arguments", len(err.Error()))
	}

	if p.Check(root) == nil {
		t.Fatal("Check must fail too")
	}

	// Exactly the limit is fine.
	atLimit := slices.Repeat([]string{"--debug"}, 1)

	for len(atLimit) < flags.MaxArgs {
		atLimit = append(atLimit, "--debug")
	}

	if err := newProvider(atLimit...).Unmarshal(root, &rootConfig{}); err != nil && !isAmbiguous(err) {
		t.Fatalf("MaxArgs arguments are allowed: %v", err)
	}
}

func TestAnOverlongArgumentFailsClosed(t *testing.T) {
	t.Parallel()

	arg := "--addr=" + strings.Repeat("x", flags.MaxArgLength)

	p := newProvider(arg)

	var got rootConfig

	err := p.Unmarshal(root, &got)

	if err == nil || got.Addr != "" {
		t.Fatalf("got %+v, %v; want a problem and nothing set", got, err)
	}

	if len(err.Error()) > 300 {
		t.Fatalf("the error is %d bytes: it must not repeat the argument", len(err.Error()))
	}

	// One byte under is fine.
	ok := "--addr=" + strings.Repeat("x", flags.MaxArgLength-len("--addr="))

	if err := newProvider(ok).Unmarshal(root, &rootConfig{}); err != nil {
		t.Fatalf("an argument of exactly MaxArgLength bytes is allowed: %v", err)
	}
}

func TestAFunctionNestedTooDeepIsAProblem(t *testing.T) {
	t.Parallel()

	path := []string{"svc"}

	var args []string

	for i := range flags.MaxDepth + 1 {
		name := "n" + string(rune('a'+i))

		path = append(path, name)

		args = append(args, "--"+name)
	}

	err := newProvider(args...).Unmarshal(path, &scoped{})

	if err == nil {
		t.Fatal("expected a problem for a path deeper than MaxDepth")
	}

	// Exactly the limit is fine.
	if err := newProvider(args[:flags.MaxDepth]...).Unmarshal(path[:flags.MaxDepth+1], &scoped{}); err != nil {
		t.Fatalf("a path of MaxDepth markers is allowed: %v", err)
	}
}

func TestAPathWithNothingToReadIsAProblem(t *testing.T) {
	t.Parallel()

	for _, path := range [][]string{nil, {}} {
		if err := newProvider("--string=x").Unmarshal(path, &scoped{}); err == nil {
			t.Errorf("Unmarshal(%v) = nil, want a problem", path)
		}
	}
}

// Nothing a caller does to the slice afterwards changes what was given.
func TestTheArgumentsAreCopied(t *testing.T) {
	t.Parallel()

	args := []string{"--addr=first"}

	p := newProvider(args...)

	args[0] = "--addr=second"

	var got rootConfig

	mustUnmarshal(t, p, root, &got)

	if got.Addr != "first" {
		t.Fatalf("Addr = %q, want the arguments as they were given", got.Addr)
	}
}

// The provider never looks at os.Args by itself: arguments come from WithArgs, so
// they are always a choice the program made.
func TestWithoutArgsNothingIsRead(t *testing.T) {
	var got rootConfig

	p := flags.New()

	mustUnmarshal(t, p, root, &got)

	if !reflectZero(got) {
		t.Fatalf("got %+v", got)
	}

	if err := p.Check(root); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// Described fields are the ones that can be given, in declaration order, with
// the long form first and then the short one.
func TestDescribe(t *testing.T) {
	t.Parallel()

	type db struct {
		Host string `flag:"host"`
	}

	type cfg struct {
		Addr  string `flag:"addr"`
		Port  int    `flag:"port,short=p"`
		DB    db     `flag:"db"`
		Plain string
		Skip  string `flag:"-"`
	}

	p := newProvider("--addr=must-not-be-read")

	got, err := p.Describe(root, &cfg{})
	if err != nil {
		t.Fatal(err)
	}

	want := []config.FieldKey{
		{Field: "Addr", Key: "--addr", Provider: "flags", Type: "string"},
		{Field: "Port", Key: "--port", Provider: "flags", Type: "int"},
		{Field: "Port", Key: "-p", Provider: "flags", Type: "int"},
		{Field: "DB.Host", Key: "--db-host", Provider: "flags", Type: "string"},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// For a function that isn't the root, it says what to type after the marker.
	got, err = p.Describe([]string{"svc", "http.server"}, &serverConfig{})
	if err != nil {
		t.Fatal(err)
	}

	want = []config.FieldKey{
		{Field: "Port", Key: "--http --server --port", Provider: "flags", Type: "int"},
		{Field: "Port", Key: "--http --server -p", Provider: "flags", Type: "int"},
		{Field: "Addr", Key: "--http --server --addr", Provider: "flags", Type: "string"},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDescribeReportsMistakesInTheStruct(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Good string `flag:"good"`
		Bad  string `flag:"Bad"`
	}

	got, err := newProvider().Describe(root, &cfg{})

	if len(fieldErrors(err)) != 1 || !slices.Equal(got, []config.FieldKey{{Field: "Good", Key: "--good", Provider: "flags", Type: "string"}}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The tag parser runs on struct definitions, which are code; the arguments are
// what an attacker might control.
func FuzzArguments(f *testing.F) {
	for _, seed := range []string{
		"--addr=x", "--addr\x00x", "-p\x008080", "--a\x00--string=x\x00--c\x00--num=1",
		"--debug\x00true", "--", "-", "---x", "-pd", "--=", "-=", "\x00", "--port=-1\x00-p=1",
		"--a\x00--a", "--\xff", "--addr\x00--addr",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, "\x00")

		paths := [][]string{root, {"svc", "a"}, {"svc", "a", "c"}, {"svc", "http.server"}}

		run := func() ([]string, string) {
			p := newProvider(args...)

			var all []string

			for _, path := range paths {
				_ = p.Unmarshal(path, &rootConfig{})
				_ = p.Unmarshal(path, &scoped{})

				_, _ = p.Describe(path, &rootConfig{})
			}

			err := p.Check(root)

			for _, fe := range fieldErrors(err) {
				if !utf8.ValidString(fe.Key) || len(fe.Key) > flags.MaxNameLength+8 {
					t.Fatalf("key %q is not bounded valid text", fe.Key)
				}

				if fe.Provider != "flags" {
					t.Fatalf("provider = %q", fe.Provider)
				}

				all = append(all, fe.Key)
			}

			s := ""
			if err != nil {
				s = err.Error()
			}

			return all, s
		}

		keys1, text1 := run()
		keys2, text2 := run()

		// The same arguments always give the same report.
		if !slices.Equal(keys1, keys2) || text1 != text2 {
			t.Fatalf("not deterministic: %q vs %q", keys1, keys2)
		}
	})
}
