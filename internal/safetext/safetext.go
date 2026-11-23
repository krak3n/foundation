// Package safetext makes text that an attacker can influence safe to log or print.
package safetext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// cutMark is what Clip puts at the end of what it cut short.
const cutMark = "...(cut)"

// Escape makes s safe to print: valid UTF-8, and anything a terminal could act on
// or a reader could be misled by, such as control characters, bidirectional
// overrides, zero width and other format characters, line and paragraph
// separators and spaces that look like a space, shown as an escape (\x1b,
// ‮). Names and messages come from files, variables and peers, so a newline
// or an escape sequence in them would otherwise forge a line or repaint a
// terminal whatever the log handler does.
//
// Newlines are kept when keepNewlines is set, for the lines of a help text;
// elsewhere they are escaped too, so a name can't start a line of its own. Each
// character becomes at most ten bytes, and escaping the result changes nothing.
func Escape(s string, keepNewlines bool) string {
	s = strings.ToValidUTF8(s, "�")

	var b strings.Builder

	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
			b.WriteRune(r)
		case unicode.IsGraphic(r) && r != ' ' && !unicode.IsSpace(r):
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(r)
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
	}

	return b.String()
}

// Clip is Escape for text that has no size of its own, such as an error that
// quotes its input: s is cut to at most max bytes, at a character boundary, before
// anything is escaped, so a huge input costs no more than a small one, and what was
// cut is marked. The result is at most ten bytes for each of max bytes and the
// mark. A max that is not positive keeps nothing. Newlines are escaped.
func Clip(s string, max int) string {
	max = min(max, len(s))

	if max < 0 {
		max = 0
	}

	cut := len(s) > max
	if cut {
		for max > 0 && !utf8.RuneStart(s[max]) {
			max--
		}

		s = s[:max]
	}

	s = Escape(s, false)

	if cut {
		s += cutMark
	}

	return s
}
