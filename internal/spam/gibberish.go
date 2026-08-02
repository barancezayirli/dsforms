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
// Skips tokens that look URL-shaped (contain "http" or "www.") since domains
// naturally have low vowel ratios and are not synthetic text. Also skips tokens
// with very few Latin characters, since the vowel-ratio heuristic only works for
// Latin-based text and would incorrectly flag Cyrillic, Arabic, CJK, etc.
func fieldHasGibberish(value string) bool {
	for _, token := range strings.Fields(value) {
		if isURLShapedToken(token) {
			continue
		}
		if isTokenTooNonLatin(token) {
			continue
		}
		if isGibberishToken(token) {
			return true
		}
	}
	return false
}

// isTokenTooNonLatin reports whether a token contains too few Latin letters
// to be reliably evaluated by the Latin-based gibberish heuristic. This prevents
// false positives when evaluating tokens in Cyrillic, CJK, Arabic, or other
// non-Latin scripts, where the vowel-ratio check only recognizes Latin vowels
// (aeiouAEIOU) and would incorrectly flag all-Cyrillic tokens as gibberish (0 vowels).
// The 33% Latin threshold is chosen to protect primarily non-Latin text while
// allowing mixed-script tokens to still be evaluated if they contain other signals.
func isTokenTooNonLatin(token string) bool {
	latinLetters := 0
	totalLetters := 0
	for _, r := range token {
		if !unicode.IsLetter(r) {
			continue
		}
		totalLetters++
		// Use unicode.Is(unicode.Latin, r) to properly count all Latin letters
		// including accented ones (é, ñ, ö, ü, etc.), not just ASCII a-z/A-Z.
		if unicode.Is(unicode.Latin, r) {
			latinLetters++
		}
	}
	// Skip if <= 33% of letters are Latin (to avoid flagging Cyrillic, CJK, Arabic, etc.)
	if totalLetters > 0 && float64(latinLetters)/float64(totalLetters) <= 0.33 {
		return true
	}
	return false
}

// isURLShapedToken reports whether a token looks like it could be a URL by
// containing the same URL markers used by the main spam filter's containsLink logic.
func isURLShapedToken(token string) bool {
	return strings.Contains(token, "http") || strings.Contains(token, "www.")
}
