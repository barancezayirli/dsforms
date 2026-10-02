package oauth

import (
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Paths this server answers when OAuth is on.
const (
	PathAuthorize        = "/oauth/authorize"
	PathToken            = "/oauth/token"
	PathRegister         = "/oauth/register"
	PathServerMetadata   = "/.well-known/oauth-authorization-server"
	PathResourceMetadata = "/.well-known/oauth-protected-resource"
	PathMCP              = "/mcp"
)

// ServerMetadata is the RFC 8414 document. It is a local type because the
// SDK's always emits "jwks_uri", and an empty one invites a client to fetch "".
type ServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// Issuer is BASE_URL without a trailing slash. The SDK client compares the
// issuer it is given against the one it discovered and refuses a mismatch.
func Issuer(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/")
}

// ResourceURL is the MCP endpoint, which is the protected resource.
func ResourceURL(baseURL string) string {
	return Issuer(baseURL) + PathMCP
}

// ResourceMetadataURL is where the 401 challenge points a client. The path
// form is the one the SDK client tries first.
func ResourceMetadataURL(baseURL string) string {
	return Issuer(baseURL) + PathResourceMetadata + PathMCP
}

// NewServerMetadata describes this authorization server.
func NewServerMetadata(baseURL string, scopes []string) ServerMetadata {
	iss := Issuer(baseURL)
	return ServerMetadata{
		Issuer:                            iss,
		AuthorizationEndpoint:             iss + PathAuthorize,
		TokenEndpoint:                     iss + PathToken,
		RegistrationEndpoint:              iss + PathRegister,
		ScopesSupported:                   scopes,
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{GrantAuthorizationCode, GrantRefreshToken},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		// Advertised, so iss is always sent: the SDK client requires it once
		// advertised and refuses it when not (RFC 9207, mix-up defence).
		AuthorizationResponseIssParameterSupported: true,
	}
}

// NewResourceMetadata describes the MCP endpoint as a protected resource
// (RFC 9728), naming this instance as its only authorization server.
func NewResourceMetadata(baseURL string, scopes []string) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               ResourceURL(baseURL),
		AuthorizationServers:   []string{Issuer(baseURL)},
		ScopesSupported:        scopes,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "dsforms",
	}
}
