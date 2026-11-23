//go:build unix

package foundation_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/json"
)

// A Kubernetes ConfigMap, and a secret mounted the same way, is not updated in
// place: the directory holds a link, ..data, that points at a directory with the
// current files, and an update makes a new directory and moves the link over the
// old one in one step. The names of the files never change, and they are links
// that go through ..data. The watch has to see that as a change to the file, which
// it does by looking at what the name leads to, and not at the name, and without
// the hidden names (..data and the directories) being documents of their own.
func TestAnAtomicLinkSwapIsAChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		dir := t.TempDir()

		for i, v := range []string{"..v1", "..v2"} {
			if err := os.Mkdir(filepath.Join(dir, v), 0o700); err != nil {
				t.Fatal(err)
			}

			edit(t, filepath.Join(dir, v, "svc.json"), addrDoc(":"+string(rune('1'+i))), i+1)
		}

		if err := os.Symlink("..v1", filepath.Join(dir, "..data")); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(filepath.Join("..data", "svc.json"), filepath.Join(dir, "svc.json")); err != nil {
			t.Fatal(err)
		}

		go func() {
			time.Sleep(5 * time.Second)

			tmp := filepath.Join(dir, "..data_tmp")

			if err := os.Symlink("..v2", tmp); err != nil {
				t.Error(err)

				return
			}

			if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
				t.Error(err)
			}
		}()

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.Dir(dir, asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)
	})
}

// A link to a file that is moved to another is a change of the file, as it is for
// a single file named by a link.
func TestALinkedFileThatIsRetargetedIsAChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		dir := t.TempDir()

		one, two := filepath.Join(dir, "one.target"), filepath.Join(dir, "two.target")
		link := filepath.Join(dir, "svc.json")

		edit(t, one, addrDoc(":1"), 1)
		edit(t, two, addrDoc(":2"), 2)

		if err := os.Symlink(one, link); err != nil {
			t.Fatal(err)
		}

		go func() {
			time.Sleep(5 * time.Second)

			tmp := link + ".tmp"

			if err := os.Symlink(two, tmp); err != nil {
				t.Error(err)

				return
			}

			if err := os.Rename(tmp, link); err != nil {
				t.Error(err)
			}
		}()

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(link, asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)
	})
}
