package foundationtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
)

// LogEntry captures a slog record along with its handler-level attrs (from
// [slog.Handler.WithAttrs] calls), allowing tests to verify both record-level
// and handler-level attributes.
type LogEntry struct {
	Record slog.Record
	Attrs  []slog.Attr // handler-level attrs from With()
}

// LogRecorder captures slog records for test assertions. It implements
// [slog.Handler] and stores each log record along with the handler-level
// attributes that were active at the time of the call.
//
// Child handlers created via [slog.Handler.WithAttrs] share the same underlying
// storage, so all records are captured in a single place regardless of which
// child handler produced them.
type LogRecorder struct {
	mu      *sync.Mutex
	entries *[]LogEntry
	attrs   []slog.Attr
}

// NewLogRecorder creates a new [LogRecorder] ready to capture log records.
func NewLogRecorder() *LogRecorder {
	return &LogRecorder{
		mu:      &sync.Mutex{},
		entries: &[]LogEntry{},
	}
}

// Install sets the [LogRecorder] as the default slog logger and registers a
// [testing.TB.Cleanup] function to restore the original default logger when the
// test completes.
func (r *LogRecorder) Install(t testing.TB) {
	t.Helper()

	original := slog.Default()
	slog.SetDefault(slog.New(r))

	t.Cleanup(func() {
		slog.SetDefault(original)
	})
}

// Entries returns a copy of all captured log entries.
func (r *LogRecorder) Entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(*r.entries)
}

// Enabled implements [slog.Handler]. All levels are enabled.
func (r *LogRecorder) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle implements [slog.Handler]. It captures the record along with the
// handler-level attrs.
func (r *LogRecorder) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	*r.entries = append(*r.entries, LogEntry{
		Record: record,
		Attrs:  slices.Clone(r.attrs),
	})

	return nil
}

// WithAttrs implements [slog.Handler]. It returns a new [LogRecorder] that
// shares the same underlying storage but has the additional attrs.
func (r *LogRecorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogRecorder{
		mu:      r.mu,
		entries: r.entries,
		attrs:   append(slices.Clone(r.attrs), attrs...),
	}
}

// WithGroup implements [slog.Handler]. Groups are not tracked.
func (r *LogRecorder) WithGroup(_ string) slog.Handler {
	return r
}
