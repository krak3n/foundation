// Package nilcheck says whether a value is nil in the way that matters when it is
// held in an interface: a nil pointer inside an interface is not equal to nil, and
// calling a method on it may panic.
//
// The package watch keeps a copy of Is, because it imports only the standard
// library: a change to one is a change to the other.
package nilcheck

import "reflect"

// Is reports whether v is nil, or holds a nil pointer, map, slice, channel or
// function.
func Is(v any) bool {
	if v == nil {
		return true
	}

	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}
