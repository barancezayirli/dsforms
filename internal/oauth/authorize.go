package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxStateLen bounds the opaque state a client round-trips through us. A valid
// state is echoed into a redirect and a form field; unbounded, it would be a
// request-size knob handed to a stranger. An oversized one is never echoed.
const MaxStateLen = 1024

// ConsentTTL is how long a rendered consent page stays submittable.
const ConsentTTL = 10 * time.Minute

// AuthorizeRequest is an authorization request. Its protocol fields are valid
// when it comes from ParseAuthorizeRequest or ConsentSigner.VerifyForm — the
// only two places one should be built. The client and redirect URI are only
// shaped here; whether they belong together is a registry question, answered by
// the caller.
type AuthorizeRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scopes        []string // as requested; the caller intersects with known scopes
	Resource      string   // always this server's resource once parsed
}

// ParseAuthorizeRequest validates the protocol fields of an authorization
// request against this server's resource URL.
//
// The returned error is one to send back to the client's redirect URI — but
// only after the caller has established the client and redirect URI are
// genuine. Errors about those two must never redirect (RFC 6749 §4.1.2.1).
func ParseAuthorizeRequest(q url.Values, resource string) (AuthorizeRequest, *Error) {
	req := AuthorizeRequest{
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		State:         q.Get("state"),
		CodeChallenge: q.Get("code_challenge"),
		Scopes:        strings.Fields(q.Get("scope")),
		Resource:      resource,
	}

	if q.Get("response_type") != "code" {
		return req, errorf(CodeUnsupportedResponseType, `only response_type "code" is supported`)
	}
	if len(req.State) > MaxStateLen {
		// Dropped, not echoed: the error goes back to the client's redirect
		// URI, and carrying an oversized state there is exactly the
		// request-size knob this limit exists to take away.
		req.State = ""
		return req, errorf(CodeInvalidRequest, "state is too long")
	}
	// PKCE is mandatory, and S256 the only method (see VerifyPKCE).
	if q.Get("code_challenge_method") != "S256" {
		return req, errorf(CodeInvalidRequest, `code_challenge_method must be "S256"`)
	}
	if !ValidChallenge(req.CodeChallenge) {
		return req, errorf(CodeInvalidRequest, "code_challenge is missing or malformed")
	}
	// RFC 8707: a client may name the resource it wants a token for. Absent
	// means ours; anything else is a token this server must not mint.
	if r := q.Get("resource"); r != "" && !SameResource(r, resource) {
		return req, errorf(CodeInvalidTarget, "resource is not this server")
	}
	return req, nil
}

// SameResource compares resource indicators allowing only a trailing slash to
// differ, as the SDK's own audience check does.
func SameResource(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// ConsentSigner protects the consent form.
//
// The admin has no CSRF tokens: the session cookie is SameSite=Lax and that is
// the whole defence. For the one form that hands a credential to a third party
// that is not enough to rest on, so the consent page carries a signature over
// the request it is approving, the user it was shown to, and an expiry. A POST
// that changes the redirect URI, swaps the PKCE challenge, or was rendered for
// someone else does not verify.
type ConsentSigner struct {
	key []byte
	now func() time.Time
}

// NewConsentSigner derives a consent-only key from the instance secret, so a
// signature made here can never be replayed as some other HMAC the instance
// produces under the same secret (the flash cookie, for one).
//
// A nil now means time.Now. The zero ConsentSigner — one never built here —
// signs nothing and verifies nothing, so a handler wired without one fails
// closed instead of panicking mid-request.
func NewConsentSigner(secret string, now func() time.Time) ConsentSigner {
	if now == nil {
		now = time.Now
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("dsforms oauth consent v1"))
	return ConsentSigner{key: mac.Sum(nil), now: now}
}

// Sign returns "<unix expiry>.<hex mac>", or "" from a zero signer.
func (s ConsentSigner) Sign(userID string, req AuthorizeRequest) string {
	if len(s.key) == 0 {
		return ""
	}
	exp := s.now().Add(ConsentTTL).Unix()
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(s.mac(exp, userID, req))
}

// Verify reports whether sig was issued by Sign for exactly this user and
// request, and has not expired.
func (s ConsentSigner) Verify(sig, userID string, req AuthorizeRequest) bool {
	if len(s.key) == 0 {
		return false
	}
	expStr, macHex, ok := strings.Cut(sig, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || s.now().Unix() > exp {
		return false
	}
	got, err := hex.DecodeString(macHex)
	if err != nil {
		return false
	}
	return hmac.Equal(got, s.mac(exp, userID, req))
}

// VerifyForm reads a posted consent form back into the request it approves,
// and reports whether its signature covers exactly that request for userID.
// It is how the consent handler gets a request, so the protocol fields never
// come from the form without the signature that vouches for them.
func (s ConsentSigner) VerifyForm(form url.Values, userID string) (AuthorizeRequest, bool) {
	req := AuthorizeRequest{
		ClientID:      form.Get("client_id"),
		RedirectURI:   form.Get("redirect_uri"),
		State:         form.Get("state"),
		CodeChallenge: form.Get("code_challenge"),
		Resource:      form.Get("resource"),
	}
	if !s.Verify(form.Get("consent"), userID, req) {
		return AuthorizeRequest{}, false
	}
	return req, true
}

// mac length-prefixes every field, so "ab"+"c" and "a"+"bc" sign differently.
// Scopes are deliberately absent: the user picks them on the consent page.
func (s ConsentSigner) mac(exp int64, userID string, req AuthorizeRequest) []byte {
	mac := hmac.New(sha256.New, s.key)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(exp))
	mac.Write(buf[:])
	for _, f := range []string{userID, req.ClientID, req.RedirectURI, req.State, req.CodeChallenge, req.Resource} {
		binary.BigEndian.PutUint64(buf[:], uint64(len(f)))
		mac.Write(buf[:])
		mac.Write([]byte(f))
	}
	return mac.Sum(nil)
}
