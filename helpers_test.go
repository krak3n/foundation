package foundation_test

import (
	"slices"
	"testing"

	"go.krak3n.dev/foundation/foundationtest"
)

// installExit records the exit status of Run for the length of the test, so a Fail
// or a panic doesn't end the test binary.
func installExit(t *testing.T) *foundationtest.ExitRecorder {
	t.Helper()

	r := foundationtest.NewExitRecorder()
	r.Install(t)

	return r
}

func assertExitCodes(t *testing.T, r *foundationtest.ExitRecorder, want ...int) {
	t.Helper()

	if got := r.Codes(); !slices.Equal(got, want) {
		t.Fatalf("unexpected exit codes\n got: %v\nwant: %v", got, want)
	}
}
