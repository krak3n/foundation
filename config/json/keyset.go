package json

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.krak3n.dev/foundation/config"
)

// keyset records the paths in the document that a provider has been asked for,
// whether or not the document has anything there, so that strict mode can tell
// what nothing asked for. Keys are compared without regard to case, as they are
// when loading, so they are stored lowercased. It is safe for concurrent use.
type keyset struct {
	mu   sync.Mutex
	root keyNode
}

// A keyNode is a place in the document that was asked for: a field, whose value
// is the value and not more configuration, or a place on the way to one.
type keyNode struct {
	children map[string]*keyNode
}

// add records that path was asked for.
func (k *keyset) add(path []string) {
	k.mu.Lock()
	defer k.mu.Unlock()

	node := &k.root

	for _, key := range path {
		key = strings.ToLower(key)

		child, ok := node.children[key]
		if !ok {
			if node.children == nil {
				node.children = make(map[string]*keyNode)
			}

			child = &keyNode{}
			node.children[key] = child
		}

		node = child
	}
}

// unknown lists the paths under root in the document doc that were not asked
// for, as they are spelt in the document, sorted. A place nothing asked for is
// one path, not one for everything inside it. A field is not looked inside: what
// it holds is its value, such as the entries of a map.
func (k *keyset) unknown(doc map[string]any, root []string) [][]string {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Follow the root through the document and through what was asked for. If
	// nothing was asked for there, everything in the document is unknown.
	node, known := &k.root, true

	// The path is built from the document's own spelling of each key.
	spelt := make([]string, 0, len(root))

	for _, key := range root {
		actual, child := lookup(doc, key)
		doc, _ = child.(map[string]any)

		if doc == nil {
			return nil
		}

		spelt = append(spelt, actual)

		if known {
			node, known = node.children[strings.ToLower(key)]
		}
	}

	if !known {
		node = &keyNode{}
	}

	var out [][]string

	walk(doc, node, spelt, &out)

	return out
}

func walk(obj map[string]any, node *keyNode, path []string, out *[][]string) {
	keys := make([]string, 0, len(obj))

	for key := range obj {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	for _, key := range keys {
		here := append(slices.Clone(path), key)

		child, ok := node.children[strings.ToLower(key)]

		switch {
		case !ok:
			*out = append(*out, here)
		case len(child.children) > 0:
			if sub, isObject := obj[key].(map[string]any); isObject {
				walk(sub, child, here, out)
			}
		}
	}
}

// unknownErrors turns the unknown paths into errors. source names where the
// document came from, such as a file, or is empty.
func unknownErrors(paths [][]string, source string) error {
	var errs []error

	for _, path := range paths {
		cause := config.ErrUnknown
		if source != "" {
			cause = fmt.Errorf("%w (in %s)", config.ErrUnknown, source)
		}

		errs = append(errs, &config.FieldError{Provider: "json", Key: strings.Join(path, "."), Err: cause})
	}

	return errors.Join(errs...)
}

// lookup finds key in m without regard to case, returning the key as m spells it
// and its value.
func lookup(m map[string]any, key string) (string, any) {
	lower := strings.ToLower(key)

	for k, v := range m {
		if strings.ToLower(k) == lower {
			return k, v
		}
	}

	return key, nil
}
