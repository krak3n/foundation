package monitor

// A Status is what a probe found. Only [StatusHealthy] passes: anything else,
// including a value that is none of these, is a failure.
type Status uint

const (
	// StatusUnknown is no verdict, the zero value. It is a failure.
	StatusUnknown Status = iota

	// StatusHealthy is the one status that passes.
	StatusHealthy

	// StatusUnhealthy is a failure.
	StatusUnhealthy
)

// String is the name of the status, "unknown" for anything that is not one.
func (s Status) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}
