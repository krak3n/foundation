package foundation_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/internal/config/decode"
)

// The framework refuses a struct whose tag repeats a key when it reads it, but an
// example or a doc that is never run would teach the mistake all the same, and
// that is how `config:"addr" config:"required"` got into one. So every Go file in
// the repository, the examples and the other modules included, is read for struct
// tags, and none may repeat a key the framework reads or be cut off in the middle
// of one. Test files are left out: they hold the mistakes on purpose.
func TestNoStructTagInTheRepositoryRepeatsAKey(t *testing.T) {
	var (
		seen int
		fset = token.NewFileSet()
	)

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("%s: %v", path, err)

			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}

			for _, field := range st.Fields.List {
				if field.Tag == nil {
					continue
				}

				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					continue
				}

				seen++

				if err := decode.TagMistake(reflect.StructTag(tag)); err != nil {
					t.Errorf("%s: %v", fset.Position(field.Tag.Pos()), err)
				}
			}

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A scan that read nothing would pass for the wrong reason.
	if seen < 30 {
		t.Errorf("only %d struct tags were read: the scan is not finding the repository's files", seen)
	}

}
