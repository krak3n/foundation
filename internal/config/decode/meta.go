package decode

import (
	"fmt"
	"reflect"
	"strings"
)

// MaxHelpTag is the longest help tag, in bytes. Help is text for a person to read
// next to a flag, and a tag past this is a mistake, not a description.
const MaxHelpTag = 1024

// Meta is what a field says about itself for documentation: what it is for, from
// its help tag, and what type it is, as a person would write it.
type Meta struct {
	Help string
	Type string
}

// MetaOf finds the field at goPath in the struct type rt and returns its [Meta].
// goPath is a Go path as providers report it: "TLS.CertFile", where fields of
// embedded structs are named as if declared in the parent. found is false for a
// path that leads nowhere, which a provider can't produce for a struct it
// described. The error is for a help tag over [MaxHelpTag]; it never contains the
// tag.
func MetaOf(rt reflect.Type, goPath string) (m Meta, found bool, err error) {
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}

	segments := strings.Split(goPath, ".")

	var sf reflect.StructField

	for i, seg := range segments {
		if rt.Kind() != reflect.Struct {
			return Meta{}, false, nil
		}

		var ok bool

		sf, ok = fieldByName(rt, seg)
		if !ok {
			return Meta{}, false, nil
		}

		if i < len(segments)-1 {
			rt = sf.Type
			if rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
		}
	}

	if mistake := TagMistake(sf.Tag); mistake != nil {
		return Meta{}, true, mistake
	}

	help := sf.Tag.Get("help")
	if len(help) > MaxHelpTag {
		return Meta{}, true, fmt.Errorf("the help tag is longer than %d bytes", MaxHelpTag)
	}

	return Meta{Help: help, Type: TypeName(sf.Type)}, true, nil
}

// fieldByName finds a field by Go name, looking into embedded structs, which
// providers load as if their fields were declared in the parent.
func fieldByName(rt reflect.Type, name string) (reflect.StructField, bool) {
	for i := range rt.NumField() {
		sf := rt.Field(i)

		if sf.Name == name {
			return sf, true
		}
	}

	for i := range rt.NumField() {
		sf := rt.Field(i)

		if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			if got, ok := fieldByName(sf.Type, name); ok {
				return got, true
			}
		}
	}

	return reflect.StructField{}, false
}

// TypeName names a field type as a person would write it in help: string, int,
// bool, duration, url, "list of T", "map of K to V", and for a type that parses
// itself from text (net.IP) its own name. Pointers are looked through, and sized
// numbers keep their size (int64, float32).
func TypeName(rt reflect.Type) string {
	if rt.Kind() == reflect.Pointer {
		return TypeName(rt.Elem())
	}

	switch Classify(rt) {
	case Duration:
		return "duration"
	case URL:
		return "url"
	case Text:
		if rt.Name() != "" {
			return rt.Name()
		}

		return "text"
	case String:
		return "string"
	case Bool:
		return "bool"
	case Int, Uint, Float:
		return rt.Kind().String()
	}

	switch rt.Kind() {
	case reflect.Slice:
		return "list of " + TypeName(rt.Elem())
	case reflect.Map:
		return "map of " + TypeName(rt.Key()) + " to " + TypeName(rt.Elem())
	}

	return rt.String()
}
