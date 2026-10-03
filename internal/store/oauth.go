package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RefreshTokenPrefix marks a dsforms OAuth refresh token on sight, for the same
// reason APITokenPrefix exists: a leaked one should be recognisable as a
// credential by a human and a secret scanner.
const RefreshTokenPrefix = "dsr_"

// Lifetimes of what the authorization server issues.
const (
	// AuthCodeTTL is short because a code only has to survive one redirect and
	// one back-channel request.
	AuthCodeTTL = time.Minute
	// AccessTokenTTL bounds what a stolen access token is worth.
	AccessTokenTTL = time.Hour
	// RefreshTokenTTL is how long a connected client may sit idle. Each refresh
	// issues a new one with a fresh lifetime.
	RefreshTokenTTL = 30 * 24 * time.Hour
	// UnusedClientTTL is how long a self-registered client is kept with no live
	// grant. A real client registers and is approved within the same minute, so
	// an hour is generous; registration is open to anyone, and a short life is
	// what keeps it from being a way to fill the database.
	UnusedClientTTL = time.Hour
	// SpentCodeRetention is how long a redeemed code is kept after it expires,
	// so a late replay is still recognised as one and revokes what it issued.
	SpentCodeRetention = 24 * time.Hour
)

var (
	// ErrCodeReused is a second presentation of an authorization code. What the
	// code issued has already been revoked when this is returned.
	ErrCodeReused = errors.New("authorization code already used")
	// ErrRefreshReused is a rotated-out refresh token presented again. The
	// whole grant has already been revoked when this is returned.
	ErrRefreshReused = errors.New("refresh token already used")
	// ErrCodeRejected is a code the caller's checks refused (PKCE, client,
	// redirect URI, resource). The code is spent all the same.
	ErrCodeRejected = errors.New("authorization code rejected")
	// ErrRevokeFailed is joined to ErrCodeReused or ErrRefreshReused when the
	// replay was detected but revoking what it issued failed. Those tokens may
	// still be live; the caller must say so loudly, not as routine noise.
	ErrRevokeFailed = errors.New("revoking after a replay failed")
)

// OAuthClient is a self-registered MCP client.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	GrantTypes   []string
	CreatedAt    time.Time
}

// AuthCode is what an authorization code stands for: the consent the operator
// gave, and the request it was given to. It is the input to CreateAuthCode and
// what ExchangeAuthCode hands its caller's check; the grant is always written
// from the stored row, never from a struct a caller could have changed.
type AuthCode struct {
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scopes        []string
	FormIDs       []string
}

// OAuthGrant is one approved connection, as Connected apps shows it.
type OAuthGrant struct {
	ID         string
	ClientID   string
	ClientName string
	// RedirectURI is the address the approved code was delivered to — the one
	// the operator saw on the consent page, not merely the client's first
	// registered one.
	RedirectURI string
	Scopes      []string
	FormIDs     []string
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

// Scope is the set of forms this grant reaches, read the same way as a token's.
func (g OAuthGrant) Scope() FormScope {
	return ParseFormScope(strings.Join(g.FormIDs, ","))
}

// TokenPair is what the token endpoint hands a client.
type TokenPair struct {
	GrantID   string
	Access    string
	Refresh   string
	ExpiresIn time.Duration
	Scopes    []string
}

// CreateOAuthClient registers a client. redirect_uris is stored newline-joined,
// which is safe only because every URI has passed oauth.ValidateRedirectURI:
// url.Parse refuses control characters, so none can contain a newline.
func (s *Store) CreateOAuthClient(name string, redirectURIs, grantTypes []string) (OAuthClient, error) {
	c := OAuthClient{
		ID:           uuid.New().String(),
		Name:         name,
		RedirectURIs: redirectURIs,
		GrantTypes:   grantTypes,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}
	_, err := s.conn().Exec(
		"INSERT INTO oauth_clients (id, name, redirect_uris, grant_types, created_at) VALUES (?, ?, ?, ?, ?)",
		c.ID, c.Name, strings.Join(redirectURIs, "\n"), joinScopes(grantTypes), sqliteTimestamp(c.CreatedAt),
	)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("create oauth client: %w", err)
	}
	return c, nil
}

// GetOAuthClient returns a registered client, or ErrNotFound.
func (s *Store) GetOAuthClient(id string) (OAuthClient, error) {
	var c OAuthClient
	var uris, grants string
	err := s.conn().QueryRow(
		"SELECT id, name, redirect_uris, grant_types, created_at FROM oauth_clients WHERE id = ?", id,
	).Scan(&c.ID, &c.Name, &uris, &grants, &c.CreatedAt)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("get oauth client: %w", err)
	}
	c.RedirectURIs = strings.Split(uris, "\n")
	c.GrantTypes = splitScopes(grants)
	return c, nil
}

// MakeRoomForOAuthClient keeps the number of clients with no live grant below
// max, deleting the oldest first, and reports how many went.
//
// Registration is open, so this is what bounds it. Evicting the oldest rather
// than refusing new registrations means a flood cannot lock legitimate clients
// out: a real client registers and is approved within a minute, so it is never
// the oldest for long. A client with a grant is never evicted.
func (s *Store) MakeRoomForOAuthClient(max int) (int64, error) {
	res, err := s.conn().Exec(
		"DELETE FROM oauth_clients WHERE id IN ("+
			"SELECT id FROM oauth_clients WHERE id NOT IN (SELECT client_id FROM oauth_grants) "+
			"ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)",
		max-1,
	)
	if err != nil {
		return 0, fmt.Errorf("make room for oauth client: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("make room for oauth client: %w", err)
	}
	return n, nil
}

// CreateAuthCode records an approved authorization request and returns the
// code, which is the only time it exists outside the client.
func (s *Store) CreateAuthCode(c AuthCode) (string, error) {
	raw, err := newSecret("")
	if err != nil {
		return "", fmt.Errorf("create auth code: %w", err)
	}
	_, err = s.conn().Exec(
		"INSERT INTO oauth_auth_codes (code_hash, client_id, user_id, redirect_uri, code_challenge, resource, scopes, form_ids, expires_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		hashToken(raw), c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, c.Resource,
		joinScopes(c.Scopes), joinScopes(c.FormIDs), sqliteTimestamp(time.Now().Add(AuthCodeTTL)),
	)
	if err != nil {
		return "", fmt.Errorf("create auth code: %w", err)
	}
	return raw, nil
}

// ExchangeAuthCode redeems a code and, if accept approves it, issues the grant
// and its first tokens — all in one transaction.
//
// One transaction is the point. Redeeming and issuing separately left a window
// in which a replay saw the code spent but no grant yet, revoked nothing, and
// the grant was issued anyway behind it.
//
// The code is spent before accept runs, so a refused check burns it too: an
// attacker holding an intercepted code gets one guess at the verifier.
//   - unknown or expired: ErrNotFound
//   - seen before: ErrCodeReused, after revoking what it issued (RFC 6749
//     §4.1.2) — the replay may be the attacker, and the tokens out may be theirs
//   - refused by accept: ErrCodeRejected
//
// A spent code is recognised for SpentCodeRetention past its expiry, so a late
// replay is still treated as one. Issuing replaces any grant the same user
// already gave the same client: a client that reconnects would otherwise leave
// its old grant's tokens live behind the new one. Another user's grant for the
// same client is theirs and is left alone.
func (s *Store) ExchangeAuthCode(raw string, accept func(AuthCode) bool) (TokenPair, error) {
	if raw == "" {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", ErrNotFound)
	}
	tx, err := s.conn().Begin()
	if err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	defer tx.Rollback()

	hash := hashToken(raw)
	var c AuthCode
	var scopes, formIDs, grantID string
	var used, live bool
	// used is decided by whether the column is set, never by whether it parses:
	// an unparseable timestamp must not read as "unused" and let a replay in.
	err = tx.QueryRow(
		"SELECT client_id, user_id, redirect_uri, code_challenge, resource, scopes, form_ids, grant_id, "+
			"used_at != '', expires_at > datetime('now') FROM oauth_auth_codes WHERE code_hash = ?",
		hash,
	).Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Resource, &scopes, &formIDs, &grantID, &used, &live)
	if err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	c.Scopes = splitScopes(scopes)
	c.FormIDs = splitScopes(formIDs)

	if used {
		return TokenPair{}, replayed(tx, grantID, ErrCodeReused)
	}
	if !live {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", ErrNotFound)
	}

	// Single use rests on the row, not on lock timing: only the request whose
	// UPDATE changes it may go on.
	res, err := tx.Exec("UPDATE oauth_auth_codes SET used_at = ? WHERE code_hash = ? AND used_at = ''",
		sqliteTimestamp(time.Now()), hash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", ErrCodeReused)
	}

	if !accept(c) {
		if err := tx.Commit(); err != nil {
			return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
		}
		return TokenPair{}, ErrCodeRejected
	}

	if err := replaceGrants(tx, c.ClientID, c.UserID); err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: replacing the earlier grant: %w", err)
	}
	grantID = uuid.New().String()
	if _, err := tx.Exec(
		"INSERT INTO oauth_grants (id, client_id, user_id, redirect_uri, scopes, form_ids, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		grantID, c.ClientID, c.UserID, c.RedirectURI, scopes, formIDs, sqliteTimestamp(time.Now()),
	); err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	if _, err := tx.Exec("UPDATE oauth_auth_codes SET grant_id = ? WHERE code_hash = ?", grantID, hash); err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	pair, err := mintTokens(tx, grantID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TokenPair{}, fmt.Errorf("exchange auth code: %w", err)
	}
	return pair, nil
}

// replayed revokes what a replayed credential issued and returns sentinel. If
// the revocation fails, ErrRevokeFailed is joined in, because the tokens it
// meant to kill may still be live.
func replayed(tx *sql.Tx, grantID string, sentinel error) error {
	if grantID == "" {
		return sentinel
	}
	if err := revokeGrant(tx, grantID); err != nil {
		return errors.Join(sentinel, ErrRevokeFailed, err)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(sentinel, ErrRevokeFailed, err)
	}
	return sentinel
}

// RefreshGrant rotates a refresh token: it is spent, and a new access and
// refresh token are issued under the same grant.
//
// The token is bound to the client it was issued to; from any other client it
// is ErrNotFound and is not spent. A token already spent is ErrRefreshReused,
// and the whole grant is revoked first: two parties hold it and there is no
// telling which is legitimate.
//
// The previous access token is left to run out its hour. Deleting it would fail
// a request the client had in flight while it refreshed.
func (s *Store) RefreshGrant(raw, clientID string) (TokenPair, error) {
	if raw == "" {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", ErrNotFound)
	}
	tx, err := s.conn().Begin()
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	defer tx.Rollback()

	hash := hashToken(raw)
	var grantID, grantClient string
	var used bool
	// used by whether it is set, never by whether it parses; see ExchangeAuthCode.
	err = tx.QueryRow(
		"SELECT r.grant_id, g.client_id, r.used_at != '' FROM oauth_refresh_tokens r "+
			"JOIN oauth_grants g ON g.id = r.grant_id "+
			"WHERE r.token_hash = ? AND r.expires_at > datetime('now')",
		hash,
	).Scan(&grantID, &grantClient, &used)
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	if grantClient != clientID {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", ErrNotFound)
	}

	if used {
		return TokenPair{}, replayed(tx, grantID, ErrRefreshReused)
	}

	res, err := tx.Exec("UPDATE oauth_refresh_tokens SET used_at = ? WHERE token_hash = ? AND used_at = ''",
		sqliteTimestamp(time.Now()), hash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", ErrRefreshReused)
	}
	pair, err := mintTokens(tx, grantID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	return pair, nil
}

// mintTokens issues an access token and a refresh token for a grant, reading
// the scopes, forms and client name from the grant itself so a refresh can
// never widen what was consented to.
func mintTokens(tx *sql.Tx, grantID string) (TokenPair, error) {
	var userID, scopes, formIDs, clientName string
	err := tx.QueryRow(
		"SELECT g.user_id, g.scopes, g.form_ids, c.name FROM oauth_grants g "+
			"JOIN oauth_clients c ON c.id = g.client_id WHERE g.id = ?",
		grantID,
	).Scan(&userID, &scopes, &formIDs, &clientName)
	if err != nil {
		return TokenPair{}, err
	}

	access, _, err := insertAPIToken(tx, userID, clientName, splitScopes(scopes), splitScopes(formIDs), AccessTokenTTL, grantID)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := newSecret(RefreshTokenPrefix)
	if err != nil {
		return TokenPair{}, err
	}
	if _, err := tx.Exec(
		"INSERT INTO oauth_refresh_tokens (token_hash, grant_id, expires_at) VALUES (?, ?, ?)",
		hashToken(refresh), grantID, sqliteTimestamp(time.Now().Add(RefreshTokenTTL)),
	); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		GrantID:   grantID,
		Access:    access,
		Refresh:   refresh,
		ExpiresIn: AccessTokenTTL,
		Scopes:    splitScopes(scopes),
	}, nil
}

// replaceGrants revokes every grant one user gave one client.
func replaceGrants(tx *sql.Tx, clientID, userID string) error {
	rows, err := tx.Query("SELECT id FROM oauth_grants WHERE client_id = ? AND user_id = ?", clientID, userID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := revokeGrant(tx, id); err != nil {
			return err
		}
	}
	return nil
}

// revokeGrant removes a grant and everything it issued. Its refresh tokens go
// by the foreign key; its access tokens are deleted explicitly, because
// api_tokens.grant_id was added by ALTER with an empty-string default, and
// SQLite only allows an ALTER-added foreign key whose default is NULL.
func revokeGrant(q execQuerier, grantID string) error {
	if _, err := q.Exec("DELETE FROM api_tokens WHERE grant_id = ?", grantID); err != nil {
		return err
	}
	_, err := q.Exec("DELETE FROM oauth_grants WHERE id = ?", grantID)
	return err
}

// ListOAuthGrants returns one user's connected clients, newest first.
func (s *Store) ListOAuthGrants(userID string) ([]OAuthGrant, error) {
	rows, err := s.conn().Query(
		"SELECT g.id, g.client_id, c.name, g.redirect_uri, g.scopes, g.form_ids, g.created_at, g.last_used_at "+
			"FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id "+
			"WHERE g.user_id = ? ORDER BY g.created_at DESC, g.id",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list oauth grants: %w", err)
	}
	defer rows.Close()

	var out []OAuthGrant
	for rows.Next() {
		var g OAuthGrant
		var scopes, formIDs string
		var lastUsed any
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.RedirectURI, &scopes, &formIDs, &g.CreatedAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("list oauth grants: %w", err)
		}
		g.Scopes = splitScopes(scopes)
		g.FormIDs = splitScopes(formIDs)
		g.LastUsedAt = sqliteTimeValue(lastUsed)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list oauth grants: %w", err)
	}
	return out, nil
}

// RevokeOAuthGrant disconnects one of a user's clients and reports whether a
// grant went. Scoped by owner, like DeleteAPIToken: the id arrives from a form
// post.
func (s *Store) RevokeOAuthGrant(userID, id string) (bool, error) {
	tx, err := s.conn().Begin()
	if err != nil {
		return false, fmt.Errorf("revoke oauth grant: %w", err)
	}
	defer tx.Rollback()

	var found string
	err = tx.QueryRow("SELECT id FROM oauth_grants WHERE id = ? AND user_id = ?", id, userID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("revoke oauth grant: %w", err)
	}
	if err := revokeGrant(tx, found); err != nil {
		return false, fmt.Errorf("revoke oauth grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("revoke oauth grant: %w", err)
	}
	return true, nil
}

// CleanExpiredOAuth removes what can no longer be used: codes past
// SpentCodeRetention, expired refresh tokens, expired OAuth access tokens, and
// clients with no live grant older than UnusedClientTTL.
//
// Spent refresh tokens are kept until their own expiry, and spent codes for a
// day past theirs, since replay detection depends on recognising them. Grants
// are kept: they are the operator's record of a connection and go only when
// revoked — and a client with a grant is never pruned. Hand-made tokens are
// never touched, expired or not; those are the operator's to see and revoke.
//
// Every statement runs even if an earlier one fails, so one bad table cannot
// stop the client pruning that keeps registration open.
func (s *Store) CleanExpiredOAuth() error {
	codeCutoff := sqliteTimestamp(time.Now().Add(-SpentCodeRetention))
	clientCutoff := sqliteTimestamp(time.Now().Add(-UnusedClientTTL))
	stmts := []struct {
		query string
		args  []any
	}{
		{"DELETE FROM oauth_auth_codes WHERE expires_at <= ?", []any{codeCutoff}},
		{"DELETE FROM oauth_refresh_tokens WHERE expires_at <= datetime('now')", nil},
		{"DELETE FROM api_tokens WHERE grant_id != '' AND expires_at != '' AND expires_at <= datetime('now')", nil},
		{"DELETE FROM oauth_clients WHERE created_at < ? AND id NOT IN (SELECT client_id FROM oauth_grants)", []any{clientCutoff}},
	}
	var errs []error
	for _, st := range stmts {
		if _, err := s.conn().Exec(st.query, st.args...); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("clean expired oauth: %w", err)
	}
	return nil
}
