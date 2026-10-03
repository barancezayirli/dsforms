package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Grant types this server issues.
const (
	GrantAuthorizationCode = "authorization_code"
	GrantRefreshToken      = "refresh_token"
)

// Registration limits and defaults.
const (
	MaxRedirectURIs    = 10
	MaxClientNameRunes = 100
	DefaultClientName  = "Unnamed client"
)

// Registration is a client registration request (RFC 7591) reduced to what
// dsforms keeps. Every client is public: there are no secrets to issue, and the
// token endpoint authenticates the code with PKCE instead.
type Registration struct {
	Name         string
	RedirectURIs []string
	GrantTypes   []string
}

// registrationRequest decodes only the fields dsforms reads. Unknown fields are
// ignored, which matters for jwks: the SDK's own type declares it a string, and
// a client sending the object RFC 7591 describes would fail to decode against it.
type registrationRequest struct {
	ClientName    string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
}

// ParseRegistration reads and validates a registration request body.
//
// RFC 7591 §2 lets the server replace requested values, and dsforms does so
// where refusing would only lock clients out: grant types are narrowed to the
// two this server issues (a request without authorization_code is refused),
// and the requested auth method is ignored — every client is registered as
// public, and the handler answers "none" whatever was asked.
func ParseRegistration(body io.Reader) (Registration, error) {
	var req registrationRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return Registration{}, errorf(CodeInvalidClientMetadata, "body is not a JSON registration request")
	}

	if len(req.RedirectURIs) == 0 {
		return Registration{}, errorf(CodeInvalidRedirectURI, "redirect_uris is required")
	}
	if len(req.RedirectURIs) > MaxRedirectURIs {
		return Registration{}, errorf(CodeInvalidRedirectURI, fmt.Sprintf("at most %d redirect_uris", MaxRedirectURIs))
	}
	for _, u := range req.RedirectURIs {
		if err := ValidateRedirectURI(u); err != nil {
			return Registration{}, errorf(CodeInvalidRedirectURI, err.Error())
		}
	}

	if len(req.ResponseTypes) > 0 && !slices.Contains(req.ResponseTypes, "code") {
		return Registration{}, errorf(CodeInvalidClientMetadata, `response_types must include "code"`)
	}

	// Omitted means authorization_code (RFC 7591 §2). Refresh comes with it,
	// because a connector that cannot refresh stops working hourly.
	grants := []string{GrantAuthorizationCode, GrantRefreshToken}
	if len(req.GrantTypes) > 0 {
		if !slices.Contains(req.GrantTypes, GrantAuthorizationCode) {
			return Registration{}, errorf(CodeInvalidClientMetadata, `grant_types must include "authorization_code"`)
		}
		grants = []string{GrantAuthorizationCode}
		if slices.Contains(req.GrantTypes, GrantRefreshToken) {
			grants = append(grants, GrantRefreshToken)
		}
	}

	return Registration{
		Name:         clientName(req.ClientName),
		RedirectURIs: req.RedirectURIs,
		GrantTypes:   grants,
	}, nil
}

// clientName trims and caps the name a client chose for itself. It is shown on
// the consent page, so it is bounded by runes, never cut mid-character.
func clientName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return DefaultClientName
	}
	if r := []rune(name); len(r) > MaxClientNameRunes {
		name = string(r[:MaxClientNameRunes])
	}
	return name
}
