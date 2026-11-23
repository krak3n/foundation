package decode_test

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"go.krak3n.dev/foundation/internal/config/decode"
)

// A struct tag is read with the first value for a key, so a key that is given
// twice (`config:"addr" config:"required"`) quietly drops the second: the field is
// not required, and nothing says so. A tag that can't be read past a bad quote is
// worse, since everything after it is invisible. Both are mistakes in code, found
// when the struct is read, for the keys the framework looks at.

const (
	dup       = "more than once"
	malformed = "malformed"
)

func TestTagMistakeFindsAKeyGivenTwice(t *testing.T) {
	tests := map[string]string{
		"config twice":                         `config:"addr" config:"required"`,
		"env twice":                            `env:"A" env:"B"`,
		"flag twice":                           `flag:"addr" flag:"a,short=x"`,
		"help twice":                           `help:"one" help:"two"`,
		"json twice":                           `json:"a" json:"b"`,
		"yaml twice":                           `yaml:"a" yaml:"b"`,
		"the same value twice":                 `config:"addr" config:"addr"`,
		"not next to each other":               `config:"a" json:"x" env:"Y" config:"b"`,
		"three times":                          `config:"a" config:"b" config:"c"`,
		"beside unrelated keys":                `validate:"x" config:"a" db:"y" config:"b"`,
		"after a value with escaped quotes":    `help:"say \"hi\"" config:"a" config:"b"`,
		"with a tab between":                   "config:\"a\"\tconfig:\"b\"",
		"a very long tag":                      strings.Repeat(`x:"1" `, 100000) + `config:"a" config:"b"`,
		"many keys, the last of them repeated": strings.Repeat(`k:"1" `, 50000) + `env:"A" env:"B"`,
	}

	for name, tag := range tests {
		t.Run(name, func(t *testing.T) {
			err := decode.TagMistake(reflect.StructTag(tag))
			if err == nil {
				t.Fatal("a repeated key was not found")
			}

			if !strings.Contains(err.Error(), dup) {
				t.Errorf("the reason is %q, want it to say %q", err, dup)
			}
		})
	}
}

func TestTagMistakeNamesTheKeyAndNeverTheValue(t *testing.T) {
	for _, key := range []string{"config", "env", "flag", "help", "json", "yaml"} {
		tag := reflect.StructTag(key + `:"canary-secret-9d2e" ` + key + `:"other-canary-1f3a"`)

		err := decode.TagMistake(tag)
		if err == nil {
			t.Fatalf("%s: a repeated key was not found", key)
		}

		if !strings.Contains(err.Error(), key) {
			t.Errorf("the reason %q does not name %s", err, key)
		}

		if strings.Contains(err.Error(), "canary") {
			t.Errorf("the reason %q repeats a value", err)
		}
	}
}

func TestTagMistakeFindsATagThatCannotBeReadToTheEnd(t *testing.T) {
	tests := map[string]string{
		"an unterminated quote":                    `config:"addr,required`,
		"an unterminated quote after a good key":   `json:"x" config:"addr,required`,
		"a value that is not quoted":               `config:addr`,
		"a bad escape":                             `config:"a\q"`,
		"a key with no value":                      `json:"x" config`,
		"a missing colon":                          `config"addr"`,
		"a quote that is never closed, then a key": `help:"oops env:"A"`,
	}

	for name, tag := range tests {
		t.Run(name, func(t *testing.T) {
			err := decode.TagMistake(reflect.StructTag(tag))
			if err == nil {
				t.Fatal("a tag that can't be read was not found")
			}

			if !strings.Contains(err.Error(), malformed) {
				t.Errorf("the reason is %q, want it to say %q", err, malformed)
			}
		})
	}
}

func TestTagMistakeNeverRepeatsWhatItCouldNotRead(t *testing.T) {
	err := decode.TagMistake(reflect.StructTag(`config:"canary-secret-9d2e`))
	if err == nil {
		t.Fatal("a tag that can't be read was not found")
	}

	if strings.Contains(err.Error(), "canary") {
		t.Errorf("the reason %q repeats the tag", err)
	}
}

// What is fine stays fine: the check is for the keys the framework reads, and a
// tag that is odd but has nothing of ours in it is not ours to refuse.
func TestTagMistakeLeavesAlone(t *testing.T) {
	tests := map[string]string{
		"no tag":                                 ``,
		"one of each key":                        `config:"a" env:"B" flag:"c,short=x" help:"text" json:"j" yaml:"y"`,
		"the usual required tag":                 `config:"addr,required"`,
		"an unrelated key twice":                 `validate:"x" validate:"y"`,
		"an unrelated key twice beside ours":     `config:"a" gorm:"p" gorm:"q"`,
		"escaped quotes in a value":              `help:"say \"hi\"" config:"a"`,
		"a value that looks like a repeated key": `help:"config:\"x\" config:\"y\""`,
		"a key that ends like ours":              `myconfig:"a" config:"b"`,
		"a key that starts like ours":            `configs:"a" config:"b"`,
		"keys are case sensitive":                `Config:"a" config:"b"`,
		"a malformed tag with nothing of ours":   `validate:"x`,
		"a malformed unrelated key":              `validate:x`,
		"a value with a colon":                   `config:"a:b"`,
		"a value that is empty":                  `config:""`,
		"several spaces between":                 `config:"a"    env:"B"`,
		"a very long tag with no repeats":        strings.Repeat(`x:"1" `, 100000) + `config:"a"`,
	}

	for name, tag := range tests {
		t.Run(name, func(t *testing.T) {
			if err := decode.TagMistake(reflect.StructTag(tag)); err != nil {
				t.Errorf("TagMistake(%q) = %v, want none", tag, err)
			}
		})
	}
}

// A tag is built from pairs the test knows, and the answer is known too: it
// finds a repeat when the pairs have one, and none when they don't.
func TestTagMistakeAgreesWithHowTheTagWasBuilt(t *testing.T) {
	ours := []string{"config", "env", "flag", "help", "json", "yaml"}
	others := []string{"validate", "db", "gorm", "x", "yml", "toml"}
	values := []string{
		``, `a`, `a,required`, `a,b=c`, `say \"hi\"`, `config:\"x\" config:\"y\"`, `a b`, `\\`, `日本語`, `tab\there`,
	}

	rng := rand.New(rand.NewSource(1))

	for range 5000 {
		var (
			b    strings.Builder
			seen = map[string]int{}
		)

		for range 1 + rng.Intn(8) {
			key := ours[rng.Intn(len(ours))]
			if rng.Intn(3) == 0 {
				key = others[rng.Intn(len(others))]
			}

			seen[key]++

			b.WriteString(key + `:"` + values[rng.Intn(len(values))] + `" `)
		}

		tag := strings.TrimSpace(b.String())

		want := false

		for _, k := range ours {
			if seen[k] > 1 {
				want = true
			}
		}

		err := decode.TagMistake(reflect.StructTag(tag))

		if got := err != nil; got != want {
			t.Fatalf("TagMistake(%q) = %v, want an error: %v", tag, err, want)
		}

		if err != nil && !strings.Contains(err.Error(), dup) {
			t.Fatalf("TagMistake(%q) = %v, want a repeated key", tag, err)
		}
	}
}

func FuzzTagMistake(f *testing.F) {
	for _, s := range []string{
		``, `config:"a"`, `config:"a" config:"b"`, `config:"a`, `config:a`, `json:"x" env:"Y" json:"z"`,
		`help:"say \"hi\""`, "config:\"a\x00\" env:\"b\"", `config:"\xff"`, "\u202e", `:"x"`, `""`, `a:"b" a:"c`,
		strings.Repeat(`k:"v" `, 1000),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, tag string) {
		err := decode.TagMistake(reflect.StructTag(tag))

		if err == nil {
			return
		}

		// A reason, never the tag: short, plain, and the same each time.
		msg := err.Error()

		if len(msg) > 200 {
			t.Fatalf("TagMistake(%q) = a reason of %d bytes", tag, len(msg))
		}

		for _, r := range msg {
			if unicode.IsControl(r) || r == '�' {
				t.Fatalf("TagMistake(%q) = %q, which has %U in it", tag, msg, r)
			}
		}

		if again := decode.TagMistake(reflect.StructTag(tag)); again == nil || again.Error() != msg {
			t.Fatalf("TagMistake(%q) said %q and then %v", tag, msg, again)
		}

		// When the tag can be read, reflect agrees that the key is there, so what is
		// refused is something the struct would really have had. A tab or a newline
		// between pairs hides what follows from reflect, which only skips a space,
		// and is no reason to let a repeat through, so it is read as a space here.
		if strings.Contains(msg, dup) {
			found := false

			spaced := reflect.StructTag(strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(tag))

			for _, k := range []string{"config", "env", "flag", "help", "json", "yaml"} {
				if _, ok := spaced.Lookup(k); ok && strings.Contains(msg, strconv.Quote(k)) {
					found = true
				}
			}

			if !found {
				t.Fatalf("TagMistake(%q) = %q, but reflect finds no such key", tag, msg)
			}
		}
	})
}
