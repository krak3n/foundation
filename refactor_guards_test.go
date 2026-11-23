package foundation_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/foundationtest"
)

// Guards for code that is about to be tidied: what it does now is what it must keep
// doing.

// A problem a provider has already reported for a template field is the one that is
// said. The field is not also expanded, which would add a second complaint about a
// variable that has nothing to do with what went wrong.
func TestAProviderProblemForATemplateFieldIsNotFollowedByAnExpansionProblem(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	boom := config.ProviderFunc(func(_ []string, dst any) error {
		dst.(*struct {
			Name string `config:"expand"`
		}).Name = "${NOT_SET_ANYWHERE}"

		return &config.FieldError{Provider: "test", Field: "Name", Err: errors.New("boom")}
	})

	foundation.Run("svc", func(f *foundation.F) {
		var cfg struct {
			Name string `config:"expand"`
		}

		f.Load(&cfg, boom)
	}, foundation.WithLookupEnv(func(string) (string, bool) { return "", false }))

	assertExitCodes(t, exit, 1)

	problems := problemsLogged(recorder)

	if len(problems) != 1 {
		t.Fatalf("%d problems were reported, want only the provider's: %v", len(problems), problems)
	}

	if got := problems[0].attrs["err"]; got != "boom" {
		t.Errorf("the problem is %q, want the provider's", got)
	}

	if key := problems[0].attrs["key"]; key != "" {
		t.Errorf("a problem about the variable %q was reported as well", key)
	}
}

// The limits of a restart policy are what keep a mistake in code from turning a
// source of changes into a restart loop or a program that never restarts: past
// them startup stops.
func TestARestartPolicyOutsideItsLimitsStopsStartup(t *testing.T) {
	day := 24 * time.Hour

	tests := map[string]struct {
		policy foundation.RestartPolicy
		says   string
	}{
		"a negative debounce":                 {foundation.RestartPolicy{Debounce: -1}, "Debounce must be between"},
		"a negative max debounce":             {foundation.RestartPolicy{MaxDebounce: -1}, "MaxDebounce must be between"},
		"a negative interval":                 {foundation.RestartPolicy{MinInterval: -1}, "MinInterval must be between"},
		"a negative window":                   {foundation.RestartPolicy{Window: -1}, "Window must be between"},
		"a debounce over a day":               {foundation.RestartPolicy{Debounce: day + 1}, "Debounce must be between"},
		"a max debounce over a day":           {foundation.RestartPolicy{MaxDebounce: day + 1}, "MaxDebounce must be between"},
		"an interval over a day":              {foundation.RestartPolicy{MinInterval: day + 1}, "MinInterval must be between"},
		"a window over a day":                 {foundation.RestartPolicy{Window: day + 1}, "Window must be between"},
		"a negative limit":                    {foundation.RestartPolicy{Limit: -1}, "Limit must be between"},
		"a limit over a thousand":             {foundation.RestartPolicy{Limit: 1001}, "Limit must be between"},
		"a debounce past its maximum":         {foundation.RestartPolicy{Debounce: 5 * time.Second, MaxDebounce: time.Second}, "Debounce is longer than MaxDebounce"},
		"a debounce past the default maximum": {foundation.RestartPolicy{Debounce: time.Minute}, "Debounce is longer than MaxDebounce"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			res := runStartup(t, foundation.WithRestartPolicy(tt.policy))

			refusesStartup(t, res, "invalid configuration options", "WithRestartPolicy", tt.says)
		})
	}
}

func TestARestartPolicyAtItsLimitsIsAccepted(t *testing.T) {
	day := 24 * time.Hour

	for name, policy := range map[string]foundation.RestartPolicy{
		"the defaults":                    {},
		"every limit at its largest":      {Debounce: day, MaxDebounce: day, MinInterval: day, Window: day, Limit: 1000},
		"a debounce equal to its maximum": {Debounce: time.Second, MaxDebounce: time.Second},
		"validation skipped":              {SkipValidation: true},
	} {
		t.Run(name, func(t *testing.T) {
			res := runStartup(t, foundation.WithRestartPolicy(policy))

			if !res.started || len(res.codes) != 0 {
				t.Errorf("started = %v, exit codes = %v, failure %q", res.started, res.codes, res.failure)
			}
		})
	}
}

// Every mistake is said, and the same way every time: the report is what a person
// compares between two runs.
func TestARestartPolicyWithSeveralMistakesSaysAllOfThemTheSameWayEachTime(t *testing.T) {
	policy := foundation.RestartPolicy{
		Debounce: -1, MaxDebounce: -1, MinInterval: -1, Window: -1, Limit: 5000,
	}

	var first string

	for i := range 30 {
		res := runStartup(t, foundation.WithRestartPolicy(policy))

		if i == 0 {
			first = res.failure

			for _, want := range []string{"Debounce", "MaxDebounce", "MinInterval", "Window", "Limit"} {
				if !strings.Contains(first, want+" must be between") {
					t.Errorf("the failure %q does not say %s is out of range", first, want)
				}
			}

			continue
		}

		if res.failure != first {
			t.Fatalf("run %d said\n%q\nwhere the first said\n%q", i+1, res.failure, first)
		}
	}
}
