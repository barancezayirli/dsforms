package screen

import (
	"strings"
	"testing"

	"github.com/barancezayirli/dsforms/internal/screen/internal/addr"
)

// senderCanonical is the canonical form of a submission's sender, for asserting
// the allow-match property. The test lives inside internal/screen, so it can
// reach the sealed packages that callers cannot — which is exactly the right
// asymmetry: the invariant is checked against the real definition, not a copy.
func senderCanonical(fields map[string]string) (string, bool) {
	raw, state := addr.SenderAddress(fields)
	if state != addr.SenderOne {
		return "", false
	}
	return addr.Canonical(raw)
}

// FuzzDecide attacks the decision at its one entry point.
//
// This is the check that three rounds of hand-written tests could not be. Each
// round fixed the bypass that had been reported and wrote a regression test
// enumerating that layer's variants — the field name, then the field value —
// and the next round found the layer beneath. Enumeration cannot close a space
// nobody has finished imagining.
//
// So this asserts properties rather than outcomes. A property holds for every
// input or it does not hold; there is no list to fall behind.
func FuzzDecide(f *testing.F) {
	// Seeds: the three forms that were live bypasses, plus the shapes around
	// them. A fuzzer explores outward from its corpus, so the historical bugs
	// are the right neighbourhood to start in.
	seeds := []struct {
		sender, other, ruleVal string
		kind, typ              string
		threshold              int
	}{
		{"vip@customer.com", "hello", "vip@customer.com", KindAllow, TypeEmail, 6},
		{"mallory@spam.example", "vip@customer.com", "vip@customer.com", KindAllow, TypeEmail, 6}, // round 1
		{"MİKE@works.com", "casino", "mike@works.com", KindAllow, TypeEmail, 6},                   // round 3, U+0130
		{"MIKE@works.com", "casino", "mike@works.com", KindAllow, TypeEmail, 6},                   // round 3, U+212A
		{"Bot <bot@example.com>", "hello", "bot@example.com", KindBlock, TypeEmail, 6},            // round 3
		{"a@x.com", "<a href=x>casino</a>", "", "", "", 6},
		{"", "", "", "", "", 6},
		{"a@mail.example.ru", "hi", "example.ru", KindBlock, TypeDomain, 4},
	}
	for _, s := range seeds {
		f.Add(s.sender, s.other, s.ruleVal, s.kind, s.typ, s.threshold)
	}

	f.Fuzz(func(t *testing.T, sender, other, ruleVal, kind, typ string, threshold int) {
		// Keep the generated rule inside the shape the store can actually
		// persist; a rule that could never be stored proves nothing.
		switch kind {
		case KindAllow, KindBlock:
		default:
			kind = KindBlock
		}
		switch typ {
		case TypeEmail, TypeDomain, TypeIP, TypeCIDR, TypeKeyword:
		default:
			typ = TypeEmail
		}
		if threshold < 1 || threshold > 100 {
			threshold = DefaultThreshold
		}

		var rs []Rule
		// Only a rule the operator could really have created. Validate is the
		// same normalisation the store applies, so this keeps the two sides of
		// every comparison honest.
		if norm, err := ValidateRule(typ, ruleVal); err == nil {
			rs = append(rs, Rule{ID: "R1", Kind: kind, Type: typ, Value: norm})
		}

		in := Input{
			FormID:    "f1",
			Fields:    map[string]string{"email": sender, "message": other},
			IP:        "203.0.113.5",
			Rules:     rs,
			Threshold: threshold,
		}

		// Property 1: never panics. Everything below depends on this holding.
		v := decide(in, false)

		// Property 2 — allow-match soundness. This is the one all three
		// bypasses violated, and nothing before this expressed it.
		//
		// If an allow rule decided the verdict, the submission's canonical
		// sender must be byte-equal to that rule's stored value (or, for a
		// domain rule, its host must be that domain or a subdomain of it).
		// "Some field somewhere contained the address" is precisely the
		// reasoning that made junk fields, case variants and Unicode homoglyphs
		// into skeleton keys.
		if v.MatchedRuleID != "" && len(rs) > 0 && rs[0].Kind == KindAllow {
			r := rs[0]
			switch r.Type {
			case TypeEmail:
				got, ok := senderCanonical(in.Fields)
				if !ok || got != r.Value {
					t.Fatalf("allow rule %q fired for a submission whose canonical sender is %q (ok=%v).\n"+
						"An allow rule skips the block list and all scoring, so it must match the sender "+
						"exactly — not merely resemble it.\nfields=%#v", r.Value, got, ok, in.Fields)
				}
			case TypeDomain:
				got, ok := senderCanonical(in.Fields)
				at := strings.LastIndex(got, "@")
				if !ok || at < 0 {
					t.Fatalf("allow domain rule %q fired with no canonical sender (got %q)", r.Value, got)
				}
				host := got[at+1:]
				if host != r.Value && !strings.HasSuffix(host, "."+r.Value) {
					t.Fatalf("allow domain rule %q fired for sender host %q", r.Value, host)
				}
			}
		}

		// Property 3 — the breakdown must account for the score. An operator
		// judging a held submission reads the signals; if they do not sum to the
		// number beside them, the screen is lying about its own reasoning.
		if v.Hold {
			sum := 0
			for _, s := range v.Signals {
				sum += s.Weight
			}
			if sum != v.Score {
				t.Fatalf("signal weights sum to %d but score is %d: %#v", sum, v.Score, v.Signals)
			}
		}

		// Property 4 — a hold is never negative or unexplained.
		if v.Score < 0 {
			t.Fatalf("negative score %d", v.Score)
		}
		if v.Hold && len(v.Signals) == 0 {
			t.Fatalf("held with no signals — the operator would see no reason")
		}

		// Property 5 — a block rule cannot be suppressed by adding fields.
		// Appending junk was the original bypass; it must not turn Hold into
		// Accept for a submission a block rule matches.
		if len(rs) > 0 && rs[0].Kind == KindBlock {
			base := decide(in, false)
			if base.Hold {
				noisy := Input{
					FormID: in.FormID, IP: in.IP, Rules: in.Rules, Threshold: in.Threshold,
					Fields: map[string]string{
						"email": sender, "message": other,
						"zz": "vip@customer.com", "Email": "someone@elsewhere.test",
					},
				}
				if got := decide(noisy, false); !got.Hold {
					t.Fatalf("a block rule held this submission, but adding fields made it accepted.\n"+
						"rule=%+v fields=%#v", rs[0], noisy.Fields)
				}
			}
		}
	})
}
