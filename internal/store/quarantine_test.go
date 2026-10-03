package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

// seedForm creates a form to hang submissions off. Every quarantine test needs
// one because submissions.form_id is a foreign key and the DB is opened with
// foreign_keys(1).
func seedForm(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.CreateForm(Form{ID: id, Name: "Contact", EmailTo: "me@example.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
}

func heldFixture(id, formID string, score int, at time.Time) Submission {
	return Submission{
		ID:        id,
		FormID:    formID,
		Data:      map[string]string{"name": "Bot", "message": "[url=http://x]buy[/url]"},
		RawData:   `{"name":"Bot","message":"[url=http://x]buy[/url]"}`,
		IP:        "203.0.113.7",
		CreatedAt: at,
	}
}

func TestCreateHeldSubmissionStoresScoreAndSignals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC().Truncate(time.Second)
	signals := []SpamSignal{
		{Check: "markup", Field: "message", Match: "[url=", Weight: 6},
		{Check: "extra_links", Field: "", Match: "", Weight: 2},
	}
	if err := s.CreateHeldSubmission(heldFixture("s1", "f1", 8, now), 8, 6, signals); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held, want 1", len(held))
	}
	if held[0].SpamScore != 8 {
		t.Errorf("SpamScore = %d, want 8", held[0].SpamScore)
	}
	if held[0].HeldThreshold != 6 {
		t.Errorf("HeldThreshold = %d, want 6 (the threshold actually applied)", held[0].HeldThreshold)
	}
	if !held[0].IsHeld {
		t.Error("IsHeld = false, want true")
	}
	if held[0].Notified {
		t.Error("Notified = true; a held submission must not be marked notified — restore relies on it")
	}

	got, err := s.SubmissionSignals("s1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if !reflect.DeepEqual(got, signals) {
		t.Errorf("SubmissionSignals =\n  %+v\nwant\n  %+v", got, signals)
	}
}

// A held submission must not appear in the form's normal submission list, or
// quarantine would leak spam into the inbox it exists to keep clean.
func TestHeldSubmissionsAreHiddenFromTheInbox(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	if err := s.CreateSubmission(Submission{
		ID: "clean", FormID: "f1", Data: map[string]string{"name": "Jane"},
		RawData: `{"name":"Jane"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("spam", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "clean" {
		t.Fatalf("ListSubmissions returned %d rows (%v), want just the clean one", len(subs), subs)
	}

	total, err := s.CountSubmissions("f1")
	if err != nil {
		t.Fatalf("CountSubmissions: %v", err)
	}
	if total != 1 {
		t.Errorf("CountSubmissions = %d, want 1 — held rows must not inflate the form's total", total)
	}

	paged, err := s.ListSubmissionsPaged("f1", 10, 0)
	if err != nil {
		t.Fatalf("ListSubmissionsPaged: %v", err)
	}
	if len(paged) != 1 {
		t.Errorf("ListSubmissionsPaged returned %d rows, want 1", len(paged))
	}
}

func TestRestoreSubmission(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	signals := []SpamSignal{{Check: "keyword", Field: "message", Match: "casino", Weight: 5}}
	if err := s.CreateHeldSubmission(heldFixture("s1", "f1", 8, now), 8, 6, signals); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	restored, err := s.RestoreSubmission("s1")
	if err != nil {
		t.Fatalf("RestoreSubmission: %v", err)
	}
	if restored.ID != "s1" {
		t.Errorf("returned submission ID = %q, want s1", restored.ID)
	}
	if restored.Read {
		t.Error("a restored submission must land unread — it has never been seen")
	}

	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("got %d held after restore, want 0", len(held))
	}

	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "s1" {
		t.Fatalf("restored submission did not return to the form: %v", subs)
	}

	// The breakdown is history: it survives the restore so a later reviewer can
	// still see why the filter got it wrong.
	got, err := s.SubmissionSignals("s1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("signals were discarded on restore: got %d, want 1", len(got))
	}
}

func TestRestoreSubmissionUnknownID(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if _, err := s.RestoreSubmission("nope"); err == nil {
		t.Error("RestoreSubmission on an unknown id should error, got nil")
	}
}

func TestDeleteHeldCascadesSignals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 8, now), 8, 6,
			[]SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
			t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
		}
	}

	n, err := s.DeleteHeld([]string{"a", "b"}, AllForms())
	if err != nil {
		t.Fatalf("DeleteHeld: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteHeld reported %d rows, want 2", n)
	}

	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 || held[0].ID != "c" {
		t.Fatalf("got %v, want only c remaining", held)
	}
	// ON DELETE CASCADE should have taken the signals with them.
	sig, err := s.SubmissionSignals("a")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(sig) != 0 {
		t.Errorf("signals for a deleted submission survived: %v", sig)
	}
}

// DeleteHeld must refuse to touch accepted submissions even when handed their
// ids — the quarantine bulk action is scoped to the queue, and an id from
// another form arriving on that endpoint is either a bug or an attack.
func TestDeleteHeldIgnoresAcceptedSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	if err := s.CreateSubmission(Submission{
		ID: "clean", FormID: "f1", Data: map[string]string{"name": "Jane"},
		RawData: `{"name":"Jane"}`, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	// The reported count is now the direct evidence the is_held guard held: an
	// accepted submission's id matches nothing, so nothing is deleted and
	// nothing is claimed.
	n, err := s.DeleteHeld([]string{"clean"}, AllForms())
	if err != nil {
		t.Fatalf("DeleteHeld: %v", err)
	}
	if n != 0 {
		t.Errorf("DeleteHeld reported %d rows for an accepted submission, want 0", n)
	}
	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 {
		t.Error("DeleteHeld deleted an accepted submission")
	}
}

func TestDeleteHeldEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	n, err := s.DeleteHeld(nil, AllForms())
	if err != nil {
		t.Errorf("DeleteHeld(nil) = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("DeleteHeld(nil) reported %d rows, want 0", n)
	}
}

// Time is injected rather than slept on (TDD rule 9).
func TestPurgeHeldOlderThan(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	recent := now.Add(-29 * 24 * time.Hour)

	if err := s.CreateHeldSubmission(heldFixture("old", "f1", 8, old), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission(old): %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("recent", "f1", 8, recent), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission(recent): %v", err)
	}

	n, err := s.PurgeHeldOlderThan(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PurgeHeldOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}

	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 || held[0].ID != "recent" {
		t.Errorf("got %v, want only the recent one to survive", held)
	}
}

func TestHeldCount(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 8, now), 8, 6, nil); err != nil {
			t.Fatalf("CreateHeldSubmission: %v", err)
		}
	}
	n, err := s.HeldCount()
	if err != nil {
		t.Fatalf("HeldCount: %v", err)
	}
	if n != 2 {
		t.Errorf("HeldCount = %d, want 2", n)
	}
}

func TestNavCounts(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	if err := s.CreateSubmission(Submission{
		ID: "unread", FormID: "f1", Data: map[string]string{"a": "b"},
		RawData: `{"a":"b"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("held", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}
	if err := s.CreateWaitlist(Waitlist{ID: "w1", Name: "Launch"}); err != nil {
		t.Fatalf("CreateWaitlist: %v", err)
	}
	if _, _, err := s.CreateEntry(WaitlistEntry{ID: "e1", WaitlistID: "w1", Email: "a@example.com", CreatedAt: now}); err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	nav, err := s.NavCounts(AllForms())
	if err != nil {
		t.Fatalf("NavCounts: %v", err)
	}
	if nav.Unread != 1 {
		t.Errorf("Unread = %d, want 1 (held rows must not count as unread)", nav.Unread)
	}
	if nav.Held != 1 {
		t.Errorf("Held = %d, want 1", nav.Held)
	}
	if nav.Waitlist != 1 {
		t.Errorf("Waitlist = %d, want 1", nav.Waitlist)
	}
}

// TestUpgradeFromPreQuarantineSchema is the migration test that matters. Every
// other test starts from the current schema, so none of them would notice if
// runAlterMigrations stopped working — but a real deployment upgrading in place
// starts from a submissions table with none of the quarantine columns, and an
// index declared over a column that does not exist yet fails the whole open.
func TestUpgradeFromPreQuarantineSchema(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/legacy.db"

	// The submissions/forms tables exactly as they were before this feature.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE forms (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, email_to TEXT NOT NULL DEFAULT '',
			redirect TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE submissions (
			id TEXT PRIMARY KEY,
			form_id TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
			data TEXT NOT NULL, ip TEXT NOT NULL DEFAULT '',
			read INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO forms (id, name) VALUES ('f1', 'Contact');
		INSERT INTO submissions (id, form_id, data) VALUES ('old1', 'f1', '{"name":"Jane"}');
	`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatalf("New() on a pre-quarantine database failed: %v", err)
	}
	defer s.Close()

	// The pre-existing row must survive, and must not have become held.
	subs, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "old1" {
		t.Fatalf("legacy submission lost in migration: %v", subs)
	}

	// And the new machinery must work against the upgraded file.
	if err := s.CreateHeldSubmission(heldFixture("new1", "f1", 8, time.Now().UTC()), 8, 6,
		[]SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
		t.Fatalf("CreateHeldSubmission after upgrade: %v", err)
	}
	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions after upgrade: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held after upgrade, want 1", len(held))
	}
	// The accepted-path write, on an upgraded database. This function was widened
	// from five columns to seven, and the columns it gained come from the ALTER
	// pass on an existing database rather than from the CREATE — which is exactly
	// the difference this test exists to cover, and the path it did not exercise.
	if err := s.CreateSubmission(Submission{
		ID: "upgraded-accepted", FormID: "f1", RawData: `{"name":"Ana"}`,
		SpamScore: 3, HeldThreshold: 6,
	}); err != nil {
		t.Fatalf("CreateSubmission on an upgraded database: %v", err)
	}
	if got, err := s.GetSubmission("upgraded-accepted"); err != nil {
		t.Fatalf("GetSubmission: %v", err)
	} else if got.SpamScore != 3 || got.HeldThreshold != 6 {
		t.Errorf("upgraded row: SpamScore=%d HeldThreshold=%d, want 3 and 6",
			got.SpamScore, got.HeldThreshold)
	}

}

func TestNeighbours(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	base := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	// Newest first once ordered, so c, b, a.
	for i, id := range []string{"a", "b", "c"} {
		if err := s.CreateSubmission(Submission{
			ID: id, FormID: "f1", Data: map[string]string{"n": id},
			RawData: `{"n":"` + id + `"}`, CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("CreateSubmission(%s): %v", id, err)
		}
	}
	// A held submission must not appear in the sequence the drawer walks.
	if err := s.CreateHeldSubmission(heldFixture("held", "f1", 8, base.Add(90*time.Minute)), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	tests := []struct {
		id                   string
		wantNewer, wantOlder string
		wantPos              int
	}{
		{id: "c", wantNewer: "", wantOlder: "b", wantPos: 1},
		{id: "b", wantNewer: "c", wantOlder: "a", wantPos: 2},
		{id: "a", wantNewer: "b", wantOlder: "", wantPos: 3},
	}
	for _, tt := range tests {
		newer, older, pos, total, err := s.Neighbours("f1", tt.id)
		if err != nil {
			t.Fatalf("Neighbours(%s): %v", tt.id, err)
		}
		if newer != tt.wantNewer || older != tt.wantOlder {
			t.Errorf("Neighbours(%s) = newer %q older %q, want %q / %q", tt.id, newer, older, tt.wantNewer, tt.wantOlder)
		}
		if pos != tt.wantPos {
			t.Errorf("Neighbours(%s) position = %d, want %d", tt.id, pos, tt.wantPos)
		}
		if total != 3 {
			t.Errorf("Neighbours(%s) total = %d, want 3 (held rows excluded)", tt.id, total)
		}
	}
}

// TestPaginationIsStableWithTiedTimestamps guards against a subtle data-loss
// bug rather than a crash. Submissions arriving in the same second have equal
// created_at values, and SQLite may order tied rows differently between
// queries — so with LIMIT/OFFSET and no tiebreaker, a row can appear on two
// pages or on none. The drawer's Neighbours query already tie-breaks by id, so
// an untied list query would also make the up/down arrows walk a different
// sequence from the table behind them.
func TestPaginationIsStableWithTiedTimestamps(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	// Every row at the identical instant — the worst case, and a realistic one
	// for an import or a burst.
	same := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("s%02d", i)
		if err := s.CreateSubmission(Submission{
			ID: id, FormID: "f1", Data: map[string]string{"n": id},
			RawData: `{"n":"` + id + `"}`, CreatedAt: same,
		}); err != nil {
			t.Fatalf("CreateSubmission: %v", err)
		}
	}

	collect := func() []string {
		var ids []string
		for offset := 0; offset < 30; offset += 10 {
			page, err := s.ListSubmissionsPaged("f1", 10, offset)
			if err != nil {
				t.Fatalf("ListSubmissionsPaged(offset %d): %v", offset, err)
			}
			for _, sub := range page {
				ids = append(ids, sub.ID)
			}
		}
		return ids
	}

	first := collect()
	if len(first) != 30 {
		t.Fatalf("paging returned %d rows, want 30", len(first))
	}
	seen := map[string]bool{}
	for _, id := range first {
		if seen[id] {
			t.Fatalf("submission %s appeared on more than one page: %v", id, first)
		}
		seen[id] = true
	}

	// And the order must be repeatable, or a reload reshuffles the list under
	// whoever is reading it.
	for i := 0; i < 5; i++ {
		if again := collect(); !reflect.DeepEqual(first, again) {
			t.Fatalf("page order is not stable across queries:\n  %v\n  %v", first, again)
		}
	}

	// The drawer must walk the same sequence the table shows.
	for i, id := range first {
		newer, older, pos, _, err := s.Neighbours("f1", id)
		if err != nil {
			t.Fatalf("Neighbours(%s): %v", id, err)
		}
		if pos != i+1 {
			t.Errorf("%s is row %d in the list but Neighbours reports position %d", id, i+1, pos)
		}
		if i > 0 && newer != first[i-1] {
			t.Errorf("%s: newer = %q, want %q (the row above it in the list)", id, newer, first[i-1])
		}
		if i < len(first)-1 && older != first[i+1] {
			t.Errorf("%s: older = %q, want %q (the row below it in the list)", id, older, first[i+1])
		}
	}
}

// TestGetSubmissionPopulatesQuarantineFields is a regression test for a bug the
// type design invited: GetSubmission selected only the pre-quarantine columns,
// so SpamScore came back 0 on every read. The submission reader renders that
// number beside the stored signal breakdown, so a restored submission displayed
// "score 0" above weights summing to 11 — the one place spam.Detail's
// weights-sum-to-score invariant is shown to a human was the place it broke.
func TestGetSubmissionPopulatesQuarantineFields(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	signals := []SpamSignal{
		{Check: "markup", Field: "message", Match: "[url=", Weight: 6},
		{Check: "keyword", Field: "message", Match: "backlinks", Weight: 5},
	}
	if err := s.CreateHeldSubmission(heldFixture("s1", "f1", 11, time.Now().UTC()), 11, 6, signals); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	// While held.
	sub, err := s.GetSubmission("s1")
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.SpamScore != 11 {
		t.Errorf("SpamScore = %d, want 11 — the reader renders this beside the breakdown", sub.SpamScore)
	}
	if !sub.IsHeld {
		t.Error("IsHeld = false on a held submission; a handler guard written against it would be a silent no-op")
	}
	if sub.HeldThreshold != 6 {
		t.Errorf("HeldThreshold = %d, want 6", sub.HeldThreshold)
	}

	// And after restore, which is when the reader actually shows it.
	if _, err := s.RestoreSubmission("s1"); err != nil {
		t.Fatalf("RestoreSubmission: %v", err)
	}
	sub, err = s.GetSubmission("s1")
	if err != nil {
		t.Fatalf("GetSubmission after restore: %v", err)
	}
	if sub.IsHeld {
		t.Error("IsHeld = true after restore")
	}

	stored, err := s.SubmissionSignals("s1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	sum := 0
	for _, sig := range stored {
		sum += sig.Weight
	}
	if sum != sub.SpamScore {
		t.Errorf("the reader would show score %d beside a breakdown summing to %d", sub.SpamScore, sum)
	}
}

// TestDeleteHeldExceedsSQLiteVariableLimit covers the bound-parameter ceiling.
// SQLite caps variables at 32766, and "Empty quarantine" used to hand every held
// id to a single IN (?,?,…) — so on a queue large enough to actually need
// emptying, the button failed outright and the operator had no other way to
// clear it. 30-day retention plus one spam run gets you there.
//
// The ids do not need to exist: the statement binds every one of them before
// SQLite looks at a single row, so a list of mostly-absent ids reproduces the
// failure exactly while keeping the fixture to three inserts.
func TestDeleteHeldExceedsSQLiteVariableLimit(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	real := []string{"a", "b", "c"}
	for _, id := range real {
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 6, now), 6, 6, nil); err != nil {
			t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
		}
	}

	ids := append([]string{}, real...)
	for i := 0; i < 40000; i++ {
		ids = append(ids, fmt.Sprintf("absent%06d", i))
	}

	n, err := s.DeleteHeld(ids, AllForms())
	if err != nil {
		t.Fatalf("DeleteHeld with %d ids: %v", len(ids), err)
	}
	// 40 000 of the ids are absent, so a count of len(ids) would be a lie the
	// operator sees. Only the real rows may be counted.
	if n != len(real) {
		t.Errorf("DeleteHeld reported %d rows, want %d (absent ids must not count)", n, len(real))
	}
	left, err := s.HeldCount()
	if err != nil {
		t.Fatalf("HeldCount: %v", err)
	}
	if left != 0 {
		t.Errorf("%d held rows remain; the batches did not cover every id", left)
	}
}

// DeleteAllHeld is what "Empty quarantine" should use: one statement, no id
// list, and a truthful count of what it removed.
func TestDeleteAllHeld(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Now().UTC()
	if err := s.CreateSubmission(Submission{
		ID: "clean", FormID: "f1", Data: map[string]string{"n": "x"},
		RawData: `{"n":"x"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 6, now), 6, 6,
			[]SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
			t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
		}
	}

	n, err := s.DeleteAllHeld()
	if err != nil {
		t.Fatalf("DeleteAllHeld: %v", err)
	}
	if n != 3 {
		t.Errorf("reported %d deleted, want 3 — the flash message shows this number", n)
	}

	held, _ := s.HeldSubmissions(AllForms(), 10, 0)
	if len(held) != 0 {
		t.Errorf("%d held rows survived", len(held))
	}
	// Accepted submissions must be untouched, and the signals must cascade.
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 1 || subs[0].ID != "clean" {
		t.Errorf("accepted submissions were affected: %v", subs)
	}
	sig, _ := s.SubmissionSignals("a")
	if len(sig) != 0 {
		t.Errorf("signals survived the delete: %v", sig)
	}
}

// TestPurgeHeldOlderThanIgnoresAcceptedSubmissions is the guard test the
// equivalent DeleteHeld case already had and Purge did not.
//
// This sweep runs unattended at every startup and every 24 hours. Without the
// is_held = 1 guard it would delete every *accepted* submission older than the
// retention window — permanent data loss whose only trace is a log line reading
// "purged N submission(s)".
func TestPurgeHeldOlderThanIgnoresAcceptedSubmissions(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -40)

	if err := s.CreateSubmission(Submission{
		ID: "old-accepted", FormID: "f1", Data: map[string]string{"n": "x"},
		RawData: `{"n":"x"}`, CreatedAt: old,
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := s.CreateHeldSubmission(heldFixture("old-held", "f1", 6, old), 6, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	n, err := s.PurgeHeldOlderThan(now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PurgeHeldOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want exactly the held one", n)
	}
	subs, _ := s.ListSubmissions("f1")
	if len(subs) != 1 || subs[0].ID != "old-accepted" {
		t.Fatalf("the sweep deleted an accepted submission: %v", subs)
	}
}

// TestAcceptedReadsCarryEveryColumn pins the invariant chosen over splitting
// Submission into held and accepted types: every read path populates every
// column, so a Submission means the same thing regardless of which function
// returned it.
//
// Four read paths hand-wrote a subset of the columns, which had two
// consequences. The
// schema defaults notified to 1, so an accepted row came back claiming it had
// never been notified — and a guard written as `if !sub.Notified { send it }`
// would have fired on every listed submission. And a *restored* submission
// keeps its spam_score on purpose (it is the evidence of a false positive), so
// those paths reported score 0 for a row the database says scored 11.
//
// Nothing rendered these fields from a partial path yet, which is exactly why
// this is worth pinning: the same class of bug already shipped once, as a
// quarantine panel reading "score 0" above a breakdown summing to 11.
func TestAcceptedReadsCarryEveryColumn(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if err := s.CreateForm(Form{ID: "f1", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}

	// Hold it, then restore it: that is the state where the quarantine columns
	// on an accepted row are non-zero and therefore observable.
	now := time.Now().UTC()
	if err := s.CreateHeldSubmission(heldFixture("r1", "f1", 11, now), 11, 6,
		[]SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 11}}); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}
	if _, err := s.RestoreSubmission("r1"); err != nil {
		t.Fatalf("RestoreSubmission: %v", err)
	}
	if err := s.MarkNotified("r1"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	find := func(subs []Submission) (Submission, bool) {
		for _, sub := range subs {
			if sub.ID == "r1" {
				return sub, true
			}
		}
		return Submission{}, false
	}

	check := func(t *testing.T, path string, sub Submission) {
		t.Helper()
		if sub.SpamScore != 11 {
			t.Errorf("%s: SpamScore = %d, want 11 (a restored row keeps its score)", path, sub.SpamScore)
		}
		if sub.HeldThreshold != 6 {
			t.Errorf("%s: HeldThreshold = %d, want 6", path, sub.HeldThreshold)
		}
		if !sub.Notified {
			t.Errorf("%s: Notified = false, want true", path)
		}
		if sub.IsHeld {
			t.Errorf("%s: IsHeld = true for a restored submission", path)
		}
	}

	t.Run("ListSubmissions", func(t *testing.T) {
		subs, err := s.ListSubmissions("f1")
		if err != nil {
			t.Fatalf("ListSubmissions: %v", err)
		}
		sub, ok := find(subs)
		if !ok {
			t.Fatal("restored submission missing from ListSubmissions")
		}
		check(t, "ListSubmissions", sub)
	})

	t.Run("ListSubmissionsPaged", func(t *testing.T) {
		subs, err := s.ListSubmissionsPaged("f1", 25, 0)
		if err != nil {
			t.Fatalf("ListSubmissionsPaged: %v", err)
		}
		sub, ok := find(subs)
		if !ok {
			t.Fatal("restored submission missing from ListSubmissionsPaged")
		}
		check(t, "ListSubmissionsPaged", sub)
	})

	t.Run("RecentSubmissions", func(t *testing.T) {
		recent, err := s.RecentSubmissions(10)
		if err != nil {
			t.Fatalf("RecentSubmissions: %v", err)
		}
		for _, rec := range recent {
			if rec.ID == "r1" {
				check(t, "RecentSubmissions", rec.Submission)
				if rec.FormName != "Contact" {
					t.Errorf("RecentSubmissions: FormName = %q, want Contact", rec.FormName)
				}
				return
			}
		}
		t.Fatal("restored submission missing from RecentSubmissions")
	})

	t.Run("SearchSubmissions", func(t *testing.T) {
		hits, err := s.SearchSubmissions("Bot", AllForms(), 25)
		if err != nil {
			t.Fatalf("SearchSubmissions: %v", err)
		}
		for _, hit := range hits {
			if hit.ID == "r1" {
				check(t, "SearchSubmissions", hit.Submission)
				if hit.FormName != "Contact" {
					t.Errorf("SearchSubmissions: FormName = %q, want Contact", hit.FormName)
				}
				return
			}
		}
		t.Fatal("restored submission missing from SearchSubmissions")
	})
}

// TestMarkNotifiedRecordsDelivery covers the column directly.
//
// It was reachable only through the restore handler, and not really even there:
// RestoreSubmission carries AND is_held = 1, so a second restore returns
// ErrNoRows and the handler bails before the !sub.Notified guard. The
// idempotency test therefore proved the is_held guard, not the notified column,
// and would have passed identically with MarkNotified as a no-op.
//
// The flag means "the withheld notification was actually delivered", which is
// why the restore path sets it only after a successful send.
func TestMarkNotifiedRecordsDelivery(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if err := s.CreateForm(Form{ID: "f1", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}

	now := time.Now().UTC()
	if err := s.CreateHeldSubmission(heldFixture("h1", "f1", 8, now), 8, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	// A held submission has notified = 0: that is the whole point of holding it.
	held, err := s.GetHeldSubmission("h1")
	if err != nil {
		t.Fatalf("GetHeldSubmission: %v", err)
	}
	if held.Notified {
		t.Fatal("a held submission must not be marked notified")
	}

	if _, err := s.RestoreSubmission("h1"); err != nil {
		t.Fatalf("RestoreSubmission: %v", err)
	}
	if err := s.MarkNotified("h1"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	got, err := s.GetSubmission("h1")
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if !got.Notified {
		t.Error("MarkNotified did not set the column")
	}
}

// GetHeldSubmission had no direct test, and its ErrNoRows path is what the
// restore handler now uses to tell "already restored" from a real fault.
func TestGetHeldSubmission(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if err := s.CreateForm(Form{ID: "f1", Name: "Contact"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	now := time.Now().UTC()
	if err := s.CreateHeldSubmission(heldFixture("h1", "f1", 9, now), 9, 6, nil); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	got, err := s.GetHeldSubmission("h1")
	if err != nil {
		t.Fatalf("GetHeldSubmission: %v", err)
	}
	if got.SpamScore != 9 || !got.IsHeld {
		t.Errorf("got score %d held %v, want 9 / true", got.SpamScore, got.IsHeld)
	}

	// The two reasons a held row can be missing must be distinguishable, because
	// the restore handler owes opposite messages for them: "already restored —
	// it is in the inbox" versus "it no longer exists". Reporting the first for
	// the second sends an operator looking for a permanently deleted submission.
	t.Run("an id that never existed is ErrSubmissionGone", func(t *testing.T) {
		_, err := s.GetHeldSubmission("nope")
		if !errors.Is(err, ErrSubmissionGone) {
			t.Errorf("err = %v, want ErrSubmissionGone", err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v must not also read as 'not held' — the two mean opposite things to the operator", err)
		}
	})

	t.Run("a deleted submission is ErrSubmissionGone", func(t *testing.T) {
		if err := s.CreateHeldSubmission(heldFixture("purged", "f1", 9, now), 9, 6, nil); err != nil {
			t.Fatalf("CreateHeldSubmission: %v", err)
		}
		// Scoped to this subtest's own row. DeleteAllHeld would take h1 with it
		// and break the sibling below — these subtests share one store, so a
		// global mutation here is a landmine for whoever adds the next one.
		if _, err := s.DeleteHeld([]string{"purged"}, AllForms()); err != nil {
			t.Fatalf("DeleteHeld: %v", err)
		}
		if _, err := s.GetHeldSubmission("purged"); !errors.Is(err, ErrSubmissionGone) {
			t.Errorf("err = %v, want ErrSubmissionGone for a purged submission", err)
		}
	})

	t.Run("an accepted submission is not held, but still exists", func(t *testing.T) {
		if _, err := s.RestoreSubmission("h1"); err != nil {
			t.Fatalf("RestoreSubmission: %v", err)
		}
		_, err := s.GetHeldSubmission("h1")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNoRows for a restored submission", err)
		}
		if errors.Is(err, ErrSubmissionGone) {
			t.Errorf("err = %v must not read as gone — the submission is in the inbox", err)
		}
	})
}

// HeldCountForForm drives the "—" vs "0" distinction on the form detail page.
func TestHeldCountForForm(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	for _, id := range []string{"f1", "f2"} {
		if err := s.CreateForm(Form{ID: id, Name: id}); err != nil {
			t.Fatalf("CreateForm: %v", err)
		}
	}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateHeldSubmission(heldFixture(id, "f1", 8, now), 8, 6, nil); err != nil {
			t.Fatalf("CreateHeldSubmission: %v", err)
		}
	}

	n, err := s.HeldCountForForm("f1")
	if err != nil {
		t.Fatalf("HeldCountForForm: %v", err)
	}
	if n != 2 {
		t.Errorf("f1 held = %d, want 2", n)
	}
	// Scoped per form, not global — the stat sits on one form's page.
	if n, err := s.HeldCountForForm("f2"); err != nil || n != 0 {
		t.Errorf("f2 held = %d (%v), want 0", n, err)
	}
}

// acceptedFixture is a submission that passed screening: notified, with a real
// sub-threshold score. That combination is what the MarkSpam tests below are
// about, so it is spelled out rather than defaulted.
func acceptedFixture(id, formID string, score, threshold int) Submission {
	return Submission{
		ID:            id,
		FormID:        formID,
		Data:          map[string]string{"name": "Real Person", "message": "hello"},
		RawData:       `{"name":"Real Person","message":"hello"}`,
		IP:            "198.51.100.4",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		SpamScore:     score,
		HeldThreshold: threshold,
	}
}

// TestMarkSpamMovesAnAcceptedSubmissionIntoQuarantine is the basic shape: the
// row leaves the inbox and joins the review queue, rather than being destroyed.
func TestMarkSpamMovesAnAcceptedSubmissionIntoQuarantine(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	got, err := s.MarkSpam("s1", "admin")
	if err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}
	if !got.IsHeld {
		t.Error("returned submission IsHeld = false, want true")
	}

	// Assert the behaviour, not its absence: gone from the inbox could equally
	// mean deleted, which is the opposite of what this is for.
	inbox, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("inbox still holds %d submissions, want 0", len(inbox))
	}
	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions: %v", err)
	}
	if len(held) != 1 || held[0].ID != "s1" {
		t.Fatalf("quarantine holds %+v, want exactly s1 — it was destroyed, not quarantined", held)
	}
}

// TestMarkSpamPreservesNotified is the one that matters most, and the one an
// "is it held?" assertion would sail straight past.
//
// An accepted submission has already had its email and webhook sent. The restore
// path re-sends whatever the hold withheld, gated on notified — so resetting
// that column here would make every restore deliver a second copy of a
// notification the recipient already has. Asserted on the column, because the
// only other way to see it is to drive the whole handler.
func TestMarkSpamPreservesNotified(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	got, err := s.MarkSpam("s1", "admin")
	if err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}
	if !got.Notified {
		t.Error("returned Notified = false; restoring this would re-send a notification already delivered")
	}

	held, err := s.GetHeldSubmission("s1")
	if err != nil {
		t.Fatalf("GetHeldSubmission: %v", err)
	}
	if !held.Notified {
		t.Error("stored notified = 0; restoring this would re-send a notification already delivered")
	}
}

// TestMarkSpamPreservesTheOriginalScore. The score is the record of what the
// filter actually thought. Overwriting it with the threshold — or with zero —
// would make the quarantine meter state a number this submission never had, which
// is the same defect as the partial reads that shipped once already.
func TestMarkSpamPreservesTheOriginalScore(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	got, err := s.MarkSpam("s1", "admin")
	if err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}
	if got.SpamScore != 3 {
		t.Errorf("SpamScore = %d, want 3 — the score the filter actually gave it", got.SpamScore)
	}
	if got.HeldThreshold != 6 {
		t.Errorf("HeldThreshold = %d, want 6 — the bar it was actually judged against", got.HeldThreshold)
	}
}

// TestMarkSpamRecordsWhoDidIt. A held submission with no signal rows reads as
// "held for no recorded reason" in the quarantine breakdown, and — worse — the
// reader uses the presence of signals to tell a held-then-restored submission
// from one that simply passed.
func TestMarkSpamRecordsWhoDidIt(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if _, err := s.MarkSpam("s1", "baran"); err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}

	signals, err := s.SubmissionSignals("s1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("got %d signals, want exactly 1", len(signals))
	}
	if signals[0].Check != "manual" {
		t.Errorf("Check = %q, want %q", signals[0].Check, "manual")
	}
	if signals[0].Match != "baran" {
		t.Errorf("Match = %q, want the actor %q", signals[0].Match, "baran")
	}
	// Weight zero, because no rule fired and the score is unchanged. A non-zero
	// weight here would make the breakdown sum to more than the stored score.
	if signals[0].Weight != 0 {
		t.Errorf("Weight = %d, want 0 — the score is unchanged, so the breakdown must still sum to it", signals[0].Weight)
	}
}

// TestMarkSpamIsIdempotent. A double-submitted POST or a retried tool call must
// not write a second signal row, and must be distinguishable from a submission
// that no longer exists at all.
func TestMarkSpamIsIdempotent(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if _, err := s.MarkSpam("s1", "admin"); err != nil {
		t.Fatalf("first MarkSpam: %v", err)
	}

	_, err := s.MarkSpam("s1", "admin")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("second MarkSpam error = %v, want ErrNotFound (already held)", err)
	}
	if errors.Is(err, ErrSubmissionGone) {
		t.Error("second MarkSpam reported the submission as gone; it is held, and the two need different messages")
	}

	signals, err := s.SubmissionSignals("s1")
	if err != nil {
		t.Fatalf("SubmissionSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Errorf("got %d signals after two MarkSpam calls, want 1", len(signals))
	}
}

// TestMarkSpamOnAMissingSubmissionSaysGone — the other side of the same
// classification. Reporting "already held" for a row that was deleted sends an
// operator to search a queue it is not in.
func TestMarkSpamOnAMissingSubmissionSaysGone(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")

	_, err := s.MarkSpam("nope", "admin")
	if !errors.Is(err, ErrSubmissionGone) {
		t.Fatalf("MarkSpam on a missing submission error = %v, want ErrSubmissionGone", err)
	}
}

// TestMarkSpamIsRestorable closes the loop the MCP surface depends on: marking
// as spam is reversible, so a mistaken call is recoverable from the admin.
func TestMarkSpamIsRestorable(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if _, err := s.MarkSpam("s1", "admin"); err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}

	restored, err := s.RestoreSubmission("s1")
	if err != nil {
		t.Fatalf("RestoreSubmission: %v", err)
	}
	if restored.IsHeld {
		t.Error("IsHeld = true after restore")
	}
	if !restored.Notified {
		t.Error("Notified = false after restore; the handler would now send a duplicate notification")
	}
	inbox, err := s.ListSubmissions("f1")
	if err != nil {
		t.Fatalf("ListSubmissions: %v", err)
	}
	if len(inbox) != 1 {
		t.Errorf("inbox holds %d submissions after restore, want 1", len(inbox))
	}
}

// TestMarkSpamMarksUnread. A submission pulled out of the inbox for review
// should not come back silently read if it is restored.
func TestMarkSpamMarksUnread(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	if err := s.CreateSubmission(acceptedFixture("s1", "f1", 3, 6)); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if err := s.MarkRead("s1"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	got, err := s.MarkSpam("s1", "admin")
	if err != nil {
		t.Fatalf("MarkSpam: %v", err)
	}
	if got.Read {
		t.Error("Read = true; a quarantined submission has not been read by anyone")
	}
}

// clearFixture seeds two forms with held spam of two ages, an accepted
// submission, and a signal, so each ClearHeld bound has something on both
// sides of it.
func clearFixture(t *testing.T) *Store {
	t.Helper()
	s := mustNew(t)
	seedForm(t, s, "f1")
	seedForm(t, s, "f2")
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-40 * 24 * time.Hour)
	sig := []SpamSignal{{Check: "markup", Field: "message", Match: "[url=", Weight: 6}}
	for _, h := range []struct {
		id, form string
		at       time.Time
	}{
		{"f1-new", "f1", now}, {"f1-old", "f1", old}, {"f2-new", "f2", now}, {"f2-old", "f2", old},
	} {
		if err := s.CreateHeldSubmission(heldFixture(h.id, h.form, 8, h.at), 8, 6, sig); err != nil {
			t.Fatal(err)
		}
	}
	// An accepted submission in the same form: the inbox is never in reach.
	if err := s.CreateSubmission(Submission{ID: "inbox", FormID: "f1", RawData: `{"m":"hi"}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return s
}

func remaining(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	rows, err := s.conn().Query("SELECT id FROM submissions")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

func TestClearHeld(t *testing.T) {
	t.Parallel()

	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	cases := []struct {
		name    string
		filter  HeldFilter
		deleted []string
	}{
		{"every form", HeldFilter{Forms: AllForms()}, []string{"f1-new", "f1-old", "f2-new", "f2-old"}},
		// A form-bound token clears its own forms and nothing else.
		{"one form", HeldFilter{Forms: OnlyForms([]string{"f1"})}, []string{"f1-new", "f1-old"}},
		{"older than a cutoff", HeldFilter{Forms: AllForms(), HeldBefore: cutoff}, []string{"f1-old", "f2-old"}},
		{"one form and older", HeldFilter{Forms: OnlyForms([]string{"f2"}), HeldBefore: cutoff}, []string{"f2-old"}},
		// A scope naming no forms reaches nothing — never everything.
		{"no forms", HeldFilter{Forms: OnlyForms(nil)}, nil},
		{"zero scope", HeldFilter{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := clearFixture(t)

			// The count guard must not be what makes this pass: a filter that
			// reached more than it should would be refused, and a refusal looks
			// the same as "nothing to delete". So the filter's reach is counted
			// first, on its own, and the clear must then succeed outright.
			reach, err := s.CountHeld(tc.filter)
			if err != nil {
				t.Fatalf("CountHeld: %v", err)
			}
			if reach != len(tc.deleted) {
				t.Fatalf("the filter reaches %d held submissions, want %d", reach, len(tc.deleted))
			}
			n, err := s.ClearHeld(tc.filter, len(tc.deleted))
			if err != nil {
				t.Fatalf("ClearHeld: %v", err)
			}
			if n != len(tc.deleted) {
				t.Errorf("deleted %d, want %d", n, len(tc.deleted))
			}
			left := remaining(t, s)
			for _, id := range tc.deleted {
				if left[id] {
					t.Errorf("%s survived", id)
				}
			}
			if !left["inbox"] {
				t.Fatal("an accepted submission was deleted")
			}
			if want := 5 - len(tc.deleted); len(left) != want {
				t.Errorf("%d rows left, want %d: %v", len(left), want, left)
			}
		})
	}
}

// The guard. The count and the delete run in one transaction, so spam that
// arrived after the client looked cannot be deleted unseen: a count that does
// not match deletes nothing and says what the real count is.
func TestClearHeldRefusesAMismatchedCount(t *testing.T) {
	t.Parallel()
	s := clearFixture(t)

	for _, expected := range []int{3, 5, 0} {
		n, err := s.ClearHeld(HeldFilter{Forms: AllForms()}, expected)
		var mm *CountMismatchError
		if !errors.As(err, &mm) {
			t.Fatalf("expected %d: err = %v, want CountMismatchError", expected, err)
		}
		if mm.Actual != 4 || n != 0 {
			t.Errorf("expected %d: actual = %d, deleted = %d; want 4 and 0", expected, mm.Actual, n)
		}
	}
	if len(remaining(t, s)) != 5 {
		t.Error("a refused clear deleted something")
	}
}

func TestClearHeldTakesTheSignalsWithIt(t *testing.T) {
	t.Parallel()
	s := clearFixture(t)
	if _, err := s.ClearHeld(HeldFilter{Forms: AllForms()}, 4); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.conn().QueryRow("SELECT COUNT(*) FROM spam_signals").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d signals outlived their submissions", n)
	}
}

// A held message's age is counted from when it entered quarantine, not from
// when it was submitted — one definition, read by the retention sweep and by
// ClearHeld alike through HeldFilter.HeldBefore.
//
// If it were the submission date, the 30-day review promise would be false for
// exactly the messages a person had just looked at: an inbox message older than
// 30 days, marked as spam, would be deleted at the next sweep.
func TestHeldAgeIsCountedFromEnteringQuarantine(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	// seed builds one held message: submitted at `submitted`, and in
	// quarantine since `held` (zero means marked as spam just now).
	seed := func(t *testing.T, submitted, held time.Time) *Store {
		t.Helper()
		s := mustNew(t)
		seedForm(t, s, "f1")
		if err := s.CreateSubmission(Submission{ID: "m", FormID: "f1", RawData: `{"m":"hi"}`, CreatedAt: submitted}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkSpam("m", "admin"); err != nil {
			t.Fatal(err)
		}
		if !held.IsZero() {
			if _, err := s.conn().Exec("UPDATE submissions SET held_at = ? WHERE id = 'm'", sqliteTimestamp(held)); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	cases := []struct {
		name            string
		submitted, held time.Time
		swept           bool
	}{
		// The bug: 40 days old, but in quarantine for seconds.
		{"old message marked as spam today", now.Add(-40 * 24 * time.Hour), time.Time{}, false},
		{"marked as spam 29 days ago", now.Add(-50 * 24 * time.Hour), now.Add(-29 * 24 * time.Hour), false},
		{"marked as spam 31 days ago", now.Add(-50 * 24 * time.Hour), now.Add(-31 * 24 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := 0
			if tc.swept {
				want = 1
			}

			sweep := seed(t, tc.submitted, tc.held)
			if n, err := sweep.PurgeHeldOlderThan(cutoff); err != nil || n != want {
				t.Errorf("the sweep deleted %d (%v), want %d", n, err, want)
			}
			clear := seed(t, tc.submitted, tc.held)
			if n, err := clear.ClearHeld(HeldFilter{Forms: AllForms(), HeldBefore: cutoff}, want); err != nil || n != want {
				t.Errorf("ClearHeld deleted %d (%v), want %d", n, err, want)
			}
		})
	}
}

// A held row with no held_at recorded falls back to its submission date. An
// empty held_at compares as earlier than every cutoff, so without the fallback
// such a row would be swept the moment the sweep ran, however new it was.
func TestHeldAgeFallsBackToSubmissionWhenHeldAtIsMissing(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	for _, tc := range []struct {
		name      string
		submitted time.Time
		swept     int
	}{
		{"recent", now.Add(-time.Hour), 0},
		{"old", now.Add(-40 * 24 * time.Hour), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustNew(t)
			seedForm(t, s, "f1")
			if err := s.CreateHeldSubmission(heldFixture("h", "f1", 8, tc.submitted), 8, 6, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.conn().Exec("UPDATE submissions SET held_at = '' WHERE id = 'h'"); err != nil {
				t.Fatal(err)
			}
			if n, err := s.CountHeld(HeldFilter{Forms: AllForms(), HeldBefore: cutoff}); err != nil || n != tc.swept {
				t.Errorf("the filter counts %d (%v), want %d", n, err, tc.swept)
			}
			if n, err := s.PurgeHeldOlderThan(cutoff); err != nil || n != tc.swept {
				t.Errorf("the sweep deleted %d (%v), want %d", n, err, tc.swept)
			}
		})
	}
}

// The unguarded delete, which the admin's empty-quarantine and the sweep use:
// a filter with no scope must reach nothing here too, where no count guard
// would stop it.
func TestDeleteHeldWhereWithNoScopeDeletesNothing(t *testing.T) {
	t.Parallel()
	s := clearFixture(t)
	for _, f := range []HeldFilter{{}, {Forms: OnlyForms(nil)}} {
		n, err := deleteHeldWhere(s.conn(), f)
		if err != nil || n != 0 {
			t.Errorf("deleted %d (%v), want 0", n, err)
		}
	}
	if left := remaining(t, s); len(left) != 5 {
		t.Errorf("%d rows left, want all 5", len(left))
	}
}

// "Strictly before": a message held at exactly the cutoff is not yet past it.
func TestHeldBeforeIsStrict(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	cutoff := time.Now().UTC().Truncate(time.Second).Add(-30 * 24 * time.Hour)
	if err := s.CreateHeldSubmission(heldFixture("edge", "f1", 8, cutoff), 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateHeldSubmission(heldFixture("past", "f1", 8, cutoff.Add(-time.Second)), 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeHeldOlderThan(cutoff)
	if err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want only the one past the cutoff", n, err)
	}
	if _, err := s.GetHeldSubmission("edge"); err != nil {
		t.Errorf("the message held exactly at the cutoff was swept: %v", err)
	}
}

// Restoring a message and marking it as spam again starts its time in
// quarantine over: MarkSpam overwrites held_at.
func TestReMarkingAsSpamRestartsTheClock(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	now := time.Now().UTC()
	if err := s.CreateHeldSubmission(heldFixture("m", "f1", 8, now.Add(-40*24*time.Hour)), 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreSubmission("m"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSpam("m", "admin"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeHeldOlderThan(now.Add(-30 * 24 * time.Hour)); err != nil || n != 0 {
		t.Errorf("the sweep deleted %d (%v); a message held again today is not 30 days in quarantine", n, err)
	}
}

// Every way the store puts a submission in quarantine records when. The
// created_at fallback in HeldFilter is for rows that predate held_at; a writer
// that forgot to set it would silently age its rows by their submission date.
func TestEveryHoldRecordsWhen(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.CreateHeldSubmission(heldFixture("arrived", "f1", 8, now.Add(-2*time.Hour)), 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSubmission(Submission{ID: "marked", FormID: "f1", RawData: `{"m":"hi"}`, CreatedAt: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSpam("marked", "admin"); err != nil {
		t.Fatal(err)
	}

	arrived, err := s.GetHeldSubmission("arrived")
	if err != nil {
		t.Fatal(err)
	}
	// Held on arrival: in quarantine since it was submitted.
	if !arrived.HeldAt.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("arrived.HeldAt = %v, want its submission time", arrived.HeldAt)
	}
	marked, err := s.GetHeldSubmission("marked")
	if err != nil {
		t.Fatal(err)
	}
	if marked.HeldAt.IsZero() || marked.HeldAt.Before(now.Add(-time.Minute)) {
		t.Errorf("marked.HeldAt = %v, want about now", marked.HeldAt)
	}
	if !marked.QuarantinedSince().Equal(marked.HeldAt) {
		t.Errorf("QuarantinedSince = %v, want HeldAt", marked.QuarantinedSince())
	}

	// Restored: no longer held, and the time is cleared with it.
	back, err := s.RestoreSubmission("marked")
	if err != nil {
		t.Fatal(err)
	}
	if !back.HeldAt.IsZero() {
		t.Errorf("a restored submission still has HeldAt = %v", back.HeldAt)
	}
}

// QuarantinedSince is the Go side of the one definition: held_at, or the submission
// time when none was recorded. It must agree with the SQL in HeldFilter.
func TestQuarantinedSinceFallsBackToCreatedAt(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	held := created.Add(72 * time.Hour)
	if got := (Submission{CreatedAt: created, HeldAt: held}).QuarantinedSince(); !got.Equal(held) {
		t.Errorf("QuarantinedSince = %v, want HeldAt", got)
	}
	if got := (Submission{CreatedAt: created}).QuarantinedSince(); !got.Equal(created) {
		t.Errorf("QuarantinedSince with no HeldAt = %v, want CreatedAt", got)
	}
}

// The listing and the count take the same filter the clear does, so what a
// client lists and counts is exactly what a clear with that filter deletes.
func TestHeldSubmissionsWhereAndCountHeldMatchTheClear(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for _, f := range []HeldFilter{
		{Forms: AllForms()},
		{Forms: OnlyForms([]string{"f1"})},
		{Forms: AllForms(), HeldBefore: cutoff},
		{Forms: OnlyForms([]string{"f2"}), HeldBefore: cutoff},
		{},
	} {
		s := clearFixture(t)
		listed, err := s.HeldSubmissionsWhere(f, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		count, err := s.CountHeld(f)
		if err != nil {
			t.Fatal(err)
		}
		if count != len(listed) {
			t.Errorf("filter %+v: CountHeld = %d but %d were listed", f, count, len(listed))
		}
		deleted, err := s.ClearHeld(f, count)
		if err != nil {
			t.Fatalf("filter %+v: ClearHeld: %v", f, err)
		}
		if deleted != count {
			t.Errorf("filter %+v: counted %d, deleted %d", f, count, deleted)
		}
		left := remaining(t, s)
		for _, sub := range listed {
			if left[sub.ID] {
				t.Errorf("filter %+v: %s was listed but not deleted", f, sub.ID)
			}
		}
	}
}

// The list is ordered by time in quarantine, most recently held first, so the
// last row is the one closest to being swept.
func TestHeldSubmissionsAreOrderedByTimeInQuarantine(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	seedForm(t, s, "f1")
	now := time.Now().UTC()
	// Submitted long ago, but marked as spam just now: newest in quarantine.
	if err := s.CreateSubmission(Submission{ID: "marked-now", FormID: "f1", RawData: `{"m":"hi"}`, CreatedAt: now.Add(-40 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSpam("marked-now", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateHeldSubmission(heldFixture("held-10d", "f1", 8, now.Add(-10*24*time.Hour)), 8, 6, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateHeldSubmission(heldFixture("held-20d", "f1", 8, now.Add(-20*24*time.Hour)), 8, 6, nil); err != nil {
		t.Fatal(err)
	}

	held, err := s.HeldSubmissions(AllForms(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, h := range held {
		ids = append(ids, h.ID)
	}
	want := []string{"marked-now", "held-10d", "held-20d"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestCountMismatchErrorNamesBothNumbers(t *testing.T) {
	t.Parallel()
	msg := (&CountMismatchError{Expected: 3, Actual: 7}).Error()
	if !strings.Contains(msg, "7") || !strings.Contains(msg, "3") || !strings.Contains(msg, "nothing was deleted") {
		t.Errorf("message = %q", msg)
	}
}

// ClearHeld's count and delete are one atomic step with respect to every other
// writer. Shown on a real file database, under sustained pressure: writers keep
// holding new spam while clearers keep counting and clearing.
//
// A single race does not show this. Released once, the first goroutine runs
// both of its statements on the warm connection before the others have opened
// theirs, and the calls never overlap: an earlier version of this test passed
// forty times in a row against a ClearHeld with no transaction at all. So the
// overlap is manufactured by volume, and three things are held to account:
//   - a clear that succeeds deleted exactly the number it was told to expect
//   - a clear that is refused deleted nothing
//   - at the end, every row written was deleted by a clear that counted it
//
// Without the transaction a writer lands between the count and the delete, and
// the delete takes rows nobody counted.
//
// SQLITE_BUSY is not one of the failures. It means the write lock was not free
// within busy_timeout, which this much contention can produce on a slow
// machine: CI, under the race detector, starved a clearer for the full five
// seconds. A call refused that way never began, so it counted and deleted
// nothing; it is tried again. Treating it as a failure made this a test of
// lock fairness on whatever ran it.
func TestClearHeldIsAtomicUnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	s := fileStore(t)
	seedForm(t, s, "f1")
	now := time.Now().UTC()
	all := HeldFilter{Forms: AllForms()}

	// A bounded workload. Unbounded writers starve everything else of the write
	// lock and turn the test into one about lock fairness.
	const writers, perWriter, clearers = 2, 100, 2

	var written, deleted, succeeded, refused, busied atomic.Int64
	// Everyone starts together, so the writers cannot finish before a clearer
	// has had its first look.
	start := make(chan struct{})
	var writing sync.WaitGroup
	for w := 0; w < writers; w++ {
		writing.Add(1)
		go func(w int) {
			defer writing.Done()
			<-start
			for i := 0; i < perWriter; {
				err := s.CreateHeldSubmission(heldFixture(fmt.Sprintf("w%d-%d", w, i), "f1", 8, now), 8, 6, nil)
				if isBusy(err) {
					// Not written: the same row is tried again.
					busied.Add(1)
					continue
				}
				if err != nil {
					t.Errorf("CreateHeldSubmission: %v", err)
					return
				}
				written.Add(1)
				i++
			}
		}(w)
	}
	writersDone := make(chan struct{})
	go func() {
		writing.Wait()
		close(writersDone)
	}()

	var clearing sync.WaitGroup
	for c := 0; c < clearers; c++ {
		clearing.Add(1)
		go func() {
			defer clearing.Done()
			<-start
			for {
				// Sampled before the count, not after. Read the other way
				// round, a clearer can count zero, have the last writer commit
				// and finish behind it, and then leave a row nobody clears.
				// Zero counted after the writers are known to be done is final.
				done := false
				select {
				case <-writersDone:
					done = true
				default:
				}
				want, err := s.CountHeld(all)
				if err != nil {
					t.Errorf("CountHeld: %v", err)
					return
				}
				if want == 0 {
					// An empty queue is not an attempt: nothing to clear yet,
					// or nothing left.
					if done {
						return
					}
					runtime.Gosched()
					continue
				}
				n, err := s.ClearHeld(all, want)
				var mismatch *CountMismatchError
				switch {
				case err == nil:
					if n != want {
						t.Errorf("a clear expecting %d deleted %d", want, n)
					}
					deleted.Add(int64(n))
					succeeded.Add(1)
				case errors.As(err, &mismatch):
					if n != 0 {
						t.Errorf("a refused clear reported %d deleted", n)
					}
					refused.Add(1)
				case isBusy(err):
					// The clear never began; nothing was counted or deleted.
					if n != 0 {
						t.Errorf("a clear that could not get the lock reported %d deleted", n)
					}
					busied.Add(1)
				default:
					// Notably "counted N but deleted M": the count and the
					// delete were not one step.
					t.Errorf("ClearHeld: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	clearing.Wait()
	// A clearer that hit an error returns early. Wait for the writers as well,
	// so none of them is still running, or reporting, when the test ends and
	// the store under them is closed.
	writing.Wait()

	left, err := s.CountHeld(all)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d still held after the clearers finished", left)
	}
	if want := int64(writers * perWriter); written.Load() != want || deleted.Load() != want {
		t.Errorf("wrote %d and deleted %d, want %d of each: a clear deleted rows it did not count, or missed some",
			written.Load(), deleted.Load(), want)
	}
	if succeeded.Load() == 0 {
		t.Fatal("no clear succeeded, so no delete was ever checked")
	}
	// A refusal is the evidence that writers and clearers really overlapped: a
	// writer got between a clearer's look and its clear. Without one, this run
	// could not have caught a clear that is not atomic.
	//
	// That needs two things running at once. On a single core the writers get
	// through their rows between a clearer's turns and nothing overlaps, which
	// is the scheduler and not a fault; the run is then reported for what it
	// was rather than failed. With cores to spare, no overlap means the test
	// has stopped doing its job and should say so.
	if refused.Load() == 0 {
		if runtime.GOMAXPROCS(0) > 1 {
			t.Fatalf("%d clears succeeded and none was refused: writers and clearers never overlapped, "+
				"so this run proved nothing", succeeded.Load())
		}
		t.Log("single core: writers and clearers did not overlap, so this run could not have caught a non-atomic clear")
	}
	t.Logf("%d clears succeeded, %d refused, %d calls waited out the lock and were retried",
		succeeded.Load(), refused.Load(), busied.Load())
}

// isBusy reports SQLITE_BUSY: the write lock was not free within busy_timeout.
// By the driver's error type and code rather than its text, through whatever
// wrapping the store added.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == 5 // SQLITE_BUSY
}
