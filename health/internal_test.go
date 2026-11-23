package health

import "testing"

func TestDefaultServerConfig(t *testing.T) {
	t.Parallel()

	cfg := defaultServerConfig()

	// The address is the HTTP server's, which health gives a default of its own.
	if cfg.Verbose {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}
