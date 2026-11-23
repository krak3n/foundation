package foundation_test

import (
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
)

// Strict mode checks the providers of the program: the ones the root was given.
// A provider handed straight to f.Load is a narrower, deliberate use, such as a
// second source for one part of the program, and is neither strict checked nor
// read by anything else, so a key it holds that nothing asked for is not reported.
// This pins the limit that WithStrictConfig and Load document.

func TestStrictConfigChecksTheRootsProvidersButNotOnesGivenToLoad(t *testing.T) {
	typo := map[string]string{"SVC_NAME": "x", "SVC_ADRR": ":9"}

	tests := map[string]struct {
		root   map[string]string // what the program's own provider holds
		direct map[string]string // what a provider given to Load holds, if any
		want   []int             // exit codes
	}{
		"a typo in the root's provider is reported": {
			root: typo,
			want: []int{1},
		},
		"the same typo in a provider given to Load is not": {
			root:   map[string]string{},
			direct: typo,
			want:   nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			exit := installExit(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			var loaded string

			foundation.Run("svc", func(f *foundation.F) {
				var cfg strictConfig

				if tt.direct != nil {
					f.Load(&cfg, env.New(env.WithMap(tt.direct)))
				} else {
					f.Load(&cfg)
				}

				loaded = cfg.Name
			}, foundation.WithConfigProviders(env.New(env.WithMap(tt.root))), foundation.WithStrictConfig())

			if loaded != "x" && tt.direct != nil {
				t.Errorf("the provider given to Load was not read: Name = %q", loaded)
			}

			assertExitCodes(t, exit, tt.want...)

			problems := problemsLogged(recorder)

			if len(tt.want) == 0 && len(problems) != 0 {
				t.Errorf("unexpected problems: %v", keysOf(problems))
			}
		})
	}
}
