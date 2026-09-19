package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// newUser creates a second user, so the owner-scoping tests have someone to be
// scoped away from.
func newUser(t *testing.T, s *Store, username string) User {
	t.Helper()
	if err := s.CreateUser(username, "hunter2hunter2"); err != nil {
		t.Fatalf("CreateUser(%q) error = %v", username, err)
	}
	u, err := s.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("GetUserByUsername(%q) error = %v", username, err)
	}
	return u
}

// admin returns the seeded default user.
func admin(t *testing.T, s *Store) User {
	t.Helper()
	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin) error = %v", err)
	}
	return u
}

// TestCreateAPITokenStoresOnlyTheHash is the property the whole design rests on:
// the database must never hold a value that can be replayed as a credential.
//
// Asserted against the raw column rather than through the API, because a
// GetAPIToken round trip passes whether the column holds the hash or the token
// itself.
func TestCreateAPITokenStoresOnlyTheHash(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	raw, tok, err := s.CreateAPIToken(u.ID, "laptop", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	if !strings.HasPrefix(raw, APITokenPrefix) {
		t.Errorf("raw token = %q, want the %q prefix so secret scanners can spot it", raw, APITokenPrefix)
	}
	if len(raw) <= len(APITokenPrefix) {
		t.Fatalf("raw token %q carries no random part", raw)
	}

	var stored string
	if err := s.conn().QueryRow("SELECT token_hash FROM api_tokens WHERE id = ?", tok.ID).Scan(&stored); err != nil {
		t.Fatalf("reading token_hash: %v", err)
	}
	if stored == raw {
		t.Fatal("token_hash column holds the raw token; anyone with database read access has a live credential")
	}
	if stored != hashToken(raw) {
		t.Errorf("token_hash = %q, want the SHA-256 of the raw token", stored)
	}
}

// TestGetAPITokenAcceptsANeverExpiringToken is the regression test for the
// column-type trap.
//
// expires_at is TEXT and defaults to ”, so a guard written as the obvious
// `expires_at > datetime('now')` is a *string* comparison in which ” sorts
// below every real timestamp — rejecting exactly the tokens that never expire.
// Dropping the `expires_at = ”` clause must fail this test.
func TestGetAPITokenAcceptsANeverExpiringToken(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	raw, created, err := s.CreateAPIToken(u.ID, "forever", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	if !created.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want the zero time for a token created with no expiry", created.ExpiresAt)
	}

	got, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("GetAPIToken on a never-expiring token error = %v; the expires_at = '' clause is missing", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
	if got.UserID != u.ID {
		t.Errorf("UserID = %q, want %q", got.UserID, u.ID)
	}
}

// TestGetAPITokenRejectsAnExpiredToken is the other half: a guard that accepts
// everything would satisfy the test above just as well.
func TestGetAPITokenRejectsAnExpiredToken(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	// A negative expiry rather than a sleep — the row is born expired.
	raw, _, err := s.CreateAPIToken(u.ID, "stale", []string{"read"}, -time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	if _, err := s.GetAPIToken(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAPIToken on an expired token error = %v, want ErrNotFound", err)
	}
}

// TestGetAPITokenRejectsAnUnknownToken pins the miss, so a lookup that silently
// returned the first row would be caught.
func TestGetAPITokenRejectsAnUnknownToken(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)
	if _, _, err := s.CreateAPIToken(u.ID, "real", []string{"read"}, 0); err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	for _, raw := range []string{"", "dsf_nope", "not-even-close"} {
		if _, err := s.GetAPIToken(raw); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetAPIToken(%q) error = %v, want ErrNotFound", raw, err)
		}
	}
}

// TestAPITokenScopesRoundTrip. Scopes are stored as one TEXT column, so the
// split and join are a real encoding, not a field copy.
func TestAPITokenScopesRoundTrip(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"one", []string{"read"}, []string{"read"}},
		{"several", []string{"read", "write", "delete"}, []string{"read", "write", "delete"}},
		{"empty means no powers at all", nil, nil},
		{"blanks and spaces are dropped", []string{" read ", "", "write"}, []string{"read", "write"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, _, err := s.CreateAPIToken(u.ID, tt.name, tt.in, 0)
			if err != nil {
				t.Fatalf("CreateAPIToken error = %v", err)
			}
			got, err := s.GetAPIToken(raw)
			if err != nil {
				t.Fatalf("GetAPIToken error = %v", err)
			}
			if !slices.Equal(got.Scopes, tt.want) {
				t.Errorf("Scopes = %v, want %v", got.Scopes, tt.want)
			}
		})
	}
}

// TestListAPITokensIsScopedToItsUser. Tokens are per-user credentials; listing
// another user's would leak both their existence and their powers.
func TestListAPITokensIsScopedToItsUser(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	mine := admin(t, s)
	theirs := newUser(t, s, "someone-else")

	if _, _, err := s.CreateAPIToken(mine.ID, "mine", []string{"read"}, 0); err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	if _, _, err := s.CreateAPIToken(theirs.ID, "theirs", []string{"read"}, 0); err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	got, err := s.ListAPITokens(mine.ID)
	if err != nil {
		t.Fatalf("ListAPITokens error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAPITokens returned %d tokens, want 1", len(got))
	}
	if got[0].Name != "mine" {
		t.Errorf("Name = %q, want %q", got[0].Name, "mine")
	}
}

// TestDeleteAPITokenCannotRevokeAnotherUsersToken.
//
// The id is a UUID, so this is not the likeliest attack — but the delete is
// reachable from a form post, and AGENT.md §5 requires every id-scoped statement
// to be scoped by owner as well. Without the user_id clause this deletes
// someone else's credential and reports success.
func TestDeleteAPITokenCannotRevokeAnotherUsersToken(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	mine := admin(t, s)
	theirs := newUser(t, s, "victim")

	raw, victim, err := s.CreateAPIToken(theirs.ID, "theirs", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	removed, err := s.DeleteAPIToken(mine.ID, victim.ID)
	if err != nil {
		t.Fatalf("DeleteAPIToken error = %v", err)
	}
	if removed {
		t.Error("DeleteAPIToken reported a deletion across user boundaries")
	}
	// Assert the token still works, not merely that the call said false — the
	// two come apart if the statement deletes and then miscounts.
	if _, err := s.GetAPIToken(raw); err != nil {
		t.Errorf("the victim's token stopped working: %v", err)
	}
}

// TestDeleteAPITokenRevokesImmediately is the same method's happy path, asserted
// through the credential rather than through the return value.
func TestDeleteAPITokenRevokesImmediately(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	raw, tok, err := s.CreateAPIToken(u.ID, "revoke-me", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	removed, err := s.DeleteAPIToken(u.ID, tok.ID)
	if err != nil {
		t.Fatalf("DeleteAPIToken error = %v", err)
	}
	if !removed {
		t.Fatal("DeleteAPIToken reported no deletion for a token that exists")
	}
	if _, err := s.GetAPIToken(raw); !errors.Is(err, ErrNotFound) {
		t.Errorf("a revoked token still verifies: err = %v, want ErrNotFound", err)
	}

	// Idempotent: a double-submitted revoke is not an error, it deleted nothing.
	removed, err = s.DeleteAPIToken(u.ID, tok.ID)
	if err != nil {
		t.Fatalf("second DeleteAPIToken error = %v", err)
	}
	if removed {
		t.Error("second DeleteAPIToken reported a deletion")
	}
}

// TestDeletingAUserRevokesTheirTokens. The FK cascade is the whole reason tokens
// are bound to a user: removing someone's account must remove their access, not
// leave a live credential behind with no owner to answer for it.
func TestDeletingAUserRevokesTheirTokens(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := newUser(t, s, "departing")

	raw, _, err := s.CreateAPIToken(u.ID, "laptop", []string{"read", "write"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	if _, err := s.GetAPIToken(raw); err != nil {
		t.Fatalf("token did not work before the delete: %v", err)
	}

	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser error = %v", err)
	}
	if _, err := s.GetAPIToken(raw); !errors.Is(err, ErrNotFound) {
		t.Errorf("a departed user's token still verifies: err = %v, want ErrNotFound", err)
	}
}

// TestTouchAPITokenRecordsLastUse. last_used_at is what lets an operator spot a
// token they forgot about, so "never used" and "used" have to be distinguishable.
func TestTouchAPITokenRecordsLastUse(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	raw, tok, err := s.CreateAPIToken(u.ID, "laptop", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	if !tok.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt = %v on a fresh token, want the zero time", tok.LastUsedAt)
	}

	if err := s.TouchAPIToken(tok.ID); err != nil {
		t.Fatalf("TouchAPIToken error = %v", err)
	}

	got, err := s.GetAPIToken(raw)
	if err != nil {
		t.Fatalf("GetAPIToken error = %v", err)
	}
	if got.LastUsedAt.IsZero() {
		t.Error("LastUsedAt is still zero after TouchAPIToken")
	}
}

// TestTheDriverReturnsTimeOrStringFromADatetimeColumn characterises the driver,
// not our code — which is exactly why it is worth four lines.
//
// modernc.org/sqlite converts a DATETIME column to a time.Time when the value
// parses, and hands back a bare string when it does not. Our columns use ” for
// "unset", so one column yields two Go types, and scanning either concrete type
// is wrong in one direction. scanAPIToken handles both; this is what tells us if
// a dependency bump changes the deal underneath it.
func TestTheDriverReturnsTimeOrStringFromADatetimeColumn(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	_, set, err := s.CreateAPIToken(u.ID, "expiring", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}
	_, unset, err := s.CreateAPIToken(u.ID, "forever", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken error = %v", err)
	}

	read := func(id string) any {
		t.Helper()
		var v any
		if err := s.conn().QueryRow("SELECT expires_at FROM api_tokens WHERE id = ?", id).Scan(&v); err != nil {
			t.Fatalf("reading expires_at: %v", err)
		}
		return v
	}

	if _, ok := read(set.ID).(time.Time); !ok {
		t.Errorf("a populated DATETIME came back as %T, want time.Time", read(set.ID))
	}
	if _, ok := read(unset.ID).(string); !ok {
		t.Errorf("an empty DATETIME came back as %T, want string", read(unset.ID))
	}
}

// TestCreateAPITokenRejectsAnEmptyUser. A token with no owner cannot be revoked
// by deleting anyone, and nothing in the schema forbids the empty string.
func TestCreateAPITokenRejectsAnEmptyUser(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	if _, _, err := s.CreateAPIToken("", "orphan", []string{"read"}, 0); err == nil {
		t.Fatal("CreateAPIToken(\"\", …) succeeded; an ownerless token cannot be revoked")
	}
}

// TestCreateAPITokensAreUnique guards the generator rather than the schema: a
// constant or short token would still insert fine on the first call.
func TestCreateAPITokensAreUnique(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	u := admin(t, s)

	seen := map[string]bool{}
	for i := range 32 {
		raw, _, err := s.CreateAPIToken(u.ID, "t", []string{"read"}, 0)
		if err != nil {
			t.Fatalf("CreateAPIToken #%d error = %v", i, err)
		}
		if seen[raw] {
			t.Fatalf("CreateAPIToken returned a duplicate token on call %d", i)
		}
		seen[raw] = true
	}
}
