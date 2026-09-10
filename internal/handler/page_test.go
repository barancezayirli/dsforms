package handler

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDatabaseSizeCountsTheWriteAheadLog is the regression test for a figure
// that told an operator their data was gone.
//
// dbStatus stat'ed only the main database file. This instance runs in WAL mode,
// where committed data lives in the -wal file until a checkpoint folds it back
// in. Measured on a running instance after 40 submissions: main file 4,096
// bytes, WAL 2,084,752. The card read "4.0 KB". Someone opening the Backups page
// to ask "is my data actually there" was shown a number that says no.
//
// The sizes here are chosen so the bug and the fix cannot report the same
// string: the main file alone rounds to 4.0 KB, the WAL alone to 160 KB, and
// the two together to a third value. A test using a tiny WAL would pass on the
// unfixed code through rounding.
func TestDatabaseSizeCountsTheWriteAheadLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dsforms.db")
	writeSized(t, dbPath, 4*1024)
	writeSized(t, dbPath+"-wal", 160*1024)
	// Present, and deliberately not counted. Without this file in the fixture,
	// widening the sum to "every SQLite file" passes every test here — the
	// exclusion would be a decision defended only by a comment.
	writeSized(t, dbPath+"-shm", 32*1024)

	b := &Base{DBPath: dbPath}
	got := b.dbStatus().Size

	if got == "4.0 KB" {
		t.Fatalf("Size = %q — the write-ahead log is not counted.\n"+
			"In WAL mode the main file stays small between checkpoints, so this is "+
			"the number that told an operator their database was empty while it held "+
			"164 KB.", got)
	}
	if want := "164.0 KB"; got != want {
		t.Errorf("Size = %q, want %q (4 KB database + 160 KB write-ahead log)", got, want)
	}
}

// TestDatabaseSizeWithNoWriteAheadLog covers the other half: a database not in
// WAL mode, or one freshly checkpointed, has no -wal file beside it. A missing
// WAL must contribute nothing rather than making the whole figure disappear —
// dbStatus reports no size at all when it cannot stat, so an unguarded second
// stat would blank the card for every non-WAL instance.
func TestDatabaseSizeWithNoWriteAheadLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dsforms.db")
	writeSized(t, dbPath, 2*1024)

	if got, want := (&Base{DBPath: dbPath}).dbStatus().Size, "2.0 KB"; got != want {
		t.Errorf("Size = %q, want %q — a missing -wal must count as zero, not "+
			"suppress the figure", got, want)
	}
}

// TestDatabaseSizeSaysNothingWhenItCannotStat pins the existing behaviour that
// the fix must not break: an unstattable path reports an empty string, which the
// sidebar renders as nothing rather than as "0 B", which would be a claim.
func TestDatabaseSizeSaysNothingWhenItCannotStat(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, path string }{
		{"no path configured", ""},
		{"path does not exist", filepath.Join(t.TempDir(), "absent.db")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (&Base{DBPath: tc.path}).dbStatus().Size; got != "" {
				t.Errorf("Size = %q, want \"\" — reporting a number for a database "+
					"we could not measure is worse than reporting none", got)
			}
		})
	}
}

// TestHumanBytes covers the formatter, which had no test at all.
func TestHumanBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes stay bytes", 512, "512 B"},
		{"one below the first unit", 1023, "1023 B"},
		{"exactly one kilobyte", 1024, "1.0 KB"},
		{"rounds to one decimal", 1536, "1.5 KB"},
		// Deliberate, not an oversight: the loop only advances a unit once the
		// divided size reaches the next one, so the last KB reads as 1024.0 KB
		// rather than 1.0 MB. Pinned so a future reader can tell which it is.
		{"one below a megabyte", 1024*1024 - 1, "1024.0 KB"},
		{"exactly one megabyte", 1024 * 1024, "1.0 MB"},
		{"gigabytes", 3 * 1024 * 1024 * 1024, "3.0 GB"},
		// The top two rows exist because the suffix table was indexed without a
		// bound: terabytes is the last name it has, and anything past it used to
		// panic with "index out of range [4]" rather than degrade.
		{"terabytes", 1 << 40, "1.0 TB"},
		{"past the largest unit it can name", 1 << 50, "1024.0 TB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := humanBytes(tc.in); got != tc.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// writeSized creates a file of exactly n bytes.
func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestBackupsPageRendersTheMeasuredSize is the altitude the other tests in this
// file do not reach.
//
// They all stop at dbStatus. Replace the `DB: b.dbStatus()` line in Shell with a
// hardcoded DBStatus and every one of them still passes, because none renders
// anything — the fix would be dead code and the Backups page, which is where the
// bug was reported, would go on printing whatever the literal said.
//
// So this runs the real template through the real Shell against real files on
// disk, and reads the number back out of the HTML. It is the same lesson as the
// password hint one directory over: a structural test proves the function is
// right, and only a rendered one proves the operator sees it.
func TestBackupsPageRendersTheMeasuredSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dsforms.db")
	writeSized(t, dbPath, 4*1024)
	writeSized(t, dbPath+"-wal", 160*1024)

	base := Base{DBPath: dbPath, Journal: "WAL", SecretKey: "test-secret"}
	req := httptest.NewRequest("GET", "/admin/backups", nil)
	w := httptest.NewRecorder()
	data := backupPageData{PageData: base.Shell(w, req, "Backups", "backups")}

	var buf bytes.Buffer
	if err := realTemplates(t)["backups.html"].ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("execute backups.html: %v", err)
	}
	body := buf.String()

	const want = "164.0 KB"
	if !strings.Contains(body, want) {
		t.Errorf("the Backups page does not show %q.\nThe size is computed but never "+
			"reaches the page the bug was reported on.", want)
	}
	// The old, wrong number must not appear on its own either — the sidebar
	// renders DB.Size a second time from the same struct.
	//
	// Matched with a boundary rather than strings.Contains, because "164.0 KB"
	// contains "4.0 KB": the naive version of this check failed against a page
	// that was entirely correct.
	if regexp.MustCompile(`(^|[^0-9.])4\.0 KB`).MatchString(body) {
		t.Errorf("the page shows \"4.0 KB\" on its own; the write-ahead log is " +
			"missing from at least one of the two places DB.Size is rendered")
	}
	// Both render sites, not just the card: templates/base.html:85 shows it in
	// the sidebar footer and templates/backups.html:20 in the stat block.
	if n := strings.Count(body, want); n < 2 {
		t.Errorf("size appears %d time(s), want both the sidebar footer and the "+
			"Backups card — one of the two render sites has dropped it", n)
	}
}

// TestSubmissionPanelStatesAreEachTrue renders the spam-check panel in every
// state it can be in and checks that none of them says something false.
//
// The panel had two branches and five meanings. "Was held, then restored" showed
// when signal rows existed; everything else fell to "Spam check passed" beside
// "score 0" and "No link markup, keywords, injection probes or repeat-IP
// activity" — which is simply false about a submission that scored 3 and was
// delivered anyway.
//
// Adding a third branch introduced a worse one, which review caught: a restored
// submission whose breakdown could not be READ has no signals, so it fell
// through to "Scored below the threshold, so it was delivered" — printed beside
// a notice saying the submission had been held, above a score above the
// threshold. Three contradictory claims in one panel. AGENT.md §4: an inaccurate
// message is worse than a generic one.
//
// Asserted against rendered output rather than struct state, because the
// branches in this session repeatedly shipped fixes that were right in the
// function and invisible on the page.
func TestSubmissionPanelStatesAreEachTrue(t *testing.T) {
	t.Parallel()

	base := populatedPageData()["submission_detail.html"]
	data, ok := base.(submissionDetailData)
	if !ok {
		t.Fatalf("fixture is %T, not submissionDetailData", base)
	}

	render := func(t *testing.T, d submissionDetailData) string {
		t.Helper()
		var buf bytes.Buffer
		if err := realTemplates(t)["submission_detail.html"].ExecuteTemplate(&buf, "base", d); err != nil {
			t.Fatalf("execute submission_detail.html: %v", err)
		}
		return buf.String()
	}

	// The claims the panel can make. Every state must produce exactly the ones
	// that are true of it — listing the forbidden ones is the half that catches a
	// branch falling through to the wrong text.
	const (
		restored  = "Was held, then restored"
		delivered = "Scored below the threshold"
		clean     = "Nothing scored against this submission"
		legacy    = "Received before scores were recorded"
		unusable  = "Spam check unavailable"
	)

	cases := []struct {
		name       string
		mutate     func(*submissionDetailData)
		want       []string
		mustNotSay []string
	}{
		{
			name:       "held, then restored",
			mutate:     func(d *submissionDetailData) {}, // the fixture carries signals
			want:       []string{restored, "score 11"},
			mustNotSay: []string{delivered, clean, legacy, unusable},
		},
		{
			name: "delivered, but it scored something",
			mutate: func(d *submissionDetailData) {
				d.Signals = nil
				d.Submission.SpamScore = 3
				d.Submission.HeldThreshold = 6
			},
			want:       []string{delivered, "score 3 / 6"},
			mustNotSay: []string{restored, clean, legacy, unusable},
		},
		{
			name: "genuinely clean",
			mutate: func(d *submissionDetailData) {
				d.Signals = nil
				d.Submission.SpamScore = 0
				// A real bar, not zero: every accepted submission is judged
				// against a clamped threshold, so held_threshold = 0 is a row the
				// fixed code cannot write. A fixture in a state production cannot
				// reach lets a one-word slip pass — branching on HeldThreshold
				// rather than SpamScore would put "Scored below the threshold"
				// under every clean submission.
				d.Submission.HeldThreshold = 6
			},
			want:       []string{clean, "score 0 / 6"},
			mustNotSay: []string{restored, delivered, legacy, unusable},
		},
		{
			name: "received before scores were recorded",
			mutate: func(d *submissionDetailData) {
				d.Signals = nil
				d.Submission.SpamScore = 0
				d.Submission.HeldThreshold = 0
			},
			// The rows already in every existing database. The fix is not
			// retroactive — the score was never kept, so it cannot be recovered —
			// and "score 0" plus "nothing was detected" would be the same lie this
			// change removes, told about the rows that still have it.
			want:       []string{legacy, "score not recorded"},
			mustNotSay: []string{restored, delivered, clean, unusable, "score 0"},
		},
		{
			name: "the breakdown could not be read",
			mutate: func(d *submissionDetailData) {
				d.Signals = nil
				d.SignalsFailed = true
				d.Submission.SpamScore = 11
				d.Submission.HeldThreshold = 6
			},
			want: []string{unusable, "could not be read"},
			// Every other sentence would be a claim about evidence that failed to
			// load. "Scored below the threshold" was printed here, for a score of
			// 11 against a threshold of 6.
			mustNotSay: []string{restored, delivered, clean, legacy},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := data
			tc.mutate(&d)
			body := render(t, d)

			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("panel does not say %q, which is true of this submission", want)
				}
			}
			for _, forbidden := range tc.mustNotSay {
				if strings.Contains(body, forbidden) {
					t.Errorf("panel says %q, which is not true of this submission.\n"+
						"An inaccurate message is worse than a generic one.", forbidden)
				}
			}
		})
	}
}
