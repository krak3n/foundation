package etcd_test

import (
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/etcd"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

// The remote layer is above the documents and below the environment: etcd
// overrides a file, and a variable overrides etcd.
func TestTheRemoteLayerIsBetweenTheFileAndTheEnvironment(t *testing.T) {
	// Not parallel: the exit recorder replaces what Run ends the process with.
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	type cfg struct {
		A string `config:"a"`
		B string `config:"b"`
		C string `config:"c"`
	}

	p, _ := provider(t, map[string]string{
		"/p/svc/a": "etcd", "/p/svc/b": "etcd",
	}, "/p")

	var got cfg

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&got)
	},
		foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"a": "file", "b": "file", "c": "file"}}`))),
		foundation.WithRemoteConfig(p),
		foundation.SetEnvironmentConfig(env.New(env.WithMap(map[string]string{"SVC_A": "env"}))),
	)

	if codes := exit.Codes(); len(codes) != 0 {
		t.Fatalf("exit codes %v", codes)
	}

	want := cfg{A: "env", B: "etcd", C: "file"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A remote source that can't be read stops the program: it never starts on the
// file alone, as that would be a different service from the one that was meant.
func TestAnUnreadableRemoteStopsStartup(t *testing.T) {

	p, s := provider(t, nil, "/p", etcd.WithTimeout(300*time.Millisecond))
	s.Close()

	type cfg struct {
		A string `config:"a"`
	}

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		var got cfg
		f.Load(&got)
	},
		foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"a": "file"}}`))),
		foundation.WithRemoteConfig(p),
	)

	if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
		t.Errorf("exit codes %v: the program must fail, not start on the file alone", codes)
	}
}
