package config_test

import (
	"slices"
	"testing"

	"go.krak3n.dev/foundation/config"
)

func TestKeys(t *testing.T) {
	tests := []struct {
		name string
		path []string
		want []string
	}{
		{"empty path", []string{}, []string{}},
		{"plain segments", []string{"svc", "http"}, []string{"svc", "http"}},
		{"dot", []string{"svc", "http.server"}, []string{"svc", "http", "server"}},
		{"slash", []string{"svc", "http/server"}, []string{"svc", "http", "server"}},
		{"hyphen", []string{"svc", "http-server"}, []string{"svc", "http", "server"}},
		{"space", []string{"svc", "http server"}, []string{"svc", "http", "server"}},
		{"several separators in one segment", []string{"a.b/c-d"}, []string{"a", "b", "c", "d"}},
		{"every segment can split", []string{"a.b", "c.d"}, []string{"a", "b", "c", "d"}},
		{"underscore is not a separator", []string{"svc", "http_server"}, []string{"svc", "http_server"}},
		{"digits are kept", []string{"v2.api"}, []string{"v2", "api"}},
		{"case is preserved", []string{"Svc", "HTTP.Server"}, []string{"Svc", "HTTP", "Server"}},
		{"repeated separators", []string{"a..b"}, []string{"a", "b"}},
		{"leading and trailing separators", []string{".a."}, []string{"a"}},
		{"only separators contributes nothing", []string{"svc", "...", "http"}, []string{"svc", "http"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.Keys(tt.path); !slices.Equal(got, tt.want) {
				t.Fatalf("Keys(%q)\n got: %q\nwant: %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestKeysDoesNotModifyItsArgument(t *testing.T) {
	path := []string{"svc", "http.server"}

	config.Keys(path)

	if !slices.Equal(path, []string{"svc", "http.server"}) {
		t.Fatalf("expected the argument to be left alone, got %q", path)
	}
}
