package health_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health"
)

// The health server has a default address, so it needs none, and configuration
// is the only way to change it: port 0 in the address is a random port on that
// host, which is what the random port option was for.
func TestTheHealthAddressComesOnlyFromConfiguration(t *testing.T) {
	addr, stop := startHealth(t, nil, nil, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
		"TEST_HEALTH_ADDR": "127.0.0.1:0",
	}))))

	defer stop()

	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "3417" || port == "0" {
		t.Fatalf("listening on %q (%v)", addr, err)
	}

	if code, _ := fetch(t, "http://"+addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
}

// What configuration can say has no second way in code: the one option that
// sets the server up is for a registry, which configuration can't say.
func TestTheHealthOptionsAreOnlyWhatConfigurationCannotSay(t *testing.T) {
	files := parseSources(t)

	var got []string

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
				continue
			}

			for _, res := range fn.Type.Results.List {
				if id, ok := res.Type.(*ast.Ident); ok && id.Name == "ServerOption" {
					got = append(got, fn.Name.Name)
				}
			}
		}
	}

	slices.Sort(got)

	if want := []string{"WithRegistry"}; !slices.Equal(got, want) {
		t.Errorf("functions returning a ServerOption = %v, want %v", got, want)
	}
}

// The default address is a default, and an empty one is not a way to get it back
// or to turn the address off: it stops startup, in a dry run too.
func TestAnEmptyHealthAddressIsRefused(t *testing.T) {
	for name, dry := range map[string]bool{"a real run": false, "a dry run": true} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("test", func(f *foundation.F) {
				health.Server(f)

				f.Shutdown("done")
			}, foundation.WithDryRun(dry), foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"TEST_HEALTH_ADDR": ""}))))

			if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			for _, l := range loggedEntries(recorder) {
				if l.msg == "serving http server" {
					t.Fatal("it listened")
				}
			}
		})
	}
}

// parseSources parses the Go files of the package in the working directory that
// are not tests. It reads the files itself: parser.ParseDir is deprecated, and
// what is wanted is the declarations, not how files group into packages.
func parseSources(t *testing.T) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var files []*ast.File

	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		files = append(files, file)
	}

	return files
}
