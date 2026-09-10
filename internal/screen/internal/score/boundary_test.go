package score

import (
	"strings"
	"testing"
)

// The tests here pin boundaries a mutation sweep found unguarded: flipping the
// comparison at each one kept the whole suite green. A heuristic whose threshold
// nothing asserts can be retuned by accident, and these decide whether a real
// submission is held.

func TestTruncateMatchBoundary(t *testing.T) {
	t.Parallel()
	// The stored match text is submitter-controlled and shown to the operator,
	// so the cut is a real boundary, not decoration.
	for _, n := range []int{maxMatchRunes - 1, maxMatchRunes, maxMatchRunes + 1} {
		in := strings.Repeat("a", n)
		got := truncateMatch(in)
		want := n
		if n > maxMatchRunes {
			want = maxMatchRunes
		}
		if len([]rune(got)) != want {
			t.Errorf("truncateMatch(%d runes) kept %d, want %d", n, len([]rune(got)), want)
		}
		if n <= maxMatchRunes && got != in {
			t.Errorf("truncateMatch altered a value at or under the limit (%d runes)", n)
		}
	}

	t.Run("cuts on runes, not bytes", func(t *testing.T) {
		t.Parallel()
		// Every rune here is multi-byte; a byte-wise cut would split one.
		in := strings.Repeat("é", maxMatchRunes+10)
		got := truncateMatch(in)
		if len([]rune(got)) != maxMatchRunes {
			t.Errorf("kept %d runes, want %d", len([]rune(got)), maxMatchRunes)
		}
		if !isValidUTF8(got) {
			t.Error("truncation split a multi-byte character")
		}
	})
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestGibberishAloneNeverHolds(t *testing.T) {
	t.Parallel()
	// The heuristic is knowingly imprecise: it flags "Sobczyk", a real surname,
	// because the vowel-ratio and case-transition rules are biased toward
	// English/Romance phonotactics. spam.go documents that and compensates by
	// weighting the signal at half the threshold.
	//
	// So the invariant worth pinning is not "Sobczyk is not gibberish" — the
	// scorer says it is, deliberately — but that being wrong about one name
	// cannot cost a real submission. A mutation to the weight, or to the
	// boundary that decides how many fields flag, has to fail here.
	score, signals := Detail(map[string]string{"name": "Sobczyk"})
	if len(signals) == 0 {
		t.Skip("the heuristic no longer flags this token; the invariant below is moot")
	}
	if score >= DefaultThreshold {
		t.Errorf("a single gibberish hit scored %d, at or above the threshold of %d — "+
			"one misjudged name would now quarantine a real submission",
			score, DefaultThreshold)
	}

	// Two independently-flagged fields is the case the weighting does intend to
	// hold, so the compensation must not be so generous that nothing ever does.
	twoScore, _ := Detail(map[string]string{"name": "xkcdqwrtplm", "note": "zzzxqjvbn"})
	if twoScore <= score {
		t.Errorf("two gibberish fields scored %d, not more than one field's %d", twoScore, score)
	}
}

func TestGibberishSkipsNonLatinScripts(t *testing.T) {
	t.Parallel()
	// The Latin-ratio guard exists so Cyrillic, CJK and Arabic names are not
	// flagged as synthetic. Its boundary decides whose name gets quarantined.
	for _, s := range []string{
		"Иван Петров",
		"田中太郎",
		"محمد عبدالله",
	} {
		if fieldHasGibberish(s) {
			t.Errorf("%q was flagged as gibberish — a real name in a non-Latin script", s)
		}
	}
}
