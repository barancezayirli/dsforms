// Package screen decides whether a form submission is held for review or
// accepted, and says why.
//
// It is the only package that makes that decision. That is the point of it.
//
// The decision used to be split three ways — the HTTP handler decided what a
// valid sender was, the store decided the form an operator rule was stored in,
// and the matcher decided the form a submission was compared in. Three review
// rounds each found a filter bypass in the seam between two of those owners, and
// each fix closed one seam and left the next. The implementation now lives under
// internal/screen/internal, which Go forbids any other package from importing,
// so the split cannot be recreated: no caller can normalise an address, consult
// the scorer, or match a rule on its own.
//
// Callers hand over what they have — the submitted fields, the client IP, the
// operator's rules, the effective threshold — and get back a verdict. Everything
// the decision needed is an argument and everything it decided is in the return
// value, which is what makes the whole thing characterisable in a golden test
// and fuzzable at a single entry point.
package screen

import (
	"strconv"

	"github.com/barancezayirli/dsforms/internal/screen/internal/addr"
	"github.com/barancezayirli/dsforms/internal/screen/internal/repeat"
	"github.com/barancezayirli/dsforms/internal/screen/internal/rules"
	"github.com/barancezayirli/dsforms/internal/screen/internal/score"
)

// DefaultThreshold is the score at or above which a submission is held when
// nothing overrides it. Deliberately conservative: a single weak signal must not
// cross it, because a false positive is expensive to recover.
const DefaultThreshold = score.DefaultThreshold

// MinThreshold and MaxThreshold bound what an operator may set, per instance or
// per form.
//
// They live here rather than at the two places that used to restate the range —
// the form settings handler and config's SPAM_THRESHOLD parsing — because a
// threshold is policy belonging to the decision, and two copies of a policy
// constant is how one of them drifts. Decide clamps to them regardless, so a
// value that reaches it from anywhere else still cannot produce a nonsensical
// verdict.
//
// The floor is 1, not 0: at zero every submission scores at or above the
// threshold and is held with an empty breakdown, which is both useless and
// unexplainable to the operator reading it.
const (
	MinThreshold = 1
	MaxThreshold = 20
)

// Rule kinds and types. These strings are also the CHECK constraint values on
// the filter_rules table, so changing one means a migration.
const (
	KindBlock = rules.KindBlock
	KindAllow = rules.KindAllow

	TypeEmail   = rules.TypeEmail
	TypeDomain  = rules.TypeDomain
	TypeIP      = rules.TypeIP
	TypeCIDR    = rules.TypeCIDR
	TypeKeyword = rules.TypeKeyword
)

// Rule is one operator-defined override, as stored.
type Rule = rules.Rule

// Check identifies which check produced a Signal.
type Check = score.Check

// Signal is one check that fired, and what it contributed to the score.
type Signal = score.Signal

// The complete set of checks. CheckRepeatIP and CheckBlocked are stamped by the
// screener rather than the content scorer, but are declared with the rest so one
// list is the whole truth.
const (
	CheckMarkup     = score.CheckMarkup
	CheckSQL        = score.CheckSQL
	CheckKeyword    = score.CheckKeyword
	CheckGibberish  = score.CheckGibberish
	CheckURLInName  = score.CheckURLInName
	CheckExtraLinks = score.CheckExtraLinks
	CheckRepeatIP   = score.CheckRepeatIP
	CheckBlocked    = score.CheckBlocked
)

// AllChecks is every declared Check. Anything walking the set ranges this rather
// than restating it.
var AllChecks = score.AllChecks

// ValidateRule checks a rule value against its type and returns the normalised
// form to store.
//
// Normalising here rather than at the point of storage is what makes matching a
// plain comparison, and — more importantly — it means a rule is stored in
// exactly the form a submission is reduced to. Those being two different
// definitions is what made "Bot <bot@example.com>" invisible to a block rule for
// the address it contains.
func ValidateRule(ruleType, value string) (string, error) {
	return rules.Validate(ruleType, value)
}

// RuleProblem is a stored rule that can never fire.
type RuleProblem struct {
	RuleID string
	Value  string
	Reason string
}

// CheckRules reports stored rules that can never match anything.
//
// Matching compares a submission's reduced value against a rule's stored value,
// so a rule stored in a form this binary cannot produce is inert — and inert in
// the worst direction, because a *block* rule that matches nothing fails open
// while the operator sees it listed and believes they are protected.
//
// That is reachable in practice. A rule stored before the address definition was
// unified could hold "bot@localhost", which the old validator accepted and the
// current one rejects outright, or a value folded by strings.ToLower rather than
// ASCII-only. Neither can ever come out of the matcher.
//
// The test is a round trip: a healthy rule's stored value is exactly what
// ValidateRule produces from it. Anything else — including an unrecognised Kind,
// which Match skips silently — cannot fire. This detects any *future*
// normalisation change too, which a one-off migration would not.
func CheckRules(rs []Rule) []RuleProblem {
	var out []RuleProblem
	for _, r := range rs {
		switch r.Kind {
		case KindAllow, KindBlock:
		default:
			out = append(out, RuleProblem{r.ID, r.Value,
				"unrecognised kind " + strconv.Quote(r.Kind) + "; this rule is never consulted"})
			continue
		}

		norm, err := ValidateRule(r.Type, r.Value)
		if err != nil {
			out = append(out, RuleProblem{r.ID, r.Value,
				"no longer a valid " + r.Type + " value: " + err.Error()})
			continue
		}
		if norm != r.Value {
			out = append(out, RuleProblem{r.ID, r.Value,
				"stored as " + strconv.Quote(r.Value) + " but matching produces " +
					strconv.Quote(norm) + "; this rule can never match"})
		}
	}
	return out
}

// Input is everything the decision needs.
type Input struct {
	FormID    string
	Fields    map[string]string
	IP        string
	Rules     []Rule
	Threshold int
}

// Verdict is everything the decision produced.
type Verdict struct {
	Hold    bool
	Score   int
	Signals []Signal

	// Matched reports that an operator rule decided this, and MatchedRuleID names
	// it. The caller counts the hit; screening does not, because that is a
	// database write and this is a pure decision.
	//
	// Two fields rather than testing MatchedRuleID for "": the handler used to
	// gate the hit count on a non-empty id, so a rule with an empty ID would have
	// stopped incrementing the operator's "N blocked" column silently. Unreachable
	// today, since ids are UUIDs — but a lost count is exactly the class of
	// silent wrong number this codebase keeps producing, and a bool makes it a
	// visible bug instead.
	Matched       bool
	MatchedRuleID string
}

// Screener makes the decision. It holds the only mutable state involved — the
// repeat-IP tally — so Decide is otherwise a function of its arguments.
type Screener struct {
	repeats *repeat.Tracker
}

// New returns a Screener tracking at most maxTrackedIPs distinct (form, IP)
// pairs. The tally is in-process and does not survive a restart, which is an
// accepted tradeoff rather than an oversight.
func New(maxTrackedIPs int) *Screener {
	return &Screener{repeats: repeat.NewTracker(maxTrackedIPs)}
}

// SenderOK reports whether the submission names exactly one well-formed sender.
//
// Exposed so the handler can reject a malformed submission without owning the
// definition of a sender — that ownership is the thing this package exists to
// hold. Absent is fine (not every form has an email field); ambiguous is not,
// because two fields claiming to be the sender means the submitter chooses which
// value is read.
func (s *Screener) SenderOK(fields map[string]string) bool {
	return addr.SenderValid(fields)
}

// Decide screens one submission.
//
// Order is deliberate: an allow rule wins outright and skips scoring entirely,
// including the repeat-IP check, which is the point of allowlisting a busy
// office NAT. A block rule holds whatever the content scores.
//
// Calling this records the submission against its IP, so it must be called once
// per submission and not, for example, twice to "check" and then "apply".
func (s *Screener) Decide(in Input) Verdict {
	in.Threshold = clampThreshold(in.Threshold)
	// Recorded unconditionally, before any short-circuit: the tally is also the
	// record, so skipping it behind a content check would undercount an IP
	// whenever scoring caught the submission first.
	repeated := s.repeats.Seen(in.FormID, in.IP)
	return decide(in, repeated)
}

// clampThreshold bounds a caller-supplied threshold into the range a verdict can
// be sensibly produced for.
//
// Callers already clamp — the settings form and config both do — but the
// decision cannot rely on that, because "the callers are correct" is exactly the
// assumption that made the fuzzer's earlier threshold clamp hide this: a zero
// threshold holds everything with no signals, violating the invariant the same
// fuzzer asserts two properties later.
func clampThreshold(t int) int {
	switch {
	case t < MinThreshold:
		return DefaultThreshold
	case t > MaxThreshold:
		return MaxThreshold
	default:
		return t
	}
}

// decide is the pure core, split out so it can be fuzzed and characterised
// without the tracker's state.
func decide(in Input, repeated bool) Verdict {
	in.Threshold = clampThreshold(in.Threshold)
	matched, ruleHit := rules.Match(in.Rules, in.Fields, in.IP)

	switch {
	case ruleHit && matched.Kind == rules.KindAllow:
		return Verdict{Matched: true, MatchedRuleID: matched.ID}

	case ruleHit && matched.Kind == rules.KindBlock:
		// The score is stamped at the threshold so the breakdown still adds up,
		// and the single signal carries the same weight — the content itself
		// scored nothing, and the meter should not imply otherwise.
		//
		// Field stays empty: it means "the form field whose value matched", and
		// putting the rule's *type* there rendered "field cidr · matched" to the
		// operator. The label already says a filter rule fired, and Match
		// carries the rule value.
		return Verdict{
			Hold:          true,
			Score:         in.Threshold,
			Signals:       []Signal{{Check: CheckBlocked, Match: matched.Value, Weight: in.Threshold}},
			Matched:       true,
			MatchedRuleID: matched.ID,
		}

	default:
		sc, signals := score.DetailWith(in.Fields, rules.Keywords(in.Rules))
		if repeated {
			// Repeat-IP is stateful and lives outside the scorer, so it is
			// stamped here. Weighted at the threshold so it holds on its own —
			// matching the old behaviour, where a repeat IP was an outright
			// drop — while keeping the breakdown's weights summing to the score.
			signals = append(signals, Signal{Check: CheckRepeatIP, Match: in.IP, Weight: in.Threshold})
			sc += in.Threshold
		}
		return Verdict{Hold: sc >= in.Threshold, Score: sc, Signals: signals}
	}
}
