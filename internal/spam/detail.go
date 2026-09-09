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

// Rule identifies the check that produced a Signal.
//
// A defined type rather than a bare string because the documented value set had
// already gone stale on arrival: it omitted repeat_ip, which the submit handler
// stamps rather than the scorer. The values are split between this package,
// which emits most of them, and internal/handler, which stamps the rest and owns
// the display mapping — so one authoritative list is the only thing that keeps
// them in step. That list is AllRules; this comment deliberately does not
// restate it or count it. It scans to and from SQL exactly like a string.
type Rule string

// The complete set. RuleRepeatIP and RuleBlocked are stamped by the submit
// handler rather than by the scorer, but they are declared here so this list is
// the whole truth.
//
// Anything that needs to walk every rule ranges AllRules below rather than
// restating the values — a second copy of a value set, maintained by memory, is
// the exact defect this type replaced.
const (
	RuleMarkup     Rule = "markup"
	RuleSQL        Rule = "sql"
	RuleKeyword    Rule = "keyword"
	RuleGibberish  Rule = "gibberish"
	RuleURLInName  Rule = "url_in_name"
	RuleExtraLinks Rule = "extra_links"
	RuleRepeatIP   Rule = "repeat_ip"
	RuleBlocked    Rule = "rule"
)

// AllRules is every declared Rule.
//
// Go does not exhaustiveness-check anything here — not a map literal keyed by
// Rule, not a switch — so nothing about the type alone makes a forgotten display
// entry a compile error. This slice is what makes it checkable: the display
// coverage test ranges it, and TestAllRulesIsComplete derives the constant list
// from this package's source so the slice cannot fall behind the constants.
var AllRules = []Rule{
	RuleMarkup,
	RuleSQL,
	RuleKeyword,
	RuleGibberish,
	RuleURLInName,
	RuleExtraLinks,
	RuleRepeatIP,
	RuleBlocked,
}

// Signal is one rule hit that contributed to a submission's score.
//
// Signals exist so a held submission can explain itself. Score returns a bare
// int, which is enough to decide whether to hold a submission but not enough to
// review that decision later — and because a false positive is unrecoverable
// once dropped, the review is the point.
type Signal struct {
	// Rule is the check that fired.
	Rule Rule

	// Field is the form field whose value matched, or "" for whole-submission
	// rules such as extra_links.
	Field string

	// Match is the text that fired the rule, truncated to maxMatchRunes.
	//
	// For the fixed-marker rules (markup, sql, keyword) this is the marker
	// itself, lowercased, because matching runs against a lowercased copy of the
	// value and byte offsets into that copy are not portable back to the
	// original: strings.ToLower is not *byte*-length-preserving (İ is two bytes
	// and lowers to a one-byte "i"), so an index found in the lowered copy can
	// land mid-rune in the original. Only gibberish and url_in_name, which read
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
	return DetailWith(data, nil)
}

// DetailWith is Detail plus operator-supplied keywords from internal/filter.
//
// Custom keywords score at the same keywordWeight as the built-in list rather
// than short-circuiting to a hold. That is deliberate and is the whole reason
// they run through the scorer: it preserves the rule that a single keyword hit
// never holds a submission on its own, so an operator who adds an overly broad
// word does not silently start losing real mail. An operator who genuinely
// wants an instant hold has block rules for that.
func DetailWith(data map[string]string, extraKeywords []string) (int, []Signal) {
	keywords := spamKeywords
	if len(extraKeywords) > 0 {
		// Deduplicate against the built-in list and against itself, or a
		// keyword supplied twice would score twice for one hit.
		seen := make(map[string]bool, len(spamKeywords)+len(extraKeywords))
		for _, kw := range spamKeywords {
			seen[kw] = true
		}
		keywords = append([]string(nil), spamKeywords...)
		for _, kw := range extraKeywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" || seen[kw] {
				continue
			}
			seen[kw] = true
			keywords = append(keywords, kw)
		}
	}

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
				signals = append(signals, Signal{Rule: RuleMarkup, Field: key, Match: marker, Weight: weight})
			}
		}

		// SQL injection probe — same tier as markup; these strings do not occur
		// in real prose.
		for _, marker := range sqlInjectionMarkers {
			if n := strings.Count(lower, marker); n > 0 {
				weight := markupWeight * n
				score += weight
				signals = append(signals, Signal{Rule: RuleSQL, Field: key, Match: marker, Weight: weight})
			}
		}

		// High-confidence keyword hits, once per keyword however often it recurs.
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				score += keywordWeight
				signals = append(signals, Signal{Rule: RuleKeyword, Field: key, Match: kw, Weight: keywordWeight})
			}
		}

		// A URL inside a name-like field.
		if nameFieldKeys[strings.ToLower(key)] && containsLink(lower) {
			score += urlInNameWeight
			signals = append(signals, Signal{
				Rule: RuleURLInName, Field: key, Match: truncateMatch(value), Weight: urlInNameWeight,
			})
		}

		// Gibberish/synthetic-looking field value. Deliberately checked against
		// the original-case value, not lower — lowercasing would destroy the
		// case-transition signal the heuristic relies on.
		if token, ok := gibberishToken(value); ok {
			score += gibberishWeight
			signals = append(signals, Signal{
				Rule: RuleGibberish, Field: key, Match: truncateMatch(token), Weight: gibberishWeight,
			})
		}
	}

	// Each link past the first. One signal for the whole submission, not one per
	// link: the pile-up is the evidence, and no single field owns it.
	if links > 1 {
		weight := extraLinkWeight * (links - 1)
		score += weight
		signals = append(signals, Signal{Rule: RuleExtraLinks, Weight: weight})
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
