package oauth

import (
	"encoding/json"
	"log"
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
	// Only a write can fail here, and for a token response that means tokens
	// were minted and the client never saw them — its retry then looks like a
	// replay. Worth a line in the log when it happens.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("oauth: writing a %d response: %v", status, err)
	}
}

// RedirectURL adds params to a registered redirect URI, keeping the URI's own
// query (RFC 6749 §3.1.2) but letting ours win on a name collision: a client
// that reads the first "code" must read the one we issued.
//
// redirectURI has been matched against the client's registration, and
// registration only accepts what parses, so an error here means the registry
// holds something ValidateRedirectURI never accepted. It is an error rather
// than a redirect without the code, which would leave the client waiting on
// nothing.
func RedirectURL(redirectURI string, params url.Values) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
