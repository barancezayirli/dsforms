// Package spam — this file adds gibberish/synthetic-string detection, folded into
// Score by spam.go. Kept in its own file because it's a pure, self-contained
// predicate with no dependency on the rest of the scoring logic.
package spam

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// gibberishTokenMinLen is the shortest token length (in runes, not bytes) the
// heuristic runs on. Below this, short real words and abbreviations (LLC, IBM,
// NASA) are statistically indistinguishable from noise, so they are left alone
// entirely. Counted in runes because a 5-letter accented word like "Şükrü" is 8
// bytes in UTF-8 — a byte-based gate would let exactly the short non-English
// words this exemption exists to protect fall through into scoring.
const gibberishTokenMinLen = 6

// latinVowels are the vowel runes recognized by the vowel-ratio heuristic,
// lowercase only (callers lowercase with unicode.ToLower first). Accented Latin
// vowels must be listed: isTokenTooNonLatin deliberately classifies accented
// letters as Latin, so they are *not* exempted from this check, and the letter
// denominator in isGibberishToken counts every cased letter including them.
// Recognizing only ASCII aeiou would therefore push the ratio down for ordinary
// Polish, Turkish, Czech, Hungarian, and Vietnamese words — the exact opposite
// of correct. Coverage: Latin-1 Supplement, Latin Extended-A, and the Vietnamese
// range of Latin Extended Additional.
//
// Undiacriticked ASCII "y" is deliberately NOT a vowel here (unchanged from the
// original set): it is frequently consonantal, and treating it as a vowel would
// unflag genuinely synthetic tokens such as "Viqymx". Diacritic-marked y (ý, ỳ,
// ỹ) only ever appears as a syllable nucleus in Czech/Slovak/Vietnamese, so
// those do count.
const latinVowels = "aeiou" +
	"áàâäãåāăąạảấầẩẫậắằẳẵặæ" +
	"éèêëēĕėęěẹẻẽếềểễệ" +
	"íìîïīĭįıịỉĩ" +
	"óòôöõøōŏőơọỏốồổỗộớờởỡợœ" +
	"úùûüūŭůűųưụủứừửữựũ" +
	"ýÿȳỳỷỹỵ"

// latinVowelSet is latinVowels indexed for O(1) lookup — isVowel runs once per
// letter of every token of every field, so the linear scan is worth avoiding.
var latinVowelSet = buildVowelSet(latinVowels)

func buildVowelSet(vowels string) map[rune]bool {
	set := make(map[rune]bool, utf8.RuneCountInString(vowels))
	for _, r := range vowels {
		set[r] = true
	}
	return set
}

// isVowel reports whether r is a Latin vowel, accented or not, in either case.
func isVowel(r rune) bool {
	return latinVowelSet[unicode.ToLower(r)]
}

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
	if utf8.RuneCountInString(token) < gibberishTokenMinLen {
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
		if isVowel(r) {
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
// Three kinds of token are skipped before scoring:
//
//   - URL-shaped tokens (contain "http" or "www."): domains naturally have low
//     vowel ratios and are not synthetic text.
//   - Email-shaped tokens: structurally low-vowel for the same reason (a local
//     part glued to a domain), and — more importantly — correlated with the
//     other fields. Score adds gibberishWeight per *field*, and a person's email
//     usually contains their surname while a company's domain usually contains
//     the company name, so scoring "Sobczyk" once in `name` and again in
//     "m.sobczyk@sobczyk.pl" turns one signal into the two independent ones the
//     threshold assumes. The email carries no gibberish evidence the name or
//     company field does not already carry on its own. The cost is that a bot
//     whose *only* gibberish sits in the email address scores gibberishWeight
//     less; in every captured sample such bots fill name/message with gibberish
//     too (see TestRealSpamSamples), and losing a real customer is the worse error.
//   - Tokens with very few Latin characters: the vowel-ratio heuristic only
//     works for Latin-based text and would flag Cyrillic, Arabic, CJK, etc.
func fieldHasGibberish(value string) bool {
	for _, token := range strings.Fields(value) {
		if isURLShapedToken(token) || isEmailShapedToken(token) {
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
// (see latinVowels) and would incorrectly flag all-Cyrillic tokens as gibberish
// (0 vowels).
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

// isURLShapedToken reports whether a token looks like it could be a URL. Reuses
// containsLink so the token-level exemption and the main filter's link detection
// can never drift apart; containsLink expects an already-lowercased string, so
// the token is lowercased here — matching stays case-insensitive like every
// other check in this package (mobile keyboards autocapitalize sentence starts,
// so "Https://MySite.io" and "WWW.Example.com" are ordinary user input).
func isURLShapedToken(token string) bool {
	return containsLink(strings.ToLower(token))
}

// isEmailShapedToken reports whether a token is structurally an email address:
// exactly one "@", a non-empty local part, and a domain containing a dot. The
// dot and single-"@" requirements keep the exemption narrow enough that a bot
// cannot dodge the gibberish check by sprinkling an "@" into a random string.
func isEmailShapedToken(token string) bool {
	local, domain, found := strings.Cut(token, "@")
	if !found || local == "" || domain == "" {
		return false
	}
	if strings.Contains(domain, "@") {
		return false // more than one "@" — not an address
	}
	return strings.Contains(domain, ".")
}
