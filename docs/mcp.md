# MCP

dsforms can serve itself over the [Model Context Protocol][mcp], so an MCP
client — Claude Desktop, an editor, your own agent — can work through your
inbox: list what you have not read, read one, mark spam, and ask what is in the
database.

It is off by default, and it is not registered at all until you turn it on.

[mcp]: https://modelcontextprotocol.io

## Turning it on

```bash
MCP_ENABLED=true
BASE_URL=https://forms.example.com   # must be https, see below
```

The endpoint is `POST https://forms.example.com/mcp`, speaking Streamable HTTP.

**dsforms refuses to start if `MCP_ENABLED` is set and `BASE_URL` is not
`https://`.** An API token rides an `Authorization` header on every single
request, so over plain http it is readable by anything between the client and
your server. dsforms never terminates TLS itself — it is designed to sit behind
a proxy that does — so `BASE_URL` is the only thing that can tell it how clients
actually reach it.

For localhost or a trusted private network, set `MCP_ALLOW_INSECURE=true`. It
starts, and warns about it on every boot.

## Creating a token

Either from the admin at **System → API tokens**, or from the CLI:

```bash
docker compose exec dsforms ./dsforms token create admin "laptop" read,write
docker compose exec dsforms ./dsforms token list admin
docker compose exec dsforms ./dsforms token revoke admin <token-id>
```

The CLI is the safer of the two — the value is printed to a terminal you already
trust rather than crossing a network — and on a fresh install it is the only
one, since you may want the endpoint before anyone has signed in.

**The token is shown once.** Only a SHA-256 hash is stored, so it cannot be
recovered: if you lose it, revoke it and make another. Tokens belong to a user,
so deleting that user revokes theirs in the same statement, and each one is
revocable on its own without disturbing the others.

By default a token never expires. `MCP_TOKEN_TTL_DAYS` sets the lifetime of
tokens created through the admin. The CLI does not read your `.env` — that
would demand `SECRET_KEY` and make it useless on a fresh install, which is when
it is most wanted — so it takes the days as a fourth argument instead:

```bash
docker compose exec dsforms ./dsforms token create admin "ci" read 90
```

## Connecting a client

Most clients take an endpoint and a header. The shape is:

```json
{
  "mcpServers": {
    "dsforms": {
      "type": "http",
      "url": "https://forms.example.com/mcp",
      "headers": { "Authorization": "Bearer dsf_…" }
    }
  }
}
```

To check it by hand:

```bash
curl -s -X POST https://forms.example.com/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":
       {"protocolVersion":"2025-06-18","capabilities":{},
        "clientInfo":{"name":"curl","version":"1"}}}'
```

An unknown, revoked or expired token all answer `401`, with an RFC 6750
challenge:

```
WWW-Authenticate: Bearer realm="dsforms", error="invalid_token"
```

They are deliberately not distinguished from each other: telling them apart is
a distinction only useful to someone guessing.

dsforms serves **no** OAuth protected-resource metadata (RFC 9728), because it
has no authorization server to point at — these are static tokens you mint.
Advertising a discovery flow that goes nowhere would be worse than not
advertising one, so a client that insists on completing OAuth discovery will
not connect. Clients that accept a bearer token you configure will.

## Scopes

A token carries any combination of three scopes. They are enforced twice — the
tool list a client is shown is built from its scopes, and every tool checks
again before it touches the database.

| Scope | Tools |
|---|---|
| `read` | `list_forms`, `list_submissions`, `get_submission`, `search_submissions`, `list_quarantine`, `list_filter_rules`, `get_stats` |
| `write` | `mark_read`, `mark_all_read`, `mark_spam`, `add_block_rule` |
| `delete` | `delete_submission`, `delete_quarantined` |

`delete` is separate from `write` on purpose: it is the one class of action
nothing can undo, and a token that files spam should not also be able to erase
the evidence. Give a client `read` unless it needs more.

## The tools

**Submitter IP addresses are withheld by default.** They are your data and they
are what an IP block rule is written from, but an MCP client is a language model
with a context window and usually a vendor behind it, so sending every
submitter's address there should be a decision rather than a default. Set
`MCP_INCLUDE_IPS=true` to include them. The admin UI shows them either way.

**Reading.** `list_submissions` defaults to unread and never includes
quarantined submissions — `list_quarantine` is for those, and it carries the
recorded reasons each was held. `get_stats` answers the "what is in the
database" question: unread, quarantined, waitlist entries, totals, per-form and
per-day breakdowns, and which spam checks are firing most.

**`mark_spam` does not delete.** It moves an accepted submission into the same
quarantine the spam filter uses, where the admin can restore it. The submission
keeps the score the filter originally gave it — so a manually held one shows a
score *below* its threshold, which is the truth: no check fired, a person
decided — and a signal is recorded naming both the account and the token that
did it, so with several clients on one account you can tell which one acted.

Restoring is deliberately **not** an MCP tool. A restore owes the notification
email and webhook that the hold withheld, and that logic lives in the admin; a
second copy here is how the two drift apart. Use the quarantine screen.

**`add_block_rule` cannot create an allow rule.** Allow rules skip the block
list and all scoring, and an IP or CIDR allow rule turns one forgeable
`X-Forwarded-For` header into a filter bypass. Add those from the admin, where
you can see what you are doing.

**`delete_quarantined` cannot reach an inbox.** Ids that are not in quarantine
match nothing, and it reports how many actually went rather than how many you
asked for. It refuses an empty list rather than reading it as "all of them".

## Limits

MCP traffic gets its own rate limit — more generous than the form-submit budget,
because a client working through an inbox makes a burst of small calls — so an
API client cannot evict the buckets of real form submitters. Token guessing is
locked out after 10 failures from an IP for 15 minutes, before any database
lookup happens.

Listings are paged: 25 rows by default, 100 at most.

Requests are capped at 1MB, rather than the 64KB that applies everywhere else.
