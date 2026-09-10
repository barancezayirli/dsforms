package rules

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of fn.
//
// Not parallel: it swaps a package-global in the log package. That is the reason
// this file has no t.Parallel() anywhere, and why it is a separate file — so the
// constraint is visible rather than something a future test in rules_test.go
// trips over.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	fn()
	return buf.String()
}

// TestMatchLogsOnlyForAGenuinelyUnknownType pins the sentinel at the bottom of
// matches().
//
// That log line exists because returning false for an unrecognised rule type is
// fail-open for a *block* rule — the operator believes they are protected and
// nothing is matching. But the TypeEmail and TypeDomain cases fell out of their
// loops without returning, so every ordinary non-match reached the sentinel too:
// one line per rule per submission, in production, burying the one case it was
// written to make loud.
//
// Neither the golden nor the fuzzer can catch this. Both assert on the verdict,
// and the verdict was already correct — only the logging was wrong.
func TestMatchLogsOnlyForAGenuinelyUnknownType(t *testing.T) {
	data := map[string]string{"email": "jane@real.com"}

	t.Run("a valid email rule that does not match is silent", func(t *testing.T) {
		out := captureLog(t, func() {
			Match([]Rule{{ID: "R1", Kind: KindBlock, Type: TypeEmail, Value: "bot@spam.example"}}, data, "1.2.3.4")
		})
		if out != "" {
			t.Errorf("an ordinary non-match logged:\n%s", out)
		}
	})

	t.Run("a valid domain rule that does not match is silent", func(t *testing.T) {
		out := captureLog(t, func() {
			Match([]Rule{{ID: "R2", Kind: KindBlock, Type: TypeDomain, Value: "spam.example"}}, data, "1.2.3.4")
		})
		if out != "" {
			t.Errorf("an ordinary non-match logged:\n%s", out)
		}
	})

	t.Run("valid ip and cidr rules that do not match are silent", func(t *testing.T) {
		out := captureLog(t, func() {
			Match([]Rule{
				{ID: "R3", Kind: KindBlock, Type: TypeIP, Value: "203.0.113.9"},
				{ID: "R4", Kind: KindBlock, Type: TypeCIDR, Value: "198.51.100.0/24"},
			}, data, "1.2.3.4")
		})
		if out != "" {
			t.Errorf("an ordinary non-match logged:\n%s", out)
		}
	})

	t.Run("an unknown type still logs", func(t *testing.T) {
		out := captureLog(t, func() {
			Match([]Rule{{ID: "R5", Kind: KindBlock, Type: "nonsense", Value: "x"}}, data, "1.2.3.4")
		})
		if !strings.Contains(out, "R5") || !strings.Contains(out, "nonsense") {
			t.Errorf("the fail-open sentinel did not fire for an unknown type; got:\n%s", out)
		}
	})
}
