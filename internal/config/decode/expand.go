package decode

import (
	"fmt"
	"strings"
)

// Expand replaces the references in s, which is a template, with the values of
// the variables they name, read with lookup:
//
//   - ${NAME} is the value of NAME. A variable that is set, even to empty, is
//     defined; one that isn't is reported in undefined and replaced with nothing.
//   - ${NAME:-default} is the value of NAME, or default if it is unset or empty.
//     The default is literal text up to the first '}', so it can't hold a
//     reference.
//   - $$ is a literal $.
//
// Anything else is literal, including a $ that isn't followed by '{' or '$', so
// only the braced form is a reference. Names are letters, digits and
// underscores, and don't start with a digit.
//
// It reads s once from start to end and never scans a value it has inserted, so
// what a variable holds can't cause another lookup. undefined lists each
// undefined name once, in the order met. A syntax error, such as an unterminated
// reference, is returned as err.
func Expand(s string, lookup func(name string) (value string, ok bool)) (out string, undefined []string, err error) {
	var b strings.Builder

	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])

			i++

			continue
		}

		switch s[i+1] {
		case '$':
			b.WriteByte('$')

			i += 2
		case '{':
			ref, size, err := parseReference(s[i:])
			if err != nil {
				return "", nil, fmt.Errorf("%w at offset %d", err, i)
			}

			i += size

			value, ok := lookup(ref.name)

			switch {
			case ref.hasDefault && (!ok || value == ""):
				b.WriteString(ref.def)
			case !ok:
				if !contains(undefined, ref.name) {
					undefined = append(undefined, ref.name)
				}
			default:
				b.WriteString(value)
			}
		default:
			b.WriteByte('$')

			i++
		}
	}

	return b.String(), undefined, nil
}

type reference struct {
	name       string
	def        string
	hasDefault bool
}

// parseReference reads the reference at the start of s, which begins "${", and
// returns it with how many bytes it takes.
func parseReference(s string) (reference, int, error) {
	i := 2

	for i < len(s) && isNameByte(s[i], i > 2) {
		i++
	}

	name := s[2:i]

	if i >= len(s) {
		return reference{}, 0, fmt.Errorf("unterminated ${")
	}

	switch s[i] {
	case '}':
		if name == "" {
			return reference{}, 0, fmt.Errorf("empty variable name in ${}")
		}

		return reference{name: name}, i + 1, nil
	case ':':
		if i+1 >= len(s) {
			return reference{}, 0, fmt.Errorf("unterminated ${")
		}

		if s[i+1] != '-' {
			return reference{}, 0, fmt.Errorf("invalid ${%s: only :- is understood after a name", name)
		}

		if name == "" {
			return reference{}, 0, fmt.Errorf("empty variable name in ${:-")
		}

		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return reference{}, 0, fmt.Errorf("unterminated ${%s:-", name)
		}

		return reference{name: name, def: s[i+2 : i+2+end], hasDefault: true}, i + 2 + end + 1, nil
	default:
		return reference{}, 0, fmt.Errorf("invalid variable name: %q is not a letter, digit or underscore, or a name starts with a digit", s[i:i+1])
	}
}

func isNameByte(c byte, notFirst bool) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return notFirst
	}

	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}
