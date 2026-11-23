package yaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	yamllib "go.yaml.in/yaml/v3"
)

// The bounds on a document. A configuration file is a few kilobytes; these are
// well beyond any real one, and keep what a hostile one can use finite.
const (
	// maxDocument is the largest document that is parsed.
	maxDocument = 8 << 20

	// maxDepth is how deeply collections may be nested.
	maxDepth = 64

	// maxNodes is how many nodes (keys, values and collections) a document may hold.
	maxNodes = 100_000
)

// lineError is an error about a place in the document. The reason is a fixed
// phrase: the document is configuration, and may hold a secret in the wrong
// place, so nothing of it is ever repeated.
func lineError(line int, reason string) error {
	if line <= 0 {
		return fmt.Errorf("yaml: %s", reason)
	}

	return fmt.Errorf("yaml: line %d: %s", line, reason)
}

// libraryLine finds the line a parser error is about. Only digits are taken from
// the message, which would otherwise quote the input.
var libraryLine = regexp.MustCompile(`^yaml: (?:unmarshal errors:\s+)?line (\d+):`)

var unknownAnchor = regexp.MustCompile(`(?s)unknown anchor '(.*)' referenced`)

// libraryError turns an error from the parser into one that says where it was
// and what kind of mistake it was, without its words.
func libraryError(err error, data []byte) error {
	msg := err.Error()

	reason := "not valid YAML"

	switch {
	case strings.Contains(msg, "unknown anchor"):
		reason = "aliases are not supported"
	case strings.Contains(msg, "max depth"):
		reason = fmt.Sprintf("nested too deeply (the limit is %d)", maxDepth)
	}

	line := 0

	if m := libraryLine.FindStringSubmatch(msg); m != nil {
		fmt.Sscan(m[1], &line)
	}

	// An alias without an anchor is reported without a line. The name is only
	// used to look for it, and is never repeated.
	if m := unknownAnchor.FindStringSubmatch(msg); m != nil && line == 0 {
		if i := bytes.Index(data, []byte("*"+m[1])); i >= 0 {
			line = 1 + bytes.Count(data[:i], []byte("\n"))
		}
	}

	return lineError(line, reason)
}

// parse reads one document and returns its top level mapping, or nil if it has
// no content. It refuses what could make the document say something other than
// what it appears to, or cost more than it should (see the package comment), as
// a whole and wherever it is, so that a reviewer and the service read the same
// file.
func parse(data []byte) (root *yamllib.Node, err error) {
	if len(data) > maxDocument {
		return nil, errors.New("yaml: the document is too large (the limit is 8 MiB)")
	}

	if line, reason := scan(data); reason != "" {
		return nil, lineError(line, reason)
	}

	// Parsing builds every node, so a document that is clearly too big for the
	// limit is refused before that, instead of after it.
	if 2*separators(data)+1 > 4*maxNodes {
		return nil, tooMany()
	}

	// The parser is a dependency and may have bugs: a file must never be a reason
	// for the process to die.
	defer func() {
		if recover() != nil {
			root, err = nil, errors.New("yaml: the document could not be read")
		}
	}()

	dec := yamllib.NewDecoder(bytes.NewReader(data))

	var doc yamllib.Node

	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}

		return nil, libraryError(err, data)
	}

	// A second document would be read by some tools and ignored by others.
	var next yamllib.Node

	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, libraryError(err, data)
		}

		return nil, lineError(documentLine(data), "more than one document is not supported")
	}

	if len(doc.Content) != 1 {
		return nil, nil
	}

	v := &validator{}

	if err := v.walk(doc.Content[0], 1); err != nil {
		return nil, err
	}

	top := doc.Content[0]

	switch {
	case isNull(top):
		return nil, nil
	case top.Kind != yamllib.MappingNode:
		return nil, lineError(top.Line, "the document must be a mapping")
	}

	return top, nil
}

// documentLine is the line of the marker that starts a second document.
func documentLine(data []byte) int {
	first := true

	for i, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("---")) || (len(line) > 3 && line[3] != ' ' && line[3] != '\t' && line[3] != '\r') {
			continue
		}

		// The first marker may open the first document.
		if first && i == 0 {
			first = false

			continue
		}

		return i + 1
	}

	return 0
}

func tooMany() error {
	return fmt.Errorf("yaml: too many nodes in the document (the limit is %d)", maxNodes)
}

// scan finds bytes a configuration file has no use for: control characters,
// which a terminal or a log reader would act on, and what isn't UTF-8. It returns
// the line and what was wrong, or no reason.
func scan(data []byte) (int, string) {
	line := 1

	for i := 0; i < len(data); {
		b := data[i]

		switch {
		case b == '\n':
			line++
		case b < 0x20 && b != '\t' && b != '\r', b == 0x7f:
			return line, "control characters are not allowed"
		case b >= utf8.RuneSelf:
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 {
				return line, "not valid UTF-8"
			}

			i += size

			continue
		}

		i++
	}

	return 0, ""
}

// separators counts the bytes that can sit between two nodes, so that the number
// of nodes can't be much more than this. It overcounts, and is only a bound.
func separators(data []byte) int {
	n := 0

	for i, b := range data {
		switch b {
		case '\n', ',', '[', '{':
			n++
		case ':', '-', '?':
			if i+1 == len(data) || data[i+1] == ' ' || data[i+1] == '\t' || data[i+1] == '\n' || data[i+1] == '\r' {
				n++
			}
		}
	}

	return n
}

// validator checks the whole tree of a document.
type validator struct {
	nodes int
}

func (v *validator) walk(n *yamllib.Node, depth int) error {
	if v.nodes++; v.nodes > maxNodes {
		return tooMany()
	}

	if depth > maxDepth {
		return lineError(n.Line, fmt.Sprintf("nested too deeply (the limit is %d)", maxDepth))
	}

	switch {
	case n.Anchor != "":
		return lineError(n.Line, "anchors are not supported")
	case n.Kind == yamllib.AliasNode:
		return lineError(n.Line, "aliases are not supported")
	case n.Style&yamllib.TaggedStyle != 0:
		return lineError(n.Line, "tags are not supported")
	}

	switch n.Kind {
	case yamllib.SequenceNode:
		for _, item := range n.Content {
			if err := v.walk(item, depth+1); err != nil {
				return err
			}
		}
	case yamllib.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]

			switch {
			case key.Kind != yamllib.ScalarNode && key.Kind != yamllib.AliasNode:
				return lineError(key.Line, "complex keys are not supported")
			case key.Kind == yamllib.ScalarNode && key.Tag == "!!merge" && key.Style == 0:
				return lineError(key.Line, "merge keys are not supported")
			}

			if err := v.walk(key, depth+1); err != nil {
				return err
			}

			if err := v.walk(value, depth+1); err != nil {
				return err
			}
		}
	}

	return nil
}

// isNull reports whether n is an unquoted null: null, ~ or nothing at all.
// Quoted, it is a string like any other.
func isNull(n *yamllib.Node) bool {
	return n != nil && n.Kind == yamllib.ScalarNode && n.Tag == "!!null" && n.Style&(yamllib.DoubleQuotedStyle|yamllib.SingleQuotedStyle|yamllib.LiteralStyle|yamllib.FoldedStyle) == 0
}
