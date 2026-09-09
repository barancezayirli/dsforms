package spam

import (
	"sort"
	"strings"
)

// maxMatchRunes bounds Signal.Match. The matched text is attacker-controlled: it
// is persisted to spam_signals and rendered in the quarantine breakdown, so it is
// truncated here at capture rather than left for each render site to remember.
// Rendering still goes through html/template's default escaping — never
// template.HTML.
const maxMatchRunes = 200

// Signal is one rule hit that contributed to a submission's score.
//
// Signals exist so a held submission can explain itself. Score returns a bare
// int, which is enough to decide whether to hold a submission but not enough to
// review that decision later — and because a false positive is unrecoverable
// once dropped, the review is the point.
type Signal struct {
	// Rule is the check that fired: "markup", "sql", "keyword", "gibberish",
	// "url_in_name", "extra_links", or "rule" for an operator blocklist entry
	// (which short-circuits scoring and is stamped by the submit handler, not
	// here).
	Rule string

	// Field is the form field whose value matched, or "" for whole-submission
	// rules such as extra_links.
	Field string

	// Match is the text that fired the rule, truncated to maxMatchRunes.
	//
	// For the fixed-marker rules (markup, sql, keyword) this is the marker
	// itself, lowercased, because matching runs against a lowercased copy of the
	// value and byte offsets into that copy are not portable back to the
	// original: strings.ToLower is not length-preserving in Unicode (İ lowers to
	// a two-rune sequence), so slicing the original by an index found in the
	// lowered copy can cut mid-rune. Only gibberish and url_in_name, which read
	// the original string directly, carry the submitter's own casing.
	Match string

	// Weight is the number of points this hit contributed. It is stamped from
	// the weight constants rather than hardcoded, so retuning a constant flows
	// through to the stored rows and the UI without touching either.
	Weight int
}

// Detail returns a submission's weighted spam score alongside every rule hit
// that contributed to it. Higher is spammier. All matching is case-insensitive.
//
// The signals are ordered deterministically — fields sorted by name, rules
// within a field in evaluation order, and the whole-submission extra_links hit
// last. Determinism is not cosmetic: Go randomizes map iteration, so ranging
// over data directly would return a differently-ordered breakdown on every call
// for the same submission, and the quarantine panel renders signals in exactly
// the order given here.
//
// The weights always sum to the score. The breakdown panel shows both, and a
// breakdown that does not add up to the number beside it is worse than none.
func Detail(data map[string]string) (int, []Signal) {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	score := 0
	links := 0
	var signals []Signal

	for _, key := range keys {
		value := data[key]
		lower := strings.ToLower(value)

		// Count distinct links. "http" matches http:// and https://; "www."
		// catches scheme-less URLs; subtract "//www." so a scheme+www URL
		// (e.g. https://www.example.com) counts once, not twice.
		links += strings.Count(lower, "http") + strings.Count(lower, "www.") - strings.Count(lower, "//www.")

		// HTML/BBCode link markup — each match alone is enough to hold.
		// Repeats of one marker collapse into a single signal carrying their
		// combined weight: three anchors are one reason, not three, and the
		// breakdown renders one box per signal.
		for _, marker := range markupLinkMarkers {
			if n := strings.Count(lower, marker); n > 0 {
				weight := markupWeight * n
				score += weight
				signals = append(signals, Signal{Rule: "markup", Field: key, Match: marker, Weight: weight})
			}
		}

		// SQL injection probe — same tier as markup; these strings do not occur
		// in real prose.
		for _, marker := range sqlInjectionMarkers {
			if n := strings.Count(lower, marker); n > 0 {
				weight := markupWeight * n
				score += weight
				signals = append(signals, Signal{Rule: "sql", Field: key, Match: marker, Weight: weight})
			}
		}

		// High-confidence keyword hits, once per keyword however often it recurs.
		for _, kw := range spamKeywords {
			if strings.Contains(lower, kw) {
				score += keywordWeight
				signals = append(signals, Signal{Rule: "keyword", Field: key, Match: kw, Weight: keywordWeight})
			}
		}

		// A URL inside a name-like field.
		if nameFieldKeys[strings.ToLower(key)] && containsLink(lower) {
			score += urlInNameWeight
			signals = append(signals, Signal{
				Rule: "url_in_name", Field: key, Match: truncateMatch(value), Weight: urlInNameWeight,
			})
		}

		// Gibberish/synthetic-looking field value. Deliberately checked against
		// the original-case value, not lower — lowercasing would destroy the
		// case-transition signal the heuristic relies on.
		if token, ok := gibberishToken(value); ok {
			score += gibberishWeight
			signals = append(signals, Signal{
				Rule: "gibberish", Field: key, Match: truncateMatch(token), Weight: gibberishWeight,
			})
		}
	}

	// Each link past the first. One signal for the whole submission, not one per
	// link: the pile-up is the evidence, and no single field owns it.
	if links > 1 {
		weight := extraLinkWeight * (links - 1)
		score += weight
		signals = append(signals, Signal{Rule: "extra_links", Weight: weight})
	}

	return score, signals
}

// truncateMatch bounds submitter-supplied text at maxMatchRunes, counting runes
// rather than bytes so a multi-byte value is never cut mid-character.
func truncateMatch(s string) string {
	runes := []rune(s)
	if len(runes) <= maxMatchRunes {
		return s
	}
	return string(runes[:maxMatchRunes])
}
