package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/filter"
	"github.com/google/uuid"
)

// AddFilterRule validates a rule and stores its normalised form.
//
// Validation happens here rather than only in the handler so that every path
// into the table — an admin form, a "Block this sender" button in the
// quarantine drawer, a future CLI — gets the same normalisation. Storing an
// unnormalised value would quietly defeat both the UNIQUE constraint and the
// plain string comparison that filter.Match relies on.
func (s *Store) AddFilterRule(kind, ruleType, value, note string) (filter.Rule, error) {
	if kind != filter.KindBlock && kind != filter.KindAllow {
		return filter.Rule{}, fmt.Errorf("add filter rule: unknown kind %q", kind)
	}
	normalised, err := filter.Validate(ruleType, value)
	if err != nil {
		return filter.Rule{}, fmt.Errorf("add filter rule: %w", err)
	}

	rule := filter.Rule{
		ID:        uuid.New().String(),
		Kind:      kind,
		Type:      ruleType,
		Value:     normalised,
		Note:      strings.TrimSpace(note),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	_, err = s.db.Exec(
		"INSERT INTO filter_rules (id, kind, type, value, note, hits, created_at) VALUES (?, ?, ?, ?, ?, 0, ?)",
		rule.ID, rule.Kind, rule.Type, rule.Value, rule.Note, sqliteTimestamp(rule.CreatedAt),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return filter.Rule{}, fmt.Errorf("add filter rule: %s %q already exists", ruleType, normalised)
		}
		return filter.Rule{}, fmt.Errorf("add filter rule: %w", err)
	}
	return rule, nil
}

// ListFilterRules returns every rule, newest first within each kind.
//
// The whole table is read at once because it is operator-curated and small —
// tens of rows, not thousands — and the submit path needs all of it on every
// submission anyway to decide allow-before-block.
func (s *Store) ListFilterRules() ([]filter.Rule, error) {
	rows, err := s.db.Query(
		"SELECT id, kind, type, value, note, hits, created_at FROM filter_rules ORDER BY kind, created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list filter rules: %w", err)
	}
	defer rows.Close()

	var out []filter.Rule
	for rows.Next() {
		var r filter.Rule
		if err := rows.Scan(&r.ID, &r.Kind, &r.Type, &r.Value, &r.Note, &r.Hits, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("list filter rules: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list filter rules: %w", err)
	}
	return out, nil
}

// DeleteFilterRule removes one rule.
func (s *Store) DeleteFilterRule(id string) error {
	if _, err := s.db.Exec("DELETE FROM filter_rules WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete filter rule: %w", err)
	}
	return nil
}

// IncrementRuleHits records that a rule matched a submission, so the operator
// can see which rules are doing work and which are stale.
func (s *Store) IncrementRuleHits(id string) error {
	if _, err := s.db.Exec("UPDATE filter_rules SET hits = hits + 1 WHERE id = ?", id); err != nil {
		return fmt.Errorf("increment rule hits: %w", err)
	}
	return nil
}
