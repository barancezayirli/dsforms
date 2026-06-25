// Package spam provides a hardcoded, conservative weighted-scoring filter for
// detecting link/content spam in form submissions. It has no dependencies and
// no configuration: the weights and threshold are fixed in this file.
package spam

import "strings"

// threshold is the score at or above which a submission is treated as spam.
// Deliberately conservative: a single weak signal must not cross it, because
// callers drop matches silently and a false positive is unrecoverable.
const threshold = 6

// markupWeight is the score for each HTML/BBCode link marker. It equals the
// threshold so a single markup link drops on its own: a plain static-site form
// never legitimately contains <a href> or [url] markup, so it is a near-certain
// bot signature. Real captured spam relies on it (see the design doc revision).
const markupWeight = threshold

// spamKeywords are high-confidence content-spam tokens, lowercased. Kept short
// on purpose — broad word lists are how filters eat legitimate messages.
// Matching is substring-based (strings.Contains), not word-boundary — keep
// entries long/specific enough that a substring hit implies spam (this is why
// "forex" is qualified to "forex trading"/"forex signals"). A single keyword
// hit (+5) stays below the threshold, so it only drops alongside another signal:
// e.g. "unsubscribe" is bulk-email leakage, harmless alone but damning with links.
var spamKeywords = []string{
	"casino",
	"viagra",
	"cialis",
	"backlinks",
	"seo service",
	"binary options",
	"forex trading",
	"forex signals",
	"crypto pump",
	"unsubscribe",
}

// markupLinkMarkers indicate HTML/BBCode link markup, which has near-zero
// legitimate use in a plain static-site form. Each match scores markupWeight,
// enough to drop a submission on its own.
var markupLinkMarkers = []string{"[url=", "[url]", "[link]", "<a href"}

// nameFieldKeys are field names treated as "name-like"; a URL inside one is
// suspicious because names are not URLs.
var nameFieldKeys = map[string]bool{
	"name":      true,
	"fname":     true,
	"lname":     true,
	"firstname": true,
	"lastname":  true,
	"full_name": true,
}

// Score returns a non-negative weighted spam score for a submission's field
// values. Higher is spammier. All matching is case-insensitive.
func Score(data map[string]string) int {
	score := 0
	links := 0
	for key, value := range data {
		lower := strings.ToLower(value)

		// Count distinct links. "http" matches http:// and https://; "www."
		// catches scheme-less URLs; subtract "//www." so a scheme+www URL
		// (e.g. https://www.example.com) counts once, not twice.
		links += strings.Count(lower, "http") + strings.Count(lower, "www.") - strings.Count(lower, "//www.")

		// HTML/BBCode link markup — each match alone is enough to drop.
		for _, marker := range markupLinkMarkers {
			score += markupWeight * strings.Count(lower, marker)
		}

		// High-confidence keyword hits.
		for _, kw := range spamKeywords {
			if strings.Contains(lower, kw) {
				score += 5
			}
		}

		// A URL inside a name-like field.
		if nameFieldKeys[strings.ToLower(key)] && containsLink(lower) {
			score += 4
		}
	}

	// Each link past the first.
	if links > 1 {
		score += 2 * (links - 1)
	}

	return score
}

// IsSpam reports whether data scores at or above the drop threshold.
func IsSpam(data map[string]string) bool {
	return Score(data) >= threshold
}

// containsLink reports whether an already-lowercased value contains a URL.
func containsLink(lower string) bool {
	return strings.Contains(lower, "http") || strings.Contains(lower, "www.")
}
