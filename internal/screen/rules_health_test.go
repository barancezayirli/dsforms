package screen

import "testing"

// TestCheckRulesFindsRulesThatCanNeverFire is the regression test for a rule
// that fails open in silence.
//
// A block rule is stored normalised, and matching compares against that stored
// form. So a rule stored under an older normalisation can be permanently
// unmatchable: "bot@localhost" was accepted by the pre-seal validator, which did
// not require a dot after the @, and today's Canonical does — meaning matching
// can never produce that value. The rule sits on the rules screen looking active
// while protecting nothing, and nothing tells the operator.
//
// That is the same shape as the three bypasses: a wrong answer that looks like a
// right one. The difference is that this one can be detected exactly, by asking
// whether the stored value still round-trips through the validator.
func TestCheckRulesFindsRulesThatCanNeverFire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rule    Rule
		wantBad bool
		reason  string
	}{
		{"a normal email rule", Rule{ID: "1", Kind: KindBlock, Type: TypeEmail, Value: "bot@example.com"}, false, ""},
		{"a normal domain rule", Rule{ID: "2", Kind: KindBlock, Type: TypeDomain, Value: "example.com"}, false, ""},
		{"a normal cidr rule", Rule{ID: "3", Kind: KindBlock, Type: TypeCIDR, Value: "203.0.113.0/24"}, false, ""},
		{"a keyword rule", Rule{ID: "4", Kind: KindBlock, Type: TypeKeyword, Value: "casino"}, false, ""},

		// Stored by an older binary under a normalisation this one cannot produce.
		{"legacy: no dot in the domain", Rule{ID: "5", Kind: KindBlock, Type: TypeEmail, Value: "bot@localhost"}, true,
			"the old validator allowed it; Canonical requires a dot"},
		{"legacy: not normalised at all", Rule{ID: "6", Kind: KindBlock, Type: TypeEmail, Value: "Bot <bot@example.com>"}, true,
			"stored in display-name form, which matching reduces away"},
		{"legacy: uppercase never folded", Rule{ID: "7", Kind: KindBlock, Type: TypeDomain, Value: "Example.COM"}, true,
			"matching lowercases, so this can never be produced"},

		// Corrupt beyond normalisation.
		{"unknown kind", Rule{ID: "8", Kind: "BLOCK", Type: TypeEmail, Value: "bot@example.com"}, true,
			"Match iterates only allow and block, so this is skipped entirely"},
		{"unknown type", Rule{ID: "9", Kind: KindBlock, Type: "nonsense", Value: "x"}, true, "no matcher for this type"},
		{"empty value", Rule{ID: "10", Kind: KindBlock, Type: TypeEmail, Value: ""}, true, "matches nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			problems := CheckRules([]Rule{tt.rule})
			if tt.wantBad && len(problems) == 0 {
				t.Errorf("CheckRules found no problem with %+v — %s", tt.rule, tt.reason)
			}
			if !tt.wantBad && len(problems) != 0 {
				t.Errorf("CheckRules flagged a healthy rule: %v", problems)
			}
			if tt.wantBad && len(problems) == 1 && problems[0].RuleID != tt.rule.ID {
				t.Errorf("problem names rule %q, want %q", problems[0].RuleID, tt.rule.ID)
			}
		})
	}
}

// A flagged rule must genuinely be unmatchable — otherwise the warning is noise,
// and an operator who deletes a working rule because of it is worse off.
func TestCheckRulesOnlyFlagsRulesThatReallyCannotMatch(t *testing.T) {
	t.Parallel()

	// The value this rule was meant to block, submitted exactly.
	dead := Rule{ID: "d", Kind: KindBlock, Type: TypeEmail, Value: "bot@localhost"}
	if len(CheckRules([]Rule{dead})) == 0 {
		t.Fatal("expected the legacy rule to be flagged")
	}
	sc := New(8)
	v := sc.Decide(Input{FormID: "f", Fields: map[string]string{"email": "bot@localhost"},
		IP: "1.2.3.4", Rules: []Rule{dead}, Threshold: DefaultThreshold})
	if v.Matched {
		t.Error("the rule flagged as dead actually matched; the warning would be wrong")
	}

	live := Rule{ID: "l", Kind: KindBlock, Type: TypeEmail, Value: "bot@example.com"}
	if p := CheckRules([]Rule{live}); len(p) != 0 {
		t.Fatalf("healthy rule flagged: %v", p)
	}
	v = sc.Decide(Input{FormID: "f2", Fields: map[string]string{"email": "bot@example.com"},
		IP: "1.2.3.4", Rules: []Rule{live}, Threshold: DefaultThreshold})
	if !v.Matched {
		t.Error("the rule passed as healthy did not match")
	}
}
