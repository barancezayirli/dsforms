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
	// UnusedClientTTL is how long a self-registered client is kept without
	// anyone approving it. Registration is open to anyone, so this is what
	// stops it from being a way to fill the database.
	UnusedClientTTL = 24 * time.Hour
)

var (
	// ErrCodeReused is a second presentation of an authorization code. What the
	// code issued has already been revoked when this is returned.
	ErrCodeReused = errors.New("authorization code already used")
	// ErrRefreshReused is a rotated-out refresh token presented again. The
	// whole grant has already been revoked when this is returned.
	ErrRefreshReused = errors.New("refresh token already used")
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
// gave, and the request it was given to.
type AuthCode struct {
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scopes        []string
	FormIDs       []string

	hash string // set by RedeemAuthCode, so IssueGrant can link the grant back
}

// OAuthGrant is one approved connection, as Connected apps shows it.
type OAuthGrant struct {
	ID           string
	ClientID     string
	ClientName   string
	RedirectURIs []string
	Scopes       []string
	FormIDs      []string
	CreatedAt    time.Time
	LastUsedAt   time.Time
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

// CreateOAuthClient registers a client.
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

// CountPendingOAuthClients counts registered clients nobody has approved yet.
// Registration is open, so this is what bounds it.
func (s *Store) CountPendingOAuthClients() (int, error) {
	var n int
	err := s.conn().QueryRow(
		"SELECT COUNT(*) FROM oauth_clients WHERE id NOT IN (SELECT client_id FROM oauth_grants)",
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending oauth clients: %w", err)
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

// RedeemAuthCode consumes a code and returns what it stands for.
//
// The code is spent before the caller checks PKCE and the redirect URI, so a
// failed check burns it too: an attacker holding an intercepted code gets one
// guess at the verifier. Unknown and expired codes are ErrNotFound. A code seen
// before is ErrCodeReused, and whatever it issued is revoked first (RFC 6749
// §4.1.2) — the replay may be the attacker, and the tokens already out may be
// theirs.
func (s *Store) RedeemAuthCode(raw string) (AuthCode, error) {
	if raw == "" {
		return AuthCode{}, fmt.Errorf("redeem auth code: %w", ErrNotFound)
	}
	tx, err := s.conn().Begin()
	if err != nil {
		return AuthCode{}, fmt.Errorf("redeem auth code: %w", err)
	}
	defer tx.Rollback()

	c := AuthCode{hash: hashToken(raw)}
	var scopes, formIDs, grantID string
	var usedAt any
	err = tx.QueryRow(
		"SELECT client_id, user_id, redirect_uri, code_challenge, resource, scopes, form_ids, used_at, grant_id "+
			"FROM oauth_auth_codes WHERE code_hash = ? AND expires_at > datetime('now')",
		c.hash,
	).Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Resource, &scopes, &formIDs, &usedAt, &grantID)
	if err != nil {
		return AuthCode{}, fmt.Errorf("redeem auth code: %w", err)
	}

	if !sqliteTimeValue(usedAt).IsZero() {
		if grantID != "" {
			if err := revokeGrant(tx, grantID); err != nil {
				return AuthCode{}, fmt.Errorf("redeem auth code: revoking after replay: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return AuthCode{}, fmt.Errorf("redeem auth code: %w", err)
			}
		}
		return AuthCode{}, ErrCodeReused
	}

	if _, err := tx.Exec("UPDATE oauth_auth_codes SET used_at = ? WHERE code_hash = ?", sqliteTimestamp(time.Now()), c.hash); err != nil {
		return AuthCode{}, fmt.Errorf("redeem auth code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AuthCode{}, fmt.Errorf("redeem auth code: %w", err)
	}
	c.Scopes = splitScopes(scopes)
	c.FormIDs = splitScopes(formIDs)
	return c, nil
}

// IssueGrant turns a redeemed code into a grant and its first tokens, in one
// transaction: a grant without tokens, or tokens without a grant to revoke
// them by, is never visible.
//
// It replaces any grant the same user already gave the same client. A client
// that reconnects gets a new grant, and the old one's tokens would otherwise
// stay live behind it, listed twice under one name with nothing to tell them
// apart — which is what the checkpoint run found. Another user's grant for the
// same client is theirs and is left alone.
func (s *Store) IssueGrant(code AuthCode) (TokenPair, error) {
	if code.hash == "" {
		return TokenPair{}, fmt.Errorf("issue grant: the code was not redeemed")
	}
	tx, err := s.conn().Begin()
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: %w", err)
	}
	defer tx.Rollback()

	if err := replaceGrants(tx, code.ClientID, code.UserID); err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: replacing the earlier grant: %w", err)
	}

	grantID := uuid.New().String()
	if _, err := tx.Exec(
		"INSERT INTO oauth_grants (id, client_id, user_id, scopes, form_ids, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		grantID, code.ClientID, code.UserID, joinScopes(code.Scopes), joinScopes(code.FormIDs), sqliteTimestamp(time.Now()),
	); err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: %w", err)
	}
	if _, err := tx.Exec("UPDATE oauth_auth_codes SET grant_id = ? WHERE code_hash = ?", grantID, code.hash); err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: %w", err)
	}
	pair, err := mintTokens(tx, grantID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TokenPair{}, fmt.Errorf("issue grant: %w", err)
	}
	return pair, nil
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
	var usedAt any
	err = tx.QueryRow(
		"SELECT r.grant_id, g.client_id, r.used_at FROM oauth_refresh_tokens r "+
			"JOIN oauth_grants g ON g.id = r.grant_id "+
			"WHERE r.token_hash = ? AND r.expires_at > datetime('now')",
		hash,
	).Scan(&grantID, &grantClient, &usedAt)
	if err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
	}
	if grantClient != clientID {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", ErrNotFound)
	}

	if !sqliteTimeValue(usedAt).IsZero() {
		if err := revokeGrant(tx, grantID); err != nil {
			return TokenPair{}, fmt.Errorf("refresh grant: revoking after reuse: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
		}
		return TokenPair{}, ErrRefreshReused
	}

	if _, err := tx.Exec("UPDATE oauth_refresh_tokens SET used_at = ? WHERE token_hash = ?", sqliteTimestamp(time.Now()), hash); err != nil {
		return TokenPair{}, fmt.Errorf("refresh grant: %w", err)
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
// api_tokens.grant_id was added by ALTER and SQLite cannot give such a column a
// cascading foreign key.
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
		"SELECT g.id, g.client_id, c.name, c.redirect_uris, g.scopes, g.form_ids, g.created_at, g.last_used_at "+
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
		var uris, scopes, formIDs string
		var lastUsed any
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &uris, &scopes, &formIDs, &g.CreatedAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("list oauth grants: %w", err)
		}
		g.RedirectURIs = strings.Split(uris, "\n")
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

// CleanExpiredOAuth removes what can no longer be used: expired codes, expired
// refresh tokens, expired OAuth access tokens, and clients nobody approved
// within UnusedClientTTL.
//
// Spent refresh tokens are kept until their own expiry, since reuse detection
// depends on recognising them. Grants are kept: they are the operator's record
// of a connection and go only when revoked. Hand-made tokens are never
// touched, expired or not; those are the operator's to see and revoke.
func (s *Store) CleanExpiredOAuth() error {
	cutoff := sqliteTimestamp(time.Now().Add(-UnusedClientTTL))
	stmts := []struct {
		query string
		args  []any
	}{
		{"DELETE FROM oauth_auth_codes WHERE expires_at <= datetime('now')", nil},
		{"DELETE FROM oauth_refresh_tokens WHERE expires_at <= datetime('now')", nil},
		{"DELETE FROM api_tokens WHERE grant_id != '' AND expires_at != '' AND expires_at <= datetime('now')", nil},
		{"DELETE FROM oauth_clients WHERE created_at < ? AND id NOT IN (SELECT client_id FROM oauth_grants)", []any{cutoff}},
	}
	for _, st := range stmts {
		if _, err := s.conn().Exec(st.query, st.args...); err != nil {
			return fmt.Errorf("clean expired oauth: %w", err)
		}
	}
	return nil
}
