# Development

```bash
# Run locally (needs Go 1.25+)
cp .env.example .env
SECRET_KEY=dev SMTP_HOST=localhost SMTP_PORT=1025 SMTP_FROM="Dev <dev@test.com>" go run .

make test          # go test -race ./...
make build         # build the binary
make dev-up        # docker compose with Mailpit on :8025
```

[`AGENT.md`](../AGENT.md) is the working agreement — architecture, dependency
rules, SQLite and HTTP conventions, and the testing standard. Read it before
changing anything; it is written for whoever does the work next.

## Project structure

```
dsforms/
├── main.go                    # CLI dispatch + server wiring, no logic
├── internal/
│   ├── config/                # Environment loading
│   ├── store/                 # Every SQL statement in the project
│   ├── screen/                # The spam hold/accept decision, sealed
│   ├── urlsafe/               # Which URLs we hand out or call
│   ├── auth/                  # Session tokens + RequireAuth
│   ├── flash/                 # One-time flash messages
│   ├── mail/                  # SMTP notifications
│   ├── webhook/               # Slack / Discord / generic JSON
│   ├── ratelimit/             # Token bucket + login guard
│   ├── backup/                # Export (VACUUM INTO) + import (atomic swap)
│   ├── astcheck/              # Shared harness for the structural guards
│   └── handler/               # HTTP handlers
├── templates/                 # Go html/template
├── static/                    # One vanilla JS file, no framework
└── docs/                      # This documentation
```

Dependencies point one way and there are no cycles. `internal/screen` is sealed
behind a nested `internal/`, so the compiler — not a convention — stops anything
outside it reaching the scoring internals.

## Tech stack

- **Go** — single binary, `CGO_ENABLED=0`, no runtime dependencies
- **SQLite** via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO
- **chi** — HTTP routing
- **html/template** — server-rendered
- Vanilla JS for the drawer, copy-to-clipboard, bulk select and mobile nav

## Testing

The suite is unusually opinionated, and `AGENT.md` §7 explains why. In short:
tests are written before the fix and watched failing; assertions describe the
behaviour rather than its absence; structural guards ship with fixtures that
must trip them, because a guard nobody has watched fail is not a guard.

A green suite is necessary and not sufficient. An open redirect, a database size
that ignored the write-ahead log, and a drawer that needed five presses of Back
all shipped past a green suite and were found by running the thing.
