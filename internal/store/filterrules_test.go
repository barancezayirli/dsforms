package store

import (
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/screen"
)

func TestAddAndListFilterRules(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "Example.RU", "seen in logs"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeEmail, "Real@Example.RU", "restored"); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	rules, err := s.ListFilterRules()
	if err != nil {
		t.Fatalf("ListFilterRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}

	byValue := map[string]screen.Rule{}
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

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "example.ru", ""); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "EXAMPLE.RU", "")
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

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeIP, "45.155.204.7", ""); err != nil {
		t.Fatalf("block add: %v", err)
	}
	if _, err := s.AddFilterRule(screen.KindAllow, screen.TypeIP, "45.155.204.7", ""); err != nil {
		t.Errorf("allow add with the same value should be permitted: %v", err)
	}
}

func TestAddFilterRuleValidates(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeIP, "not-an-ip", ""); err == nil {
		t.Error("an invalid IP should be rejected before it reaches the database")
	}
	if _, err := s.AddFilterRule("sideways", screen.TypeIP, "1.2.3.4", ""); err == nil {
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

	r, err := s.AddFilterRule(screen.KindBlock, screen.TypeKeyword, "crypto pump", "")
	if err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}
	removed, err := s.DeleteFilterRule(r.ID)
	if err != nil {
		t.Fatalf("DeleteFilterRule: %v", err)
	}
	if !removed {
		t.Error("DeleteFilterRule reported no row removed for a rule that existed")
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

	r, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "example.ru", "")
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

// Filter rules must store timestamps in the same layout as every other write.
// A raw time.Time binds as "2026-09-09 20:51:12 +0000 UTC", which date()
// cannot parse and which sorts wrongly against rows written by the column's own
// datetime('now') default — this table has one, so the two formats would end up
// side by side.
func TestAddFilterRuleStoresQueryableTimestamps(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	if _, err := s.AddFilterRule(screen.KindBlock, screen.TypeDomain, "spam.example", ""); err != nil {
		t.Fatalf("AddFilterRule: %v", err)
	}

	var day *string
	var raw string
	err := s.db.QueryRow("SELECT date(created_at), created_at FROM filter_rules").Scan(&day, &raw)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if day == nil {
		t.Fatalf("SQLite cannot parse created_at %q — date() returned NULL", raw)
	}
	if want := time.Now().UTC().Format("2006-01-02"); *day != want {
		t.Errorf("date(created_at) = %q, want %q", *day, want)
	}
}

// An id that does not exist must report that nothing went, so the handler can
// avoid confirming a removal that did not happen.
func TestDeleteFilterRuleUnknownID(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	removed, err := s.DeleteFilterRule("no-such-rule")
	if err != nil {
		t.Fatalf("DeleteFilterRule: %v", err)
	}
	if removed {
		t.Error("DeleteFilterRule reported a removal for an id that does not exist")
	}
}
