# Waitlist Feature — Design

**Date:** 2026-05-29
**Status:** Approved (brainstorm), pending implementation plan

## Summary

Add a waitlist feature to dsforms: collect unique people by email, show them their
signup position, optionally auto-email a confirmation on join, and broadcast a message
to the whole list later (e.g. a launch announcement).

A waitlist is a **separate entity** from a Form — its own tables, admin tab, and public
endpoint — but it intentionally *behaves like a form* on the public side: static sites
POST to it the same way they POST to `/f/{id}`.

The core distinction from existing forms: dsforms today stores **events** (each
submission is an independent row; it only emails the *admin*). A waitlist stores
**people** (one row per email) and emails the *subscribers back*.

## Scope

**In scope (rungs 1–4):**
1. Email-keyed collection with dedup (one row per email per waitlist).
2. Signup position ("you're #N"), computed on demand.
3. Optional per-waitlist auto-confirmation email on join.
4. Restart-safe manual broadcast to the entire list.

**Deferred to a future spec (rungs 5–6):**
- Status lifecycle (waiting / invited / converted) and batch invites.
- Referral / viral loops (per-person codes, attribution, leaderboard). These would
  require bending dsforms' "no JS framework / minimal deps" rules and are a different
  product.

## Data Model

Four new tables, created via `CREATE TABLE IF NOT EXISTS` on startup (same migration
pattern as existing tables), all in `internal/store/store.go`.

```
waitlists
  id              TEXT PK (uuid)
  name            TEXT NOT NULL
  redirect        TEXT            -- post-submit redirect for plain-form signups
  confirm_subject TEXT            -- blank = no confirmation email sent
  confirm_body    TEXT
  created_at      TIMESTAMP

waitlist_entries
  id          TEXT PK (uuid)
  waitlist_id TEXT FK -> waitlists.id ON DELETE CASCADE
  email       TEXT NOT NULL
  data        TEXT              -- JSON blob of extra fields (name, company, ...)
  ip          TEXT
  created_at  TIMESTAMP
  UNIQUE(waitlist_id, email)    -- the dedup guarantee

broadcasts
  id          TEXT PK (uuid)
  waitlist_id TEXT FK -> waitlists.id ON DELETE CASCADE
  subject     TEXT
  body        TEXT
  status      TEXT              -- sending -> done
  created_at  TIMESTAMP

deliveries
  id           TEXT PK (uuid)
  broadcast_id TEXT FK -> broadcasts.id ON DELETE CASCADE
  email        TEXT              -- snapshotted at send time (survives entry deletion)
  status       TEXT              -- pending -> sent | failed
  error        TEXT              -- last error message
  attempts     INTEGER
  updated_at   TIMESTAMP
```

**Position is computed, not stored:** an entry's position = count of entries in that
waitlist with `created_at <= this.created_at`. No stored ranks to migrate or keep in
sync.

**Go structs** (in `store` package): `Waitlist`, `WaitlistEntry` (with a parsed
`Data map[string]string` plus raw JSON), `Broadcast`, `Delivery`, and a
`WaitlistSummary` (Waitlist + entry count) mirroring the existing `FormSummary` pattern.

## Components & Files

Following existing conventions (interfaces defined in the consuming package; all SQL in
`store`; handlers never touch `db` directly; templates cloned from `base.html`).

- **`internal/store/store.go`** — add CRUD:
  - Waitlists: create, get, list (with entry counts), update, delete.
  - Entries: upsert-with-dedup returning `(position int, alreadyJoined bool)`; list
    paginated with positions; delete; export rows.
  - Broadcasts: create, get, list per waitlist.
  - Deliveries: bulk-insert from a waitlist's entries, query pending (batched), mark
    sent, mark failed (error + attempts), count by status.
- **`internal/mail`** — add `SendMail(to, subject, body string) error` alongside the
  existing admin `SendNotification`. Both confirmation and broadcast use it. The
  consuming packages define their own narrow interface for it.
- **`internal/handler/waitlist_submit.go`** — public `POST /w/{id}`. Mirrors
  `submit.go`: honeypot, `_redirect`, JSON-or-redirect content negotiation.
- **`internal/handler/waitlist_admin.go`** — admin pages: list, new, edit, detail
  (entries + positions, paginated), delete, CSV export, delete entry, compose
  broadcast, broadcast progress.
- **`internal/broadcaster/`** (new package) — restart-safe send worker; started in
  `main.go`.
- **Templates** added to the `pageNames` clone list in `main.go`:
  `waitlists.html`, `waitlist_new.html`, `waitlist_edit.html`, `waitlist_detail.html`,
  `broadcast_new.html`, `broadcast_detail.html`.
- **`base.html`** — new "Waitlist" nav link, highlighted via `Active: "waitlists"`.

## Routes

Public (rate-limited like `/f/{id}`):
```
POST /w/{waitlistID}                                   waitlist signup
```

Admin (behind `RequireAuth`):
```
GET  /admin/waitlists                                  list
GET  /admin/waitlists/new                              new page
POST /admin/waitlists/new                              create
GET  /admin/waitlists/{id}/edit                        edit page
POST /admin/waitlists/{id}/edit                        update
POST /admin/waitlists/{id}/delete                      delete
GET  /admin/waitlists/{id}                             detail (entries, paginated)
GET  /admin/waitlists/{id}/export                      CSV export
POST /admin/waitlists/{id}/entries/{entryID}/delete    delete one entry
GET  /admin/waitlists/{id}/broadcast                   compose page + past broadcasts
POST /admin/waitlists/{id}/broadcast                   create broadcast + enqueue + kick worker
GET  /admin/waitlists/{id}/broadcasts/{bid}            broadcast status / progress
```

All admin-mutating actions are POST only (per project rules).

## Flows

### Public signup — `POST /w/{id}`
1. Lookup waitlist → 404 if missing.
2. `ParseForm`; honeypot (`_honeypot`) silent-success; capture `_redirect`.
3. Require an `email` field; validate basic email format → **400** if missing/invalid.
4. Remaining fields → JSON `data`, excluding `email` (promoted to its own column) and
   the internal fields `_honeypot`, `_redirect`, `_subject` (same exclusion set as
   `submit.go`).
5. Upsert by `(waitlist_id, email)` → determine `alreadyJoined`.
6. Compute `position`.
7. If newly joined **and** `confirm_subject` is non-empty → goroutine sends the
   confirmation email. Body/subject variable substitution uses a fixed
   `strings.Replacer` over `{{email}}`, `{{name}}`, `{{position}}` — **not** arbitrary
   template execution, to avoid injection.
8. Respond `{"success":true,"position":N,"already_joined":bool}` (JSON) or redirect with
   `?position=N` appended (plain-form path). Content negotiation mirrors `submit.go`.

### Broadcast send — `POST /admin/waitlists/{id}/broadcast`
1. Validate subject + body non-empty.
2. Create `broadcasts` row (`status=sending`).
3. Snapshot all current entry emails → insert `deliveries` rows (`status=pending`).
4. Signal the worker.
5. Redirect to the broadcast detail page (shows live progress).

### Broadcast worker (`internal/broadcaster`)
A background goroutine started in `main.go`:
- Pulls a batch of `pending` deliveries → sends each via `mail.SendMail` → marks `sent`,
  or `failed` (error + `attempts++`).
- Throttled between sends; the pacing interval is **config-injectable** and set to
  instant in tests (honors the "no `time.Sleep` in tests" rule — pacing is driven by an
  injected clock/interval, deterministic in tests).
- Failed rows retry up to a max-attempts cap.
- When a broadcast has no `pending` rows left → mark `done`.
- **Restart-safe:** on startup the worker resumes any leftover `pending` deliveries. A
  `sent` row is never re-sent (no double-sends); no recipient is dropped.

## Error Handling
- Invalid/missing email → 400, response shape matching `Accept` (JSON or page).
- Duplicate email → **not an error**: success with `already_joined=true` and the
  existing position.
- Confirmation-send failure → logged only; never fails the signup (goroutine, like the
  existing notification path).
- Delivery failure → recorded per row, retried to the cap, surfaced on the progress page.
- 64KB body limit applies as elsewhere.
- All errors wrapped with `fmt.Errorf("context: %w", err)` per project rules.

## Configuration
- Reuses the existing SMTP config for sending.
- New optional config with sane defaults: broadcast throttle interval and max delivery
  attempts. Loaded in `internal/config`.

## Testing (TDD — test file before implementation)
- **store**: dedup (same email twice = one row + correct `already_joined`), position
  calc, broadcast/deliveries CRUD, batched pending query, mark sent/failed.
- **waitlist_submit**: valid signup, missing/invalid email → 400, honeypot,
  duplicate path, JSON vs redirect, confirmation fired (fake mailer).
- **broadcaster**: drains pending→sent, failed-mark + retry, resume-on-restart,
  deterministic throttle via injected clock, `-race` clean.
- **mail**: `SendMail` via fake.
- Table-driven where multiple input cases apply; `t.Parallel()` on tests without shared
  mutable state; `go test -race ./...` must pass.

## Decisions & Rationale
1. **Separate entity, not a Form-type toggle** — dedup, position, and broadcast all
   assume "one row = one person," which fights the event-oriented Form model. Clean
   tables avoid overloading `forms`.
2. **No per-signup admin notification/webhook on waitlists** — forms have
   `email_to`/webhook; waitlists are about emailing *subscribers*, and the admin sees
   entries in the dashboard. Keeps scope tight. (Can be added later.)
3. **Identity field is literally named `email`** — the static-site form must POST an
   `email` field.
4. **Broadcast goes to all entries** — no segmenting; segmentation belongs to the
   deferred status-lifecycle rung.
5. **Fixed variable substitution, not template execution** in emails — avoids injection
   and keeps dependencies minimal.
