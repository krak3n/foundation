package envname_test

import (
	"strings"
	"testing"

	"go.krak3n.dev/foundation/internal/config/envname"
)

// A program names an environment variable in code, for an alias or for the file
// it reads. The name is checked once, here: capitals, digits and underscores, not
// starting with a digit, within a limit, and not one the system reads or one that
// holds the credentials of another.
func TestCheckAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"A", "_", "SHOP_CONFIG", "X1_Y2", "PORT", "DATABASE_URL", "CONFIG_FILE", strings.Repeat("A", 128)} {
		if err := envname.Check(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestCheckRefusesWhatIsNotAName(t *testing.T) {
	tests := map[string]string{
		"empty":           "",
		"lower case":      "shop_config",
		"mixed case":      "Shop_Config",
		"starts with 1":   "1ABC",
		"space":           "A B",
		"dash":            "A-B",
		"dot":             "A.B",
		"equals":          "A=B",
		"NUL":             "A\x00B",
		"newline":         "A\nB",
		"escape":          "A\x1b[31mB",
		"unicode":         "ÅB",
		"bidi override":   "A\u202eB",
		"too long":        strings.Repeat("A", 129),
		"a very long one": strings.Repeat("A", 1<<16),
		"percent":         "A%sB",
		"dollar":          "$A",
		"braces":          "${A}",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if err := envname.Check(in); err == nil {
				t.Fatalf("%q was accepted", in)
			}
		})
	}
}

// These are read by the system, or hold credentials: a path or an alias that
// names one would put it in a struct or an error that gets logged.
func TestCheckRefusesSystemAndCredentialNames(t *testing.T) {
	for _, name := range []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "IFS", "TMPDIR",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "SSH_AUTH_SOCK",
	} {
		if err := envname.Check(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}

// The name is part of the program but may be anything, so an error shows it
// escaped and cut short: it can't put control characters in a log or fill one.
func TestCheckErrorsAreSafeToLog(t *testing.T) {
	for _, in := range []string{
		"A\nB: forged log line",
		"A\x1b[2JB",
		strings.Repeat("Z", 1<<16) + "!",
		"A\u202eB",
	} {
		err := envname.Check(in)
		if err == nil {
			t.Fatalf("%q was accepted", in)
		}

		msg := err.Error()

		if len(msg) > 200 {
			t.Errorf("the error is %d bytes long, want it cut short", len(msg))
		}

		for _, r := range msg {
			if r < 0x20 || r == 0x7f || r > 0x7e {
				t.Errorf("the error contains %q, want it escaped: %q", r, msg)

				break
			}
		}
	}
}

func FuzzCheck(f *testing.F) {
	for _, seed := range []string{"", "A", "SHOP_CONFIG", "PATH", "a b", "A\x00", strings.Repeat("A", 200), "LD_X", "ÅÅ"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		err := envname.Check(name)
		if err == nil {
			// Whatever is accepted is plain: capitals, digits, underscores.
			if name == "" || len(name) > 128 {
				t.Fatalf("%q was accepted", name)
			}

			for i := 0; i < len(name); i++ {
				c := name[i]

				if !(c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9' && i > 0) {
					t.Fatalf("%q was accepted", name)
				}
			}

			return
		}

		// A refusal is safe to log and short.
		if len(err.Error()) > 200 {
			t.Fatalf("a refusal of %d bytes for a name of %d", len(err.Error()), len(name))
		}

		for _, r := range err.Error() {
			if r < 0x20 || r > 0x7e {
				t.Fatalf("a refusal with %q in it", r)
			}
		}
	})
}
