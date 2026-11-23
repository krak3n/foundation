package flags_test

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// A flags provider is made once by the program and used for as long as it runs:
// by every instance of a program that restarts, and by the trial run of a
// restart (a candidate), which runs beside the live instance with the same
// provider. Each of them loads the same structs again, so loading a struct again
// is not two fields sharing a flag. Two different fields sharing one still is, and
// the check tells them apart by who asks: the struct type and the field.

type (
	ownerA struct {
		Port int `flag:"port"`
	}

	// ownerB has the same Go field name and the same flag as ownerA, and is a
	// different struct.
	ownerB struct {
		Port int `flag:"port"`
	}

	// ownerC has the same flag and a different Go field name.
	ownerC struct {
		Number int `flag:"port"`
	}

	// ownerD has its own flag.
	ownerD struct {
		Other int `flag:"other"`
	}

	// ownerE takes ownerD's flag.
	ownerE struct {
		Other int `flag:"other"`
	}

	// textA and textB take a value that is text, as a secret in the wrong flag would.
	textA struct {
		Port string `flag:"port"`
	}

	textB struct {
		Port string `flag:"port"`
	}

	shortP struct {
		Port int `flag:"port,short=p"`
	}

	shortQ struct {
		Count int `flag:"count,short=p"`
	}
)

func TestTheSameStructLoadedAgainIsNotAClash(t *testing.T) {
	t.Parallel()

	p := newProvider("--port=7")

	for i := range 5 {
		var got serverConfig

		if err := p.Unmarshal(root, &got); err != nil {
			t.Fatalf("load %d: %v", i+1, err)
		}

		if got.Port != 7 {
			t.Fatalf("load %d: Port = %d, want 7", i+1, got.Port)
		}
	}

	if err := p.Check(root); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

func TestTheSameStructLoadedAgainInEveryScope(t *testing.T) {
	t.Parallel()

	p := newProvider("--a", "--port=1", "--b", "--port=2")

	for range 3 {
		var a, b serverConfig

		mustUnmarshal(t, p, []string{"svc", "a"}, &a)
		mustUnmarshal(t, p, []string{"svc", "b"}, &b)

		if a.Port != 1 || b.Port != 2 {
			t.Fatalf("a = %+v, b = %+v", a, b)
		}
	}
}

// Whatever was loaded before, a different field taking the same flag is refused,
// every time it is asked, and what was already working keeps working.
func TestADifferentFieldStillCannotTakeAFlag(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args   []string
		first  any
		second any
	}{
		"another struct, same Go name":      {[]string{"--port=1"}, &ownerA{}, &ownerB{}},
		"another struct, other Go name":     {[]string{"--port=1"}, &ownerA{}, &ownerC{}},
		"the same flag the other way round": {[]string{"--port=1"}, &ownerC{}, &ownerA{}},
		"a short flag":                      {[]string{"-p=1", "--count=2"}, &shortP{}, &shortQ{}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := newProvider(tt.args...)

			mustUnmarshal(t, p, root, tt.first)

			for i := range 3 {
				if len(fieldErrors(p.Unmarshal(root, tt.second))) == 0 {
					t.Fatalf("ask %d: the second struct took a flag that is not its own", i+1)
				}

				// The first owner is still the owner, and still loads.
				if err := p.Unmarshal(root, tt.first); err != nil {
					t.Fatalf("ask %d: the first struct stopped loading: %v", i+1, err)
				}
			}
		})
	}
}

func TestAClashNamesTheFlagAndNeverTheValue(t *testing.T) {
	t.Parallel()

	p := newProvider("--port=" + canary)

	mustUnmarshal(t, p, root, &textA{})

	errs := fieldErrors(p.Unmarshal(root, &textB{}))
	if len(errs) == 0 {
		t.Fatal("no problem")
	}

	for _, e := range errs {
		if strings.Contains(e.Error(), canary) {
			t.Errorf("the problem %q repeats a value", e)
		}

		if !strings.Contains(e.Error(), "--port") {
			t.Errorf("the problem %q does not name the flag", e)
		}
	}
}

// What the first load of a struct decided stays decided: a clash does not take the
// flag from its owner, and an owner that loads after it still does.
func TestAClashDoesNotChangeWhoOwnsTheFlag(t *testing.T) {
	t.Parallel()

	p := newProvider("--port=3")

	_ = p.Unmarshal(root, &ownerB{}) // the first to ask owns it

	var a ownerA

	if len(fieldErrors(p.Unmarshal(root, &a))) == 0 {
		t.Fatal("ownerA took the flag from ownerB")
	}

	var b ownerB

	mustUnmarshal(t, p, root, &b)

	if b.Port != 3 {
		t.Fatalf("Port = %d, want 3", b.Port)
	}
}

// The live instance and a candidate load the same structs at the same time.
func TestTheSameStructLoadedAtTheSameTimeIsFine(t *testing.T) {
	t.Parallel()

	// Two functions, so that what each reads does not depend on which loads first:
	// a function's flags end at the first flag that is not its own.
	p := newProvider("--a", "--port=9", "--d", "--other=4")

	var wg sync.WaitGroup

	errs := make(chan error, 128)

	for range 64 {
		wg.Go(func() {
			var a ownerA

			if err := p.Unmarshal([]string{"svc", "a"}, &a); err != nil || a.Port != 9 {
				errs <- fmt.Errorf("ownerA: %v, Port = %d", err, a.Port)
			}
		})

		wg.Go(func() {
			var d ownerD

			if err := p.Unmarshal([]string{"svc", "d"}, &d); err != nil || d.Other != 4 {
				errs <- fmt.Errorf("ownerD: %v, Other = %d", err, d.Other)
			}

			_ = p.Check(root)
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// pool is the structs that the model below loads, each with its one flag.
var pool = []struct {
	id   string
	flag string
	new  func() any
}{
	{"A", "port", func() any { return &ownerA{} }},
	{"B", "port", func() any { return &ownerB{} }},
	{"C", "port", func() any { return &ownerC{} }},
	{"D", "other", func() any { return &ownerD{} }},
	{"E", "other", func() any { return &ownerE{} }},
}

// checkSequence loads the structs picked by seq in one scope, and compares each
// answer with a model of the rule: the first struct to take a flag owns it, the
// same struct asking again is fine, and any other is a problem.
func checkSequence(t *testing.T, seq []byte) {
	t.Helper()

	p := newProvider("--port=1", "--other=2")

	owner := map[string]string{}

	for i, b := range seq {
		s := pool[int(b)%len(pool)]

		err := p.Unmarshal(root, s.new())

		wantClash := owner[s.flag] != "" && owner[s.flag] != s.id

		if got := len(fieldErrors(err)) > 0; got != wantClash {
			t.Fatalf("load %d of %s after %v: a problem = %v, want %v (%v)", i+1, s.id, seq[:i], got, wantClash, err)
		}

		if !wantClash {
			owner[s.flag] = s.id
		}
	}
}

func TestEveryOrderOfLoadsAgreesWithTheRule(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(1))

	for range 3000 {
		seq := make([]byte, 1+rng.Intn(20))

		for i := range seq {
			seq[i] = byte(rng.Intn(len(pool)))
		}

		checkSequence(t, seq)
	}
}

func FuzzRepeatedLoads(f *testing.F) {
	for _, seed := range [][]byte{{}, {0}, {0, 0, 0}, {0, 1}, {1, 0}, {0, 2, 0}, {3, 4, 3}, {0, 3, 1, 4, 0, 3}} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, seq []byte) {
		if len(seq) > 200 {
			seq = seq[:200]
		}

		checkSequence(t, seq)
	})
}
