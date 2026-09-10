package screen

import "testing"

// TestClampThreshold pins the exact mapping, not just that the result is sane.
//
// The fuzz properties cannot do this job: they assert a verdict is coherent, and
// clamping to 20 or to 6 both produce coherent verdicts. Of three mutations to
// clampThreshold, the fuzzer killed one — removing it entirely — and let the
// other two through. A boundary needs a test that names the answer.
func TestClampThreshold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want int
		why      string
	}{
		{0, DefaultThreshold, "zero holds everything with an empty breakdown; it is not a threshold anyone means"},
		{-5, DefaultThreshold, "same reasoning as zero, and clamping to the floor of 1 would hold almost everything"},
		{MinThreshold, MinThreshold, "the floor is a legitimate setting"},
		{DefaultThreshold, DefaultThreshold, "unchanged"},
		{MaxThreshold, MaxThreshold, "the ceiling is a legitimate setting"},
		{MaxThreshold + 1, MaxThreshold, "above the ceiling clamps down, not to the default"},
		{1000, MaxThreshold, "an operator meaning 'effectively off' gets the loosest real setting"},
	}
	for _, tt := range tests {
		if got := ClampThreshold(tt.in); got != tt.want {
			t.Errorf("ClampThreshold(%d) = %d, want %d — %s", tt.in, got, tt.want, tt.why)
		}
	}
}

// The clamp must be reachable through the public entry point, not only the
// internal helper — a caller that bypasses it would reintroduce the bug.
func TestDecideClampsThroughThePublicAPI(t *testing.T) {
	t.Parallel()
	for _, th := range []int{0, -5, 1000} {
		v := New(8).Decide(Input{FormID: "f", Fields: map[string]string{"message": "hello"},
			IP: "1.2.3.4", Threshold: th})
		if v.Hold && len(v.Signals) == 0 {
			t.Errorf("threshold %d held a clean submission with no reason", th)
		}
		if v.Threshold < MinThreshold || v.Threshold > MaxThreshold {
			t.Errorf("Decide(threshold=%d) reported threshold %d, outside [%d,%d]",
				th, v.Threshold, MinThreshold, MaxThreshold)
		}
	}
}
