package json

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.krak3n.dev/foundation/config"
)

// A document that has the same key twice, or two keys that differ only in case,
// has no single value for it: the decoder keeps the last of an exact duplicate, and
// which of two spellings a lookup finds depends on the order Go happens to range
// over a map. Reviewing one value and running another is not something a
// configuration file should allow, so a key that is read and appears more than once
// is an error.
//
// Only keys that are read matter. A repeated "_comment" in an object, or a section
// that belongs to another service, is none of this provider's business, so the
// document is scanned once to find where keys repeat, and the check is made when a
// key is looked up.

// separator joins the keys of a path. It can't be in a key that has been
// lowercased from JSON text in a way that matters here, and a clash would only be
// a false ambiguity in a document that already repeats keys.
const separator = "\x00"

// duplicates records where a document repeats a key: for each object, identified
// by the lowercase keys that lead to it, each lowercase key that appears more than
// once, with the spellings used. A nil *duplicates records nothing.
type duplicates struct {
	byObject map[string]map[string][]string
}

func (d *duplicates) add(object, key string, spellings []string) {
	if d.byObject == nil {
		d.byObject = make(map[string]map[string][]string)
	}

	if d.byObject[object] == nil {
		d.byObject[object] = make(map[string][]string)
	}

	d.byObject[object][key] = slices.Clone(spellings)
}

// objectPath is the identity of the object at the given keys.
func objectPath(keys []string) string {
	lower := make([]string, len(keys))

	for i, k := range keys {
		lower[i] = strings.ToLower(k)
	}

	return strings.Join(lower, separator)
}

// ambiguous returns the spellings of key, in the object at keys, if it appears
// there more than once, however it is spelt, or nil if it doesn't.
func (d *duplicates) ambiguous(keys []string, key string) []string {
	if d == nil {
		return nil
	}

	return d.byObject[objectPath(keys)][strings.ToLower(key)]
}

// repeated returns the keys of the object at keys that are written exactly the
// same more than once. Keys of a map are case sensitive, so for them only an exact
// repeat is ambiguous.
func (d *duplicates) repeated(keys []string) []string {
	if d == nil {
		return nil
	}

	var out []string

	for _, spellings := range d.byObject[objectPath(keys)] {
		count := map[string]int{}

		for _, s := range spellings {
			count[s]++

			if count[s] == 2 {
				out = append(out, s)
			}
		}
	}

	slices.Sort(out)

	return out
}

// scanDuplicates finds the repeated keys in data. A document that isn't valid
// JSON is reported by the decoder that reads it, so a problem here just stops the
// scan.
func scanDuplicates(data []byte) *duplicates {
	d := &duplicates{}

	type frame struct {
		object  bool
		tracked bool   // whether this object can be reached by following keys
		path    string // the lowercase keys that lead here
		key     string // the lowercase key whose value is being read
		wantKey bool
		seen    map[string][]string
	}

	var stack []*frame

	valueRead := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	for {
		tok, err := dec.Token()
		if err != nil {
			return d
		}

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				fr := &frame{object: t == '{', tracked: true}

				if n := len(stack); n > 0 {
					parent := stack[n-1]

					// Objects inside arrays are not reached by following keys.
					fr.tracked = parent.tracked && parent.object

					if fr.tracked {
						fr.path = parent.path
						if fr.path != "" {
							fr.path += separator
						}

						fr.path += parent.key
					}
				}

				if fr.object {
					fr.wantKey = true
					fr.seen = make(map[string][]string)
				}

				stack = append(stack, fr)
			default:
				stack = stack[:len(stack)-1]

				valueRead()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
				fr := stack[n-1]
				key := strings.ToLower(t)

				fr.key, fr.wantKey = key, false

				if fr.tracked {
					fr.seen[key] = append(fr.seen[key], t)

					if len(fr.seen[key]) > 1 {
						d.add(fr.path, key, fr.seen[key])
					}
				}

				continue
			}

			valueRead()
		default:
			valueRead()
		}
	}
}

// ambiguousError is the error for a key that appears more than once, naming the
// spellings used and not the values.
func ambiguousError(spellings []string) error {
	quoted := make([]string, len(spellings))

	for i, s := range spellings {
		quoted[i] = strconv.Quote(s)
	}

	return fmt.Errorf("%s: %w", strings.Join(quoted, ", "), config.ErrAmbiguous)
}
