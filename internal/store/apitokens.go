package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// APITokenPrefix marks a dsforms API token on sight.
//
// It is not a security control — the entropy is entirely in the part after it.
// It exists so a token pasted into a config file, a log, or a commit is
// recognisable as a credential by a human and by a secret scanner, which is how
// a leaked one gets revoked before it is used.
const APITokenPrefix = "dsf_"

// apiTokenBytes is the size of the random part, before hex encoding.
const apiTokenBytes = 32

// APIToken is one API credential, as read back. It deliberately has no field for
// the token itself: the raw value exists only in the return of CreateAPIToken
// and in the client that stored it.
type APIToken struct {
	ID     string
	UserID string
	Name   string

	// Scopes is what this token may do. Stored as one comma-separated column and
	// returned already split. The store does not validate the values — it does
	// not own the vocabulary — so an unrecognised scope arrives here intact and
	// is refused at the point of use. That direction matters: a scope this layer
	// does not understand must grant nothing, never everything.
	Scopes []string

	CreatedAt time.Time

	// LastUsedAt is the zero time until the token is first presented. It is what
	// lets an operator tell a token they forgot about from one in daily use.
	LastUsedAt time.Time

	// ExpiresAt is the zero time for a token that never expires.
	ExpiresAt time.Time
}

// Expired reports whether the token's expiry has passed at the given time. A
// token with no expiry is never expired.
//
// A method rather than a comparison at each call site, because "zero means
// never" is exactly the kind of convention that gets forgotten by the third
// caller — and forgetting it here reads as "expired in 1970".
func (t APIToken) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// apiTokenColumns is the shared select list, in the order scanAPIToken reads it.
const apiTokenColumns = `id, user_id, name, scopes, created_at, last_used_at, expires_at`

// scanAPIToken reads one row of apiTokenColumns.
//
// last_used_at and expires_at are scanned into `any`, not into a string or a
// time.Time, because this driver returns *both* types from one column. The
// column is declared DATETIME, so modernc.org/sqlite converts a parseable value
// to a time.Time — but the "unset" value for these two is ” rather than NULL,
// and that comes back as a plain string.
//
// Scanning either concrete type is therefore wrong in one direction and silently
// so. A *string gets database/sql's conversion of the time.Time, which is
// Go's "2026-09-19 00:54:48 +0000 UTC" layout, not the sqliteTime layout it was
// written in — so it parses to the zero time and every token reads as never
// used. That is not hypothetical: it is what this function did until
// TestTouchAPITokenRecordsLastUse caught it. A *time.Time fails outright on ”.
//
// Pinned by TestTheDriverReturnsTimeOrStringFromADatetimeColumn, because this is
// the driver's behaviour rather than ours, and it is the kind of thing that
// changes under a dependency bump without any of our own code moving.
func scanAPIToken(sc rowScanner) (APIToken, error) {
	var t APIToken
	var scopes string
	var lastUsed, expires any
	if err := sc.Scan(&t.ID, &t.UserID, &t.Name, &scopes, &t.CreatedAt, &lastUsed, &expires); err != nil {
		return APIToken{}, err
	}
	t.Scopes = splitScopes(scopes)
	t.LastUsedAt = sqliteTimeValue(lastUsed)
	t.ExpiresAt = sqliteTimeValue(expires)
	return t, nil
}

// sqliteTimeValue reads a timestamp column that may be unset.
//
// Anything it cannot make sense of is the zero time. That is the safe direction
// for both fields it serves: an unreadable expiry must not become a real
// instant, since a wrong one is either a permanently dead token or — far worse —
// a permanently live one.
func sqliteTimeValue(v any) time.Time {
	switch v := v.(type) {
	case time.Time:
		return v.UTC()
	case string:
		if t, err := time.Parse(sqliteTime, v); err == nil {
			return t.UTC()
		}
		return time.Time{}
	case []byte:
		if t, err := time.Parse(sqliteTime, string(v)); err == nil {
			return t.UTC()
		}
		return time.Time{}
	case nil:
		return time.Time{}
	default:
		// Named rather than left to fall through a type switch's silent end:
		// a new driver type arriving here is a fact worth knowing, and the zero
		// time is the safe answer while we do not know it.
		return time.Time{}
	}
}

// splitScopes decodes the scopes column. Blank entries are dropped so that "",
// "read,", and " read " all mean the same thing, and nil is returned for none —
// a token with no scopes can do nothing, which is the safe reading of an empty
// column.
func splitScopes(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinScopes is splitScopes' inverse, normalising on the way in so the stored
// column is already in the form splitScopes returns.
func joinScopes(scopes []string) string {
	return strings.Join(splitScopes(strings.Join(scopes, ",")), ",")
}

// CreateAPIToken mints a token for a user and returns the raw value, which is
// the only time it exists outside the client.
//
// expiry of zero (or less than zero at the caller's risk) means the token never
// expires; a negative duration produces an already-expired token, which is what
// the tests use instead of sleeping.
func (s *Store) CreateAPIToken(userID, name string, scopes []string, expiry time.Duration) (string, APIToken, error) {
	if userID == "" {
		// The foreign key would not catch this: '' is a value, not a missing
		// one, and no users row has it — but neither does anything else, so the
		// token would be unrevokable by deleting any account.
		return "", APIToken{}, fmt.Errorf("create api token: userID must not be empty")
	}

	b := make([]byte, apiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	raw := APITokenPrefix + hex.EncodeToString(b)

	tok := APIToken{
		ID:        uuid.New().String(),
		UserID:    userID,
		Name:      name,
		Scopes:    splitScopes(joinScopes(scopes)),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	// Written explicitly rather than left to the column default, so the returned
	// struct carries the same instant as the stored row.
	expiresAt := ""
	if expiry != 0 {
		tok.ExpiresAt = tok.CreatedAt.Add(expiry)
		expiresAt = sqliteTimestamp(tok.ExpiresAt)
	}

	_, err := s.conn().Exec(
		"INSERT INTO api_tokens (id, user_id, name, token_hash, scopes, created_at, expires_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?)",
		tok.ID, tok.UserID, tok.Name, hashToken(raw), joinScopes(scopes),
		sqliteTimestamp(tok.CreatedAt), expiresAt,
	)
	if err != nil {
		return "", APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	return raw, tok, nil
}

// GetAPIToken returns the token for a raw value, if it exists and has not
// expired. An unknown, revoked or expired token is ErrNotFound — the caller
// answers 401 for all three, and distinguishing them in the error would be a
// distinction available to whoever is guessing.
//
// The expiry guard needs both halves. expires_at is TEXT, so `expires_at >
// datetime('now')` compares strings: ” sorts below every real timestamp, and a
// guard written with that clause alone rejects exactly the tokens that never
// expire. The sessions table gets away with the single clause because every
// session has a real expiry.
func (s *Store) GetAPIToken(raw string) (APIToken, error) {
	if raw == "" {
		return APIToken{}, fmt.Errorf("get api token: %w", ErrNotFound)
	}
	tok, err := scanAPIToken(s.conn().QueryRow(
		"SELECT "+apiTokenColumns+" FROM api_tokens "+
			"WHERE token_hash = ? AND (expires_at = '' OR expires_at > datetime('now'))",
		hashToken(raw),
	))
	if err != nil {
		return APIToken{}, fmt.Errorf("get api token: %w", err)
	}
	return tok, nil
}

// ListAPITokens returns one user's tokens, newest first. It cannot return
// another user's, and there is no unscoped variant on purpose.
func (s *Store) ListAPITokens(userID string) ([]APIToken, error) {
	rows, err := s.conn().Query(
		"SELECT "+apiTokenColumns+" FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC, id",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	var out []APIToken
	for rows.Next() {
		tok, err := scanAPIToken(rows)
		if err != nil {
			return nil, fmt.Errorf("list api tokens: %w", err)
		}
		out = append(out, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return out, nil
}

// DeleteAPIToken revokes one of a user's own tokens and reports whether a row
// went.
//
// Scoped by owner as well as by id, per AGENT.md §5: the id arrives from a form
// post, and an id-only DELETE would revoke another user's credential and report
// success. Deleting nothing is not an error — a double-submitted revoke is the
// ordinary way to reach it.
func (s *Store) DeleteAPIToken(userID, id string) (bool, error) {
	res, err := s.conn().Exec("DELETE FROM api_tokens WHERE user_id = ? AND id = ?", userID, id)
	if err != nil {
		return false, fmt.Errorf("delete api token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete api token: %w", err)
	}
	return n > 0, nil
}

// TouchAPIToken records that a token was just used.
//
// Callers treat a failure here as a log line, never as a failed request: the
// request was already authenticated, and refusing it because a bookkeeping
// column would not write turns a cosmetic problem into an outage.
func (s *Store) TouchAPIToken(id string) error {
	_, err := s.conn().Exec(
		"UPDATE api_tokens SET last_used_at = ? WHERE id = ?",
		sqliteTimestamp(time.Now()), id,
	)
	if err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}
