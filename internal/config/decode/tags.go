package decode

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// tagKeys are the struct tag keys the framework reads.
var tagKeys = [...]string{"config", "env", "flag", "help", "json", "yaml"}

var errMalformedTag = errors.New("struct tag is malformed")

// TagMistake reports a mistake in a struct tag that reflect would hide: a key the
// framework reads given more than once, or a tag that can't be read to the end.
//
// reflect.StructTag.Get returns the first value of a key, so
// `config:"addr" config:"required"` quietly drops the second and the field is not
// required. After a bad quote everything is invisible, so a key behind it is
// ignored. Both are mistakes in code, so they are refused when the struct is
// read.
//
// Only the keys in tagKeys count, and a tag that can't be read is a mistake only
// if it has one of them in it: other libraries' tags are not ours to judge. The
// reason names the key and never shows the tag. The scan is a single pass that
// follows the grammar of reflect.StructTag.Lookup.
func TagMistake(tag reflect.StructTag) error {
	var (
		seen [len(tagKeys)]bool

		// last is the value read last: a quote that ended early may have swallowed
		// a pair of ours, which is then what can't be read.
		last string
	)

	rest := string(tag)

	for rest != "" {
		// Skip leading space. reflect skips only the space character; a tab
		// between two pairs hides the second from it, which is no reason to let a
		// repeat through.
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
			i++
		}

		rest = rest[i:]
		if rest == "" {
			break
		}

		// The name runs up to a colon: no control characters, spaces or quotes.
		i = 0
		for i < len(rest) && rest[i] > ' ' && rest[i] != ':' && rest[i] != '"' && rest[i] != 0x7f {
			i++
		}

		if i == 0 || i+1 >= len(rest) || rest[i] != ':' || rest[i+1] != '"' {
			return unreadable(last, rest)
		}

		pair, name := rest, rest[:i]
		rest = rest[i+1:]

		// The quoted value, which may have escaped quotes.
		i = 1
		for i < len(rest) && rest[i] != '"' {
			if rest[i] == '\\' {
				i++
			}

			i++
		}

		if i >= len(rest) {
			return unreadable(last, pair)
		}

		quoted := rest[:i+1]
		rest = rest[i+1:]

		value, err := strconv.Unquote(quoted)
		if err != nil {
			return unreadable(last, pair)
		}

		last = value

		for k, key := range tagKeys {
			if name != key {
				continue
			}

			if seen[k] {
				return fmt.Errorf("struct tag key %s is given more than once", strconv.Quote(key))
			}

			seen[k] = true
		}
	}

	return nil
}

// unreadable is the mistake for a tag that stops making sense at rest. It is
// ours to report if a key the framework reads is in what could not be read, or in
// the value before it, which an early quote may have cut short.
func unreadable(last, rest string) error {
	for _, key := range tagKeys {
		if strings.Contains(rest, key) || strings.Contains(last, key) {
			return errMalformedTag
		}
	}

	return nil
}
