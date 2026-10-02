package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// OAuth error codes (RFC 6749 §4.1.2.1 and §5.2, RFC 7591 §3.2.2, RFC 8707 §2).
// They are wire strings, not Go errors, hence the Code prefix.
const (
	CodeInvalidRequest          = "invalid_request"
	CodeInvalidClient           = "invalid_client"
	CodeInvalidGrant            = "invalid_grant"
	CodeUnauthorizedClient      = "unauthorized_client"
	CodeUnsupportedGrantType    = "unsupported_grant_type"
	CodeInvalidScope            = "invalid_scope"
	CodeAccessDenied            = "access_denied"
	CodeUnsupportedResponseType = "unsupported_response_type"
	CodeServerError             = "server_error"
	CodeTemporarilyUnavailable  = "temporarily_unavailable"
	CodeInvalidTarget           = "invalid_target"
	CodeInvalidRedirectURI      = "invalid_redirect_uri"
	CodeInvalidClientMetadata   = "invalid_client_metadata"
)

// Error is an OAuth error response. Status 0 means 400, the status RFC 6749
// §5.2 gives almost every token-endpoint error.
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Description == "" {
		return e.Code
	}
	return e.Code + ": " + e.Description
}

func errorf(code, description string) *Error {
	return &Error{Code: code, Description: description}
}

// WriteError writes e as the JSON body RFC 6749 §5.2 describes.
func WriteError(w http.ResponseWriter, e *Error) {
	status := e.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	body := struct {
		Error       string `json:"error"`
		Description string `json:"error_description,omitempty"`
	}{e.Code, e.Description}
	WriteJSON(w, status, body)
}

// WriteJSON writes v with the headers every OAuth endpoint response carries:
// these bodies hold credentials or describe them, and must never be cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RedirectURL adds params to a registered redirect URI, keeping the URI's own
// query (RFC 6749 §3.1.2) but letting ours win on a name collision: a client
// that reads the first "code" must read the one we issued.
//
// redirectURI has already been matched against the client's registration, so
// it parses; a failure here would mean the registry holds something
// ValidateRedirectURI never accepted.
func RedirectURL(redirectURI string, params url.Values) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String()
}
