package store

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

func acceptAll(AuthCode) bool { return true }

// issue walks the code path end to end and returns the pair.
func issue(t *testing.T, s *Store, c OAuthClient, userID string) TokenPair {
	t.Helper()
	pair, err := s.ExchangeAuthCode(newCode(t, s, c, userID, nil), acceptAll)
	if err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
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

func TestExchangeAuthCodeShowsTheCheckWhatWasConsented(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	raw := newCode(t, s, c, u.ID, []string{"f1"})

	var seen AuthCode
	if _, err := s.ExchangeAuthCode(raw, func(code AuthCode) bool { seen = code; return true }); err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
	}
	if seen.ClientID != c.ID || seen.UserID != u.ID || seen.RedirectURI != c.RedirectURIs[0] ||
		seen.CodeChallenge != "challenge" || seen.Resource != "https://forms.example.com/mcp" ||
		!slices.Equal(seen.Scopes, []string{"read", "write"}) || !slices.Equal(seen.FormIDs, []string{"f1"}) {
		t.Errorf("the check saw %+v", seen)
	}

	// Single use. The second presentation is the signal that the code leaked.
	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrCodeReused) {
		t.Errorf("second exchange error = %v, want ErrCodeReused", err)
	}
}

func TestExchangeAuthCodeRefusals(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	raw := newCode(t, s, c, admin(t, s).ID, nil)

	for _, code := range []string{"unknown", ""} {
		if _, err := s.ExchangeAuthCode(code, acceptAll); !errors.Is(err, ErrNotFound) {
			t.Errorf("code %q error = %v, want ErrNotFound", code, err)
		}
	}

	expireAll(t, s, "oauth_auth_codes")
	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired code error = %v, want ErrNotFound", err)
	}
}

// A code the caller's checks refuse is spent anyway: one guess at the
// verifier per intercepted code, and no grant.
func TestARejectedCodeIsBurned(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	raw := newCode(t, s, c, u.ID, nil)

	if _, err := s.ExchangeAuthCode(raw, func(AuthCode) bool { return false }); !errors.Is(err, ErrCodeRejected) {
		t.Fatalf("rejected exchange error = %v, want ErrCodeRejected", err)
	}
	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrCodeReused) {
		t.Errorf("retry after rejection error = %v, want ErrCodeReused", err)
	}
	if grants, _ := s.ListOAuthGrants(u.ID); len(grants) != 0 {
		t.Errorf("a rejected code left %d grants", len(grants))
	}
}

// "Used" is whether the column is set, not whether it parses. A timestamp the
// driver hands back in some other layout must read as used, never as fresh.
func TestAnUnparseableUsedAtStillCountsAsUsed(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	pair := issue(t, s, c, u.ID)
	raw := newCode(t, s, c, u.ID, nil)

	if _, err := s.conn().Exec("UPDATE oauth_auth_codes SET used_at = 'not a time' WHERE code_hash = ?", hashToken(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrCodeReused) {
		t.Errorf("code with garbage used_at: %v, want ErrCodeReused", err)
	}
	if _, err := s.conn().Exec("UPDATE oauth_refresh_tokens SET used_at = 'not a time' WHERE token_hash = ?", hashToken(pair.Refresh)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); !errors.Is(err, ErrRefreshReused) {
		t.Errorf("refresh with garbage used_at: %v, want ErrRefreshReused", err)
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

func TestExchangeAuthCodeIssuesAGrant(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)

	pair, err := s.ExchangeAuthCode(newCode(t, s, c, u.ID, []string{"f1"}), acceptAll)
	if err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
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
	// The address the code went to, as the operator approved it.
	if grants[0].RedirectURI != c.RedirectURIs[0] {
		t.Errorf("grant redirect = %q", grants[0].RedirectURI)
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

	pair, err := s.ExchangeAuthCode(raw, acceptAll)
	if err != nil {
		t.Fatal(err)
	}

	// RFC 6749 §4.1.2: if a code is used twice, revoke what it issued. Whoever
	// replays it may be the attacker, and the tokens already out may be theirs.
	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrCodeReused) {
		t.Fatalf("replay error = %v, want ErrCodeReused", err)
	}
	mustBeDead(t, s, pair.Access, "access token after code replay")
	if _, err := s.RefreshGrant(pair.Refresh, c.ID); err == nil {
		t.Error("refresh token survived a code replay")
	}
}

// A replay after the code's minute is up is still a replay: the spent row is
// kept for a day past its expiry so it can be recognised.
func TestALateReplayStillRevokes(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	raw := newCode(t, s, c, admin(t, s).ID, nil)
	pair, err := s.ExchangeAuthCode(raw, acceptAll)
	if err != nil {
		t.Fatal(err)
	}
	expireAll(t, s, "oauth_auth_codes")
	if err := s.CleanExpiredOAuth(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ExchangeAuthCode(raw, acceptAll); !errors.Is(err, ErrCodeReused) {
		t.Fatalf("late replay error = %v, want ErrCodeReused", err)
	}
	mustBeDead(t, s, pair.Access, "access token after a late replay")
}

// fileStore is a store on a real file, as production runs: WAL and a pool of
// connections. The in-memory store serialises everything on one connection, so
// it cannot show what concurrent requests do to each other.
func fileStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// race runs fn n times at once and returns what each returned.
func race(n int, fn func() (TokenPair, error)) ([]TokenPair, []error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var pairs []TokenPair
	var errs []error
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pair, err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				pairs = append(pairs, pair)
			} else {
				errs = append(errs, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	return pairs, errs
}

// Concurrent presentations of one code, on a real file database. Exactly one
// issues; every other one is a clean replay — never a lock error, never a
// failed revocation — and so what the winner was issued ends up revoked. Found
// in review: without a busy timeout and write transactions, the losers hit
// SQLITE_BUSY instead, revoked nothing, and raised false security alarms.
func TestConcurrentExchangesOfOneCode(t *testing.T) {
	t.Parallel()
	s := fileStore(t)
	c := newClient(t, s)
	u := admin(t, s)
	raw := newCode(t, s, c, u.ID, nil)

	const n = 8
	pairs, errs := race(n, func() (TokenPair, error) { return s.ExchangeAuthCode(raw, acceptAll) })
	if len(pairs) != 1 {
		t.Fatalf("%d exchanges succeeded, want exactly 1 (errors: %v)", len(pairs), errs)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrCodeReused) || errors.Is(err, ErrRevokeFailed) {
			t.Errorf("a losing exchange returned %v, want a clean ErrCodeReused", err)
		}
	}
	mustBeDead(t, s, pairs[0].Access, "the winner's token after concurrent replays")
	if grants, _ := s.ListOAuthGrants(u.ID); len(grants) != 0 {
		t.Errorf("%d grants left after the replays revoked them", len(grants))
	}
}

// The same for a refresh token presented twice at once: one rotation, the
// rest are reuse, and the whole grant goes.
func TestConcurrentRefreshesOfOneToken(t *testing.T) {
	t.Parallel()
	s := fileStore(t)
	c := newClient(t, s)
	u := admin(t, s)
	first := issue(t, s, c, u.ID)

	pairs, errs := race(8, func() (TokenPair, error) { return s.RefreshGrant(first.Refresh, c.ID) })
	if len(pairs) != 1 {
		t.Fatalf("%d refreshes succeeded, want exactly 1 (errors: %v)", len(pairs), errs)
	}
	// The first loser detects the reuse and revokes the grant, which takes its
	// refresh tokens with it; later losers then find nothing at all. Both are
	// refusals. A lock error or a failed revocation is not.
	var reused int
	for _, err := range errs {
		switch {
		case errors.Is(err, ErrRevokeFailed):
			t.Errorf("a losing refresh failed to revoke: %v", err)
		case errors.Is(err, ErrRefreshReused):
			reused++
		case errors.Is(err, ErrNotFound):
		default:
			t.Errorf("a losing refresh returned %v, want reuse or not found", err)
		}
	}
	if reused == 0 {
		t.Error("no losing refresh was recognised as reuse")
	}
	mustBeDead(t, s, pairs[0].Access, "the rotated token after concurrent reuse")
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
	// An approved client older than the window stays: pruning it would cascade
	// to its grant and silently disconnect a working app.
	if _, err := s.conn().Exec("UPDATE oauth_clients SET created_at = ? WHERE id = ?",
		sqliteTimestamp(time.Now().Add(-48*time.Hour)), c.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.CleanExpiredOAuth(); err != nil {
		t.Fatalf("CleanExpiredOAuth: %v", err)
	}
	if grants, _ := s.ListOAuthGrants(u.ID); len(grants) != 1 {
		t.Errorf("an old approved client's grant was pruned: %d grants", len(grants))
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
	expireAll(t, s2, "oauth_refresh_tokens")
	// Codes are kept a day past expiry for replay detection; these are older.
	if _, err := s2.conn().Exec("UPDATE oauth_auth_codes SET expires_at = ?",
		sqliteTimestamp(time.Now().Add(-SpentCodeRetention-time.Minute))); err != nil {
		t.Fatal(err)
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

// Approving a client again replaces the connection it already had: a
// reconnect would otherwise leave two grants for one client, the first one's
// tokens still live and nothing in the admin to tell them apart.
func TestExchangeReplacesTheClientsEarlierGrant(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	c := newClient(t, s)
	u := admin(t, s)
	other := newUser(t, s, "colleague")

	first := issue(t, s, c, u.ID)
	theirs := issue(t, s, c, other.ID)
	second := issue(t, s, c, u.ID)

	mustBeDead(t, s, first.Access, "the replaced grant's access token")
	if _, err := s.RefreshGrant(first.Refresh, c.ID); err == nil {
		t.Error("the replaced grant's refresh token still works")
	}
	mustBeLive(t, s, second.Access, "the new grant's access token")
	// Per user: a colleague connecting the same client keeps their own grant.
	mustBeLive(t, s, theirs.Access, "another user's grant for the same client")

	grants, _ := s.ListOAuthGrants(u.ID)
	if len(grants) != 1 || grants[0].ID != second.GrantID {
		t.Errorf("grants = %+v, want only the newest", grants)
	}
}

func TestMakeRoomForOAuthClientEvictsTheOldestPending(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	var pending []OAuthClient
	for i := 0; i < 4; i++ {
		c := newClient(t, s)
		// Distinct ages, oldest first.
		if _, err := s.conn().Exec("UPDATE oauth_clients SET created_at = ? WHERE id = ?",
			sqliteTimestamp(time.Now().Add(-time.Duration(10-i)*time.Minute)), c.ID); err != nil {
			t.Fatal(err)
		}
		pending = append(pending, c)
	}
	approved := newClient(t, s)
	if _, err := s.conn().Exec("UPDATE oauth_clients SET created_at = ? WHERE id = ?",
		sqliteTimestamp(time.Now().Add(-time.Hour)), approved.ID); err != nil {
		t.Fatal(err)
	}
	issue(t, s, approved, u.ID)

	// Room for one more under a cap of 3: two pending may stay.
	n, err := s.MakeRoomForOAuthClient(3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("evicted %d, want 2", n)
	}
	for i, c := range pending {
		_, err := s.GetOAuthClient(c.ID)
		if gone := errors.Is(err, ErrNotFound); gone != (i < 2) {
			t.Errorf("pending client %d gone = %v, want %v (oldest go first)", i, gone, i < 2)
		}
	}
	// An approved client is never evicted, however old.
	if _, err := s.GetOAuthClient(approved.ID); err != nil {
		t.Errorf("the approved client was evicted: %v", err)
	}
}
