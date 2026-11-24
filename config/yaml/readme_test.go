package yaml_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config/yaml"
)

// The README is what a reader sees first and the example in it is the first code they
// copy, so what it says is checked against the module: the program is the example that
// is built and run, the names are real, the links lead somewhere, the version is the one
// that is tested, the refusals it lists are refused, and nothing in it teaches a mistake.

func read(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return string(b)
}

type heading struct {
	level int
	text  string
	line  int
}

var headingLine = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// headings lists the headings of a document, leaving out lines inside a fenced block,
// where # starts a comment.
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

// section is the text under the first heading called name, up to the next heading of the
// same level or a higher one.
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

	if want := read(t, "example/quickstart/main.go"); strings.TrimSpace(program) != strings.TrimSpace(want) {
		t.Errorf("the quick start program is not example/quickstart/main.go:\n%s", program)
	}
}

func TestTheReadmeNamesTheGoVersionOfTheModule(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (1\.\d+)`).FindStringSubmatch(read(t, "go.mod"))
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

// exported lists the exported top level names of the package in dir.
func exported(t *testing.T, dir string) map[string]bool {
	t.Helper()

	names := map[string]bool{}

	for _, file := range parseDir(t, dir) {
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

// methodsOfF lists the exported methods of *F in the core package in dir.
func methodsOfF(t *testing.T, dir string) map[string]bool {
	t.Helper()

	names := map[string]bool{}

	for _, file := range parseDir(t, dir) {
		for _, decl := range file.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Recv == nil || len(d.Recv.List) != 1 || !d.Name.IsExported() {
				continue
			}

			if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "F" {
					names[d.Name.Name] = true
				}
			}
		}
	}

	return names
}

func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var files []*ast.File

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

		files = append(files, file)
	}

	return files
}

// codeIn is the code of a document: its fenced blocks and its inline code spans.
func codeIn(doc string) string {
	var out strings.Builder

	for _, b := range blocks(doc) {
		out.WriteString(b.text)
	}

	prose := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(doc, "")

	for _, m := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(prose, -1) {
		out.WriteString(m[1] + "\n")
	}

	return out.String()
}

// What the README shows exists: a name it uses from one of our packages, and a method it
// calls on f, is really there, so a rename can't leave it behind.
func TestEveryNameTheReadmeUsesExists(t *testing.T) {
	packages := map[string]string{
		"yaml":       ".",
		"foundation": "../..",
		"config":     "../../config",
		"env":        "../../config/env",
		"json":       "../../config/json",
	}

	known := map[string]map[string]bool{}
	for pkg, dir := range packages {
		known[pkg] = exported(t, dir)
	}

	methods := methodsOfF(t, "../..")
	code := codeIn(read(t, "README.md"))
	used := 0

	for _, m := range regexp.MustCompile(`\b(yaml|foundation|config|env|json)\.([A-Z]\w*)`).FindAllStringSubmatch(code, -1) {
		used++

		if !known[m[1]][m[2]] {
			t.Errorf("the README uses %s.%s, which does not exist", m[1], m[2])
		}
	}

	for _, m := range regexp.MustCompile(`\bf\.([A-Z]\w*)\(`).FindAllStringSubmatch(code, -1) {
		used++

		if !methods[m[1]] {
			t.Errorf("the README calls f.%s, which is not a method of *F", m[1])
		}
	}

	if used < 8 {
		t.Errorf("only %d names were checked: the README is expected to show real code", used)
	}
}

var link = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// A link goes to a file that exists, and to a heading that exists when it names one.
func TestEveryLinkInTheReadmeLeadsSomewhere(t *testing.T) {
	doc := read(t, "README.md")

	if !link.MatchString(doc) {
		t.Fatal("the README has no links")
	}

	for _, m := range link.FindAllStringSubmatch(doc, -1) {
		target := m[1]

		if strings.HasPrefix(target, "http://") {
			t.Errorf("%s is not https", target)

			continue
		}

		if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			continue
		}

		path, frag, _ := strings.Cut(target, "#")

		dest := "README.md"

		if path != "" {
			dest = path
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Errorf("the link %s leads nowhere", target)

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
			t.Errorf("%s has no heading %q", dest, frag)
		}
	}
}

// What the README says is refused is refused: each word it uses is tied to a document
// that is really an error, and the error never repeats what the document held.
func TestEveryRefusalTheReadmeListsIsRefused(t *testing.T) {
	const canary = "CANARY-7f3a91"

	refused := []struct{ word, doc string }{
		{"anchors", "svc:\n  addr: &a " + canary + "\n"},
		{"aliases", "svc:\n  addr: &a " + canary + "\n  other: *a\n"},
		{"merge keys", "base: &b\n  addr: " + canary + "\nsvc:\n  <<: *b\n"},
		{"tags", "svc:\n  addr: !!str " + canary + "\n"},
		{"more than one document", "svc:\n  addr: " + canary + "\n---\nsvc:\n  addr: y\n"},
		{"complex keys", "? [a, b]\n: " + canary + "\nsvc:\n  addr: x\n"},
		{"control characters", "svc:\n  addr: " + canary + "\x07b\n"},
		{"twice", "svc:\n  addr: " + canary + "\n  addr: y\n"},
	}

	readme := strings.ToLower(read(t, "README.md"))

	for _, r := range refused {
		t.Run(r.word, func(t *testing.T) {
			if !strings.Contains(readme, r.word) {
				t.Errorf("the README does not say %q is refused", r.word)
			}

			var dst struct {
				Addr string `config:"addr"`
			}

			err := yaml.Raw([]byte(r.doc)).Unmarshal([]string{"svc"}, &dst)
			if err == nil {
				t.Fatal("the document was accepted")
			}

			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error repeats what the document held: %v", err)
			}
		})
	}
}

// The examples teach a safe habit: the file is named by an absolute path or by the
// operator through a variable, nothing listens on every interface, and no secret is
// shown in a file.
func TestTheExamplesInTheReadmeTeachSafeHabits(t *testing.T) {
	for _, b := range blocks(read(t, "README.md")) {
		for _, line := range strings.Split(b.text, "\n") {
			for _, m := range regexp.MustCompile(`yaml\.(?:File|Dir)\("([^"]*)"`).FindAllStringSubmatch(line, -1) {
				if !strings.HasPrefix(m[1], "/") {
					t.Errorf("an example names a relative path, which depends on where the program starts: %q", strings.TrimSpace(line))
				}
			}

			if strings.Contains(line, "every interface") || strings.Contains(line, "all interfaces") {
				continue
			}

			if regexp.MustCompile(`0\.0\.0\.0|\[::\]|["'= ]:\d{2,5}\b`).MatchString(line) {
				t.Errorf("an example listens on every interface: %q", strings.TrimSpace(line))
			}

			if b.lang == "yaml" && regexp.MustCompile(`(?i)^\s*\w*(password|secret|token|apikey|api_key|private_key)\w*:`).MatchString(line) {
				t.Errorf("a YAML example shows a secret in a file: %q", strings.TrimSpace(line))
			}
		}
	}
}
