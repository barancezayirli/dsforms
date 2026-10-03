package handler

import (
	"bytes"
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
	ExchangeAuthCode(raw string, accept func(store.AuthCode) bool) (store.TokenPair, error)
	RefreshGrant(raw, clientID string) (store.TokenPair, error)
	MakeRoomForOAuthClient(max int) (int64, error)

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
	// The path-suffixed form is RFC 9728 §3.1's metadata URL for /mcp, and the
	// one the 401 names. The bare form is the MCP spec's root fallback for
	// clients that do not follow resource_metadata; it describes the same
	// resource, which a client that insists the root document name the origin
	// will reject — such a client still finds the suffixed one first.
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

// MaxPendingOAuthClients caps registered clients with no live grant.
// Registration is open to anyone and the per-IP rate limit trusts
// X-Forwarded-For, so on its own it bounds nothing; this does. A real
// deployment approves a handful of clients, ever.
const MaxPendingOAuthClients = 500

// Register is open dynamic client registration. Registering grants nothing: a
// client can do nothing until an operator approves it on the consent page.
//
// At the cap, the oldest unapproved clients are evicted to make room, rather
// than new registrations refused: refusing would let anyone keep the door shut
// for every legitimate client with a few hundred requests. A real client is
// approved within a minute of registering, so it is never the oldest for long.
func (h *OAuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if n, err := h.Store.MakeRoomForOAuthClient(MaxPendingOAuthClients); err != nil {
		log.Printf("oauth: making room for a registration: %v", err)
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeServerError, Status: http.StatusInternalServerError})
		return
	} else if n > 0 {
		log.Printf("oauth: evicted %d unapproved clients to stay under %d", n, MaxPendingOAuthClients)
	}

	reg, err := oauth.ParseRegistration(r.Body)
	if err != nil {
		// ParseRegistration only returns *oauth.Error; anything else would be a
		// change there, and is answered as a bad request all the same.
		var oe *oauth.Error
		if !errors.As(err, &oe) {
			oe = &oauth.Error{Code: oauth.CodeInvalidClientMetadata}
		}
		oauth.WriteError(w, oe)
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
	// every scope the server advertises — clients built on the MCP SDK do — so
	// pre-ticking the request would pre-tick delete for nearly everyone.
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

	// The client is checked again, not trusted from the page: it may have been
	// pruned or evicted since the page was rendered.
	client, ok := h.trustedClient(w, r.PostForm.Get("client_id"), r.PostForm.Get("redirect_uri"))
	if !ok {
		return
	}
	// The signature covers the request and the operator it was shown to. A
	// form edited after rendering, posted by someone else, or left open past
	// its lifetime approves nothing, and goes nowhere.
	req, ok := h.Consent.VerifyForm(r.PostForm, user.ID)
	if !ok {
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

	h.redirect(w, r, req.RedirectURI, url.Values{
		"code":  {code},
		"state": {req.State},
		"iss":   {oauth.Issuer(h.BaseURL)},
	})
}

// trustedClient looks up a client and checks the redirect URI is one it
// registered, rendering a page — never a redirect — when either fails.
func (h *OAuthHandler) trustedClient(w http.ResponseWriter, clientID, redirectURI string) (store.OAuthClient, bool) {
	client, err := h.Store.GetOAuthClient(clientID)
	if errors.Is(err, store.ErrNotFound) {
		h.renderFatal(w, "This client is not registered with this dsforms.")
		return store.OAuthClient{}, false
	}
	if err != nil {
		log.Printf("oauth: looking up client %q: %v", clientID, err)
		h.renderFatal(w, "This request could not be checked right now. Try again in a moment.")
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
	h.redirect(w, r, redirectURI, params)
}

// redirect sends the browser to a verified redirect URI with params. A URI
// that will not parse cannot have passed registration, so it is a page and a
// log line, never a redirect that drops the code and leaves the client waiting.
func (h *OAuthHandler) redirect(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	to, err := oauth.RedirectURL(redirectURI, params)
	if err != nil {
		log.Printf("oauth: a registered redirect URI does not parse: %q: %v", redirectURI, err)
		h.renderFatal(w, "This client's return address is invalid.")
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

func (h *OAuthHandler) renderConsent(w http.ResponseWriter, r *http.Request, client store.OAuthClient, req oauth.AuthorizeRequest, choice accessChoice, askedFor, errMsg string, status int) {
	user, _ := auth.UserFromContext(r.Context())
	forms, err := h.Store.ListForms(store.AllForms())
	formsUnavailable := err != nil
	if err != nil {
		log.Printf("oauth: listing forms for the consent page: %v", err)
	}
	host := req.RedirectURI
	if u, err := url.Parse(req.RedirectURI); err == nil {
		host = u.Host
	}
	fields := newAccessFields(choice, forms)
	// Without the list, the page must not say there are no forms: on a consent
	// screen that reads as "this reaches everything you will ever create".
	fields.FormsUnavailable = formsUnavailable
	h.renderPage(w, status, consentData{
		accessFields:  fields,
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

// renderPage renders into a buffer first, so a template failure is a 500 and
// never half a consent form under a success status.
func (h *OAuthHandler) renderPage(w http.ResponseWriter, status int, data consentData) {
	tmpl := h.Templates["oauth_consent.html"]
	if tmpl == nil {
		log.Printf("oauth: the consent template is not registered")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("oauth: consent template: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page carries a signed approval; it must not be served from a cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
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
	_, _, usedBasic := r.BasicAuth()
	if clientID == "" {
		// Public clients, but some send their id the client_secret_basic way.
		clientID, _, _ = r.BasicAuth()
	}
	client, err := h.Store.GetOAuthClient(clientID)
	if errors.Is(err, store.ErrNotFound) {
		// RFC 6749 §5.2: a client that authenticated with the Authorization
		// header is told which scheme failed.
		if usedBasic {
			w.Header().Set("WWW-Authenticate", `Basic realm="dsforms"`)
		}
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidClient, Status: http.StatusUnauthorized})
		return
	}
	if err != nil {
		// Not "you are not registered": a client told that discards its
		// registration and starts over, for a database hiccup.
		log.Printf("oauth: looking up client %q at the token endpoint: %v", clientID, err)
		oauth.WriteError(w, serverError)
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

// serverError is a transient failure. Clients retry it, and keep the code or
// refresh token they hold — which is right, because the transaction that
// failed did not spend it. invalid_grant would make them discard a credential
// that still works.
var serverError = &oauth.Error{Code: oauth.CodeServerError, Status: http.StatusInternalServerError}

func (h *OAuthHandler) exchangeCode(w http.ResponseWriter, r *http.Request, client store.OAuthClient) {
	resource := r.PostForm.Get("resource")
	// Checked inside the exchange's transaction, after the code is spent: a
	// failed check spends it too, so an intercepted code is worth one guess at
	// the verifier. The grant is written from the stored code, not from this.
	accept := func(code store.AuthCode) bool {
		return code.ClientID == client.ID &&
			code.RedirectURI == r.PostForm.Get("redirect_uri") &&
			oauth.VerifyPKCE(r.PostForm.Get("code_verifier"), code.CodeChallenge) &&
			(resource == "" || oauth.SameResource(resource, code.Resource))
	}
	pair, err := h.Store.ExchangeAuthCode(r.PostForm.Get("code"), accept)
	if err != nil {
		h.grantError(w, client, "an authorization code", err, store.ErrCodeReused)
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
		h.grantError(w, client, "a refresh token", err, store.ErrRefreshReused)
		return
	}
	h.writeTokens(w, client, pair)
}

// grantError answers a failed exchange or refresh.
//
// A refused credential is invalid_grant, with no detail on which check failed —
// that detail is for whoever is guessing. A replay is invalid_grant too, after
// the store has revoked what the credential issued; if that revocation failed,
// tokens it meant to kill may be live, so it is logged as exactly that and
// answered 500. Anything else is a transient failure: server_error, so the
// client retries with a credential that still works.
func (h *OAuthHandler) grantError(w http.ResponseWriter, client store.OAuthClient, what string, err, replay error) {
	switch {
	case errors.Is(err, store.ErrRevokeFailed):
		log.Printf("oauth: SECURITY: client %s presented %s that was already used, and revoking what it issued FAILED; "+
			"those tokens may still be live — revoke the connection in the admin: %v", client.ID, what, err)
		oauth.WriteError(w, serverError)
	case errors.Is(err, replay):
		log.Printf("oauth: client %s presented %s that was already used; anything it issued is revoked", client.ID, what)
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidGrant})
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrCodeRejected):
		oauth.WriteError(w, &oauth.Error{Code: oauth.CodeInvalidGrant})
	default:
		log.Printf("oauth: client %s presenting %s: %v", client.ID, what, err)
		oauth.WriteError(w, serverError)
	}
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
