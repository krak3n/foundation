package flags_test

import (
	"slices"
	"sync"
	"testing"
)

type scoped struct {
	String string `flag:"string"`
	Num    int    `flag:"num"`
}

type serverConfig struct {
	Port int    `flag:"port,short=p"`
	Addr string `flag:"addr"`
}

// A marker, --a, says which function the flags after it are for. Every function
// still runs; the marker only picks whose configuration the flags set.
func TestAMarkerScopesTheFlagsAfterIt(t *testing.T) {
	t.Parallel()

	p := newProvider("--string=root", "--a", "--string=in-a", "--num", "2")

	var rootCfg, a scoped

	mustUnmarshal(t, p, root, &rootCfg)
	mustUnmarshal(t, p, []string{"svc", "a"}, &a)

	if rootCfg.String != "root" || rootCfg.Num != 0 {
		t.Fatalf("root = %+v", rootCfg)
	}

	if a.String != "in-a" || a.Num != 2 {
		t.Fatalf("a = %+v", a)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

// The nearest marker before a flag owns it.
func TestAFlagBelongsToTheNearestMarker(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--string=x", "--b", "--string=y")

	var a, b scoped

	mustUnmarshal(t, p, []string{"svc", "a"}, &a)
	mustUnmarshal(t, p, []string{"svc", "b"}, &b)

	if a.String != "x" || b.String != "y" {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

// Flags never leak from one scope to another: a flag the scope in front of it
// doesn't have is not the root's, or anyone else's.
func TestFlagsDoNotLeakBetweenScopes(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--addr=:1")

	var rootCfg serverConfig

	var a scoped

	mustUnmarshal(t, p, root, &rootCfg)
	mustUnmarshal(t, p, []string{"svc", "a"}, &a)

	if rootCfg.Addr != "" {
		t.Fatal("a flag after --a must not set the root's field")
	}

	assertKeys(t, check(t, p), "--addr")
}

func TestRootFlagsMustComeBeforeAnyMarker(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--string=x", "--addr=:1")

	var rootCfg serverConfig

	var a scoped

	mustUnmarshal(t, p, root, &rootCfg)
	mustUnmarshal(t, p, []string{"svc", "a"}, &a)

	if rootCfg.Addr != "" || a.String != "x" {
		t.Fatalf("root = %+v, a = %+v", rootCfg, a)
	}

	assertKeys(t, check(t, p), "--addr")
}

// --c only ever follows --a: a sub command is written after its parent, and the
// same name under another parent is a different function.
func TestASubCommandFollowsItsParent(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--c", "--num=1", "--b", "--c", "--num=2")

	var ac, bc scoped

	mustUnmarshal(t, p, []string{"svc", "a", "c"}, &ac)
	mustUnmarshal(t, p, []string{"svc", "b", "c"}, &bc)

	if ac.Num != 1 || bc.Num != 2 {
		t.Fatalf("a/c = %+v, b/c = %+v", ac, bc)
	}

	// The parents are functions too, even with no flags of their own.
	mustUnmarshal(t, p, []string{"svc", "a"}, &scoped{})
	mustUnmarshal(t, p, []string{"svc", "b"}, &scoped{})

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

func TestAParentsFlagsComeBeforeItsSubCommand(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--string=parent", "--c", "--num=3")

	var a, ac scoped

	mustUnmarshal(t, p, []string{"svc", "a"}, &a)
	mustUnmarshal(t, p, []string{"svc", "a", "c"}, &ac)

	if a.String != "parent" || a.Num != 0 || ac.Num != 3 || ac.String != "" {
		t.Fatalf("a = %+v, a/c = %+v", a, ac)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

func TestASubCommandWithoutItsParentIsNotClaimed(t *testing.T) {
	t.Parallel()

	p := newProvider("--c", "--num=1")

	var ac scoped

	mustUnmarshal(t, p, []string{"svc", "a", "c"}, &ac)

	if ac.Num != 0 {
		t.Fatalf("a/c = %+v, want nothing set without --a in front", ac)
	}

	assertKeys(t, check(t, p), "--c", "--num")
}

// A name with a dot is the same keys as two nested names, as it is for
// environment variables and files: --http --server.
func TestADottedNameIsSeveralMarkers(t *testing.T) {
	t.Parallel()

	args := []string{"--http", "--server", "--port", "8080"}

	paths := [][]string{
		{"svc", "http.server"},
		{"svc", "http", "server"},
		{"svc", "http/server"},
		{"svc", "http-server"},
		{"svc", "HTTP.Server"},
	}

	type cfg struct {
		Port int `flag:"port"`
	}

	for _, path := range paths {
		var got cfg

		p := newProvider(args...)

		mustUnmarshal(t, p, path, &got)

		if got.Port != 8080 {
			t.Errorf("%v: Port = %d, want 8080 from --http --server --port 8080", path, got.Port)
		}

		if keys := check(t, p); keys != nil {
			t.Errorf("%v: Check reported %q", path, keys)
		}
	}
}

// Underscores read as hyphens and case is dropped, so a marker is always the
// lower case form a flag is written in.
func TestMarkersAreLowerCaseWithHyphens(t *testing.T) {
	t.Parallel()

	p := newProvider("--my-worker", "--string=x")

	var got scoped

	mustUnmarshal(t, p, []string{"svc", "My_Worker"}, &got)

	if got.String != "x" {
		t.Fatalf("got %+v, want My_Worker to be --my-worker", got)
	}
}

// A marker given twice would give two sets of flags to one function. The first is
// read, and the second, with its flags, is an argument nobody read, so the run
// fails when startup ends.
func TestAMarkerGivenTwiceIsReportedWhenNothingReadsTheSecond(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--string=x", "--a", "--string=y")

	var a scoped

	mustUnmarshal(t, p, []string{"svc", "a"}, &a)

	assertKeys(t, check(t, p), "--a", "--string")
}

// Two functions can't both claim an argument: when a marker could mean a child of
// the scope it is in or a function higher up, nothing is guessed, in whichever
// order they load.
func TestAMarkerThatTwoFunctionsCouldClaimIsAmbiguous(t *testing.T) {
	t.Parallel()

	paths := [][]string{{"svc", "a", "c"}, {"svc", "c"}}

	for _, order := range [][]int{{0, 1}, {1, 0}} {
		p := newProvider("--a", "--c")

		var ambiguous int

		for _, i := range order {
			if err := p.Unmarshal(paths[i], &scoped{}); err != nil {
				if !isAmbiguous(err) {
					t.Fatalf("order %v: %v", order, err)
				}

				ambiguous++
			}
		}

		if err := p.Check(root); err != nil && !isAmbiguous(err) {
			t.Fatalf("order %v: %v", order, err)
		}

		if ambiguous != 1 {
			t.Errorf("order %v: %d functions reported ambiguity, want exactly the later one", order, ambiguous)
		}
	}
}

// Writing them in an order that says which is meant is fine.
func TestTheSameNameAtTwoLevelsIsFineWhenTheOrderSaysWhich(t *testing.T) {
	t.Parallel()

	p := newProvider("--c", "--num=1", "--a", "--c", "--num=2")

	var top, nested scoped

	mustUnmarshal(t, p, []string{"svc", "c"}, &top)
	mustUnmarshal(t, p, []string{"svc", "a", "c"}, &nested)

	if top.Num != 1 || nested.Num != 2 {
		t.Fatalf("c = %+v, a/c = %+v", top, nested)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

// A flag with the name of a sub command can't mean both.
func TestAFlagAndASubCommandWithTheSameNameAreAmbiguous(t *testing.T) {
	t.Parallel()

	type withC struct {
		C string `flag:"c"`
	}

	p := newProvider("--a", "--c", "x")

	err1 := p.Unmarshal([]string{"svc", "a"}, &withC{})
	err2 := p.Unmarshal([]string{"svc", "a", "c"}, &scoped{})

	if !isAmbiguous(err1) && !isAmbiguous(err2) {
		t.Fatalf("got %v and %v, want one of them ambiguous", err1, err2)
	}
}

func TestAScopedFunctionWithNoMarkerGetsNothing(t *testing.T) {
	t.Parallel()

	p := newProvider("--num=3")

	var a scoped

	mustUnmarshal(t, p, []string{"svc", "a"}, &a)

	if a.Num != 0 {
		t.Fatalf("a = %+v, want flags with no marker to be the root's", a)
	}

	assertKeys(t, check(t, p), "--num")
}

// Two different fields that read the same flag are refused: one of them would
// quietly never be set. (Loading the same struct again is not that: see
// reuse_test.go.)
func TestTwoDifferentStructsInOneScopeCannotShareAFlag(t *testing.T) {
	t.Parallel()

	p := newProvider("--port=1")

	mustUnmarshal(t, p, root, &serverConfig{})

	err := p.Unmarshal(root, &sharesPort{})

	if len(fieldErrors(err)) == 0 {
		t.Fatal("expected the second struct claiming --port to be a problem")
	}
}

type sharesPort struct {
	Port int `flag:"port"`
}

// Describing and loading the same function is what Load does, so describing must
// not count as claiming.
func TestDescribingDoesNotClaim(t *testing.T) {
	t.Parallel()

	p := newProvider("--port=1")

	if _, err := p.Describe(root, &serverConfig{}); err != nil {
		t.Fatal(err)
	}

	var got serverConfig

	mustUnmarshal(t, p, root, &got)

	if got.Port != 1 {
		t.Fatalf("got %+v", got)
	}
}

// Functions that start together load together.
func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	var args []string

	paths := make([][]string, 0, 32)

	for i := range 32 {
		name := "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))

		args = append(args, "--"+name, "--num="+string(rune('1'+i%9)))

		paths = append(paths, []string{"svc", name})
	}

	p := newProvider(args...)

	var wg sync.WaitGroup

	for _, path := range paths {
		wg.Go(func() {
			var got scoped

			_ = p.Unmarshal(path, &got)
		})

		wg.Go(func() {
			_ = p.Check(root)

			_, _ = p.Describe(path, &scoped{})
		})
	}

	wg.Wait()

	if err := p.Check(root); err != nil {
		t.Fatalf("Check reported %v after every function loaded", err)
	}
}

func TestCheckIsRepeatable(t *testing.T) {
	t.Parallel()

	p := newProvider("--nope", "stray", "-z")

	first, second := check(t, p), check(t, p)

	if !slices.Equal(first, second) || !slices.Equal(first, []string{"--nope", "argument 2", "-z"}) {
		t.Fatalf("first %q, second %q", first, second)
	}
}
