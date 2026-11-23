package foundation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/internal/safetext"
)

// The limits on help. It is text for a person to read, so past these it is a
// mistake in the program, and the run fails instead of printing part of it.
const (
	// maxHelpText is the longest text given to [F.Help], in bytes.
	maxHelpText = 16 << 10

	// maxHelpOutput is the most help that is printed, in bytes.
	maxHelpOutput = 1 << 20
)

// helpField is a field that can be set from a flag, as help describes it.
type helpField struct {
	long  string
	short string
	typ   string
	help  string
}

// Help says what the function is for, in words a person running the program with
// --help would read. The first line is the summary shown in the list of
// functions, and the whole text is shown when help is asked for this function
// (--name --help). Calling it again replaces the text.
//
// It does nothing unless help was asked for (see [WithArgs]), so it costs
// nothing in a normal run, and its text is never read or logged then. The text is
// limited to 16 KiB, and anything that isn't printable is shown escaped.
func (f *F) Help(text string) {
	if !f.root.helping {
		return
	}

	f.mu.Lock()
	f.helpText = text
	f.mu.Unlock()
}

// describeLoad is [F.Load] when help was asked for: the function says what it
// has, and nothing is read or checked. The arguments are still parsed, into a
// throwaway copy, so that a mistake in them is found exactly as in a real run;
// the other providers are only asked to describe, so help works with a broken or
// empty configuration and never shows a value from any source.
func (f *F) describeLoad(dst any, providers []config.Provider) {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		f.failOnProblems([]error{fmt.Errorf("Load(%T): need a non-nil pointer to a struct", dst)})

		return
	}

	var (
		problems []error
		flagKeys []config.FieldKey
	)

	for _, p := range providers {
		switch p := p.(type) {
		case *flags.Provider:
			scratch := reflect.New(rv.Elem().Type()).Interface()

			found := map[string]bool{}

			if err := p.Unmarshal(f.Path(), scratch); err != nil {
				for _, e := range flatten(nil, err) {
					found[e.Error()] = true

					problems = append(problems, e)
				}
			}

			// Describing finds the same mistakes in the struct, and a few more of
			// its own (a help tag that is too long).
			keys, err := p.Describe(f.Path(), dst)

			for _, e := range flatten(nil, err) {
				if e != nil && !found[e.Error()] {
					problems = append(problems, e)
				}
			}

			flagKeys = append(flagKeys, keys...)
		case config.Describer:
			// What it says is not shown, but a mistake in its tags is still a mistake.
			if _, err := p.Describe(f.Path(), dst); err != nil {
				problems = flatten(problems, err)
			}
		}
	}

	fields := helpFields(flagKeys)

	f.mu.Lock()
	f.helpFields = append(f.helpFields, fields...)
	f.mu.Unlock()

	f.failOnProblems(problems)
}

// helpFields builds the fields help lists: those with a flag, in declaration
// order. A field with no flag isn't listed, so what appears in help is chosen by
// the program.
func helpFields(flagKeys []config.FieldKey) []helpField {
	byField := map[string]*helpField{}

	var order []string

	for _, k := range flagKeys {
		hf := byField[k.Field]
		if hf == nil {
			hf = &helpField{typ: k.Type, help: k.Help}
			byField[k.Field] = hf

			order = append(order, k.Field)
		}

		word := k.Key[strings.LastIndexByte(k.Key, ' ')+1:]

		switch {
		case strings.HasPrefix(word, "--"):
			hf.long = word[2:]
		case strings.HasPrefix(word, "-"):
			hf.short = word[1:]
		}
	}

	out := make([]helpField, 0, len(order))

	for _, name := range order {
		out = append(out, *byField[name])
	}

	return out
}

// helpNode is a function with something to show.
type helpNode struct {
	markers []string
	text    string
	fields  []helpField
}

// helpMarkers are the words that name a function on the command line, as the
// flags provider reads them.
func helpMarkers(path []string) []string {
	var out []string

	for _, k := range config.Keys(path[1:]) {
		out = append(out, strings.ToLower(strings.ReplaceAll(k, "_", "-")))
	}

	return out
}

// collectHelp lists f and everything under it that has something to show, in the
// order the functions were run: the functions with flags, and those above them
// that are needed to show where they are. The root is always listed. A text that
// is too long fails, wherever it is.
func collectHelp(f *F) ([]helpNode, error) {
	f.mu.Lock()
	text := f.helpText
	fields := slices.Clone(f.helpFields)
	kids := slices.Clone(f.helpKids)
	f.mu.Unlock()

	if len(text) > maxHelpText {
		return nil, fmt.Errorf("the help text of %s is longer than %d bytes", safetext.Escape(f.String(), false), maxHelpText)
	}

	var below []helpNode

	for _, k := range kids {
		nodes, err := collectHelp(k)
		if err != nil {
			return nil, err
		}

		below = append(below, nodes...)
	}

	if f.parent != nil && len(fields) == 0 && len(below) == 0 {
		return nil, nil
	}

	return append([]helpNode{{markers: helpMarkers(f.path), text: text, fields: fields}}, below...), nil
}

// finishHelp prints the help, once every function has finished loading: after the
// arguments have been checked, as a mistake in them wins over help, and only if
// nothing has failed. Anything that stops it is logged and fails the run.
func (root *F) finishHelp() {
	var problems []error

	for _, p := range root.flagProviders() {
		if err := p.Check(root.path); err != nil {
			problems = flatten(problems, err)
		}
	}

	scope, _, err := root.flags.Help()
	if err != nil {
		problems = flatten(problems, err)
	}

	if len(problems) > 0 {
		root.logProblems(problems)

		root.Log().LogAttrs(context.Background(), slog.LevelError, "invalid configuration", slog.Int("count", len(problems)))

		root.proc.failed.Store(true)

		return
	}

	out, err := root.renderHelp(scope)
	if err == nil {
		w := root.helpOut
		if w == nil {
			w = os.Stdout
		}

		_, err = io.WriteString(w, out)
	}

	if err != nil {
		root.Log().LogAttrs(context.Background(), slog.LevelError, "help failed", slog.String("err", safetext.Escape(err.Error(), false)))

		root.proc.failed.Store(true)
	}
}

// renderHelp lays out the help of the functions under scope, the markers of the
// function it was asked for, or of the whole tree when there are none.
func (root *F) renderHelp(scope []string) (string, error) {
	nodes, err := collectHelp(root)
	if err != nil {
		return "", err
	}

	var (
		header string
		rows   [][2]string
		chain  = slices.Clone(scope)
	)

	if len(scope) == 0 {
		rows = append(rows,
			[2]string{"  -h, --help", "Show this help"},
			[2]string{"      --dry-run", "Check configuration and start nothing"},
		)
	}

	for _, n := range nodes {
		if !slices.Equal(n.markers[:min(len(n.markers), len(scope))], scope) || len(n.markers) < len(scope) {
			continue
		}

		depth := len(n.markers) - len(scope)

		if depth == 0 {
			header = n.text
		} else {
			common := 0
			for common < len(chain) && common < len(n.markers) && chain[common] == n.markers[common] {
				common++
			}

			chain = chain[:common]

			for i := common; i < len(n.markers); i++ {
				desc := ""
				if i == len(n.markers)-1 {
					desc = firstLine(n.text)
				}

				rows = append(rows, [2]string{
					strings.Repeat(" ", 2*(i-len(scope))) + "--" + safetext.Escape(n.markers[i], false),
					safetext.Escape(desc, false),
				})

				chain = append(chain, n.markers[i])
			}
		}

		for _, fl := range n.fields {
			rows = append(rows, fieldRow(2*depth+2, fl))
		}
	}

	var b strings.Builder

	if text := strings.TrimRight(header, "\n"); strings.TrimSpace(text) != "" {
		b.WriteString(safetext.Escape(text, true))
		b.WriteString("\n\n")
	}

	width := 0

	for _, r := range rows {
		width = max(width, utf8.RuneCountInString(r[0]))
	}

	for _, r := range rows {
		b.WriteString(r[0])

		if r[1] != "" {
			b.WriteString(strings.Repeat(" ", width-utf8.RuneCountInString(r[0])+2))
			b.WriteString(r[1])
		}

		b.WriteByte('\n')
	}

	if b.Len() > maxHelpOutput {
		return "", fmt.Errorf("the help is longer than %d bytes", maxHelpOutput)
	}

	return b.String(), nil
}

// fieldRow is the line for a flag: its names and type on the left, what it is for
// and where else it can be set on the right. A bool is bare, so shows no type.
func fieldRow(indent int, fl helpField) [2]string {
	left := strings.Repeat(" ", indent)

	if fl.short != "" {
		left += "-" + safetext.Escape(fl.short, false) + ", "
	} else {
		left += "    "
	}

	left += "--" + safetext.Escape(fl.long, false)

	if fl.typ != "" && fl.typ != "bool" {
		left += " " + safetext.Escape(fl.typ, false)
	}

	desc := safetext.Escape(fl.help, false)

	return [2]string{left, desc}
}

// firstLine is the summary of a help text: its first line.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")

	return strings.TrimSpace(line)
}

// warningsOnly is a handler that drops everything below a warning, which is how
// the output of help stays help.
type warningsOnly struct {
	slog.Handler
}

func (h warningsOnly) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn && h.Handler.Enabled(ctx, level)
}

func (h warningsOnly) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return nil
	}

	return h.Handler.Handle(ctx, r)
}

func (h warningsOnly) WithAttrs(attrs []slog.Attr) slog.Handler {
	return warningsOnly{h.Handler.WithAttrs(attrs)}
}

func (h warningsOnly) WithGroup(name string) slog.Handler {
	return warningsOnly{h.Handler.WithGroup(name)}
}
