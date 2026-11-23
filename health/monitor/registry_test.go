package monitor_test

import (
	"context"
	"fmt"
	"iter"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.krak3n.dev/foundation/health/monitor"
)

type fakeProbe struct{ name string }

func (p fakeProbe) Name() string                                   { return p.name }
func (p fakeProbe) Mode() monitor.Mode                             { return monitor.AllModes }
func (p fakeProbe) Status(context.Context) (monitor.Status, error) { return monitor.StatusHealthy, nil }

func namesOf(seq func(yield func(monitor.Probe) bool)) []string {
	var names []string

	for p := range seq {
		names = append(names, p.Name())
	}

	return names
}

func TestRegistrySeqIsSortedByName(t *testing.T) {
	t.Parallel()

	r, err := monitor.NewRegistry(fakeProbe{"c"}, fakeProbe{"a"}, fakeProbe{"b"})
	if err != nil {
		t.Fatal(err)
	}

	for range 20 {
		if got := namesOf(r.Seq()); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Fatalf("got %v, want [a b c]", got)
		}
	}
}

// A sequence is a snapshot taken when Seq is called, so probes added afterwards
// don't change it, and adding while it is iterated is safe.
func TestRegistrySeqIsASnapshot(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"a"}, fakeProbe{"b"})

	seq := r.Seq()

	if err := r.Add(fakeProbe{"c"}); err != nil {
		t.Fatal(err)
	}

	if got := namesOf(seq); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("the snapshot changed: %v", got)
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("a new sequence should see everything: %v", got)
	}
}

func TestRegistryAddWhileIterating(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry()

	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range 500 {
			_ = r.Add(fakeProbe{fmt.Sprint(i)})

			runtime.Gosched()
		}
	})

	for range 2000 {
		for range r.Seq() {
		}

		runtime.Gosched()
	}

	wg.Wait()

	if got := len(namesOf(r.Seq())); got != 500 {
		t.Fatalf("got %d probes, want 500", got)
	}
}

func TestRegistryRejectsADuplicateName(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"a"})

	err := r.Add(fakeProbe{"a"})
	if err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("expected an error naming the probe, got %v", err)
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRegistryRejectsADuplicateWithinOneCall(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry()

	if err := r.Add(fakeProbe{"x"}, fakeProbe{"x"}); err == nil {
		t.Fatal("expected an error for the same name twice in one call")
	}

	if _, err := monitor.NewRegistry(fakeProbe{"y"}, fakeProbe{"y"}); err == nil {
		t.Fatal("expected NewRegistry to reject duplicates too")
	}
}

// A call that fails adds nothing, not the probes that came before the clash.
func TestRegistryAddIsAllOrNothing(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"existing"})

	if err := r.Add(fakeProbe{"new1"}, fakeProbe{"existing"}, fakeProbe{"new2"}); err == nil {
		t.Fatal("expected an error")
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"existing"}) {
		t.Fatalf("a failed Add left probes behind: %v", got)
	}
}

// Two components registering the same name at the same moment: one wins and the
// other is told.
func TestRegistryConcurrentAddsOfOneNameSucceedOnce(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry()

	var (
		wg        sync.WaitGroup
		succeeded atomic.Int32
	)

	for range 50 {
		wg.Go(func() {
			if r.Add(fakeProbe{"same"}) == nil {
				succeeded.Add(1)
			}
		})
	}

	wg.Wait()

	if succeeded.Load() != 1 {
		t.Fatalf("%d adds succeeded, want exactly 1", succeeded.Load())
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"same"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRegistryZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	var r monitor.Registry

	if got := namesOf(r.Seq()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}

	if err := r.Add(fakeProbe{"a"}); err != nil {
		t.Fatal(err)
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("got %v", got)
	}
}

// A Registry can be emptied, so that the names can be used again: the framework
// empties the registry of an instance when the instance has stopped completely.
// Being emptied is not something a Registrar has to do: a registrar of the
// program's own (health.WithRegistry) is the program's, and the framework never
// clears it.

// minimal is all that a registrar needs to be.
type minimal struct{}

func (minimal) Add(...monitor.Probe) error   { return nil }
func (minimal) Seq() iter.Seq[monitor.Probe] { return nil }

var (
	_ monitor.Registrar = minimal{}
	_ monitor.Registrar = (*monitor.Registry)(nil)
)

func TestClearEmptiesTheRegistry(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"a"}, fakeProbe{"b"})

	r.Clear()

	if got := namesOf(r.Seq()); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestClearOfAnEmptyRegistryAndTwiceIsFine(t *testing.T) {
	t.Parallel()

	var zero monitor.Registry

	r, _ := monitor.NewRegistry()

	for name, r := range map[string]*monitor.Registry{"the zero value": &zero, "an empty one": r} {
		for range 2 {
			r.Clear()
		}

		if got := namesOf(r.Seq()); len(got) != 0 {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

// What was cleared can be registered again, the same names included: that is the
// point of clearing.
func TestTheNamesOfAClearedRegistryCanBeUsedAgain(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"a"})

	r.Clear()

	if err := r.Add(fakeProbe{"a"}, fakeProbe{"b"}); err != nil {
		t.Fatalf("a name could not be used again: %v", err)
	}

	if got := namesOf(r.Seq()); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v", got)
	}
}

// A sequence is a snapshot, so clearing doesn't change one that was taken before.
func TestClearDoesNotChangeASnapshot(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry(fakeProbe{"a"}, fakeProbe{"b"})

	seq := r.Seq()

	r.Clear()

	if got := namesOf(seq); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("a snapshot changed: %v", got)
	}
}

// Adding, listing and clearing at the same moment is safe (this is run with the race
// detector), and what is left at the end is only what was added after the last clear.
func TestClearIsSafeWithAddAndSeq(t *testing.T) {
	t.Parallel()

	r, _ := monitor.NewRegistry()

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				_ = r.Add(fakeProbe{string(rune('a'+i)) + string(rune('a'+j%26))})

				runtime.Gosched()
			}
		})
	}

	wg.Go(func() {
		for range 200 {
			r.Clear()

			runtime.Gosched()
		}
	})

	wg.Go(func() {
		for range 500 {
			for range r.Seq() {
			}

			runtime.Gosched()
		}
	})

	wg.Wait()

	r.Clear()

	if got := namesOf(r.Seq()); len(got) != 0 {
		t.Fatalf("got %v after a last clear", got)
	}
}
