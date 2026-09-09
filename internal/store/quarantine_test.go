package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"
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
		{Rule: "markup", Field: "message", Match: "[url=", Weight: 6},
		{Rule: "extra_links", Field: "", Match: "", Weight: 2},
	}
	if err := s.CreateHeldSubmission(heldFixture("s1", "f1", 8, now), 8, 6, signals); err != nil {
		t.Fatalf("CreateHeldSubmission: %v", err)
	}

	held, err := s.HeldSubmissions(10, 0)
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
	signals := []SpamSignal{{Rule: "keyword", Field: "message", Match: "casino", Weight: 5}}
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

	held, err := s.HeldSubmissions(10, 0)
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
			[]SpamSignal{{Rule: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
			t.Fatalf("CreateHeldSubmission(%s): %v", id, err)
		}
	}

	if err := s.DeleteHeld([]string{"a", "b"}); err != nil {
		t.Fatalf("DeleteHeld: %v", err)
	}

	held, err := s.HeldSubmissions(10, 0)
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
	if err := s.DeleteHeld([]string{"clean"}); err != nil {
		t.Fatalf("DeleteHeld: %v", err)
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
	if err := s.DeleteHeld(nil); err != nil {
		t.Errorf("DeleteHeld(nil) = %v, want nil", err)
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

	held, err := s.HeldSubmissions(10, 0)
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

	nav, err := s.NavCounts()
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
		[]SpamSignal{{Rule: "markup", Field: "message", Match: "[url=", Weight: 6}}); err != nil {
		t.Fatalf("CreateHeldSubmission after upgrade: %v", err)
	}
	held, err := s.HeldSubmissions(10, 0)
	if err != nil {
		t.Fatalf("HeldSubmissions after upgrade: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held after upgrade, want 1", len(held))
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
		{Rule: "markup", Field: "message", Match: "[url=", Weight: 6},
		{Rule: "keyword", Field: "message", Match: "backlinks", Weight: 5},
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
