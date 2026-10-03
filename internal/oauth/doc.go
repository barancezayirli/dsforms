// Package oauth holds the protocol rules of dsforms' OAuth 2.1 authorization
// server for MCP clients: PKCE, redirect-URI rules, client registration, the
// consent signature, error encoding and discovery metadata.
//
// It is a leaf. Nothing here touches the database or knows what a scope means;
// the store persists clients, codes and grants, mcpserver owns the scope set,
// and handler wires them together. Keeping the protocol rules here is what lets
// every one of them be tested without a server running.
//
// The tokens this server issues are ordinary api_tokens rows with an expiry, so
// everything after issuance — verification, scopes, form binding, revocation —
// is the same code path a hand-made token takes.
package oauth
