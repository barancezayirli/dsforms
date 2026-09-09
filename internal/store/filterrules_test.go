package store

import (
	"testing"

	"github.com/youruser/dsforms/internal/filter"
)

func TestAddAndListFilterRules(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "Example.RU", "seen in logs"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeEmail, "Real@Example.RU", "restored"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}

	byValue := map[string]filter.Rule{}
	for _, r := range rules {
		byValue[r.Value] = r
	}
	// Values must come back normalised, not as typed.
	if _, ok := byValue["example.ru"]; !ok {
		t.Errorf("domain was not normalised on the way in: %v", byValue)
	}
	if got, ok := byValue["real@example.ru"]; !ok {
		t.Errorf("email was not normalised on the way in: %v", byValue)
	} else if got.Note != "restored" {
		t.Errorf("Note = %q, want %q", got.Note, "restored")
	}
}

// The unique constraint is on the normalised value, so two spellings of the
// same rule must collide rather than both being stored.
func TestAddFilterRuleRejectsDuplicates(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "example.ru", ""); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "EXAMPLE.RU", "")
	if err == nil {
		t.Fatal("adding the same rule in different case should fail, got nil")
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("got %d rules after a duplicate add, want 1", len(rules))
	}
}

// The same value may legitimately exist as both a block and an allow of
// different types; only (kind, type, value) together must be unique.
func TestAddFilterRuleAllowsSameValueDifferentKind(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeIP, "45.155.204.7", ""); err != nil {
		t.Fatalf("block add: %v", err)
	}
	if _, err := s.AddFilterRule(filter.KindAllow, filter.TypeIP, "45.155.204.7", ""); err != nil {
		t.Errorf("allow add with the same value should be permitted: %v", err)
	}
}

func TestAddFilterRuleValidates(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(filter.KindBlock, filter.TypeIP, "not-an-ip", ""); err == nil {
		t.Error("an invalid IP should be rejected before it reaches the database")
	}
	if _, err := s.AddFilterRule("sideways", filter.TypeIP, "1.2.3.4", ""); err == nil {
		t.Error("an unknown kind should be rejected")
	}
	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("invalid rules were persisted: %v", rules)
	}
}

func TestDeleteFilterRule(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	r, err := s.AddFilterRule(filter.KindBlock, filter.TypeKeyword, "crypto pump", "")
	if err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	if err := s.DeleteFilterRule(r.ID); err != nil {
		t.Fatalf("DeleteFilterRule: %v", err)
	}
	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("rule survived deletion: %v", rules)
	}
}

// Hit counts are what tell an operator which rules are earning their keep and
// which are dead weight they can remove.
func TestIncrementRuleHits(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	r, err := s.AddFilterRule(filter.KindBlock, filter.TypeDomain, "example.ru", "")
	if err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.IncrementRuleHits(r.ID); err != nil {
			t.Fatalf("IncrementRuleHits: %v", err)
		}
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Hits != 3 {
		t.Errorf("Hits = %v, want 3", rules)
	}
}
