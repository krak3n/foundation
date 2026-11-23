// Package envname holds the rule for the name of an environment variable that a
// program names in code: for an alias in the env provider, and for the variable
// that says where a file is in the file providers. One rule, so they agree.
package envname

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxLength is the longest a name can be, in bytes.
const MaxLength = 128

// A name that means something to the system, or that holds the credentials of
// another one, is not for configuration to read: it would put them in a struct
// that gets logged, or make the system's own settings choose a file. This is a
// safety net against mistakes, not a complete list: what a service reads is for
// review, and Describe lists it.
var (
	refused = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "PWD": true,
		"OLDPWD": true, "IFS": true, "TMPDIR": true,
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
	}

	refusedPrefixes = []string{"LD_", "DYLD_", "SSH_"}
)

// Check reports why name can't be read as an environment variable: it must be in
// capitals, digits and underscores (not starting with a digit), at most
// [MaxLength] bytes, and not one that is refused. The error shows the name quoted
// and cut short, as it is part of the program but may be anything, so it is safe
// to log.
func Check(name string) error {
	shown := quoteShort(name)

	switch {
	case name == "":
		return fmt.Errorf("the variable name is empty")
	case len(name) > MaxLength:
		return fmt.Errorf("variable name %s is too long (the limit is %d bytes)", shown, MaxLength)
	case !valid(name):
		return fmt.Errorf("variable name %s is not valid: use capital letters, digits and underscores, not starting with a digit", shown)
	case refused[name] || hasAnyPrefix(name, refusedPrefixes):
		return fmt.Errorf("variable name %s is not allowed: it is read by the system or holds credentials of another one", shown)
	}

	return nil
}

// Quote quotes s for an error: escaped, and cut short.
func Quote(s string) string {
	return quoteShort(s)
}

func quoteShort(s string) string {
	// Cut before quoting so a huge name costs nothing.
	if len(s) > 64 {
		s = s[:64]
	}

	q := strconv.QuoteToASCII(s)
	if len(q) > 40 {
		q = q[:37] + `..."`
	}

	return q
}

func valid(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]

		switch {
		case c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}

	return name != ""
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}

	return false
}
