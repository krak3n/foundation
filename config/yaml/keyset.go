package yaml

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	yamllib "go.yaml.in/yaml/v3"

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

// unknown lists the paths under root in doc that were not asked for, as they are
// spelt in the document, sorted at each level. A place nothing asked for is one
// path, not one for everything inside it, and a field is not looked inside: what
// it holds is its value, such as the entries of a map.
func (k *keyset) unknown(doc *yamllib.Node, root []string) [][]string {
	k.mu.Lock()
	defer k.mu.Unlock()

	node, known := &k.root, true

	spelt := make([]string, 0, len(root))

	for _, key := range root {
		found := find(entries(doc), key)
		if len(found) == 0 {
			return nil
		}

		doc = asMapping(found[0].value)
		if doc == nil {
			return nil
		}

		spelt = append(spelt, found[0].key)

		if known {
			node, known = node.children[strings.ToLower(key)]
		}
	}

	if !known {
		node = &keyNode{}
	}

	var out [][]string

	walk(doc, node, spelt, &out)

	// A key that is there twice is one problem.
	seen := map[string]bool{}

	return slices.DeleteFunc(out, func(path []string) bool {
		id := strings.Join(path, "\x00")
		dup := seen[id]
		seen[id] = true

		return dup
	})
}

func walk(m *yamllib.Node, node *keyNode, path []string, out *[][]string) {
	es := entries(m)

	slices.SortStableFunc(es, func(a, b entry) int { return strings.Compare(a.key, b.key) })

	for _, e := range es {
		here := append(slices.Clone(path), e.key)

		child, ok := node.children[strings.ToLower(e.key)]

		switch {
		case !ok:
			*out = append(*out, here)
		case len(child.children) > 0:
			if sub := asMapping(e.value); sub != nil {
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

		errs = append(errs, &config.FieldError{Provider: "yaml", Key: strings.Join(path, "."), Err: cause})
	}

	return errors.Join(errs...)
}
