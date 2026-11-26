package etcd_test

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config/etcd"
)

type fuzzConfig struct {
	S string         `config:"s"`
	I int            `config:"i"`
	B bool           `config:"b"`
	D string         `config:"d"`
	L []string       `config:"l"`
	M map[string]int `config:"m"`
}

// Whatever a key holds, loading it neither panics nor gives a different answer
// the second time, and a bad value is a reason, never the value.
func FuzzValue(f *testing.F) {
	for _, seed := range []string{"", "1", "-1", "true", "9999999999999999999999", "a,b", "k:1,j:2", "k:", ":", "\xff", "a\x00b", "https://u:p@h/", " ", "\n", strings.Repeat("a", 4096)} {
		f.Add([]byte(seed), []byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw, text []byte) {
		keys := []string{"s", "i", "b", "d", "l", "m"}

		values := map[string]string{}
		for _, k := range keys {
			values["/p/svc/"+k] = string(raw)
		}

		p, _ := provider(t, values, "/p")

		var first, second fuzzConfig

		err1 := p.Unmarshal([]string{"svc"}, &first)
		err2 := p.Unmarshal([]string{"svc"}, &second)

		if (err1 == nil) != (err2 == nil) || !reflect.DeepEqual(first, second) {
			t.Fatalf("two answers: %+v, %v then %+v, %v", first, err1, second, err2)
		}

		// A value wrapped in a marker the parsers' own words can't contain: the
		// marker, and so the value, must not come back in an error.
		marker := "zqxj" + hex.EncodeToString(text) + "zqxj"

		for _, k := range keys {
			values["/p/svc/"+k] = marker
		}

		q, s := provider(t, values, "/p")
		_ = s

		var third fuzzConfig
		if err := q.Unmarshal([]string{"svc"}, &third); err != nil {
			noLeak(t, err, "zqxj")
		}
	})
}

// A prefix that is accepted is one that cannot reach outside itself or confuse
// what a key is, and every key the provider would read is inside it.
func FuzzPrefix(f *testing.F) {
	for _, seed := range []string{"", "/", "/a", "/a/b", "/a/", "//", "/a/../b", "/a\x00", "/\xff", "/a ", strings.Repeat("/a", 200)} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, prefix string) {
		p := etcd.New(nil, prefix)

		var dst fuzzConfig

		keys, err := p.Describe([]string{"svc", "http.server"}, &dst)
		if err != nil {
			// A short prefix can be a part of the fixed reason ("a" of "start with a
			// slash"), which is no leak.
			if len(prefix) >= 16 {
				noLeak(t, err, prefix)
			}

			if len(keys) != 0 {
				t.Fatalf("keys with an error: %v", keys)
			}

			return
		}

		if !utf8.ValidString(prefix) || !strings.HasPrefix(prefix, "/") || prefix == "/" || strings.HasSuffix(prefix, "/") || len(prefix) > etcd.MaxPrefixBytes {
			t.Fatalf("accepted the prefix %q", prefix)
		}

		for _, seg := range strings.Split(prefix[1:], "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("accepted the prefix %q", prefix)
			}
		}

		for _, r := range prefix {
			if r < 0x20 || r == 0x7f || r == utf8.RuneError || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) {
				t.Fatalf("accepted the prefix %q", prefix)
			}
		}

		for _, k := range keys {
			if !strings.HasPrefix(k.Key, prefix+"/") {
				t.Fatalf("key %q is outside %q", k.Key, prefix)
			}
		}
	})
}

// Whatever the function is called, its keys are inside the prefix and are made of
// plain words.
func FuzzPath(f *testing.F) {
	for _, seed := range []string{"svc", "http.server", "../../etc", "a/b", "", ".", "..", "a\x00b", "é", "A-B", "\xff"} {
		f.Add("svc", seed)
	}

	f.Fuzz(func(t *testing.T, root, name string) {
		p := etcd.New(nil, "/p")

		var dst fuzzConfig

		keys, err := p.Describe([]string{root, name}, &dst)
		if err != nil {
			return
		}

		for _, k := range keys {
			if !strings.HasPrefix(k.Key, "/p/") {
				t.Fatalf("key %q is outside the prefix", k.Key)
			}

			for _, seg := range strings.Split(k.Key[1:], "/") {
				if seg == "" || seg == "." || seg == ".." {
					t.Fatalf("key %q has a segment %q", k.Key, seg)
				}

				for _, r := range seg {
					if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
						t.Fatalf("key %q has %q", k.Key, r)
					}
				}
			}
		}
	})
}
