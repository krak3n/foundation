package yaml

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The README states the limits, and the numbers it gives are the ones the parser
// enforces, so it can't promise more than the code does.
func TestTheReadmeStatesTheLimitsTheParserEnforces(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}

	readme := string(b)

	for _, c := range []struct {
		name string
		re   string
		want int
	}{
		{"document size in MiB", `(\d+) MiB`, maxDocument >> 20},
		{"depth in levels", `(\d+) levels`, maxDepth},
		{"number of nodes", `([\d,]+) nodes`, maxNodes},
	} {
		m := regexp.MustCompile(c.re).FindStringSubmatch(readme)
		if m == nil {
			t.Errorf("the README does not state the %s", c.name)

			continue
		}

		got, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		if err != nil || got != c.want {
			t.Errorf("the README says %s for the %s, the parser enforces %d", m[1], c.name, c.want)
		}
	}
}
