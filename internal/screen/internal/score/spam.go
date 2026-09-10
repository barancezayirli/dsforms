// Package score provides a conservative weighted-scoring filter for detecting
// link/content spam in form submissions. It has no dependencies.
//
// The weights are fixed in this file and deliberately absolute: a stored
// spam_signals row records why one specific submission was held, and retuning a
// weight must not retroactively rewrite that history. The *threshold* they are
// compared against is not fixed — an operator sets it per instance
// (SPAM_THRESHOLD) and per form, with DefaultThreshold as the fallback.
package score

import "strings"

// DefaultThreshold is the score at or above which a submission is treated as
// spam when nothing overrides it. Deliberately conservative: a single weak
// signal must not cross it, because a false positive is expensive to recover.
//
// It is a default rather than the threshold because operators can move it, per
// instance (SPAM_THRESHOLD) and per form. The weights below are absolute and do
// not track it — a stored spam_signals row records why one specific submission
// was held, and moving the slider must not retroactively rewrite that history.
// The consequence is that the two invariants documented on markupWeight and
// keywordWeight hold at this default and can be violated at the extremes; see
// each constant, and note that the sensitivity control's hint text names the
// tradeoff for the operator making the choice.
const DefaultThreshold = 6

// markupWeight is the score for each HTML/BBCode link marker. It equals the
// default threshold so a single markup link holds on its own: a plain
// static-site form never legitimately contains <a href> or [url] markup, so it
// is a near-certain bot signature. Real captured spam relies on it (see the
// 2026-06-25 revision in docs/design/specs/2026-06-25-spam-filter-design.md).
//
// Above the default (Lenient · 9) a lone markup link no longer holds. That is
// the operator's explicit choice, and the sensitivity control says so.
const markupWeight = DefaultThreshold

// keywordWeight is the score for each spamKeywords hit. It sits below the
// default threshold by design, so a single keyword never holds a message on its
// own — it only contributes to a pile-up alongside another signal.
//
// Below the default (Strict · 4) a lone keyword does hold. Same note as above:
// chosen, and surfaced at the point of choosing.
const keywordWeight = 5

// urlInNameWeight is the score for a URL inside a name-like field. Pile-up only:
// names are not URLs, but a single such field is thin evidence on its own.
const urlInNameWeight = 4

// extraLinkWeight is the score for each raw link past the first. One link is
// ordinary — people cite their own site. A pile of them is a link farm.
const extraLinkWeight = 2

// gibberishWeight is the score for a field whose value contains a token that
// looks synthetically generated rather than real text. Kept at half the
// threshold — pile-up only, never an instant hold — because the underlying
// vowel-ratio/case-transition heuristic is biased toward English/Romance-
// language phonotactics and can flag real names from consonant-heavy
// languages (e.g. "Sobczyk"). A single flagged field must never lose a real
// submission; two independently-flagged fields reliably means a bot.
const gibberishWeight = 3

// spamKeywords are high-confidence content-spam tokens, lowercased. Kept short
// on purpose — broad word lists are how filters eat legitimate messages.
// Matching is substring-based (strings.Contains), not word-boundary — keep
// entries long/specific enough that a substring hit implies spam (this is why
// "forex" is qualified to "forex trading"/"forex signals"). Because a single
// keyword hit (keywordWeight) stays below the threshold, it only holds alongside
// another signal: e.g. "unsubscribe" is bulk-email leakage, harmless alone but
// damning combined with link markup or multiple raw URLs.
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
// enough to hold a submission on its own.
var markupLinkMarkers = []string{"[url=", "[url]", "[link]", "<a href"}

// sqlInjectionMarkers indicate a SQL-injection probe (sqlmap-style automated
// scanning), which has zero legitimate use in a plain static-site form field.
// Each match scores markupWeight, enough to hold a submission on its own —
// same tier as markup, because these strings do not occur in real prose.
// "-- -" is sqlmap's default comment-out suffix and alone covers most probes;
// the rest are common blind/error/union injection constructs.
var sqlInjectionMarkers = []string{
	"union select",
	"extractvalue(",
	"' or '1'='1",
	"' or 1=1",
	"xp_cmdshell",
	"waitfor delay",
	"-- -",
}

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
//
// It is a wrapper over Detail, which carries the scoring rules themselves. Use
// Detail wherever the reason matters — holding a submission, rendering the
// quarantine breakdown — and Score only where a bare number is genuinely enough.
func Score(data map[string]string) int {
	score, _ := Detail(data)
	return score
}

// IsSpam reports whether data scores at or above DefaultThreshold.
//
// It ignores per-instance and per-form threshold overrides, so it is not on the
// submission hot path any more: the submit handler resolves the effective
// threshold and compares Detail's score against that. IsSpam remains for callers
// that genuinely want the default.
func IsSpam(data map[string]string) bool {
	return Score(data) >= DefaultThreshold
}

// containsLink reports whether an already-lowercased value contains a URL.
func containsLink(lower string) bool {
	return strings.Contains(lower, "http") || strings.Contains(lower, "www.")
}
