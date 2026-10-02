package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func newClient(t *testing.T, s *Store) OAuthClient {
	t.Helper()
	c, err := s.CreateOAuthClient("Claude", []string{"https://claude.example/cb"}, []string{"authorization_code", "refresh_token"})
	if err != nil {
		t.Fatalf("CreateOAuthClient: %v", err)
	}
	return c
}

func newCode(t *testing.T, s *Store, c OAuthClient, userID string, formIDs []string) string {
	t.Helper()
	raw, err := s.CreateAuthCode(AuthCode{
		ClientID:      c.ID,
		UserID:        userID,
		RedirectURI:   c.RedirectURIs[0],
		CodeChallenge: "challenge",
		Resource:      "https://forms.example.com/mcp",
		Scopes:        []string{"read", "write"},
		FormIDs:       formIDs,
	})
	if err != nil {
		t.Fatalf("CreateAuthCode: %v", err)
	}
	return raw
}

// issue walks the code path end to end and returns the pair.
func issue(t *testing.T, s *Store, c OAuthClient, userID string) TokenPair {
	t.Helper()
	code, err := s.RedeemAuthCode(newCode(t, s, c, userID, nil))
	if err != nil {
		t.Fatalf("RedeemAuthCode: %v", err)
	}
	pair, err := s.IssueGrant(code)
	if err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	return pair
}

func mustBeLive(t *testing.T, s *Store, raw, what string) APIToken {
	t.Helper()
	tok, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("%s: GetAPIToken error = %v, want a live token", what, err)
	}
	return tok
}

func mustBeDead(t *testing.T, s *Store, raw, what string) {
	t.Helper()
	if _, err := s.GetAPIToken(raw); !errors.Is(err, ErrNotFound) {
		t.Errorf("%s: GetAPIToken error = %v, want ErrNotFound", what, err)
	}
}

func TestNewSecret(t *testing.T) {
	t.Parallel()

	a, err := newSecret("x_")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSecret("x_")
	if !strings.HasPrefix(a, "x_") || len(a) != len("x_")+2*secretBytes {
		t.Errorf("secret = %q, want x_ and %d hex chars", a, 2*secretBytes)
	}
	if a == b {
		t.Error("two secrets were equal")
	}
}

func TestOAuthClientRoundTrip(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	c := newClient(t, s)
	got, err := s.GetOAuthClient(c.ID)
	if err != nil {
		t.Fatalf("GetOAuthClient: %v", err)
	}
	if got.Name != "Claude" || !slices.Equal(got.RedirectURIs, c.RedirectURIs) || !slices.Equal(got.GrantTypes, c.GrantTypes) {
		t.Errorf("got %+v, want %+v", got, c)
	}
	if _, err := s.GetOAuthClient("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown client error = %v, want ErrNotFound", err)
	}
}

func TestCreateOAuthClientKeepsEveryRedirectURI(t *testing.T) {
	t.Parallel()
	s := mustNew(t)

	// Stored in one column; the separator must not be something a URI can hold
	// unescaped, or one registered URI reads back as two.
	uris := []string{"https://a.example/cb?x=1,2", "http://127.0.0.1:9000/cb"}
	c, err := s.CreateOAuthClient("x", uris, []string{"authorization_code"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOAuthClient(c.ID)
	if !slices.Equal(got.RedirectURIs, uris) {
		t.Errorf("RedirectURIs = %q, want %q", got.RedirectURIs, uris)
	}
}

func TestAuthCodeStoresOnlyTheHash(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	raw := newCode(t, s, c, admin(t, s).ID, nil)

	var n int
	_ = s.conn().QueryRow("SELECT COUNT(*) FROM oauth_auth_codes WHERE code_hash = ?", raw).Scan(&n)
	if n != 0 {
		t.Fatal("the raw code is stored; a database read is a live credential")
	}
	_ = s.conn().QueryRow("SELECT COUNT(*) FROM oauth_auth_codes WHERE code_hash = ?", hashToken(raw)).Scan(&n)
	if n != 1 {
		t.Errorf("rows with the code's hash = %d, want 1", n)
	}
}

func TestRedeemAuthCode(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	raw := newCode(t, s, c, u.ID, []string{"f1"})

	code, err := s.RedeemAuthCode(raw)
	if err != nil {
		t.Fatalf("RedeemAuthCode: %v", err)
	}
	if code.ClientID != c.ID || code.UserID != u.ID || code.RedirectURI != c.RedirectURIs[0] ||
		code.CodeChallenge != "challenge" || code.Resource != "https://forms.example.com/mcp" ||
		!slices.Equal(code.Scopes, []string{"read", "write"}) || !slices.Equal(code.FormIDs, []string{"f1"}) {
		t.Errorf("redeemed %+v", code)
	}

	// Single use. The second presentation is the signal that the code leaked.
	if _, err := s.RedeemAuthCode(raw); !errors.Is(err, ErrCodeReused) {
		t.Errorf("second redeem error = %v, want ErrCodeReused", err)
	}
}

func TestRedeemAuthCodeRefusals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	raw := newCode(t, s, c, admin(t, s).ID, nil)

	if _, err := s.RedeemAuthCode("unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown code error = %v, want ErrNotFound", err)
	}
	if _, err := s.RedeemAuthCode(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty code error = %v, want ErrNotFound", err)
	}

	expireAll(t, s, "oauth_auth_codes")
	if _, err := s.RedeemAuthCode(raw); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired code error = %v, want ErrNotFound", err)
	}
}

// expireAll moves every expiry in table into the past. The store has no clock
// to inject; this is the same instant a real expiry produces.
func expireAll(t *testing.T, s *Store, table string) {
	t.Helper()
	if _, err := s.conn().Exec("UPDATE "+table+" SET expires_at = ?", sqliteTimestamp(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
}

func TestIssueGrant(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)

	code, _ := s.RedeemAuthCode(newCode(t, s, c, u.ID, []string{"f1"}))
	pair, err := s.IssueGrant(code)
	if err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}

	// The access token is an api_tokens row: the existing verifier accepts it
	// with the consented scopes and forms, and nothing downstream special-cases it.
	tok := mustBeLive(t, s, pair.Access, "access token")
	if !strings.HasPrefix(pair.Access, APITokenPrefix) {
		t.Errorf("access token %q lacks the %q prefix", pair.Access, APITokenPrefix)
	}
	if !slices.Equal(tok.Scopes, []string{"read", "write"}) || !slices.Equal(tok.FormIDs, []string{"f1"}) {
		t.Errorf("token scopes %v forms %v", tok.Scopes, tok.FormIDs)
	}
	// The name is what mcpserver records against writes, so it names the client.
	if tok.Name != "Claude" {
		t.Errorf("token name = %q, want the client's name", tok.Name)
	}
	if left := time.Until(tok.ExpiresAt); left <= AccessTokenTTL-time.Minute || left > AccessTokenTTL {
		t.Errorf("access token expires in %v, want about %v", left, AccessTokenTTL)
	}
	if pair.ExpiresIn != AccessTokenTTL {
		t.Errorf("ExpiresIn = %v", pair.ExpiresIn)
	}

	if !strings.HasPrefix(pair.Refresh, RefreshTokenPrefix) {
		t.Errorf("refresh token %q lacks the %q prefix", pair.Refresh, RefreshTokenPrefix)
	}
	if pair.Refresh == pair.Access {
		t.Error("access and refresh tokens are the same value")
	}
	if !slices.Equal(pair.Scopes, []string{"read", "write"}) {
		t.Errorf("pair scopes = %v", pair.Scopes)
	}

	grants, err := s.ListOAuthGrants(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].ClientName != "Claude" || grants[0].ID != pair.GrantID {
		t.Fatalf("grants = %+v", grants)
	}
	if !grants[0].Scope().Allows("f1") || grants[0].Scope().Allows("f2") {
		t.Error("grant scope does not match consent")
	}
}

func TestOAuthAccessTokensAreNotListedAsManualTokens(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)
	if _, _, err := s.CreateAPIToken(u.ID, "laptop", []string{"read"}, nil, 0); err != nil {
		t.Fatal(err)
	}
	issue(t, s, newClient(t, s), u.ID)

	// Every refresh mints a new access token; listing them as manual tokens
	// would bury the operator's own under a row an hour.
	tokens, err := s.ListAPITokens(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "laptop" {
		t.Errorf("ListAPITokens = %+v, want only the manual token", tokens)
	}
}

func TestReusedCodeRevokesTheGrantItIssued(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	raw := newCode(t, s, c, admin(t, s).ID, nil)

	code, _ := s.RedeemAuthCode(raw)
	pair, err := s.IssueGrant(code)
	if err != nil {
		t.Fatal(err)
	}

	// RFC 6749 §4.1.2: if a code is used twice, revoke what it issued. Whoever
	// replays it may be the attacker, and the tokens already out may be theirs.
	if _, err := s.RedeemAuthCode(raw); !errors.Is(err, ErrCodeReused) {
		t.Fatalf("replay error = %v, want ErrCodeReused", err)
	}
	mustBeDead(t, s, pair.Access, "access token after code replay")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); err == nil {
		t.Error("refresh token survived a code replay")
	}
}

func TestRefreshGrantRotates(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	first := issue(t, s, c, admin(t, s).ID)

	second, err := s.RefreshGrant(first.Refresh, c.ID)
	if err != nil {
		t.Fatalf("RefreshGrant: %v", err)
	}
	if second.Refresh == first.Refresh || second.Access == first.Access {
		t.Error("refresh did not rotate both tokens")
	}
	if second.GrantID != first.GrantID {
		t.Errorf("refresh moved to grant %q from %q", second.GrantID, first.GrantID)
	}
	tok := mustBeLive(t, s, second.Access, "refreshed access token")
	if !slices.Equal(tok.Scopes, []string{"read", "write"}) {
		t.Errorf("refreshed scopes = %v", tok.Scopes)
	}
	// The old access token runs out its hour rather than dying mid-request: a
	// client that refreshes while a call is in flight must not fail that call.
	mustBeLive(t, s, first.Access, "previous access token")

	third, err := s.RefreshGrant(second.Refresh, c.ID)
	if err != nil {
		t.Fatalf("second RefreshGrant: %v", err)
	}
	mustBeLive(t, s, third.Access, "third access token")
}

func TestReusedRefreshTokenRevokesTheGrant(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	first := issue(t, s, c, u.ID)
	second, err := s.RefreshGrant(first.Refresh, c.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A rotated-out refresh token coming back means two parties hold it. There
	// is no telling which is legitimate, so neither keeps access.
	if _, err := s.RefreshGrant(first.Refresh, c.ID); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("reuse error = %v, want ErrRefreshReused", err)
	}
	mustBeDead(t, s, first.Access, "first access token")
	mustBeDead(t, s, second.Access, "current access token")
	if _, err := s.RefreshGrant(second.Refresh, c.ID); err == nil {
		t.Error("the current refresh token survived reuse detection")
	}
	if grants, _ := s.ListOAuthGrants(u.ID); len(grants) != 0 {
		t.Errorf("grants after reuse = %d, want 0", len(grants))
	}
}

func TestRefreshGrantRefusals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	other := newClient(t, s)
	pair := issue(t, s, c, admin(t, s).ID)

	if _, err := s.RefreshGrant("dsr_unknown", c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown refresh error = %v, want ErrNotFound", err)
	}
	// A refresh token is bound to the client it was issued to (RFC 6749 §6).
	if _, err := s.RefreshGrant(pair.Refresh, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other client error = %v, want ErrNotFound", err)
	}
	// ...and presenting it from the wrong client must not burn it.
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); err != nil {
		t.Errorf("refresh after a wrong-client attempt: %v", err)
	}
}

func TestExpiredRefreshTokenIsRefused(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	pair := issue(t, s, c, admin(t, s).ID)

	expireAll(t, s, "oauth_refresh_tokens")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired refresh error = %v, want ErrNotFound", err)
	}
}

func TestRevokeOAuthGrant(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	intruder := newUser(t, s, "intruder")
	pair := issue(t, s, c, u.ID)

	// Owner-scoped: the id arrives from a form post.
	if ok, err := s.RevokeOAuthGrant(intruder.ID, pair.GrantID); err != nil || ok {
		t.Fatalf("another user's revoke = %v, %v; want false, nil", ok, err)
	}
	mustBeLive(t, s, pair.Access, "access token after a refused revoke")

	ok, err := s.RevokeOAuthGrant(u.ID, pair.GrantID)
	if err != nil || !ok {
		t.Fatalf("RevokeOAuthGrant = %v, %v", ok, err)
	}
	mustBeDead(t, s, pair.Access, "access token after revoke")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); err == nil {
		t.Error("refresh token survived revoke")
	}
	if ok, _ := s.RevokeOAuthGrant(u.ID, pair.GrantID); ok {
		t.Error("a second revoke reported a row removed")
	}
}

func TestDeletingAUserRemovesTheirGrants(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := newUser(t, s, "leaver")
	pair := issue(t, s, c, u.ID)

	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	mustBeDead(t, s, pair.Access, "access token of a deleted user")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); err == nil {
		t.Error("a deleted user's refresh token still works")
	}
}

func TestTouchAPITokenRecordsGrantUse(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)
	pair := issue(t, s, newClient(t, s), u.ID)
	tok := mustBeLive(t, s, pair.Access, "access token")

	if err := s.TouchAPIToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	// Access tokens rotate hourly, so their own last_used_at is lost with them;
	// the grant is what the operator sees in Connected apps.
	grants, _ := s.ListOAuthGrants(u.ID)
	if len(grants) != 1 || grants[0].LastUsedAt.IsZero() {
		t.Errorf("grant last used = %+v, want set", grants)
	}
}

func TestCleanExpiredOAuth(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)
	c := newClient(t, s)
	pair := issue(t, s, c, u.ID)
	rotated, err := s.RefreshGrant(pair.Refresh, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	newCode(t, s, c, u.ID, nil)

	// An abandoned registration: no grant, older than the pruning window.
	stale := newClient(t, s)
	if _, err := s.conn().Exec("UPDATE oauth_clients SET created_at = ? WHERE id = ?",
		sqliteTimestamp(time.Now().Add(-UnusedClientTTL-time.Hour)), stale.ID); err != nil {
		t.Fatal(err)
	}
	fresh := newClient(t, s)

	if err := s.CleanExpiredOAuth(); err != nil {
		t.Fatalf("CleanExpiredOAuth: %v", err)
	}
	// Nothing live is touched — including the rotated-out refresh token, which
	// reuse detection needs until it would have expired anyway.
	mustBeLive(t, s, rotated.Access, "live access token")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); !errors.Is(err, ErrRefreshReused) {
		t.Errorf("rotated-out refresh after cleanup: %v, want ErrRefreshReused", err)
	}
	if _, err := s.GetOAuthClient(stale.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale unused client survived: %v", err)
	}
	if _, err := s.GetOAuthClient(fresh.ID); err != nil {
		t.Errorf("a fresh client was pruned: %v", err)
	}

	// Now everything expires.
	s2 := mustNew(t)
	u2 := admin(t, s2)
	c2 := newClient(t, s2)
	p2 := issue(t, s2, c2, u2.ID)
	newCode(t, s2, c2, u2.ID, nil)
	for _, table := range []string{"oauth_auth_codes", "oauth_refresh_tokens"} {
		expireAll(t, s2, table)
	}
	if _, err := s2.conn().Exec("UPDATE api_tokens SET expires_at = ?", sqliteTimestamp(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s2.CleanExpiredOAuth(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"oauth_auth_codes", "oauth_refresh_tokens", "api_tokens"} {
		var n int
		_ = s2.conn().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d expired rows left", table, n)
		}
	}
	// The grant itself stays: it is the operator's record of the connection,
	// and is removed by revoking it.
	if grants, _ := s2.ListOAuthGrants(u2.ID); len(grants) != 1 || grants[0].ID != p2.GrantID {
		t.Errorf("grants after cleanup = %+v", grants)
	}
}

func TestCleanExpiredOAuthKeepsManualTokens(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)
	// An expired manual token is the operator's to see and revoke; cleanup of
	// OAuth rows must not reach it.
	raw, _, err := s.CreateAPIToken(u.ID, "old", []string{"read"}, nil, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CleanExpiredOAuth(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.conn().QueryRow("SELECT COUNT(*) FROM api_tokens WHERE token_hash = ?", hashToken(raw)).Scan(&n)
	if n != 1 {
		t.Error("cleanup removed an expired manual token")
	}
}
