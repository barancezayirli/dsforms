package oauth

import (
	"net/url"
	"slices"
	"testing"
	"time"
)

const testResource = "https://forms.example.com/mcp"

func validAuthorizeQuery() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {"client-1"},
		"redirect_uri":          {"https://client.example.com/cb"},
		"state":                 {"xyz"},
		"code_challenge":        {rfcChallenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"read write"},
		"resource":              {testResource},
	}
}

func TestParseAuthorizeRequest(t *testing.T) {
	t.Parallel()

	req, oe := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	if oe != nil {
		t.Fatalf("valid request refused: %v", oe)
	}
	want := AuthorizeRequest{
		ClientID:      "client-1",
		RedirectURI:   "https://client.example.com/cb",
		State:         "xyz",
		CodeChallenge: rfcChallenge,
		Scopes:        []string{"read", "write"},
		Resource:      testResource,
	}
	if req.ClientID != want.ClientID || req.RedirectURI != want.RedirectURI || req.State != want.State ||
		req.CodeChallenge != want.CodeChallenge || req.Resource != want.Resource || !slices.Equal(req.Scopes, want.Scopes) {
		t.Errorf("got %+v, want %+v", req, want)
	}
}

func TestParseAuthorizeRequestRefusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(url.Values)
		code   string
	}{
		{"implicit flow", func(q url.Values) { q.Set("response_type", "token") }, CodeUnsupportedResponseType},
		{"no response type", func(q url.Values) { q.Del("response_type") }, CodeUnsupportedResponseType},
		// OAuth 2.1 makes PKCE mandatory. A request without it is how an
		// intercepted code becomes a usable one.
		{"no challenge", func(q url.Values) { q.Del("code_challenge") }, CodeInvalidRequest},
		{"plain method", func(q url.Values) { q.Set("code_challenge_method", "plain") }, CodeInvalidRequest},
		{"method omitted", func(q url.Values) { q.Del("code_challenge_method") }, CodeInvalidRequest},
		{"malformed challenge", func(q url.Values) { q.Set("code_challenge", "short") }, CodeInvalidRequest},
		// RFC 8707: a token minted for some other resource must not be minted here.
		{"another resource", func(q url.Values) { q.Set("resource", "https://other.example/mcp") }, CodeInvalidTarget},
		{"state too long", func(q url.Values) { q.Set("state", string(make([]byte, MaxStateLen+1))) }, CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := validAuthorizeQuery()
			tc.mutate(q)
			_, oe := ParseAuthorizeRequest(q, testResource)
			if oe == nil || oe.Code != tc.code {
				t.Errorf("error = %v, want %q", oe, tc.code)
			}
		})
	}
}

func TestParseAuthorizeRequestResourceIsOptional(t *testing.T) {
	t.Parallel()

	q := validAuthorizeQuery()
	q.Del("resource")
	req, oe := ParseAuthorizeRequest(q, testResource)
	if oe != nil {
		t.Fatalf("refused without resource: %v", oe)
	}
	// Absent means this server's resource, so the code is bound to it either way.
	if req.Resource != testResource {
		t.Errorf("Resource = %q, want %q", req.Resource, testResource)
	}
}

func TestParseAuthorizeRequestTrailingSlashResource(t *testing.T) {
	t.Parallel()

	q := validAuthorizeQuery()
	q.Set("resource", testResource+"/")
	if _, oe := ParseAuthorizeRequest(q, testResource); oe != nil {
		t.Errorf("trailing slash refused: %v", oe)
	}
}

func TestConsentSignature(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := NewConsentSigner("secret", clock)

	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	sig := s.Sign("user-1", req)

	if !s.Verify(sig, "user-1", req) {
		t.Fatal("a fresh signature did not verify")
	}

	tampered := func(mut func(*AuthorizeRequest)) AuthorizeRequest {
		r := req
		r.Scopes = slices.Clone(req.Scopes)
		mut(&r)
		return r
	}

	cases := []struct {
		name   string
		sig    string
		userID string
		req    AuthorizeRequest
	}{
		// The consent form posts these fields back. Each one, if it could be
		// changed after signing, would let a forged POST redirect a code
		// somewhere else or bind it to an attacker's verifier.
		{"another user", sig, "user-2", req},
		{"another client", sig, "user-1", tampered(func(r *AuthorizeRequest) { r.ClientID = "client-2" })},
		{"another redirect", sig, "user-1", tampered(func(r *AuthorizeRequest) { r.RedirectURI = "https://evil.example/cb" })},
		{"another state", sig, "user-1", tampered(func(r *AuthorizeRequest) { r.State = "abc" })},
		{"another challenge", sig, "user-1", tampered(func(r *AuthorizeRequest) { r.CodeChallenge = rfcVerifier[:43] })},
		{"another resource", sig, "user-1", tampered(func(r *AuthorizeRequest) { r.Resource = "https://x.example/mcp" })},
		{"garbage signature", "nope", "user-1", req},
		{"empty signature", "", "user-1", req},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if s.Verify(tc.sig, tc.userID, tc.req) {
				t.Error("verified, want refused")
			}
		})
	}
}

func TestConsentSignatureFieldBoundaries(t *testing.T) {
	t.Parallel()

	s := NewConsentSigner("secret", time.Now)
	a := AuthorizeRequest{ClientID: "ab", RedirectURI: "c", Resource: testResource}
	b := AuthorizeRequest{ClientID: "a", RedirectURI: "bc", Resource: testResource}
	// Naive concatenation signs "abc" for both. Length-prefixing tells them apart.
	if s.Verify(s.Sign("u", a), "u", b) {
		t.Error("a signature moved across a field boundary")
	}
}

func TestConsentSignatureExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	signedAt := NewConsentSigner("secret", func() time.Time { return now })
	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	sig := signedAt.Sign("user-1", req)

	later := NewConsentSigner("secret", func() time.Time { return now.Add(ConsentTTL + time.Second) })
	if later.Verify(sig, "user-1", req) {
		t.Error("an expired consent signature verified")
	}
	justBefore := NewConsentSigner("secret", func() time.Time { return now.Add(ConsentTTL - time.Second) })
	if !justBefore.Verify(sig, "user-1", req) {
		t.Error("a consent signature inside its lifetime was refused")
	}
}

func TestConsentSignatureDependsOnTheKey(t *testing.T) {
	t.Parallel()

	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	sig := NewConsentSigner("secret", time.Now).Sign("u", req)
	if NewConsentSigner("other", time.Now).Verify(sig, "u", req) {
		t.Error("a signature verified under another key")
	}
}

// The error for an oversized state goes back to the client's redirect URI, so
// the state must not ride along with it — that is the request-size knob
// MaxStateLen exists to take away.
func TestAnOversizedStateIsNotEchoed(t *testing.T) {
	t.Parallel()
	q := validAuthorizeQuery()
	q.Set("state", string(make([]byte, MaxStateLen+1)))
	req, oe := ParseAuthorizeRequest(q, testResource)
	if oe == nil {
		t.Fatal("oversized state accepted")
	}
	if req.State != "" {
		t.Errorf("State kept %d bytes", len(req.State))
	}
	if req.RedirectURI == "" || req.ClientID == "" {
		t.Error("the client and redirect URI are needed to report the error")
	}
}

// A zero ConsentSigner — a handler built without one — fails closed rather
// than panicking mid-request or signing with an empty key.
func TestAZeroConsentSignerFailsClosed(t *testing.T) {
	t.Parallel()
	var s ConsentSigner
	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	if sig := s.Sign("u", req); sig != "" {
		t.Errorf("zero signer signed: %q", sig)
	}
	if s.Verify("1.00", "u", req) {
		t.Error("zero signer verified")
	}
	if _, ok := s.VerifyForm(url.Values{}, "u"); ok {
		t.Error("zero signer verified a form")
	}
}

func TestNewConsentSignerDefaultsTheClock(t *testing.T) {
	t.Parallel()
	s := NewConsentSigner("secret", nil)
	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	if !s.Verify(s.Sign("u", req), "u", req) {
		t.Error("a signer built with a nil clock does not round-trip")
	}
}

// VerifyForm is the one way a consent post becomes a request: the fields come
// back out of the form only together with a signature over them.
func TestVerifyForm(t *testing.T) {
	t.Parallel()
	s := NewConsentSigner("secret", time.Now)
	req, _ := ParseAuthorizeRequest(validAuthorizeQuery(), testResource)
	form := url.Values{
		"client_id":      {req.ClientID},
		"redirect_uri":   {req.RedirectURI},
		"state":          {req.State},
		"code_challenge": {req.CodeChallenge},
		"resource":       {req.Resource},
		"consent":        {s.Sign("u", req)},
	}

	got, ok := s.VerifyForm(form, "u")
	if !ok {
		t.Fatal("a signed form did not verify")
	}
	if got.ClientID != req.ClientID || got.RedirectURI != req.RedirectURI || got.State != req.State ||
		got.CodeChallenge != req.CodeChallenge || got.Resource != req.Resource {
		t.Errorf("got %+v, want %+v", got, req)
	}

	form.Set("redirect_uri", "https://evil.example/cb")
	if _, ok := s.VerifyForm(form, "u"); ok {
		t.Error("an edited form verified")
	}
}
