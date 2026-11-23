package foundation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
)

// The README is the first thing a reader sees and the example in it is the first
// code they copy, so what it says is checked against the repository: the program
// is the example that is built and run, the names are real, the links lead
// somewhere, the version is the one that is tested, and nothing in it makes a
// reader listen on every interface by accident.

// canonicalApache2 is the SHA-256 of the text published at
// https://www.apache.org/licenses/LICENSE-2.0.txt.
const canonicalApache2 = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"

func read(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return string(b)
}

// heading is a markdown heading.
type heading struct {
	level int
	text  string
	line  int
}

var headingLine = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// headings lists the headings of a document, leaving out lines inside a fenced
// block, where # starts a comment.
func headings(doc string) []heading {
	var out []heading

	fenced := false

	for i, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced

			continue
		}

		if fenced {
			continue
		}

		if m := headingLine.FindStringSubmatch(line); m != nil {
			out = append(out, heading{level: len(m[1]), text: m[2], line: i + 1})
		}
	}

	return out
}

// anchor is how GitHub names a heading in a link.
func anchor(text string) string {
	text = strings.ToLower(text)
	text = regexp.MustCompile("[`*_]").ReplaceAllString(text, "")
	text = regexp.MustCompile(`[^\p{L}\p{N} _-]`).ReplaceAllString(text, "")

	return strings.ReplaceAll(text, " ", "-")
}

// section is the text under the first heading called name, up to the next heading
// of the same level or a higher one.
func section(t *testing.T, doc, name string) string {
	t.Helper()

	hs := headings(doc)
	lines := strings.Split(doc, "\n")

	for i, h := range hs {
		if h.text != name {
			continue
		}

		end := len(lines)

		for _, next := range hs[i+1:] {
			if next.level <= h.level {
				end = next.line - 1

				break
			}
		}

		return strings.Join(lines[h.line:end], "\n")
	}

	t.Fatalf("no section %q", name)

	return ""
}

// block is a fenced code block.
type block struct {
	lang string
	text string
}

func blocks(doc string) []block {
	var (
		out []block
		cur *block
	)

	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case cur == nil && strings.HasPrefix(trimmed, "```"):
			cur = &block{lang: strings.TrimPrefix(trimmed, "```")}
		case cur != nil && strings.HasPrefix(trimmed, "```"):
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			cur.text += line + "\n"
		}
	}

	return out
}

func TestTheQuickStartProgramIsTheExample(t *testing.T) {
	var program string

	for _, b := range blocks(section(t, read(t, "README.md"), "Quick start")) {
		if b.lang == "go" {
			program = b.text

			break
		}
	}

	if want := read(t, "examples/quickstart/main.go"); strings.TrimSpace(program) != strings.TrimSpace(want) {
		t.Errorf("the quick start program is not examples/quickstart/main.go:\n%s", program)
	}
}

// The example is what people copy, so what it listens on is the safe choice.
func TestTheQuickStartListensOnLoopbackByDefault(t *testing.T) {
	src := read(t, "examples/quickstart/main.go")

	if !strings.Contains(src, `"127.0.0.1:`) {
		t.Error("the example does not set a loopback address")
	}

	if regexp.MustCompile(`"(0\.0\.0\.0|\[::\]|):\d+"`).MatchString(src) {
		t.Error("the example listens on every interface")
	}
}

func TestTheReadmeNamesTheGoVersionOfTheModules(t *testing.T) {
	mod := read(t, "go.mod")

	m := regexp.MustCompile(`(?m)^go (1\.\d+)`).FindStringSubmatch(mod)
	if m == nil {
		t.Fatal("no go line in go.mod")
	}

	readme := read(t, "README.md")

	if !strings.Contains(readme, "Go "+m[1]) {
		t.Errorf("the README does not say Go %s, which go.mod asks for", m[1])
	}

	for _, v := range regexp.MustCompile(`Go (1\.\d+)`).FindAllStringSubmatch(readme, -1) {
		if v[1] != m[1] {
			t.Errorf("the README says Go %s, go.mod says %s", v[1], m[1])
		}
	}
}

// No example makes a reader listen on every interface without saying so.
func TestNoExampleInTheReadmeListensOnEveryInterface(t *testing.T) {
	for _, b := range blocks(read(t, "README.md")) {
		for _, line := range strings.Split(b.text, "\n") {
			if strings.Contains(line, "every interface") || strings.Contains(line, "all interfaces") {
				continue
			}

			if regexp.MustCompile(`0\.0\.0\.0|\[::\]|["'= ]:\d{2,5}\b`).MatchString(line) {
				t.Errorf("an example listens on every interface: %q", strings.TrimSpace(line))
			}
		}
	}
}

// exported lists the exported top level names of the package in dir.
func exported(t *testing.T, dir string) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	fset := token.NewFileSet()

	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, id := range s.Names {
							names[id.Name] = true
						}
					}
				}
			}
		}
	}

	return names
}

// What the README shows exists: a name it uses from one of our packages, and a
// method it calls on f, is really there, so a rename can't leave it behind.
func TestEveryNameTheReadmeUsesExists(t *testing.T) {
	packages := map[string]string{
		"foundation":     ".",
		"interrupt":      "interrupt",
		"health":         "health",
		"monitor":        "health/monitor",
		"config":         "config",
		"env":            "config/env",
		"flags":          "config/flags",
		"watch":          "watch",
		"foundationtest": "foundationtest",
		"transport":      "transport/http",
	}

	known := map[string]map[string]bool{}
	for pkg, dir := range packages {
		known[pkg] = exported(t, dir)
	}

	methods := map[string]bool{}
	typ := reflect.TypeFor[*foundation.F]()

	for i := range typ.NumMethod() {
		methods[typ.Method(i).Name] = true
	}

	used := 0

	for _, b := range blocks(read(t, "README.md")) {
		if b.lang != "go" {
			continue
		}

		for _, m := range regexp.MustCompile(`\b(foundation|interrupt|health|monitor|config|env|flags|watch|foundationtest|transport)\.([A-Z]\w*)`).FindAllStringSubmatch(b.text, -1) {
			used++

			if !known[m[1]][m[2]] {
				t.Errorf("the README uses %s.%s, which does not exist", m[1], m[2])
			}
		}

		for _, m := range regexp.MustCompile(`\bf\.([A-Z]\w*)\(`).FindAllStringSubmatch(b.text, -1) {
			used++

			if !methods[m[1]] {
				t.Errorf("the README calls f.%s, which is not a method of *F", m[1])
			}
		}
	}

	if used < 10 {
		t.Errorf("only %d names were checked: the README is expected to show real code", used)
	}
}

var link = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// A link in a document goes to a file that exists, and to a heading that exists
// when it names one.
func checkLinks(t *testing.T, file string) {
	t.Helper()

	doc := read(t, file)

	for _, m := range link.FindAllStringSubmatch(doc, -1) {
		target := m[1]

		if strings.HasPrefix(target, "http://") {
			t.Errorf("%s: %s is not https", file, target)

			continue
		}

		if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			continue
		}

		path, frag, _ := strings.Cut(target, "#")

		dest := file

		if path != "" {
			dest = filepath.Join(filepath.Dir(file), path)
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Errorf("%s: the link %s leads nowhere", file, target)

			continue
		}

		if frag == "" || info.IsDir() || !strings.HasSuffix(dest, ".md") {
			continue
		}

		found := false

		for _, h := range headings(read(t, dest)) {
			if anchor(h.text) == frag {
				found = true
			}
		}

		if !found {
			t.Errorf("%s: %s has no heading %q", file, dest, frag)
		}
	}
}

func TestEveryLinkInTheDocsLeadsSomewhere(t *testing.T) {
	files := []string{"README.md", "CONFIG.md", "SECURITY.md"}

	more, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range append(files, more...) {
		t.Run(f, func(t *testing.T) { checkLinks(t, f) })
	}
}

func TestTheLicenseIsTheStandardApache2Text(t *testing.T) {
	sum := sha256.Sum256([]byte(read(t, "LICENSE")))

	if got := hex.EncodeToString(sum[:]); got != canonicalApache2 {
		t.Errorf("LICENSE is not the standard Apache-2.0 text (sha256 %s)", got)
	}

}
