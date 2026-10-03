package oauth

import (
	"encoding/json"
	"slices"
	"testing"
)

var testScopes = []string{"read", "write", "delete"}

func TestServerMetadata(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"https://forms.example.com", "https://forms.example.com/"} {
		m := NewServerMetadata(base, testScopes)

		// The SDK client compares the issuer to the URL it discovered from, and
		// refuses a mismatch; a trailing slash here would be one.
		if m.Issuer != "https://forms.example.com" {
			t.Errorf("base %q: Issuer = %q", base, m.Issuer)
		}
		if m.AuthorizationEndpoint != "https://forms.example.com"+PathAuthorize ||
			m.TokenEndpoint != "https://forms.example.com"+PathToken ||
			m.RegistrationEndpoint != "https://forms.example.com"+PathRegister {
			t.Errorf("base %q: endpoints = %+v", base, m)
		}
	}

	m := NewServerMetadata("https://forms.example.com", testScopes)
	// The SDK client errors with "does not implement PKCE" when this is empty.
	if !slices.Equal(m.CodeChallengeMethodsSupported, []string{"S256"}) {
		t.Errorf("CodeChallengeMethodsSupported = %v", m.CodeChallengeMethodsSupported)
	}
	if !slices.Equal(m.TokenEndpointAuthMethodsSupported, []string{"none"}) {
		t.Errorf("TokenEndpointAuthMethodsSupported = %v", m.TokenEndpointAuthMethodsSupported)
	}
	if !slices.Equal(m.ResponseTypesSupported, []string{"code"}) {
		t.Errorf("ResponseTypesSupported = %v", m.ResponseTypesSupported)
	}
	if !slices.Equal(m.GrantTypesSupported, []string{GrantAuthorizationCode, GrantRefreshToken}) {
		t.Errorf("GrantTypesSupported = %v", m.GrantTypesSupported)
	}
	// Advertised, and therefore always sent: the SDK refuses an iss it was not
	// told about and requires one it was.
	if !m.AuthorizationResponseIssParameterSupported {
		t.Error("iss parameter support not advertised")
	}
	if !slices.Equal(m.ScopesSupported, testScopes) {
		t.Errorf("ScopesSupported = %v", m.ScopesSupported)
	}
}

func TestServerMetadataJSONHasNoEmptyJWKS(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(NewServerMetadata("https://forms.example.com", testScopes))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	// Tokens are opaque; an empty jwks_uri invites a client to fetch "".
	if _, ok := raw["jwks_uri"]; ok {
		t.Errorf("jwks_uri present: %s", b)
	}
	if raw["issuer"] != "https://forms.example.com" {
		t.Errorf("issuer = %v", raw["issuer"])
	}
}

func TestResourceMetadata(t *testing.T) {
	t.Parallel()

	m := NewResourceMetadata("https://forms.example.com/", testScopes)
	// The SDK client requires this to equal the MCP URL byte for byte.
	if m.Resource != "https://forms.example.com/mcp" {
		t.Errorf("Resource = %q", m.Resource)
	}
	if !slices.Equal(m.AuthorizationServers, []string{"https://forms.example.com"}) {
		t.Errorf("AuthorizationServers = %v", m.AuthorizationServers)
	}
	if !slices.Equal(m.ScopesSupported, testScopes) {
		t.Errorf("ScopesSupported = %v", m.ScopesSupported)
	}
}

func TestResourceURLs(t *testing.T) {
	t.Parallel()

	if got := ResourceURL("https://forms.example.com/"); got != "https://forms.example.com/mcp" {
		t.Errorf("ResourceURL = %q", got)
	}
	if got := ResourceMetadataURL("https://forms.example.com"); got != "https://forms.example.com"+PathResourceMetadata+"/mcp" {
		t.Errorf("ResourceMetadataURL = %q", got)
	}
}
