// Package spam — this file adds gibberish/synthetic-string detection, folded into
// Score by spam.go. Kept in its own file because it's a pure, self-contained
// predicate with no dependency on the rest of the scoring logic.
package spam

import (
	"strings"
	"unicode"
)

// gibberishTokenMinLen is the shortest token length the heuristic runs on. Below
// this, short real words and abbreviations (LLC, IBM, NASA) are statistically
// indistinguishable from noise, so they are left alone entirely.
const gibberishTokenMinLen = 6

// gibberishVowelRatioMax is the vowel-ratio floor below which a token is treated
// as synthetic. Chosen specifically below the ratio of known vowel-light real
// brand names (Dropbox 29%, Flickr/Tumblr ~17% — the latter still flag, which is
// why this signal stays low-weight and pile-up-only; see spam.go).
const gibberishVowelRatioMax = 0.20

// gibberishCaseTransitionsMin is the minimum number of letter-case flips (after
// the first character) that marks a token as synthetic. Real words/names are
// lowercase, Title Case, or ALLCAPS — they don't alternate case letter-by-letter
// the way generated IDs do (e.g. "AvAJQuWVbv...").
const gibberishCaseTransitionsMin = 3

// isGibberishToken reports whether a single whitespace-delimited token looks like
// a randomly generated string rather than real text. Must be called with the
// token's original casing preserved — lowercasing first would destroy the
// case-transition signal.
func isGibberishToken(token string) bool {
	if len(token) < gibberishTokenMinLen {
		return false
	}

	letters := 0
	vowels := 0
	transitions := 0
	prevWasUpper := false
	hasPrev := false

	for _, r := range token {
		isUpper := unicode.IsUpper(r)
		isLower := unicode.IsLower(r)
		if !isUpper && !isLower {
			continue // digits/punctuation don't count toward letter stats
		}
		letters++
		if strings.ContainsRune("aeiouAEIOU", r) {
			vowels++
		}
		if hasPrev && isUpper != prevWasUpper {
			transitions++
		}
		prevWasUpper = isUpper
		hasPrev = true
	}

	if letters == 0 {
		return false
	}
	if transitions >= gibberishCaseTransitionsMin {
		return true
	}
	return float64(vowels)/float64(letters) < gibberishVowelRatioMax
}

// fieldHasGibberish reports whether value contains at least one gibberish token.
func fieldHasGibberish(value string) bool {
	for _, token := range strings.Fields(value) {
		if isGibberishToken(token) {
			return true
		}
	}
	return false
}
