// Package mcpserver exposes a dsforms instance over the Model Context Protocol,
// so an MCP client can read submissions, triage them, and ask what is in the
// database.
//
// It owns the MCP vocabulary — the scopes, the tool set, and the shapes those
// tools return — and reaches storage only through the Store interface it
// declares itself. It knows nothing about HTTP authentication: the caller wraps
// Handler in middleware that resolves a bearer token into scopes, and every tool
// reads those scopes back off the request.
//
// Two rules run through the whole package:
//
// Scope is enforced twice. The tool list a client sees is built from its token's
// scopes, and every handler checks again before touching the store. The listing
// is a hint to a client; the handler is the gate. One of the two being wrong
// must not be enough.
//
// Nothing here is a second implementation of a decision made elsewhere. The
// screening decision stays in internal/screen, the restore contract — which owes
// a withheld email and webhook — stays in internal/handler, and this package
// deliberately exposes no tool that would have to reimplement either.
package mcpserver

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Store is what this package needs from storage, and nothing else.
//
// Declared by the consumer, per AGENT.md §3, and pinned here rather than in
// main.go — the same arrangement internal/broadcaster uses, because this package
// is not a handler and main.go's assertion block is read by a test that requires
// every handler field to be wired.
type Store interface {
	// Reads.
	CountAllSubmissions() (int, error)
	GetSubmission(id string) (store.Submission, error)
	GetUserByID(id string) (store.User, error)
	HeldSince(days int) (held, total int, err error)
	HeldSubmissions(limit, offset int) ([]store.Submission, error)
	ListFilterRules() ([]screen.Rule, error)
	ListForms() ([]store.FormSummary, error)
	ListSubmissionsFiltered(formID string, read store.ReadFilter, limit, offset int) ([]store.Submission, error)
	NavCounts() (store.NavCounts, error)
	PerFormStats() ([]store.FormStats, error)
	SearchSubmissions(query string, limit int) ([]store.SearchResult, error)
	SubmissionSignals(submissionID string) ([]store.SpamSignal, error)
	SubmissionsPerDay(days int) ([]store.DayCounts, error)
	TopSpamSignals(days int) ([]store.SignalTally, error)

	// Writes.
	AddFilterRule(kind, ruleType, value, note string) (screen.Rule, error)
	MarkAllRead(formID string) error
	MarkRead(submissionID string) error
	MarkSpam(id, actor string) (store.Submission, error)
	MarkUnread(submissionID string) error

	// Destructive.
	DeleteHeld(ids []string) (int, error)
	DeleteSubmission(id string) error
}

var _ Store = (*store.Store)(nil)

// Paging bounds. A client that asks for everything gets a page, because the
// other end of this connection is a language model with a context window and an
// unbounded list is a denial of service against it as much as against us.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// maxRequestBody bounds an incoming JSON-RPC message.
//
// Set explicitly rather than left to the SDK default so this endpoint has one
// owner for the limit: the router's global 64KB MaxBytesReader is skipped for
// /mcp precisely so the two cannot disagree about which applies.
const maxRequestBody = 1 << 20 // 1 MiB

// Server builds the per-scope MCP servers and the HTTP handler in front of them.
type Server struct {
	store   Store
	version string

	// byScope holds one prebuilt mcp.Server per scope subset, keyed by
	// Scopes.Key(). There are 2^len(AllScopes) of them — eight today — so they
	// are built once at startup rather than per request, which would repeat JSON
	// schema inference for every tool on every call.
	//
	// Selecting a server by scope set is what makes tools/list honest: a
	// read-only token is not told about tools it cannot call. It is not the
	// security boundary — each handler checks its own scope — but a client that
	// is shown a tool and then refused has been told two different things.
	byScope map[string]*mcp.Server
}

// New builds the server. version is reported to clients in the initialize
// handshake, so a client can tell which dsforms it is talking to.
func New(st Store, version string) *Server {
	s := &Server{store: st, version: version, byScope: map[string]*mcp.Server{}}
	for _, scopes := range scopeSubsets() {
		s.byScope[scopes.Key()] = s.build(scopes)
	}
	return s
}

// scopeSubsets enumerates every combination of AllScopes, including the empty
// one — a token whose scopes column is empty or entirely unrecognised lands
// there and is served a server with no tools at all, which is the correct answer
// rather than an error.
//
// Derived from AllScopes rather than written out, so adding a scope does not
// leave a combination unbuilt. A missing key would make getServer return nil,
// which the SDK answers with 400 — a token silently unable to do anything.
func scopeSubsets() []Scopes {
	subsets := []Scopes{nil}
	for _, scope := range AllScopes {
		next := make([]Scopes, 0, len(subsets)*2)
		for _, existing := range subsets {
			next = append(next, existing)
			with := append(append(Scopes{}, existing...), scope)
			next = append(next, ParseScopes(with.Strings()))
		}
		subsets = next
	}
	return subsets
}

// build assembles the mcp.Server advertising exactly the tools these scopes
// allow.
func (s *Server) build(scopes Scopes) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "dsforms",
		Title:   "dsforms",
		Version: s.version,
		Description: "Read and triage form submissions held by a self-hosted " +
			"dsforms instance.",
	}, &mcp.ServerOptions{
		Instructions: "dsforms collects form submissions from static websites. " +
			"Submissions are either accepted (they appear in a form's inbox, " +
			"read or unread) or held in quarantine for spam review. " +
			"Marking a message as spam moves it to quarantine, where it can be " +
			"restored from the dsforms admin; it is not deleted.",
	})
	s.registerTools(srv, scopes)
	return srv
}

// Handler returns the HTTP handler for the MCP endpoint.
//
// It expects to be wrapped in bearer-token middleware that puts an auth.TokenInfo
// on the request context: with no token info the request is refused rather than
// served an unauthenticated server, which is the fail-closed direction if this
// is ever mounted without its middleware.
func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(s.serverFor, &mcp.StreamableHTTPOptions{
		// Stateless: there is no server-initiated work here, every tool call is
		// a self-contained request/response, and a session map would be state to
		// expire and to leak across tokens for no gain.
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: maxRequestBody,
		// DNS-rebinding protection is deliberately left at the SDK default (on).
	})
}

// serverFor picks the prebuilt server matching the request's token scopes.
//
// Returning nil makes the SDK answer 400, which is the right outcome for the two
// ways to get here without a token: the endpoint mounted without its middleware,
// or middleware that admitted a request it should not have.
func (s *Server) serverFor(r *http.Request) *mcp.Server {
	info := auth.TokenInfoFromContext(r.Context())
	if info == nil {
		log.Print("mcp: request reached the handler with no token info; refusing")
		return nil
	}
	scopes := ParseScopes(info.Scopes)
	srv, ok := s.byScope[scopes.Key()]
	if !ok {
		// Unreachable while byScope is derived from AllScopes, which is what
		// scopeSubsets guarantees. Said out loud anyway, because the failure is
		// otherwise a silent 400 for a perfectly valid token.
		log.Printf("mcp: no server built for scope set %q; refusing", scopes)
		return nil
	}
	return srv
}

// tokenScopes reads the calling token's scopes off a tool request.
//
// Absent token info means no scopes at all. A request cannot normally reach a
// handler without it — the middleware and serverFor both refuse first — so this
// is the third place the same thing is checked, and the only one where guessing
// wrong would grant rather than deny.
func tokenScopes(req *mcp.CallToolRequest) Scopes {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	return ParseScopes(req.Extra.TokenInfo.Scopes)
}

// requireScope is the gate every tool handler passes through first.
//
// It is not redundant with the per-scope server selection above: that decides
// what a client is *told* about, this decides what actually runs. A wiring
// mistake in one is caught by the other, and only this one is on the path that
// touches the database.
func requireScope(req *mcp.CallToolRequest, want Scope) error {
	if tokenScopes(req).Has(want) {
		return nil
	}
	return fmt.Errorf("this token does not carry the %q scope", want)
}

// actor names whoever is behind a token, for the record a write leaves behind.
//
// Falls back to the token's user id, and then to a marker, rather than to an
// empty string: a signal row reading "marked as spam by " is worse than one
// naming an id, and both are better than a write that cannot say who made it.
func (s *Server) actor(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return "an api token"
	}
	id := req.Extra.TokenInfo.UserID
	if id == "" {
		return "an api token"
	}
	user, err := s.store.GetUserByID(id)
	if err != nil {
		log.Printf("mcp: resolving actor %s: %v", id, err)
		return id
	}
	return user.Username
}

// clampLimit bounds a client-supplied page size.
//
// Zero means "unset" and takes the default, because that is what a client that
// omits the field sends. Negative is clamped the same way rather than rejected:
// there is no sensible page of -1 rows, and failing the call over it tells a
// model nothing it can act on.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	default:
		return n
	}
}

// clampOffset keeps a page offset non-negative. A negative OFFSET is a SQL error
// rather than a smaller page.
func clampOffset(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// clampDays bounds a reporting window.
func clampDays(n int) int {
	switch {
	case n <= 0:
		return 7
	case n > 90:
		return 90
	default:
		return n
	}
}

// rfc3339 renders a timestamp for a client. Empty for the zero time, so a
// missing value reads as missing rather than as the year 1.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
