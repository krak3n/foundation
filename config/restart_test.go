package config_test

import (
	"testing"

	"go.krak3n.dev/foundation/config"
)

// A program that wants to restart when a file changes says so on the provider that
// reads it, in code, with an option that every file provider takes. Nothing in an
// environment or a document can ask for it, and without it a provider is never
// watched: the zero value is off.
func TestWithRestartOnChange(t *testing.T) {
	tests := map[string]struct {
		opts []config.FileOption
		want config.FileSettings
	}{
		"not asked for":      {nil, config.FileSettings{}},
		"asked for":          {[]config.FileOption{config.WithRestartOnChange()}, config.FileSettings{RestartOnChange: true}},
		"twice":              {[]config.FileOption{config.WithRestartOnChange(), config.WithRestartOnChange()}, config.FileSettings{RestartOnChange: true}},
		"beside the others":  {[]config.FileOption{config.WithRequired(), config.WithRestartOnChange(), config.WithoutPermissionCheck()}, config.FileSettings{Required: true, RestartOnChange: true, NoPermissionCheck: true}},
		"the other way":      {[]config.FileOption{config.WithoutPermissionCheck(), config.WithRestartOnChange()}, config.FileSettings{RestartOnChange: true, NoPermissionCheck: true}},
		"inside a slice":     {[]config.FileOption{config.FileOptions{config.WithRestartOnChange()}}, config.FileSettings{RestartOnChange: true}},
		"a nil beside it":    {[]config.FileOption{nil, config.WithRestartOnChange()}, config.FileSettings{RestartOnChange: true}},
		"others without it":  {[]config.FileOption{config.WithRequired(), config.WithoutPermissionCheck()}, config.FileSettings{Required: true, NoPermissionCheck: true}},
		"a slice, no option": {[]config.FileOption{config.FileOptions{}}, config.FileSettings{}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := config.ResolveFile(tt.opts...); got != tt.want {
				t.Fatalf("ResolveFile = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Asking for a restart on change turns nothing else off: the permission check is
// still on and a missing file is still skipped, unless those are asked for too.
func TestRestartOnChangeLeavesTheOtherDefaultsAlone(t *testing.T) {
	got := config.ResolveFile(config.WithRestartOnChange())

	if got.NoPermissionCheck || got.Required {
		t.Errorf("WithRestartOnChange changed another setting: %+v", got)
	}
}

// restarting is a provider written the way a third party would write one: it uses
// only package config, and says what it reads by implementing Restartable.
type restarting struct {
	sources []config.Source
}

func (restarting) DocumentLayer() {}

func (restarting) Unmarshal([]string, any) error { return nil }

func (r restarting) RestartSources() []config.Source { return r.sources }

// A provider that reads files says which, as plain data, and the framework does the
// watching. The data is what the provider reads and nothing more: where it is,
// what it is called in a log if the path is not for one, whether it is a directory
// and, for a directory, which names are documents.
func TestRestartableIsPlainData(t *testing.T) {
	var p config.Restartable = restarting{sources: []config.Source{
		{Path: "/etc/svc.json"},
		{Path: "/run/conf", Label: "$SVC_CONFIG_DIR", Dir: true, Extensions: []string{".yaml", ".yml"}},
	}}

	got := p.RestartSources()
	if len(got) != 2 {
		t.Fatalf("got %d sources", len(got))
	}

	if got[0].Path != "/etc/svc.json" || got[0].Label != "" || got[0].Dir || got[0].Extensions != nil {
		t.Errorf("source 0 = %+v", got[0])
	}

	if got[1].Path != "/run/conf" || got[1].Label != "$SVC_CONFIG_DIR" || !got[1].Dir || len(got[1].Extensions) != 2 {
		t.Errorf("source 1 = %+v", got[1])
	}
}
