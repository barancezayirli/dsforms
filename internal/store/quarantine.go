package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SpamSignal is one stored reason a submission was held: which rule fired, on
// which field, on what text, and for how many points.
//
// It mirrors spam.Signal but is a separate type on purpose — this one is a
// historical record read back from the database, and it must not change meaning
// when the weights or the threshold in internal/spam are retuned.
type SpamSignal struct {
	Rule   string
	Field  string
	Match  string
	Weight int
}

// NavCounts are the badge numbers the sidebar shows on every page.
type NavCounts struct {
	Unread   int
	Held     int
	Waitlist int
}

// heldColumns is the shared select list for held submissions. Kept in one place
// so the column order can never drift between the list and single-row scans.
const heldColumns = `id, form_id, data, ip, read, created_at, is_held, spam_score, held_threshold, notified`

// scanHeld reads one row of heldColumns. rows may be *sql.Row or *sql.Rows.
func scanHeld(sc interface{ Scan(...any) error }) (Submission, error) {
	var (
		sub      Submission
		rawData  string
		readInt  int
		heldInt  int
		notified int
	)
	if err := sc.Scan(&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt,
		&heldInt, &sub.SpamScore, &sub.HeldThreshold, &notified); err != nil {
		return Submission{}, err
	}
	sub.RawData = rawData
	sub.Read = readInt == 1
	sub.IsHeld = heldInt == 1
	sub.Notified = notified == 1
	sub.Data = decodeSubmissionData(sub.ID, rawData)
	return sub, nil
}

// CreateHeldSubmission stores a submission that crossed the spam threshold,
// together with the breakdown that put it there.
//
// threshold is the value actually applied at hold time, not today's setting —
// an operator can move the threshold afterwards, and the quarantine meter has
// to keep showing what this submission was judged against.
//
// The row is written with notified = 0 so RestoreSubmission can tell that the
// notification was withheld and still owes sending.
func (s *Store) CreateHeldSubmission(sub Submission, score, threshold int, signals []SpamSignal) error {
	raw := sub.RawData
	if raw == "" {
		b, err := json.Marshal(sub.Data)
		if err != nil {
			return fmt.Errorf("create held submission: marshal data: %w", err)
		}
		raw = string(b)
	}

	createdAt := sub.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	createdAt = createdAt.UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("create held submission: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	if _, err := tx.Exec(`
		INSERT INTO submissions (id, form_id, data, ip, read, created_at, is_held, spam_score, held_threshold, held_at, notified)
		VALUES (?, ?, ?, ?, 0, ?, 1, ?, ?, ?, 0)`,
		// Formatted, not handed over as a time.Time: the driver would stringify
		// it as "2026-09-09 19:53:04 +0000 UTC", which SQLite's date()/datetime()
		// cannot parse and which sorts differently from every other timestamp in
		// this table. CreateSubmission has always used sqliteTime; so must this.
		sub.ID, sub.FormID, raw, sub.IP, createdAt.Format(sqliteTime), score, threshold, createdAt.Format(sqliteTime),
	); err != nil {
		return fmt.Errorf("create held submission: %w", err)
	}

	for _, sig := range signals {
		if _, err := tx.Exec(`
			INSERT INTO spam_signals (submission_id, rule, field, match_text, weight)
			VALUES (?, ?, ?, ?, ?)`,
			sub.ID, sig.Rule, sig.Field, sig.Match, sig.Weight,
		); err != nil {
			return fmt.Errorf("create held submission: signal %s: %w", sig.Rule, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create held submission: commit: %w", err)
	}
	return nil
}

// HeldSubmissions returns a page of the quarantine queue, newest first.
func (s *Store) HeldSubmissions(limit, offset int) ([]Submission, error) {
	rows, err := s.db.Query(
		"SELECT "+heldColumns+" FROM submissions WHERE is_held = 1 ORDER BY created_at DESC, id LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("held submissions: %w", err)
	}
	defer rows.Close()

	var out []Submission
	for rows.Next() {
		sub, err := scanHeld(rows)
		if err != nil {
			return nil, fmt.Errorf("held submissions: %w", err)
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("held submissions: %w", err)
	}
	return out, nil
}

// GetHeldSubmission returns one held submission by id.
func (s *Store) GetHeldSubmission(id string) (Submission, error) {
	sub, err := scanHeld(s.db.QueryRow(
		"SELECT "+heldColumns+" FROM submissions WHERE id = ? AND is_held = 1", id))
	if err != nil {
		if err == sql.ErrNoRows {
			return Submission{}, err
		}
		return Submission{}, fmt.Errorf("get held submission: %w", err)
	}
	return sub, nil
}

// HeldCount returns the number of submissions currently awaiting review.
func (s *Store) HeldCount() (int, error) {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM submissions WHERE is_held = 1").Scan(&n); err != nil {
		return 0, fmt.Errorf("held count: %w", err)
	}
	return n, nil
}

// SubmissionSignals returns the stored breakdown for a submission, in insertion
// order — which is the order internal/spam emitted them, and the order the
// quarantine panel renders them.
func (s *Store) SubmissionSignals(submissionID string) ([]SpamSignal, error) {
	rows, err := s.db.Query(
		"SELECT rule, field, match_text, weight FROM spam_signals WHERE submission_id = ? ORDER BY id",
		submissionID,
	)
	if err != nil {
		return nil, fmt.Errorf("submission signals: %w", err)
	}
	defer rows.Close()

	var out []SpamSignal
	for rows.Next() {
		var sig SpamSignal
		if err := rows.Scan(&sig.Rule, &sig.Field, &sig.Match, &sig.Weight); err != nil {
			return nil, fmt.Errorf("submission signals: %w", err)
		}
		out = append(out, sig)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("submission signals: %w", err)
	}
	return out, nil
}

// RestoreSubmission moves a held submission back into its form as unread and
// returns it, so the caller can send the notification that was withheld.
//
// The spam_signals rows are deliberately kept. They are the evidence of a false
// positive, and someone tuning the filter later needs to see what it got wrong.
func (s *Store) RestoreSubmission(id string) (Submission, error) {
	res, err := s.db.Exec(
		"UPDATE submissions SET is_held = 0, read = 0, held_at = '' WHERE id = ? AND is_held = 1", id)
	if err != nil {
		return Submission{}, fmt.Errorf("restore submission: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Submission{}, fmt.Errorf("restore submission: %w", err)
	}
	if n == 0 {
		return Submission{}, fmt.Errorf("restore submission %s: not held", id)
	}

	sub, err := scanHeld(s.db.QueryRow("SELECT "+heldColumns+" FROM submissions WHERE id = ?", id))
	if err != nil {
		return Submission{}, fmt.Errorf("restore submission: reload: %w", err)
	}
	return sub, nil
}

// MarkNotified records that a restored submission's withheld notification has
// been sent, so a second restore cannot send it twice.
func (s *Store) MarkNotified(id string) error {
	if _, err := s.db.Exec("UPDATE submissions SET notified = 1 WHERE id = ?", id); err != nil {
		return fmt.Errorf("mark notified: %w", err)
	}
	return nil
}

// DeleteHeld permanently removes held submissions. The is_held = 1 guard scopes
// the delete to the quarantine queue: an accepted submission's id arriving on
// this path is either a bug or someone probing, and either way it must not take
// a real submission with it. spam_signals follow via ON DELETE CASCADE.
func (s *Store) DeleteHeld(ids []string) error {
	// Batched because SQLite caps bound parameters at 32766 (SQLITE_MAX_VARIABLE_NUMBER).
	// One IN (?,?,…) over an unbounded list fails outright with "too many SQL
	// variables" — which is how "Empty quarantine" used to break on exactly the
	// large queue that needed emptying. Callers here are bounded by a page of
	// checkboxes, but the limit is a property of the statement, not the caller.
	// To clear the whole queue use DeleteAllHeld, which binds nothing.
	const batch = 500

	for len(ids) > 0 {
		n := batch
		if len(ids) < n {
			n = len(ids)
		}
		chunk := ids[:n]
		ids = ids[n:]

		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query := "DELETE FROM submissions WHERE is_held = 1 AND id IN (" + strings.Join(placeholders, ",") + ")"
		if _, err := s.db.Exec(query, args...); err != nil {
			return fmt.Errorf("delete held: %w", err)
		}
	}
	return nil
}

// DeleteAllHeld empties the quarantine in one statement and reports how many
// rows went.
//
// Enumerating ids to delete them all is both slower and bounded by SQLite's
// parameter limit; a set-based DELETE has neither problem. The returned count is
// what the operator is shown, so it must be the real RowsAffected rather than
// the length of a list we happened to fetch first.
func (s *Store) DeleteAllHeld() (int, error) {
	res, err := s.db.Exec("DELETE FROM submissions WHERE is_held = 1")
	if err != nil {
		return 0, fmt.Errorf("delete all held: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete all held: %w", err)
	}
	return int(n), nil
}

// PurgeHeldOlderThan deletes held submissions created before cutoff and returns
// how many went. The caller supplies the cutoff rather than a duration so the
// sweep is testable without sleeping.
func (s *Store) PurgeHeldOlderThan(cutoff time.Time) (int, error) {
	res, err := s.db.Exec("DELETE FROM submissions WHERE is_held = 1 AND created_at < ?", cutoff.UTC().Format(sqliteTime))
	if err != nil {
		return 0, fmt.Errorf("purge held: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge held: %w", err)
	}
	return int(n), nil
}

// NavCounts returns the three sidebar badge numbers in one round trip. It runs
// on every admin page render, so it is deliberately three indexed COUNTs and
// nothing more.
func (s *Store) NavCounts() (NavCounts, error) {
	var n NavCounts
	if err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM submissions WHERE read = 0 AND is_held = 0),
			(SELECT COUNT(*) FROM submissions WHERE is_held = 1),
			(SELECT COUNT(*) FROM waitlist_entries)
	`).Scan(&n.Unread, &n.Held, &n.Waitlist); err != nil {
		return NavCounts{}, fmt.Errorf("nav counts: %w", err)
	}
	return n, nil
}

// HeldCountForForm returns how many of one form's submissions are in
// quarantine, for the "Held" stat on the form detail page.
func (s *Store) HeldCountForForm(formID string) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM submissions WHERE form_id = ? AND is_held = 1", formID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("held count for form: %w", err)
	}
	return n, nil
}

// Neighbours locates a submission within its form's list so the reader drawer
// can show "3 of 612" and step to the adjacent one without closing.
//
// The ordering must match ListSubmissionsPaged exactly (newest first, held rows
// excluded), or the drawer's up/down arrows would walk a different sequence
// from the table behind them. Newer is the row above (a lower row number).
func (s *Store) Neighbours(formID, subID string) (newerID, olderID string, position, total int, err error) {
	var newer, older *string
	err = s.db.QueryRow(`
		WITH ordered AS (
			SELECT id, ROW_NUMBER() OVER (ORDER BY created_at DESC, id) AS rn
			FROM submissions WHERE form_id = ? AND is_held = 0
		)
		SELECT
			COALESCE((SELECT rn FROM ordered WHERE id = ?), 0),
			(SELECT COUNT(*) FROM ordered),
			(SELECT id FROM ordered WHERE rn = (SELECT rn FROM ordered WHERE id = ?) - 1),
			(SELECT id FROM ordered WHERE rn = (SELECT rn FROM ordered WHERE id = ?) + 1)`,
		formID, subID, subID, subID,
	).Scan(&position, &total, &newer, &older)
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("submission neighbours: %w", err)
	}
	if newer != nil {
		newerID = *newer
	}
	if older != nil {
		olderID = *older
	}
	return newerID, olderID, position, total, nil
}
