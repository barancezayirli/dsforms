package store

import (
	"fmt"
	"log"
	"strings"
	"unicode"
)

// Full-text search over submission payloads.
//
// FTS5 is an external-content index over submissions.data, kept in sync by
// triggers rather than by application code — anything that writes a submission
// (the submit handler, a restore, a backup import) gets indexed without having
// to remember to. modernc.org/sqlite ships FTS5 under CGO_ENABLED=0, so this
// costs no build-tag gymnastics and no cgo.
const searchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS submissions_fts USING fts5(
    data,
    content='submissions',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS submissions_fts_ai AFTER INSERT ON submissions BEGIN
    INSERT INTO submissions_fts(rowid, data) VALUES (new.rowid, new.data);
END;

CREATE TRIGGER IF NOT EXISTS submissions_fts_ad AFTER DELETE ON submissions BEGIN
    INSERT INTO submissions_fts(submissions_fts, rowid, data) VALUES ('delete', old.rowid, old.data);
END;

CREATE TRIGGER IF NOT EXISTS submissions_fts_au AFTER UPDATE ON submissions BEGIN
    INSERT INTO submissions_fts(submissions_fts, rowid, data) VALUES ('delete', old.rowid, old.data);
    INSERT INTO submissions_fts(rowid, data) VALUES (new.rowid, new.data);
END;
`

// SearchResult is one hit, with the form it belongs to.
type SearchResult struct {
	Submission
	FormName string
}

// syncSearchIndex builds the index when it is out of step with the table.
//
// Needed because the triggers only cover rows written after they exist: a
// database upgraded from before this feature has submissions and an empty
// index, and a restored backup arrives with whatever index that file carried.
// Comparing counts keeps this a no-op on every normal startup rather than an
// O(n) rebuild on each boot.
//
// The count comes from the submissions_fts_docsize shadow table, not from
// `SELECT COUNT(*) FROM submissions_fts`. On an external-content FTS5 table the
// latter counts the *content* table — submissions — so it reports the same
// number whether the index holds every document or none, and a staleness check
// built on it could never fire. docsize holds one row per actually-indexed
// document and reads 0 on an empty index.
func syncSearchIndex(db execQuerier) error {
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&rows); err != nil {
		return fmt.Errorf("search index: count submissions: %w", err)
	}

	var indexed int
	if err := db.QueryRow("SELECT COUNT(*) FROM submissions_fts_docsize").Scan(&indexed); err != nil {
		// Log before rebuilding. The rebuild rewrites the very shadow tables the
		// failed read came from, so a genuine signal — a malformed image, an I/O
		// error — would otherwise be erased by the recovery and leave no trace
		// that anything was ever wrong.
		log.Printf("search index: docsize unreadable (%v); rebuilding", err)
		// The shadow table is an FTS5 implementation detail. If a future
		// version renames it, rebuild unconditionally rather than quietly
		// serving an empty index — a slower startup beats a search that
		// silently finds nothing.
		if _, rebuildErr := db.Exec("INSERT INTO submissions_fts(submissions_fts) VALUES ('rebuild')"); rebuildErr != nil {
			return fmt.Errorf("search index: rebuild after count failed (%v): %w", err, rebuildErr)
		}
		return nil
	}

	if rows == indexed {
		return nil
	}
	if _, err := db.Exec("INSERT INTO submissions_fts(submissions_fts) VALUES ('rebuild')"); err != nil {
		return fmt.Errorf("search index: rebuild: %w", err)
	}
	return nil
}

// SearchSubmissions returns accepted submissions matching a query, best first.
func (s *Store) SearchSubmissions(query string, limit int) ([]SearchResult, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}

	rows, err := s.db.Query(`
		SELECT `+heldColumnsWithFormName("s")+`
		FROM submissions_fts x
		JOIN submissions s ON s.rowid = x.rowid
		JOIN forms f ON f.id = s.form_id
		WHERE submissions_fts MATCH ? AND s.is_held = 0
		ORDER BY rank
		LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("search submissions: %w", err)
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		sub, formName, err := scanHeldWithFormName(rows)
		if err != nil {
			return nil, fmt.Errorf("search submissions: %w", err)
		}
		res := SearchResult{Submission: sub, FormName: formName}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search submissions: %w", err)
	}
	return out, nil
}

// ftsQuery turns arbitrary user input into a safe FTS5 MATCH expression.
//
// FTS5 has its own query syntax — bare AND/OR/NOT/NEAR are operators, and a
// stray quote, colon, caret or parenthesis is a syntax error that surfaces to
// the user as a 500 rather than as "no results". Someone searching for
// `O'Brien` or `budget?` is not writing a query language.
//
// Each whitespace-separated token is therefore wrapped as a quoted phrase
// (with embedded quotes doubled, which is how FTS5 escapes them) and the tokens
// are ANDed. A trailing prefix match is deliberately not added: `smith*` would
// surprise anyone searching for an exact address.
func ftsQuery(input string) string {
	var tokens []string
	for _, raw := range strings.FieldsFunc(input, func(r rune) bool {
		return unicode.IsSpace(r)
	}) {
		// Strip characters that carry meaning to FTS5 but not to the person
		// typing, so a token never collapses to something unparseable.
		cleaned := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) ||
				r == '@' || r == '.' || r == '-' || r == '_' || r == '\'' {
				return r
			}
			return ' '
		}, raw)
		for _, tok := range strings.Fields(cleaned) {
			tokens = append(tokens, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
		}
	}
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " AND ")
}
