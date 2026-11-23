// Package decode holds the rules config providers share for which field types
// they support and how a value is turned into one, so that every provider
// accepts and rejects the same things. It works on the text form of a value (an
// environment variable, a string in a file); providers whose format has native
// numbers, lists and objects build on it.
package decode

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Category says how a type is decoded from text.
type Category int

const (
	// None is a type that is not a single value: a struct to walk into, a
	// slice, a map, or something unsupported.
	None Category = iota

	// Text implements encoding.TextUnmarshaler, such as time.Time or net.IP.
	Text

	// URL is url.URL, which parses itself with url.Parse.
	URL

	// Duration is time.Duration.
	Duration

	String
	Bool
	Int
	Uint
	Float
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	urlType             = reflect.TypeFor[url.URL]()
	durationType        = reflect.TypeFor[time.Duration]()
)

// Classify reports how values of type rt are decoded from text, or None if rt
// is not a single value.
func Classify(rt reflect.Type) Category {
	if rt.Kind() == reflect.Pointer {
		return None
	}

	if reflect.PointerTo(rt).Implements(textUnmarshalerType) {
		return Text
	}

	switch {
	case rt == urlType:
		return URL
	case rt == durationType:
		return Duration
	}

	switch rt.Kind() {
	case reflect.String:
		return String
	case reflect.Bool:
		return Bool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Int
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Uint
	case reflect.Float32, reflect.Float64:
		return Float
	}

	return None
}

// IsGroup reports whether rt is a struct to be walked into, as opposed to a
// struct that is a single value such as time.Time.
func IsGroup(rt reflect.Type) bool {
	return rt.Kind() == reflect.Struct && Classify(rt) == None
}

// IsPlainString reports whether rt is a string, or a pointer to one, that has no
// parsing of its own. For these an empty value is a real value.
func IsPlainString(rt reflect.Type) bool {
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}

	return Classify(rt) == String
}

// Check reports whether a field of type rt can be set from text: a single value,
// a pointer to one, a slice of them or a map of them.
func Check(rt reflect.Type) error {
	if Classify(rt) != None {
		return nil
	}

	switch rt.Kind() {
	case reflect.Pointer:
		if Classify(rt.Elem()) != None {
			return nil
		}
	case reflect.Slice:
		if Classify(rt.Elem()) != None {
			return nil
		}
	case reflect.Map:
		if Classify(rt.Key()) != None && Classify(rt.Elem()) != None {
			return nil
		}
	}

	return fmt.Errorf("unsupported type %s", rt)
}

// SetLeaf sets rv, an addressable single value (see [Classify]), from s.
// Integers are base 10, so a leading zero is not octal.
func SetLeaf(rv reflect.Value, s string) error {
	rt := rv.Type()

	switch Classify(rt) {
	case Text:
		if err := rv.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s)); err != nil {
			return invalid(Text, rt, err)
		}
	case URL:
		u, err := url.Parse(s)
		if err != nil {
			return invalid(URL, rt, err)
		}

		rv.Set(reflect.ValueOf(*u))
	case Duration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return invalid(Duration, rt, err)
		}

		rv.SetInt(int64(d))
	case String:
		rv.SetString(s)
	case Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return invalid(Bool, rt, err)
		}

		rv.SetBool(b)
	case Int:
		i, err := strconv.ParseInt(s, 10, rt.Bits())
		if err != nil {
			return invalid(Int, rt, err)
		}

		rv.SetInt(i)
	case Uint:
		u, err := strconv.ParseUint(s, 10, rt.Bits())
		if err != nil {
			return invalid(Uint, rt, err)
		}

		rv.SetUint(u)
	case Float:
		f, err := strconv.ParseFloat(s, rt.Bits())
		if err != nil {
			return invalid(Float, rt, err)
		}

		rv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported type %s", rt)
	}

	return nil
}

// invalid is the error for text that can't be turned into a value of type rt,
// which is of the given category. It says what was wrong without repeating the
// text: the parsing functions quote their input, and the input is configuration,
// which may be a password in the wrong field or a URL with credentials in it,
// and errors are logged. The field and key say where to look.
//
// What kind of mistake it was is kept for a number, as the error wraps
// [strconv.ErrSyntax] or [strconv.ErrRange].
func invalid(category Category, rt reflect.Type, err error) error {
	switch category {
	case Int, Uint:
		return invalidNumber("integer", err)
	case Float:
		return invalidNumber("number", err)
	case Bool:
		return invalidNumber("bool", err)
	case Duration:
		return errors.New("not a valid duration, such as 30s")
	case URL:
		// Even the reason can repeat part of the text, such as a bad escape.
		return errors.New("not a valid URL")
	}

	// A type with a parser of its own: its error probably quotes its input.
	return fmt.Errorf("not a valid %s", rt)
}

func invalidNumber(noun string, err error) error {
	var numErr *strconv.NumError

	if errors.As(err, &numErr) {
		return fmt.Errorf("not a valid %s: %w", noun, numErr.Err)
	}

	return fmt.Errorf("not a valid %s", noun)
}

// SetString sets rv from the text s, for a field of any type [Check] accepts. A
// pointer is given a new value rather than changing the one it points to, as
// that may be shared. A slice is a comma separated list, and a map a comma
// separated list of key:value pairs, split on the first colon so that values
// may contain colons. Both replace what was there.
func SetString(rv reflect.Value, s string) error {
	rt := rv.Type()

	if Classify(rt) != None {
		return SetLeaf(rv, s)
	}

	switch rt.Kind() {
	case reflect.Pointer:
		value := reflect.New(rt.Elem())

		if err := SetLeaf(value.Elem(), s); err != nil {
			return err
		}

		rv.Set(value)
	case reflect.Slice:
		items := splitList(s)
		out := reflect.MakeSlice(rt, len(items), len(items))

		for i, item := range items {
			if err := SetLeaf(out.Index(i), item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}

		rv.Set(out)
	case reflect.Map:
		out := reflect.MakeMap(rt)

		for i, item := range splitList(s) {
			if item == "" {
				continue // a trailing comma is not an entry
			}

			k, v, ok := strings.Cut(item, ":")
			if !ok {
				return fmt.Errorf("item %d: expected key:value", i)
			}

			key, value := reflect.New(rt.Key()).Elem(), reflect.New(rt.Elem()).Elem()

			if err := SetLeaf(key, strings.TrimSpace(k)); err != nil {
				return fmt.Errorf("item %d key: %w", i, err)
			}

			if err := SetLeaf(value, strings.TrimSpace(v)); err != nil {
				return fmt.Errorf("item %d value: %w", i, err)
			}

			out.SetMapIndex(key, value)
		}

		rv.Set(out)
	default:
		return fmt.Errorf("unsupported type %s", rt)
	}

	return nil
}

func splitList(s string) []string {
	items := strings.Split(s, ",")

	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}

	return items
}
