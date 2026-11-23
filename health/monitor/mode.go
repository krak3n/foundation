package monitor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Mode is a bitmask that determines which health check endpoints a [Probe]
// participates in. Modes can be combined using bitwise OR to register a probe
// with multiple endpoints.
type Mode uint8

const (
	// StartupMode indicates a probe participates in startup health checks.
	StartupMode Mode = 1 << iota

	// LivenessMode indicates a probe participates in liveness health checks.
	LivenessMode

	// ReadinessMode indicates a probe participates in readiness health checks.
	ReadinessMode
)

const (
	// StartupLivenessMode is a convenience constant combining [StartupMode]
	// and [LivenessMode].
	StartupLivenessMode = StartupMode | LivenessMode

	// AllModes is a convenience constant combining all modes.
	AllModes = StartupMode | LivenessMode | ReadinessMode
)

var modeNames = []struct {
	mode Mode
	name string
}{
	{StartupMode, "startup"},
	{LivenessMode, "liveness"},
	{ReadinessMode, "readiness"},
}

// String returns a comma-separated list of mode names. For example,
// (StartupMode | LivenessMode).String() returns "startup,liveness".
func (m Mode) String() string {
	var parts []string

	for _, mn := range modeNames {
		if m&mn.mode != 0 {
			parts = append(parts, mn.name)
		}
	}

	return strings.Join(parts, ",")
}

// MarshalJSON encodes the mode as a JSON array of mode name strings.
// For example, (StartupMode | LivenessMode) encodes as ["startup","liveness"].
func (m Mode) MarshalJSON() ([]byte, error) {
	var parts []string

	for _, mn := range modeNames {
		if m&mn.mode != 0 {
			parts = append(parts, mn.name)
		}
	}

	if parts == nil {
		parts = []string{}
	}

	return json.Marshal(parts)
}

// UnmarshalJSON decodes a JSON array of mode name strings into a Mode bitmask.
// For example, ["startup","liveness"] decodes to (StartupMode | LivenessMode).
func (m *Mode) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return err
	}

	var result Mode

	for _, name := range names {
		var found bool

		for _, mn := range modeNames {
			if mn.name == name {
				result |= mn.mode
				found = true

				break
			}
		}

		if !found {
			return fmt.Errorf("unknown health mode: %q", name)
		}
	}

	*m = result

	return nil
}
