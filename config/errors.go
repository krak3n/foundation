package config

import (
	"errors"
	"strings"
)

// ErrUnknown is the cause of a [FieldError] for a key that is present in a source
// but that nothing asked for, such as a misspelt environment variable. Test for
// it with [errors.Is].
var ErrUnknown = errors.New("not recognised")

// ErrRequired is the cause of a [FieldError] for a field marked
// `config:"required"` that no provider, and no default, gave a value. Test for it
// with [errors.Is].
var ErrRequired = errors.New("required but not set")

// ErrUndefined is the cause of a [FieldError] for a ${NAME} reference in a field
// marked `config:"expand"` to a variable that isn't set and has no default. Its
// Key is the variable. Test for it with [errors.Is].
var ErrUndefined = errors.New("undefined variable")

// ErrAmbiguous is the cause of a [FieldError] for a key that a source holds more
// than once, such as a JSON object with both "addr" and "ADDR". Which one a
// reader would pick is not defined, so none is used. Test for it with [errors.Is].
var ErrAmbiguous = errors.New("appears more than once")

// FieldError is a problem with one field of the struct being loaded: a value
// that couldn't be parsed, or a field that can never be loaded, such as one with
// an unsupported type. Providers return one for every such problem, joined with
// [errors.Join], so that all of them are reported together instead of one at a
// time.
//
// It unwraps to the underlying cause, so [errors.Is] and [errors.As] see
// through it, and an error that joins several can be searched with [errors.As].
type FieldError struct {
	// Provider names the provider that found the problem, such as "env".
	Provider string

	// Field is the Go name of the field, for nested structs the path to it:
	// "TLS.CertFile". An embedded struct's fields are named as if they were
	// declared in the parent.
	Field string

	// Key is where the value is looked up by that provider: the environment
	// variable APP_TLS_CERT_FILE, or the path in a document, svc.tls.cert_file.
	// It is empty when it isn't known, for example when the field's tag has no
	// name. For a problem that isn't any one provider's, such as a missing
	// required field, it lists where each provider that can say would look,
	// separated by commas.
	Key string

	// Err is what was wrong.
	Err error
}

// Error implements the error interface, for example
// "env: field TLS.CertFile (APP_TLS_CERT_FILE): invalid syntax", or, for a key
// that matches no field, "env: APP_TLS_CERT_FIL: not recognised".
func (e *FieldError) Error() string {
	var parts []string

	if e.Provider != "" {
		parts = append(parts, e.Provider)
	}

	switch {
	case e.Field != "" && e.Key != "":
		parts = append(parts, "field "+e.Field+" ("+e.Key+")")
	case e.Field != "":
		parts = append(parts, "field "+e.Field)
	case e.Key != "":
		parts = append(parts, e.Key)
	}

	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}

	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying cause.
func (e *FieldError) Unwrap() error {
	return e.Err
}
