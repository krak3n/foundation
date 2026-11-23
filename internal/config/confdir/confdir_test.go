package confdir_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"go.krak3n.dev/foundation/internal/config/confdir"
)

// A directory of configuration holds more than configuration: editor swap files,
// backups, the README, a .git directory. Only names that say they are a document
// of the provider's format are read, and those that look like leftovers or can't
// be shown safely never are.
func TestMatchSelectsDocumentsByExtension(t *testing.T) {
	tests := map[string]struct {
		name string
		exts []string
		want bool
	}{
		"plain":                   {"a.json", []string{".json"}, true},
		"with a prefix":           {"00-base.json", []string{".json"}, true},
		"upper case stem":         {"A.json", []string{".json"}, true},
		"dots in the stem":        {"a.b.json", []string{".json"}, true},
		"second extension":        {"x.yml", []string{".yaml", ".yml"}, true},
		"first extension":         {"x.yaml", []string{".yaml", ".yml"}, true},
		"unicode stem":            {"é.json", []string{".json"}, true},
		"no name":                 {".json", []string{".json"}, false},
		"empty":                   {"", []string{".json"}, false},
		"hidden":                  {".x.json", []string{".json"}, false},
		"vim backup":              {"x.json~", []string{".json"}, false},
		"emacs autosave":          {"#x.json#", []string{".json"}, false},
		"swap file":               {"x.json.swp", []string{".json"}, false},
		"other extension":         {"x.txt", []string{".json"}, false},
		"no extension":            {"README", []string{".json"}, false},
		"other format":            {"x.yaml", []string{".json"}, false},
		"upper case extension":    {"x.JSON", []string{".json"}, false},
		"trailing space":          {"x.json ", []string{".json"}, false},
		"newline":                 {"a\n.json", []string{".json"}, false},
		"escape":                  {"a\x1b[2J.json", []string{".json"}, false},
		"NUL":                     {"a\x00.json", []string{".json"}, false},
		"DEL":                     {"a\x7f.json", []string{".json"}, false},
		"invalid UTF-8":           {"\xff.json", []string{".json"}, false},
		"bidi override":           {"a\u202e.json", []string{".json"}, false},
		"separator":               {"a/b.json", []string{".json"}, false},
		"too long":                {strings.Repeat("a", 256) + ".json", []string{".json"}, false},
		"no extensions asked":     {"a.json", nil, false},
		"only the extension asks": {"a.json", []string{".yaml"}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := confdir.Match(tt.name, tt.exts...); got != tt.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tt.name, tt.exts, got, tt.want)
			}
		})
	}
}

func TestTheLimitsAreBounded(t *testing.T) {
	if confdir.MaxFiles < 1 || confdir.MaxFiles > 1024 {
		t.Errorf("MaxFiles = %d", confdir.MaxFiles)
	}

	if confdir.MaxTotalBytes < 8<<20 || confdir.MaxTotalBytes > 64<<20 {
		t.Errorf("MaxTotalBytes = %d", confdir.MaxTotalBytes)
	}
}

func FuzzMatch(f *testing.F) {
	for _, seed := range []string{"", "a.json", ".x.json", "x.json~", "#x.json#", "a\n.json", "\xff.json", "a/b.json", strings.Repeat("a", 300) + ".json", "é.yml"} {
		f.Add(seed)
	}

	exts := []string{".json", ".yml"}

	f.Fuzz(func(t *testing.T, name string) {
		if !confdir.Match(name, exts...) {
			return
		}

		// Whatever is selected is a plain, short, printable name of a document.
		switch {
		case len(name) > 255:
			t.Fatalf("%d bytes long was selected", len(name))
		case !utf8.ValidString(name):
			t.Fatalf("%q is not UTF-8", name)
		case strings.HasPrefix(name, "."), strings.HasSuffix(name, "~"):
			t.Fatalf("%q is a leftover", name)
		case strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"):
			t.Fatalf("%q is a leftover", name)
		case strings.ContainsAny(name, "/\\"):
			t.Fatalf("%q has a separator", name)
		case !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".yml"):
			t.Fatalf("%q has no extension asked for", name)
		}

		for _, r := range name {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x2066 || r == 0x2067 || r == 0x2068 || r == 0x2069 || r == 0x202a || r == 0x202b || r == 0x202c || r == 0x202d {
				t.Fatalf("%q has %q", name, r)
			}
		}
	})
}
