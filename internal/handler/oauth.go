package handler

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/mcpserver"
	"github.com/barancezayirli/dsforms/internal/oauth"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// OAuthStore is what the authorization server needs from storage.
type OAuthStore interface {
	CreateOAuthClient(name string, redirectURIs, grantTypes []string) (store.OAuthClient, error)
	GetOAuthClient(id string) (store.OAuthClient, error)
	CreateAuthCode(c store.AuthCode) (string, error)
	RedeemAuthCode(raw string) (store.AuthCode, error)
	IssueGrant(code store.AuthCode) (store.TokenPair, error)
	RefreshGrant(raw, clientID string) (store.TokenPair, error)

	// ListForms is for the consent page's form picker, as on the token form.
	ListForms(forms store.FormScope) ([]store.FormSummary, error)
}

// OAuthHandler is the authorization server MCP clients sign in through when
// MCP_OAUTH is on. The protocol rules live in internal/oauth; this wires them
// to the store, the session and the consent page.
type OAuthHandler struct {
	Base
	Store   OAuthStore
	Consent oauth.ConsentSigner
}

// Mount registers the OAuth routes. One function for main.go and the tests, so
// the routes under test are the routes that ship. requireAuth guards the
// consent page; limit rate-limits the endpoints anyone on the internet can call.
func (h *OAuthHandler) Mount(r chi.Router, requireAuth, limit func(http.Handler) http.Handler) {
	scopes := scopeNames()

	prm := mcpauth.ProtectedResourceMetadataHandler(oauth.NewResourceMetadata(h.BaseURL, scopes))
	// Both forms: the SDK client tries the path-suffixed one first, and other
	// clients read the bare one (RFC 9728 §3.1).
	r.Handle(oauth.PathResourceMetadata, prm)
	r.Handle(oauth.PathResourceMetadata+oauth.PathMCP, prm)

	r.Group(func(r chi.Router) {
		r.Use(cors)
		r.Get(oauth.PathServerMetadata, h.ServerMetadata)
		r.With(limit).Post(oauth.PathRegister, h.Register)
		r.With(limit).Post(oauth.PathToken, h.Token)
		for _, p := range []string{oauth.PathServerMetadata, oauth.PathRegister, oauth.PathToken} {
			r.Options(p, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
		}
	})

	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get(oauth.PathAuthorize, h.AuthorizePage)
		r.Post(oauth.PathAuthorize, h.AuthorizeSubmit)
	})
}

// cors opens the metadata, registration and token endpoints to browser-based
// clients. Safe with "*": nothing on these routes reads a cookie, so a
// cross-origin caller gets nothing a direct request would not.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version")
		next.ServeHTTP(w, r)
	})
}

// scopeNames is the scope vocabulary as mcpserver owns it.
func scopeNames() []string {
	return mcpserver.Scopes(mcpserver.AllScopes).Strings()
}

// ServerMetadata serves RFC 8414 authorization server metadata.
func (h *OAuthHandler) ServerMetadata(w http.ResponseWriter, r *http.Request) {
	oauth.WriteJSON(w, http.StatusOK, oauth.NewServerMetadata(h.BaseURL, scopeNames()))
}

// registrationResponse is RFC 7591 §3.2.1, for a public client.
type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register is open dynamic client registration. Registering grants nothing: a
// client can do nothing until an operator approves it on the consent page, and
// one nobody approves is pruned after a day.
func (h *OAuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	reg, err := oauth.ParseRegistration(r.Body)
	if err != nil {
		var oe *oauth.Error
		if errors.As(err, &oe) {
			oauth.WriteError(w, oe)
			return
		}
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidClientMetadata})
		return
	}
	c, err := h.Store.CreateOAuthClient(reg.Name, reg.RedirectURIs, reg.GrantTypes)
	if err != nil {
		log.Printf("oauth: registering a client: %v", err)
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeServerError, Status: http.StatusInternalServerError})
		return
	}
	log.Printf("oauth: registered client %s (%q)", c.ID, c.Name)
	oauth.WriteJSON(w, http.StatusCreated, registrationResponse{
		ClientID:                c.ID,
		ClientIDIssuedAt:        c.CreatedAt.Unix(),
		ClientName:              c.Name,
		RedirectURIs:            c.RedirectURIs,
		GrantTypes:              c.GrantTypes,
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
}

// consentData is the consent page.
type consentData struct {
	accessFields
	AssetVer string
	Version  string

	// Fatal replaces the form: the request cannot be trusted enough to send the
	// browser back to the client, so the page says so and goes nowhere.
	Fatal string
	// Error is a fixable problem with what was chosen.
	Error string

	Username     string
	ClientName   string
	RedirectHost string
	// AskedFor is the scopes the client requested, for the operator to read.
	// They are not pre-ticked; see AuthorizePage.
	AskedFor string

	// The request being approved, posted back with its signature.
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Resource      string
	Signature     string
}

// AuthorizePage validates an authorization request and shows the consent page.
//
// The order matters. The client and redirect URI are established first, and
// any failure there renders a page: until both are known good, redirecting
// would hand the browser — and the error — to a URI an attacker chose. Only
// then are protocol errors sent back to the client, as RFC 6749 §4.1.2.1 asks.
func (h *OAuthHandler) AuthorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, ok := h.trustedClient(w, q.Get("client_id"), q.Get("redirect_uri"))
	if !ok {
		return
	}

	req, oe := oauth.ParseAuthorizeRequest(q, oauth.ResourceURL(h.BaseURL))
	if oe != nil {
		h.redirectError(w, r, req.RedirectURI, req.State, oe.Code, oe.Description)
		return
	}

	// The boxes start at read, whatever the client asked for — the rule the
	// token form follows, for the reason it gives: a form that opens with more
	// ticked makes granting more the path of least resistance. Clients ask for
	// every scope the server advertises (Claude Code did, at the checkpoint),
	// so pre-ticking the request would have pre-ticked delete for everyone.
	// What was asked for is shown instead, and the operator ticks the rest.
	var asked []string
	for _, s := range req.Scopes {
		if slices.Contains(scopeNames(), s) && !slices.Contains(asked, s) {
			asked = append(asked, s)
		}
	}
	h.renderConsent(w, r, client, req, accessChoice{Scopes: []string{string(mcpserver.ScopeRead)}, AllForms: true},
		strings.Join(asked, ", "), "", http.StatusOK)
}

// AuthorizeSubmit records the operator's decision.
func (h *OAuthHandler) AuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFatal(w, "That request could not be read.")
		return
	}
	user, _ := auth.UserFromContext(r.Context())

	client, ok := h.trustedClient(w, r.PostForm.Get("client_id"), r.PostForm.Get("redirect_uri"))
	if !ok {
		return
	}
	req := oauth.AuthorizeRequest{
		ClientID:      client.ID,
		RedirectURI:   r.PostForm.Get("redirect_uri"),
		State:         r.PostForm.Get("state"),
		CodeChallenge: r.PostForm.Get("code_challenge"),
		Resource:      r.PostForm.Get("resource"),
	}
	// The signature covers the request and the operator it was shown to. A
	// form edited after rendering, posted by someone else, or left open past
	// its lifetime approves nothing, and goes nowhere.
	if !h.Consent.Verify(r.PostForm.Get("consent"), user.ID, req) {
		h.renderFatal(w, "This approval has expired or was not issued to you.")
		return
	}

	if r.PostForm.Get("action") != "approve" {
		log.Printf("oauth: %s denied client %s (%q)", user.Username, client.ID, client.Name)
		h.redirectError(w, r, req.RedirectURI, req.State, oauth.CodeAccessDenied, "")
		return
	}

	choice := readAccessChoice(r)
	scopes, formIDs, err := choice.validate(h.Store)
	if err != nil {
		h.renderConsent(w, r, client, req, choice, "", capitalise(err.Error())+".", http.StatusOK)
		return
	}

	code, err := h.Store.CreateAuthCode(store.AuthCode{
		ClientID:      client.ID,
		UserID:        user.ID,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		Resource:      req.Resource,
		Scopes:        scopes.Strings(),
		FormIDs:       formIDs,
	})
	if err != nil {
		log.Printf("oauth: creating a code for client %s: %v", client.ID, err)
		h.redirectError(w, r, req.RedirectURI, req.State, oauth.CodeServerError, "")
		return
	}
	log.Printf("oauth: %s approved client %s (%q) with scopes %s, %s",
		user.Username, client.ID, client.Name, scopes, store.ParseFormScope(strings.Join(formIDs, ",")))

	http.Redirect(w, r, oauth.RedirectURL(req.RedirectURI, url.Values{
		"code":  {code},
		"state": {req.State},
		"iss":   {oauth.Issuer(h.BaseURL)},
	}), http.StatusFound)
}

// trustedClient looks up a client and checks the redirect URI is one it
// registered, rendering a page — never a redirect — when either fails.
func (h *OAuthHandler) trustedClient(w http.ResponseWriter, clientID, redirectURI string) (store.OAuthClient, bool) {
	client, err := h.Store.GetOAuthClient(clientID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("oauth: looking up client %q: %v", clientID, err)
		}
		h.renderFatal(w, "This client is not registered with this dsforms.")
		return store.OAuthClient{}, false
	}
	if !oauth.MatchRedirectURI(client.RedirectURIs, redirectURI) {
		h.renderFatal(w, "This request names a return address the client did not register.")
		return store.OAuthClient{}, false
	}
	return client, true
}

// redirectError sends an error back to a client whose redirect URI has
// already been matched against its registration.
func (h *OAuthHandler) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	params := url.Values{"error": {code}, "iss": {oauth.Issuer(h.BaseURL)}}
	if state != "" {
		params.Set("state", state)
	}
	if description != "" {
		params.Set("error_description", description)
	}
	http.Redirect(w, r, oauth.RedirectURL(redirectURI, params), http.StatusFound)
}

func (h *OAuthHandler) renderConsent(w http.ResponseWriter, r *http.Request, client store.OAuthClient, req oauth.AuthorizeRequest, choice accessChoice, askedFor, errMsg string, status int) {
	user, _ := auth.UserFromContext(r.Context())
	forms, err := h.Store.ListForms(store.AllForms())
	if err != nil {
		log.Printf("oauth: listing forms for the consent page: %v", err)
	}
	host := req.RedirectURI
	if u, err := url.Parse(req.RedirectURI); err == nil {
		host = u.Host
	}
	h.renderPage(w, status, consentData{
		accessFields:  newAccessFields(choice, forms),
		AssetVer:      h.AssetVer,
		Version:       h.Version,
		Error:         errMsg,
		AskedFor:      askedFor,
		Username:      user.Username,
		ClientName:    client.Name,
		RedirectHost:  host,
		ClientID:      client.ID,
		RedirectURI:   req.RedirectURI,
		State:         req.State,
		CodeChallenge: req.CodeChallenge,
		Resource:      req.Resource,
		Signature:     h.Consent.Sign(user.ID, req),
	})
}

func (h *OAuthHandler) renderFatal(w http.ResponseWriter, msg string) {
	h.renderPage(w, http.StatusBadRequest, consentData{AssetVer: h.AssetVer, Version: h.Version, Fatal: msg})
}

func (h *OAuthHandler) renderPage(w http.ResponseWriter, status int, data consentData) {
	tmpl := h.Templates["oauth_consent.html"]
	if tmpl == nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page carries a signed approval; it must not be served from a cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("oauth: consent template: %v", err)
	}
}

// tokenResponse is RFC 6749 §5.1.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

// Token is the token endpoint: authorization_code and refresh_token grants for
// public clients. Every refusal of a presented credential is invalid_grant, with
// no detail on which check failed — that detail is for whoever is guessing.
func (h *OAuthHandler) Token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidRequest})
		return
	}
	clientID := r.PostForm.Get("client_id")
	if clientID == "" {
		// Public clients, but some send their id the client_secret_basic way.
		clientID, _, _ = r.BasicAuth()
	}
	client, err := h.Store.GetOAuthClient(clientID)
	if err != nil {
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidClient, Status: http.StatusUnauthorized})
		return
	}

	switch r.PostForm.Get("grant_type") {
	case oauth.GrantAuthorizationCode:
		h.exchangeCode(w, r, client)
	case oauth.GrantRefreshToken:
		h.refresh(w, r, client)
	default:
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeUnsupportedGrantType})
	}
}

func (h *OAuthHandler) exchangeCode(w http.ResponseWriter, r *http.Request, client store.OAuthClient) {
	invalid := &oauth.Error{Code: oauth.CodeInvalidGrant}

	// Redeeming spends the code before any check below, so a failed check
	// spends it too: one guess at the verifier per intercepted code.
	code, err := h.Store.RedeemAuthCode(r.PostForm.Get("code"))
	if err != nil {
		if errors.Is(err, store.ErrCodeReused) {
			log.Printf("oauth: client %s replayed an authorization code; its grant is revoked", client.ID)
		} else if !errors.Is(err, store.ErrNotFound) {
			log.Printf("oauth: redeeming a code: %v", err)
		}
		oauth.WriteError(w, invalid)
		return
	}
	resource := r.PostForm.Get("resource")
	if code.ClientID != client.ID ||
		code.RedirectURI != r.PostForm.Get("redirect_uri") ||
		!oauth.VerifyPKCE(r.PostForm.Get("code_verifier"), code.CodeChallenge) ||
		(resource != "" && !oauth.SameResource(resource, code.Resource)) {
		oauth.WriteError(w, invalid)
		return
	}

	pair, err := h.Store.IssueGrant(code)
	if err != nil {
		log.Printf("oauth: issuing a grant to client %s: %v", client.ID, err)
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeServerError, Status: http.StatusInternalServerError})
		return
	}
	h.writeTokens(w, client, pair)
}

func (h *OAuthHandler) refresh(w http.ResponseWriter, r *http.Request, client store.OAuthClient) {
	if !slices.Contains(client.GrantTypes, oauth.GrantRefreshToken) {
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeUnauthorizedClient})
		return
	}
	pair, err := h.Store.RefreshGrant(r.PostForm.Get("refresh_token"), client.ID)
	if err != nil {
		if errors.Is(err, store.ErrRefreshReused) {
			log.Printf("oauth: client %s reused a refresh token; its grant is revoked", client.ID)
		} else if !errors.Is(err, store.ErrNotFound) {
			log.Printf("oauth: refreshing for client %s: %v", client.ID, err)
		}
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidGrant})
		return
	}
	h.writeTokens(w, client, pair)
}

// writeTokens answers a successful grant. A client that did not register for
// refresh is not given a refresh token, though the grant holds one.
func (h *OAuthHandler) writeTokens(w http.ResponseWriter, client store.OAuthClient, pair store.TokenPair) {
	resp := tokenResponse{
		AccessToken: pair.Access,
		TokenType:   "Bearer",
		ExpiresIn:   int64(pair.ExpiresIn.Seconds()),
		Scope:       strings.Join(pair.Scopes, " "),
	}
	if slices.Contains(client.GrantTypes, oauth.GrantRefreshToken) {
		resp.RefreshToken = pair.Refresh
	}
	oauth.WriteJSON(w, http.StatusOK, resp)
}
