package store

import (
	"fmt"
	"time"

	"github.com/barancezayirli/dsforms/internal/screen"
)

// DayCounts is one day's split of accepted against held submissions.
type DayCounts struct {
	Day      time.Time
	Accepted int
	Held     int
}

// dayKey formats a time the way SQLite's date() does, so Go-side bucketing and
// SQL grouping agree on what a day is.
const dayKey = "2006-01-02"

// SubmissionsPerDay returns one bucket per day for the last n days, oldest
// first, including days with no submissions.
//
// The zero-filling is the point: the chart's x axis is time, so a quiet day has
// to occupy its slot. Letting SQL return only non-empty days would silently
// compress the timeline and make a sparse week look busy.
func (s *Store) SubmissionsPerDay(days int, forms FormScope) ([]DayCounts, error) {
	if days < 1 {
		days = 1
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))

	scopeClause, scopeArgs := forms.clause("form_id")
	args := append([]any{sqliteTimestamp(start)}, scopeArgs...)
	rows, err := s.conn().Query(`
		SELECT date(created_at) AS day,
		       COUNT(CASE WHEN is_held = 0 THEN 1 END),
		       COUNT(CASE WHEN is_held = 1 THEN 1 END)
		FROM submissions
		WHERE created_at >= ?`+scopeClause+`
		GROUP BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("submissions per day: %w", err)
	}
	defer rows.Close()

	type pair struct{ accepted, held int }
	byDay := make(map[string]pair, days)
	for rows.Next() {
		var day string
		var p pair
		if err := rows.Scan(&day, &p.accepted, &p.held); err != nil {
			return nil, fmt.Errorf("submissions per day: %w", err)
		}
		byDay[day] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("submissions per day: %w", err)
	}

	out := make([]DayCounts, 0, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		p := byDay[day.Format(dayKey)]
		out = append(out, DayCounts{Day: day, Accepted: p.accepted, Held: p.held})
	}
	return out, nil
}

// SubmissionsPerFormPerDay returns each form's daily accepted counts for the
// last n days, oldest first, for the sparkline on its card.
//
// One grouped query rather than one per form: a instance with fifty forms would
// otherwise issue fifty queries to render a single page.
func (s *Store) SubmissionsPerFormPerDay(days int) (map[string][]int, error) {
	if days < 1 {
		days = 1
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))

	rows, err := s.conn().Query(`
		SELECT form_id, date(created_at) AS day, COUNT(*)
		FROM submissions
		WHERE created_at >= ? AND is_held = 0
		GROUP BY form_id, day`, sqliteTimestamp(start))
	if err != nil {
		return nil, fmt.Errorf("submissions per form per day: %w", err)
	}
	defer rows.Close()

	index := make(map[string]int, days)
	for i := 0; i < days; i++ {
		index[start.AddDate(0, 0, i).Format(dayKey)] = i
	}

	out := make(map[string][]int)
	for rows.Next() {
		var formID, day string
		var n int
		if err := rows.Scan(&formID, &day, &n); err != nil {
			return nil, fmt.Errorf("submissions per form per day: %w", err)
		}
		series, ok := out[formID]
		if !ok {
			series = make([]int, days)
		}
		if i, ok := index[day]; ok {
			series[i] = n
		}
		out[formID] = series
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("submissions per form per day: %w", err)
	}
	return out, nil
}

// FormStats is one row of the "By form" table on the overview.
type FormStats struct {
	FormID   string
	Name     string
	Received int
	Held     int
	Unread   int
	Read     int
}

// PerFormStats returns per-form totals for the overview table.
func (s *Store) PerFormStats(forms FormScope) ([]FormStats, error) {
	// Scoped on the form rather than on the join, so an out-of-scope form is
	// absent entirely rather than present with zeroes — which would disclose
	// both that it exists and what it is called.
	scopeClause, scopeArgs := forms.clause("f.id")
	rows, err := s.conn().Query(`
		SELECT f.id, f.name,
		       COUNT(CASE WHEN s.is_held = 0 THEN 1 END),
		       COUNT(CASE WHEN s.is_held = 1 THEN 1 END),
		       COUNT(CASE WHEN s.is_held = 0 AND s.read = 0 THEN 1 END),
		       COUNT(CASE WHEN s.is_held = 0 AND s.read = 1 THEN 1 END)
		FROM forms f
		LEFT JOIN submissions s ON s.form_id = f.id
		WHERE 1 = 1`+scopeClause+`
		GROUP BY f.id
		ORDER BY 3 DESC, f.created_at DESC`, scopeArgs...)
	if err != nil {
		return nil, fmt.Errorf("per form stats: %w", err)
	}
	defer rows.Close()

	var out []FormStats
	for rows.Next() {
		var fs FormStats
		if err := rows.Scan(&fs.FormID, &fs.Name, &fs.Received, &fs.Held, &fs.Unread, &fs.Read); err != nil {
			return nil, fmt.Errorf("per form stats: %w", err)
		}
		out = append(out, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("per form stats: %w", err)
	}
	return out, nil
}

// RecentSubmission is one row of the overview's activity feed.
type RecentSubmission struct {
	Submission
	FormName string
}

// RecentSubmissions returns the newest accepted submissions across every form.
func (s *Store) RecentSubmissions(n int) ([]RecentSubmission, error) {
	rows, err := s.conn().Query(`
		SELECT `+heldColumnsWithFormName("s")+`
		FROM submissions s
		JOIN forms f ON f.id = s.form_id
		WHERE s.is_held = 0
		ORDER BY s.created_at DESC, s.id
		LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("recent submissions: %w", err)
	}
	defer rows.Close()

	var out []RecentSubmission
	for rows.Next() {
		sub, formName, err := scanHeldWithFormName(rows)
		if err != nil {
			return nil, fmt.Errorf("recent submissions: %w", err)
		}
		rs := RecentSubmission{Submission: sub, FormName: formName}
		out = append(out, rs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent submissions: %w", err)
	}
	return out, nil
}

// SignalTally is one row of the "Top spam signals" panel.
type SignalTally struct {
	Check  screen.Check
	Hits   int
	Weight int // the weight this rule was recorded at, for the "w6" label
}

// TopSpamSignals counts which rules fired most over the last n days.
//
// It reads the stored spam_signals rows rather than re-scoring anything: the
// weights in internal/spam can be retuned and the threshold is configurable, so
// re-scoring old submissions would report reasons that were never applied.
func (s *Store) TopSpamSignals(days int, forms FormScope) ([]SignalTally, error) {
	start := time.Now().UTC().AddDate(0, 0, -days)
	// Through the join: spam_signals has no form_id of its own, so the scope
	// has to reach it by way of the submission it belongs to.
	scopeClause, scopeArgs := forms.clause("s.form_id")
	args := append([]any{sqliteTimestamp(start)}, scopeArgs...)
	rows, err := s.conn().Query(`
		SELECT g.rule, COUNT(*), MAX(g.weight)
		FROM spam_signals g
		JOIN submissions s ON s.id = g.submission_id
		WHERE s.created_at >= ?`+scopeClause+`
		GROUP BY g.rule
		ORDER BY 2 DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("top spam signals: %w", err)
	}
	defer rows.Close()

	var out []SignalTally
	for rows.Next() {
		var t SignalTally
		if err := rows.Scan(&t.Check, &t.Hits, &t.Weight); err != nil {
			return nil, fmt.Errorf("top spam signals: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("top spam signals: %w", err)
	}
	return out, nil
}

// HeldSince counts submissions held in the last n days, for the "15.7% of
// traffic" style stats.
func (s *Store) HeldSince(days int, forms FormScope) (held, total int, err error) {
	start := time.Now().UTC().AddDate(0, 0, -days)
	scopeClause, scopeArgs := forms.clause("form_id")
	args := append([]any{sqliteTimestamp(start)}, scopeArgs...)
	row := s.conn().QueryRow(`
		SELECT COUNT(CASE WHEN is_held = 1 THEN 1 END), COUNT(*)
		FROM submissions WHERE created_at >= ?`+scopeClause, args...)
	if err := row.Scan(&held, &total); err != nil {
		return 0, 0, fmt.Errorf("held since: %w", err)
	}
	return held, total, nil
}
