package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/spam"
)

// SpamSignal is one stored reason a submission was held: which rule fired, on
// which field, on what text, and for how many points.
//
// Separate from spam.Signal because this one is a row: it is scanned from the
// database, carries whatever an older binary wrote, and is rendered rather than
// computed. The Rule field is typed by internal/spam all the same, so the value
// set has one definition — the same reason store returns filter.Rule directly.
type SpamSignal struct {
	Rule   spam.Rule
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

// ErrSubmissionGone means there is no such submission at all — deleted, purged
// by the retention sweep, or never real.
//
// It exists because sql.ErrNoRows on the quarantine path answers two different
// questions with one value: "this row is no longer held" and "this row does not
// exist". A caller that cannot tell them apart has to guess, and guessing wrong
// tells an operator their submission is safe in the inbox when it has been
// permanently deleted.
var ErrSubmissionGone = errors.New("submission does not exist")

// classifyMissingHeld turns a missing held row into the specific reason, so the
// caller can say something true. Called only once a held lookup has already come
// back empty.
func (s *Store) classifyMissingHeld(id string) error {
	var isHeld int
	switch err := s.db.QueryRow("SELECT is_held FROM submissions WHERE id = ?", id).Scan(&isHeld); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("submission %s: %w", id, ErrSubmissionGone)
	case err != nil:
		return fmt.Errorf("submission %s: classifying a missing held row: %w", id, err)
	default:
		// The row is there and accepted, which is what a second restore looks
		// like. sql.ErrNoRows stays the sentinel for that case.
		return fmt.Errorf("submission %s: not held: %w", id, sql.ErrNoRows)
	}
}

// heldColumns is the shared select list for held submissions. Kept in one place
// so the column order can never drift between the list and single-row scans.
const heldColumns = `id, form_id, data, ip, read, created_at, is_held, spam_score, held_threshold, notified`

// heldColumnsFor is heldColumns qualified with a table alias, for the queries
// that join forms and would otherwise have an ambiguous "id".
func heldColumnsFor(alias string) string {
	cols := strings.Split(heldColumns, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// rowScanner is *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(...any) error }

// scanHeld reads one row of heldColumns. sc is *sql.Row or *sql.Rows.
func scanHeld(sc rowScanner) (Submission, error) {
	return scanHeldExtra(sc)
}

// scanHeldWithFormName reads heldColumns plus a joined form name.
//
// A concrete function rather than a variadic: the destinations after
// heldColumns are positional, so a JOIN that put f.name *before* the shared list
// would shift every destination by one with no error at all — database/sql
// scans the TEXT form name into ID quite happily, and you get a list where
// every id is a form name. There is exactly one extra column in this codebase,
// so owning both the column and its destination here removes the hazard rather
// than documenting it.
func scanHeldWithFormName(sc rowScanner) (Submission, string, error) {
	var formName string
	sub, err := scanHeldExtra(sc, &formName)
	return sub, formName, err
}

// heldColumnsWithFormName is the select list scanHeldWithFormName expects, with
// the extra column last. Built here so the list and the destinations cannot
// drift apart.
func heldColumnsWithFormName(alias string) string {
	return heldColumnsFor(alias) + ", f.name"
}

func scanHeldExtra(sc rowScanner, extra ...any) (Submission, error) {
	var (
		sub      Submission
		rawData  string
		readInt  int
		heldInt  int
		notified int
	)
	dest := []any{&sub.ID, &sub.FormID, &rawData, &sub.IP, &readInt, &sub.CreatedAt,
		&heldInt, &sub.SpamScore, &sub.HeldThreshold, &notified}
	if err := sc.Scan(append(dest, extra...)...); err != nil {
		return Submission{}, err
	}
	sub.RawData = rawData
	sub.Read = readInt == 1
	sub.IsHeld = heldInt == 1
	sub.Notified = notified == 1
	sub.Data = decodeSubmissionData(sub.ID, rawData)
	return sub, nil
}

// querySubmissions runs a heldColumns query and scans every row.
//
// Routing the four read paths through scanHeld left three byte-identical loop
// bodies around it, differing only in the query, the args and the error string —
// the third copy of a shape the modularity rule says to consolidate at the
// second. what prefixes the wrapped error, so each caller keeps its own context.
func (s *Store) querySubmissions(what, query string, args ...any) ([]Submission, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()

	var subs []Submission
	for rows.Next() {
		sub, err := scanHeld(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return subs, nil
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
//
// score and threshold come from the parameters, and sub.SpamScore /
// sub.HeldThreshold are ignored. That asymmetry exists because Submission is
// both a read result and a write argument: reads populate every column, writes
// legitimately pass zeros. Passing a populated Submission here and expecting
// its own fields to win would silently store the parameters instead — so the
// parameters are the only input, and this comment is the warning.
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
		sub.ID, sub.FormID, raw, sub.IP, sqliteTimestamp(createdAt), score, threshold, sqliteTimestamp(createdAt),
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
	return s.querySubmissions("held submissions",
		"SELECT "+heldColumns+" FROM submissions WHERE is_held = 1 ORDER BY created_at DESC, id LIMIT ? OFFSET ?",
		limit, offset)
}

// GetHeldSubmission returns one held submission by id.
func (s *Store) GetHeldSubmission(id string) (Submission, error) {
	sub, err := scanHeld(s.db.QueryRow(
		"SELECT "+heldColumns+" FROM submissions WHERE id = ? AND is_held = 1", id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Which kind of missing? A double-clicked restore and a submission
			// the retention sweep deleted both land here, and they need opposite
			// messages.
			return Submission{}, s.classifyMissingHeld(id)
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
	// UPDATE … RETURNING rather than update-then-select. With two statements the
	// row is already restored by the time the re-read runs, so a failure there
	// returned an error for a submission that *was* restored — and the handler
	// told the operator "could not be restored", pointing them at the queue
	// while the submission sat unread in the inbox with its notification never
	// sent. One statement removes the window entirely.
	//
	// The AND is_held = 1 guard is what makes this idempotent: a double-click or
	// a resubmitted POST affects no rows and returns sql.ErrNoRows rather than
	// restoring twice and sending the withheld notification twice.
	sub, err := scanHeld(s.db.QueryRow(
		"UPDATE submissions SET is_held = 0, read = 0, held_at = '' WHERE id = ? AND is_held = 1 "+
			"RETURNING "+heldColumns, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The AND is_held = 1 guard makes a second restore affect no rows.
			// Same question as above: already restored, or gone entirely?
			return Submission{}, s.classifyMissingHeld(id)
		}
		return Submission{}, fmt.Errorf("restore submission: %w", err)
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

// DeleteHeld permanently removes held submissions and reports how many rows
// actually went. The is_held = 1 guard scopes the delete to the quarantine
// queue: an accepted submission's id arriving on this path is either a bug or
// someone probing, and either way it must not take a real submission with it.
// spam_signals follow via ON DELETE CASCADE.
//
// The count is returned rather than left to the caller because that guard makes
// len(ids) a different number: ids the retention sweep already purged, or that
// another admin acted on, match nothing. Reporting the length of the request
// tells the operator "20 deleted" when 12 went.
func (s *Store) DeleteHeld(ids []string) (int, error) {
	// Batched because SQLite caps bound parameters at 32766 (SQLITE_MAX_VARIABLE_NUMBER).
	// One IN (?,?,…) over an unbounded list fails outright with "too many SQL
	// variables" — which is how "Empty quarantine" used to break on exactly the
	// large queue that needed emptying. Callers here are bounded by a page of
	// checkboxes, but the limit is a property of the statement, not the caller.
	// To clear the whole queue use DeleteAllHeld, which binds nothing.
	const batch = 500

	total := 0
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
		// The running total is returned *with* the error, not discarded. Each
		// batch autocommits on its own, so a failure in batch three leaves the
		// first two permanently deleted — and returning 0 there tells the
		// operator nothing went when a thousand rows did. That is the same
		// untruth this function's count was added to prevent, inverted.
		res, err := s.db.Exec(query, args...)
		if err != nil {
			return total, fmt.Errorf("delete held: after %d rows: %w", total, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("delete held: after %d rows: %w", total, err)
		}
		total += int(affected)
	}
	return total, nil
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
	res, err := s.db.Exec("DELETE FROM submissions WHERE is_held = 1 AND created_at < ?", sqliteTimestamp(cutoff))
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
