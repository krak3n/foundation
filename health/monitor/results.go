package monitor

import (
	"strings"

	"go.krak3n.dev/foundation/internal/safetext"
)

// A Result is what one probe reported.
type Result struct {
	Name   string
	Status Status
	Error  error
}

// A ResultsMarshaler turns the results of the probes into a response body.
type ResultsMarshaler interface {
	MarshalResults([]Result) ([]byte, error)
}

// ResultsMarshalerFunc is a [ResultsMarshaler] made from a function.
type ResultsMarshalerFunc func([]Result) ([]byte, error)

// MarshalResults calls f.
func (f ResultsMarshalerFunc) MarshalResults(results []Result) ([]byte, error) {
	return f(results)
}

// KubernetesResultsMarshaler writes one line per result in the style of
// Kubernetes' own health endpoints: [+]name ok, or [-]name failed with the
// reason. Anything that is not healthy is shown as failed, so it agrees with the
// handler, which fails closed. Names and errors are escaped, so text in them
// can't start a line of its own or hide in one.
func KubernetesResultsMarshaler() ResultsMarshaler {
	return ResultsMarshalerFunc(func(results []Result) ([]byte, error) {
		var b strings.Builder

		for _, result := range results {
			b.WriteString(line(result))
			b.WriteByte('\n')
		}

		return []byte(b.String()), nil
	})
}

func line(r Result) string {
	name := safetext.Escape(r.Name, false)

	if r.Status == StatusHealthy {
		return "[+]" + name + " ok"
	}

	switch {
	case r.Error != nil:
		return "[-]" + name + " failed: " + safetext.Escape(r.Error.Error(), false)
	case r.Status == StatusUnhealthy:
		return "[-]" + name + " failed"
	default:
		return "[-]" + name + " failed: " + r.Status.String()
	}
}
