package oauth

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseRegistration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		wantCode string // "" means success
		wantName string
	}{
		{
			name:     "minimal",
			body:     `{"redirect_uris":["https://client.example.com/cb"]}`,
			wantName: DefaultClientName,
		},
		{
			name:     "named, public, both grants",
			body:     `{"client_name":"Claude","redirect_uris":["https://claude.example/cb"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`,
			wantName: "Claude",
		},
		{
			// The SDK's own registration type declares jwks as a string; a client
			// sending the JSON object RFC 7591 describes must still register.
			name:     "jwks as an object is ignored, not fatal",
			body:     `{"redirect_uris":["https://client.example.com/cb"],"jwks":{"keys":[]}}`,
			wantName: DefaultClientName,
		},
		{
			// RFC 7591 §2 lets the server replace requested values. dsforms has
			// no client secrets, so a confidential method is replaced with "none"
			// rather than refused: refusing would lock out clients that ask for a
			// secret by default and cope fine without one.
			name:     "confidential auth method is replaced",
			body:     `{"redirect_uris":["https://client.example.com/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
			wantName: DefaultClientName,
		},
		{
			name:     "whitespace-only name falls back",
			body:     `{"client_name":"   ","redirect_uris":["https://client.example.com/cb"]}`,
			wantName: DefaultClientName,
		},

		{name: "not json", body: `nope`, wantCode: CodeInvalidClientMetadata},
		{name: "no redirect uris", body: `{"client_name":"x"}`, wantCode: CodeInvalidRedirectURI},
		{name: "empty redirect uris", body: `{"redirect_uris":[]}`, wantCode: CodeInvalidRedirectURI},
		{name: "one bad redirect uri", body: `{"redirect_uris":["https://ok.example/cb","http://evil.example/cb"]}`, wantCode: CodeInvalidRedirectURI},
		{name: "too many redirect uris", body: `{"redirect_uris":[` + strings.Repeat(`"https://a.example/cb",`, MaxRedirectURIs) + `"https://a.example/cb"]}`, wantCode: CodeInvalidRedirectURI},
		{name: "no authorization_code grant", body: `{"redirect_uris":["https://a.example/cb"],"grant_types":["client_credentials"]}`, wantCode: CodeInvalidClientMetadata},
		{name: "implicit response type", body: `{"redirect_uris":["https://a.example/cb"],"response_types":["token"]}`, wantCode: CodeInvalidClientMetadata},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, err := ParseRegistration(strings.NewReader(tc.body))
			if tc.wantCode != "" {
				var oe *Error
				if !errors.As(err, &oe) || oe.Code != tc.wantCode {
					t.Fatalf("err = %v, want OAuth error %q", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reg.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", reg.Name, tc.wantName)
			}
			if len(reg.RedirectURIs) == 0 {
				t.Error("RedirectURIs empty on success")
			}
		})
	}
}

func TestParseRegistrationCapsTheName(t *testing.T) {
	t.Parallel()

	// The name is shown on the consent page; an unbounded one is a layout and
	// spoofing problem. Rune-counted, so a multi-byte name is not cut mid-rune.
	long := strings.Repeat("é", MaxClientNameRunes+10)
	reg, err := ParseRegistration(strings.NewReader(`{"client_name":"` + long + `","redirect_uris":["https://a.example/cb"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(reg.Name)); n != MaxClientNameRunes {
		t.Errorf("name has %d runes, want %d", n, MaxClientNameRunes)
	}
}

func TestParseRegistrationKeepsOnlySupportedGrants(t *testing.T) {
	t.Parallel()

	reg, err := ParseRegistration(strings.NewReader(`{"redirect_uris":["https://a.example/cb"],"grant_types":["authorization_code","refresh_token","client_credentials"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{GrantAuthorizationCode, GrantRefreshToken}
	if !slices.Equal(reg.GrantTypes, want) {
		t.Errorf("GrantTypes = %v, want %v", reg.GrantTypes, want)
	}
}

func TestParseRegistrationDefaultsGrantsWhenOmitted(t *testing.T) {
	t.Parallel()

	reg, err := ParseRegistration(strings.NewReader(`{"redirect_uris":["https://a.example/cb"]}`))
	if err != nil {
		t.Fatal(err)
	}
	// RFC 7591 §2: omitted grant_types means authorization_code. Refresh is
	// granted too, because a connector that cannot refresh breaks hourly.
	want := []string{GrantAuthorizationCode, GrantRefreshToken}
	if !slices.Equal(reg.GrantTypes, want) {
		t.Errorf("GrantTypes = %v, want %v", reg.GrantTypes, want)
	}
}
