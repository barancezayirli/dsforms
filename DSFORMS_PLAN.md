# DSForms — Build Plan for Claude Code

A self-hosted, single-binary form endpoint for static websites.
Think web3forms, but you own it. Go + SQLite + Docker Compose.

---

## 0. What We Are Building

Static sites POST an HTML form to `https://yourserver.com/f/<form-id>`.
DSForms stores the submission in SQLite, sends an email notification, and
redirects the user. You view submissions in a minimal admin UI.
Ships with a default admin user. You change the password and can add more users
from the admin UI or via a CLI script run inside the container.
One binary. One `docker-compose up`.

---

## 1. Repository Layout

```
dsforms/
├── main.go
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yml
├── .env.example
├── README.md
│
├── cmd/
│   └── dsforms/
│       └── main.go            # CLI entrypoint (user management commands)
│
├── internal/
│   ├── config/
│   │   └── config.go          # load from env vars
│   ├── store/
│   │   └── store.go           # all SQLite operations
│   ├── mail/
│   │   └── mail.go            # SMTP send
│   ├── auth/
│   │   └── auth.go            # session + password check
│   ├── flash/
│   │   └── flash.go           # one-time flash messages via signed cookie
│   ├── backup/
│   │   └── backup.go          # export (VACUUM INTO) and import (atomic swap)
│   └── handler/
│       ├── submit.go          # POST /f/:formID
│       ├── admin.go           # admin dashboard + forms CRUD
│       ├── users.go           # admin user management UI
│       ├── backup.go          # GET /admin/backups, export, import
│       └── auth.go            # GET/POST /admin/login, POST /admin/logout
│
└── templates/
    ├── base.html              # shared layout (title slot, body slot)
    ├── login.html
    ├── dashboard.html         # list of forms with unread counts
    ├── form_new.html          # create form
    ├── form_detail.html       # view + manage submissions
    ├── users.html             # list users, add user, change password
    ├── account.html           # change own password
    ├── backups.html           # export download + import upload
    └── success.html           # fallback "form submitted" page
```

---

## 2. Go Module & Dependencies

**Module name:** `github.com/youruser/dsforms`

**Dependencies — keep it minimal:**

```
github.com/go-chi/chi/v5          # router
modernc.org/sqlite                # pure-Go SQLite, no CGO
golang.org/x/crypto               # bcrypt
github.com/google/uuid            # UUID generation
```

**Nothing else.** Sessions via a signed cookie using `crypto/hmac` + stdlib.
Templates via `html/template` stdlib. Email via `net/smtp` stdlib.

---

## 3. Configuration (`internal/config/config.go`)

All config is read from environment variables. No config file.
The `Load()` function reads env vars and returns a `Config` struct.
Panic on missing required values so the app fails fast at startup.

```go
type Config struct {
    // Server
    ListenAddr string // default ":8080"
    BaseURL    string // e.g. "https://forms.example.com" — used in email links

    // Database
    DBPath string // default "/data/dsforms.db"

    // Auth
    SecretKey string // random 32+ char string for HMAC signing, REQUIRED

    // SMTP
    SMTPHost     string // REQUIRED
    SMTPPort     int    // default 587
    SMTPUser     string // REQUIRED
    SMTPPass     string // REQUIRED
    SMTPFrom     string // REQUIRED — "DSForms <noreply@example.com>"

    // Backup — optional, only used by the CLI backup command
    // Leave empty to disable. Inside the Docker volume, e.g. "/data/backups"
    BackupLocalDir string
}
```

Env var names (1-to-1 with fields):
`LISTEN_ADDR`, `BASE_URL`, `DB_PATH`, `SECRET_KEY`,
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM`,
`BACKUP_LOCAL_DIR` (optional)

`ADMIN_PASSWORD` is **not** an env var. The admin password lives in the `users`
table in SQLite and is managed via the admin UI or the CLI script.

---

## 4. Database (`internal/store/store.go`)

Use `modernc.org/sqlite`. Open with `_journal_mode=WAL` and `_foreign_keys=ON`.

### Schema — run on every startup (CREATE TABLE IF NOT EXISTS)

```sql
CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    username     TEXT NOT NULL UNIQUE,
    password     TEXT NOT NULL,   -- bcrypt hash
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS forms (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    email_to    TEXT NOT NULL,
    redirect    TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS submissions (
    id          TEXT PRIMARY KEY,
    form_id     TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
    data        TEXT NOT NULL,   -- JSON object: {"name":"Alice","email":"...","message":"..."}
    ip          TEXT NOT NULL DEFAULT '',
    read        INTEGER NOT NULL DEFAULT 0,  -- 0=unread, 1=read
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_submissions_form_id ON submissions(form_id);
CREATE INDEX IF NOT EXISTS idx_submissions_read ON submissions(read);
```

### Default user seed

After running migrations, check if the `users` table is empty.
If it is, insert a default admin user:

- **username:** `admin`
- **password:** `admin` (bcrypt hashed at cost 12)

Log a prominent startup warning when the default credentials are still in use:
```
⚠  WARNING: Default admin credentials are active (admin/admin).
   Change your password immediately at /admin/users.
```

This warning is printed every time the app starts until the password is changed.
To detect this: store a flag column `is_default_password BOOLEAN DEFAULT 1` on the
users table, set to `0` when the user successfully changes their password.

### Store struct and methods

```go
type Store struct { db *sql.DB }

func New(path string) (*Store, error)   // opens DB, runs migrations, seeds default user

// Users
func (s *Store) GetUserByUsername(username string) (User, error)
func (s *Store) GetUserByID(id string) (User, error)
func (s *Store) ListUsers() ([]User, error)
func (s *Store) CreateUser(username, password string) error   // bcrypts password internally
func (s *Store) UpdatePassword(userID, newPassword string) error  // bcrypts, sets is_default_password=0
func (s *Store) DeleteUser(id string) error  // cannot delete last remaining user
func (s *Store) HasDefaultPassword(userID string) (bool, error)

// Forms
func (s *Store) CreateForm(f Form) error
func (s *Store) GetForm(id string) (Form, error)
func (s *Store) ListForms() ([]FormSummary, error)  // includes UnreadCount
func (s *Store) UpdateForm(f Form) error
func (s *Store) DeleteForm(id string) error

// Submissions
func (s *Store) CreateSubmission(sub Submission) error
func (s *Store) ListSubmissions(formID string) ([]Submission, error)
func (s *Store) MarkRead(submissionID string) error
func (s *Store) MarkAllRead(formID string) error
func (s *Store) DeleteSubmission(id string) error
func (s *Store) UnreadCount(formID string) (int, error)
```

### Model types

```go
type User struct {
    ID               string
    Username         string
    IsDefaultPassword bool
    CreatedAt        time.Time
}

type Form struct {
    ID        string
    Name      string
    EmailTo   string
    Redirect  string
    CreatedAt time.Time
}

type FormSummary struct {
    Form
    UnreadCount int
}

type Submission struct {
    ID        string
    FormID    string
    Data      map[string]string  // unmarshaled from JSON for templates
    RawData   string             // the raw JSON stored in DB
    IP        string
    Read      bool
    CreatedAt time.Time
}
```

---

## 5. Auth (`internal/auth/auth.go`)

Users are stored in the `users` table. The login handler looks up the user by
username, verifies the bcrypt password, and issues a session cookie.
No hardcoded credentials anywhere in the code.

### Session cookie

- Name: `dsforms_session`
- Value: `base64( userID + ":" + timestamp + ":" + hmac_sha256(userID+timestamp, SECRET_KEY) )`
- `HttpOnly: true`, `SameSite: Lax`, `Secure: true` (set Secure only if BASE_URL starts with https)
- Session is valid for 30 days from timestamp
- Encodes the `userID` so the middleware can load the current user for display in the nav

```go
func CreateSessionCookie(userID, secretKey string) *http.Cookie
func ValidateSession(r *http.Request, secretKey string) (userID string, ok bool)
func ClearSessionCookie() *http.Cookie
```

### Middleware

```go
func RequireAuth(store *store.Store, secretKey string) func(http.Handler) http.Handler
```

- Validates the session cookie. If invalid → 302 to `/admin/login`.
- Loads the `User` from the DB using the `userID` from the cookie.
- Stores the `User` on the request context so handlers can access it
  (e.g. to show the logged-in username in the nav and to show the password
  change warning if `IsDefaultPassword` is true).

### Default password warning banner

In `base.html`, if the current user's `IsDefaultPassword` is true, render a
full-width warning banner at the top of every admin page:

```
⚠ You are using the default password. Change it now → [Account Settings]
```

Banner style: amber background `#854d0e` text on `#fef9c3`, dismissible only
by actually changing the password (not a JS dismiss button).

---

## 6. Submit Handler (`internal/handler/submit.go`)

### `POST /f/{formID}`

**Steps:**
1. Look up form by ID. If not found → 404 plain text "form not found".
2. Parse `r.FormValue(...)` for all fields.
3. **Honeypot check:** if field `_honeypot` is non-empty → silently return success (redirect or 200), do nothing else.
4. Filter out internal fields: `_honeypot`, `_redirect`, `_subject`. Everything else goes into the submission data map.
5. Validate: data map must have at least 1 key remaining. Otherwise → 400.
6. Determine redirect URL: use `_redirect` field if present and non-empty, else use `form.Redirect`, else use `/success`.
7. Save submission to DB.
8. Send email notification (async in goroutine — don't block the response).
9. Check `Accept` header: if `application/json` → return `{"success":true}`. Otherwise → 302 redirect.

**IP extraction:** check `X-Forwarded-For` first, then `X-Real-IP`, then `r.RemoteAddr`.

**Email notification format:**
```
Subject: [DSForms] New submission: <form name>

Form:      <form name>
Submitted: <timestamp>
IP:        <ip>

--- Fields ---
name:    Alice
email:   alice@example.com
message: Hello there

---
View all submissions: <BASE_URL>/admin/forms/<form_id>
```

---

## 7. Admin Handlers (`internal/handler/admin.go`)

All routes under `/admin/*` are protected by `RequireAuth` middleware.

### Routes

```
GET  /admin                     → redirect to /admin/forms
GET  /admin/forms               → dashboard: list all forms with unread counts
GET  /admin/forms/new           → form creation page
POST /admin/forms/new           → create form, redirect to /admin/forms/<id>
GET  /admin/forms/{id}          → view form detail + all submissions
GET  /admin/forms/{id}/edit     → edit form name, email_to, redirect
POST /admin/forms/{id}/edit     → save edits, redirect to /admin/forms/<id>
POST /admin/forms/{id}/delete   → delete form (and all submissions via CASCADE), redirect to /admin/forms

POST /admin/submissions/{id}/read      → mark single submission read, redirect back
POST /admin/forms/{id}/read-all        → mark all submissions in form as read
POST /admin/submissions/{id}/delete    → delete single submission, redirect back
GET  /admin/forms/{id}/export          → download submissions as CSV
```

### CSV export format

```
id,submitted_at,ip,read,<field1>,<field2>,...
```

Column headers for fields are collected from all submissions in the form (union of all keys). Missing values are empty string.

### `GET /success`

Public route. Renders `success.html` template with message "Your message has been sent."
Used as fallback when no redirect is configured.

---

## 8. User Management Handlers (`internal/handler/users.go`)

All routes protected by `RequireAuth`. Handled in a separate file for clarity.

### Routes

```
GET  /admin/users                    → list all users
GET  /admin/users/new                → add user form
POST /admin/users/new                → create user, redirect to /admin/users
POST /admin/users/{id}/delete        → delete user, redirect to /admin/users
                                       (block if it's the last user or the current user)

GET  /admin/account                  → change own password page
POST /admin/account/password         → update own password
                                       (validates current password before allowing change)
```

### `users.html` template

Two sections:

**Section 1 — Users table**
| Username | Created | Actions |
|---|---|---|
| admin | Jan 1 2025 | Delete (disabled if last user or self) |

"Add User" button opens the new user form below or navigates to `/admin/users/new`.

**Section 2 — Add User form** (or on its own page `/admin/users/new`)
- Username (text, required)
- Password (password, required)
- Confirm Password (password, required — validated server-side)
- Submit: "Add User"

### `account.html` template (change own password)

Three fields:
- Current Password (required — must verify before allowing change)
- New Password (required)
- Confirm New Password (required)

On success: flash message "Password updated." and `is_default_password` set to `0`.
On wrong current password: re-render with error "Current password is incorrect."

Link to this page appears in the nav bar as the logged-in username → "Account".

---

## 9. Auth Handlers (`internal/handler/auth.go`)

```
GET  /admin/login   → render login.html (with optional ?error=1 query param)
POST /admin/login   → look up username + verify bcrypt password, set cookie,
                      redirect to /admin/forms
                      on failure: redirect to /admin/login?error=1
POST /admin/logout  → clear cookie, redirect to /admin/login
```

Login form fields: `username` (text) + `password` (password).

---

## 10. Router Setup (`main.go`)

```go
r := chi.NewRouter()

// Middleware on all routes
r.Use(middleware.RealIP)
r.Use(middleware.Recoverer)

// Public
r.Post("/f/{formID}", submitHandler.Handle)
r.Get("/success", adminHandler.Success)
r.Get("/admin/login", authHandler.LoginPage)
r.Post("/admin/login", authHandler.LoginSubmit)

// Protected
r.Group(func(r chi.Router) {
    r.Use(auth.RequireAuth(store, cfg.SecretKey))
    r.Post("/admin/logout", authHandler.Logout)
    r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, "/admin/forms", http.StatusFound)
    })
    // Forms
    r.Get("/admin/forms", adminHandler.Dashboard)
    r.Get("/admin/forms/new", adminHandler.NewFormPage)
    r.Post("/admin/forms/new", adminHandler.CreateForm)
    r.Get("/admin/forms/{id}", adminHandler.FormDetail)
    r.Get("/admin/forms/{id}/edit", adminHandler.EditFormPage)
    r.Post("/admin/forms/{id}/edit", adminHandler.EditForm)
    r.Post("/admin/forms/{id}/delete", adminHandler.DeleteForm)
    r.Post("/admin/forms/{id}/read-all", adminHandler.MarkAllRead)
    r.Get("/admin/forms/{id}/export", adminHandler.ExportCSV)
    r.Post("/admin/submissions/{id}/read", adminHandler.MarkRead)
    // Backup
    r.Get("/admin/backups", backupHandler.Page)
    r.Get("/admin/backups/export", backupHandler.Export)
    r.Post("/admin/backups/import", backupHandler.Import)
    // Users
    r.Get("/admin/users", usersHandler.ListUsers)
    r.Get("/admin/users/new", usersHandler.NewUserPage)
    r.Post("/admin/users/new", usersHandler.CreateUser)
    r.Post("/admin/users/{id}/delete", usersHandler.DeleteUser)
    // Account (own password)
    r.Get("/admin/account", usersHandler.AccountPage)
    r.Post("/admin/account/password", usersHandler.UpdatePassword)
})

http.ListenAndServe(cfg.ListenAddr, r)
```

---

## 11. Templates

Use Go's `html/template`. Parse all templates at startup (not per-request).
Embed templates using `//go:embed templates/*` so the binary is self-contained.

### `base.html`

Minimal HTML5 layout. Inline a small amount of CSS (no external CSS framework).
Style goals: clean, readable, no visual noise. Think: white background, dark text,
subtle borders, system font stack.

```css
/* System font stack */
font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;

/* Color palette — minimal */
--bg: #ffffff;
--surface: #f9fafb;
--border: #e5e7eb;
--text: #111827;
--muted: #6b7280;
--primary: #2563eb;
--danger: #dc2626;
--success: #16a34a;
--unread: #eff6ff;   /* light blue row background for unread submissions */
```

All pages inherit from base.html using template blocks: `{{block "title" .}}` and `{{block "content" .}}`.

### Nav bar (inside `base.html`, shown on all admin pages)

```
dsforms    Forms    Users                     [username] · Logout
```

- Left: "dsforms" wordmark linking to `/admin/forms`
- Middle: "Forms" → `/admin/forms`, "Users" → `/admin/users`
- Right: logged-in username linking to `/admin/account`, then "Logout" POST button

If `CurrentUser.IsDefaultPassword` is true, show the amber warning banner
above the nav (full-width, not inside the content area):
```
⚠ You are using the default password. Change it now →
```
"Change it now" links to `/admin/account`.

### `login.html`

Centered card. Username input + password input + submit button.
If `?error=1` in URL: show "Invalid username or password." message in red.

### `users.html`

Two sections:

**Existing users table:**
| Username | Created | Actions |
|---|---|---|
| admin | Jan 1 2025 | Delete (disabled/greyed if last user or current user) |

**Add user form** below the table (inline, no separate page needed):
- Username (text, required)
- Password (password, required)
- Confirm Password (password, required)
- Button: "Add User"

### `account.html` (change own password)

H1: "Account Settings"
Subheading: logged-in username

Form:
- Current Password (password, required)
- New Password (password, required)
- Confirm New Password (password, required)
- Button: "Update Password"

On success: re-render page with green flash "Password updated successfully."
On error (wrong current password): re-render with red "Current password is incorrect."

### `dashboard.html`

H1: "Forms"
Button (top right): "New Form" → links to `/admin/forms/new`

Table of forms:
| Name | Email To | Unread | Created | Actions |
|------|----------|--------|---------|---------|
| Contact Form | me@example.com | **3** (bold if >0) | Jan 1 2025 | View · Edit · Delete |

Delete uses a `<form method="POST">` with confirm dialog via `onclick="return confirm('Delete this form and all submissions?')"`.

### `form_new.html` and `form_detail.html` (edit variant)

Form with fields:
- Name (text, required) — e.g. "Contact Form"
- Email To (email, required) — where notifications go
- Redirect URL (text, optional) — where to send users after submit

Below the form (only on detail page), show the **HTML snippet** they paste into their static site:

```html
<pre><code>&lt;form action="{{ .BaseURL }}/f/{{ .Form.ID }}" method="POST"&gt;
  &lt;input type="hidden" name="_redirect" value="https://yoursite.com/thanks"&gt;
  &lt;!-- your fields here --&gt;
  &lt;button type="submit"&gt;Send&lt;/button&gt;
&lt;/form&gt;</code></pre>
```

Include a "Copy" button next to the snippet (small inline `<script>` using `navigator.clipboard.writeText`).

### `form_detail.html`

Split into two sections:

**Top:** Form info + snippet (as above) + Edit button + "Mark all read" button + "Export CSV" link.

**Bottom:** Submissions table.
- Unread rows get background `var(--unread)`.
- Columns: Submitted (datetime), IP, Fields (inline summary like `name: Alice | email: alice@… | message: Hello`), Actions.
- Actions per row: "Mark read" button (if unread) + "Delete" button.
- If no submissions: show "No submissions yet."

### `success.html`

Simple centered message: "✓ Your message has been sent."
Link back: "← Go back"

---

## 12. Mail (`internal/mail/mail.go`)

```go
type Mailer struct {
    cfg config.Config
}

func (m *Mailer) SendNotification(form store.Form, sub store.Submission) error
```

Use `net/smtp.SendMail`. Build the email as a plain text RFC 2822 message.
Set `From`, `To`, `Subject`, `Date`, `MIME-Version`, `Content-Type: text/plain; charset=utf-8`.

If `SMTP_PORT` is 465 (implicit TLS), use `tls.Dial` + `smtp.NewClient`.
Otherwise use `smtp.SendMail` (STARTTLS on 587, plain on 25).

Do not panic if email fails. Log the error with `log.Printf`.

---

## 13. Dockerfile

```dockerfile
# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o dsforms .

# Runtime stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/dsforms .
VOLUME ["/data"]
EXPOSE 8080
CMD ["./dsforms"]
```

`CGO_ENABLED=0` works because `modernc.org/sqlite` is pure Go.

---

## 14. docker-compose.yml

```yaml
services:
  dsforms:
    build: .
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    env_file:
      - .env
```

---

## 15. .env.example

```bash
# Server
LISTEN_ADDR=:8080
BASE_URL=https://forms.example.com

# Database (leave as default unless you have a reason to change)
DB_PATH=/data/dsforms.db

# Auth
# SECRET_KEY is used to sign session cookies. Generate with:
#   openssl rand -base64 32
SECRET_KEY=change-me-to-a-random-32-char-string

# SMTP — works with Gmail App Passwords, Fastmail, Resend SMTP, Mailgun SMTP, etc.
SMTP_HOST=smtp.fastmail.com
SMTP_PORT=587
SMTP_USER=you@example.com
SMTP_PASS=your-app-password
SMTP_FROM=DSForms <noreply@example.com>

# Backup (CLI only — optional)
# If set, "docker compose exec dsforms ./dsforms backup create" writes a
# snapshot here. Leave unset if you don't need CLI-triggered backups.
# UI export/import works regardless of this setting.
#BACKUP_LOCAL_DIR=/data/backups
```

No `ADMIN_PASSWORD` env var. The default credentials (`admin` / `admin`) are
seeded into the DB on first run. Change the password immediately via the admin UI
at `/admin/account` or using the CLI script described in Section 15.

---

## 16. CLI User Management (`cmd/dsforms/main.go`)

A subcommand CLI baked into the same binary for managing users without needing
to log into the UI — useful for resetting a forgotten password from inside the container.

### Usage

```bash
# From host machine via docker compose
docker compose exec dsforms ./dsforms user list
docker compose exec dsforms ./dsforms user set-password admin newpassword
docker compose exec dsforms ./dsforms user add alice newpassword
docker compose exec dsforms ./dsforms user delete alice
```

### Implementation

`main.go` checks `os.Args[1]` at startup:
- If no args, or args[1] is not `user` → start the HTTP server (normal mode)
- If args[1] is `user` → run the CLI subcommand and exit

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "user" {
        runCLI(os.Args[2:])
        return
    }
    runServer()
}
```

### Subcommands

```
user list                          — print all usernames and created dates
user add <username> <password>     — create a new user
user set-password <username> <pw>  — reset any user's password (sets is_default_password=0)
user delete <username>             — delete a user (error if last remaining user)
```

All subcommands open the same SQLite DB (`DB_PATH` from env, default `/data/dsforms.db`),
perform the operation, print a success or error message to stdout/stderr, then exit.

`set-password` bcrypts the new password at cost 12 before storing.

**Error cases to handle explicitly:**
- `user add` with a username that already exists → exit 1, print "user already exists"
- `user delete` on the last user → exit 1, print "cannot delete the last user"
- `user set-password` on a username that doesn't exist → exit 1, print "user not found"
- Any DB error → exit 1, print the error

### Example output

```bash
$ docker compose exec dsforms ./dsforms user list
USERNAME    CREATED
admin       2025-01-01 12:00:00

$ docker compose exec dsforms ./dsforms user set-password admin mynewpassword
Password updated for user "admin".

$ docker compose exec dsforms ./dsforms user add alice secret123
User "alice" created.

$ docker compose exec dsforms ./dsforms user list
USERNAME    CREATED
admin       2025-01-01 12:00:00
alice       2025-01-02 09:15:00
```

---

## 17. Backup & Restore (`internal/backup/backup.go`)

### Overview

Simple, manual backup and restore from the admin UI. No scheduler, no S3,
no cron, no external dependencies. One button to download everything, one
button to restore it.

**Export** — downloads a single `.db` file that is a clean SQLite snapshot
of the entire database: forms, submissions, users, everything. The file can
be opened with any SQLite client or used as a direct drop-in replacement for
`/data/dsforms.db`.

**Import** — upload a previously exported `.db` file to fully restore the
application state. Replaces the live database atomically.

### Routes

```
GET  /admin/backups              → backup & restore page
GET  /admin/backups/export       → stream the .db file as a download
POST /admin/backups/import       → upload a .db file and restore
```

Add **Backups** to the admin nav bar. Always visible — no config required.

### Export (`GET /admin/backups/export`)

```go
func (h *BackupHandler) Export(w http.ResponseWriter, r *http.Request)
```

Steps:
1. Create a temp file in the OS temp dir: `dsforms-backup-<timestamp>.db`
2. Run `VACUUM INTO '<tempfile>'` via `database/sql` — consistent online
   snapshot, safe while the server is handling requests
3. Set response headers:
   ```
   Content-Type:        application/octet-stream
   Content-Disposition: attachment; filename="dsforms-backup-2025-01-15.db"
   ```
4. Stream the temp file to the response with `io.Copy`
5. Delete the temp file after streaming (`defer os.Remove`)

The downloaded file is a standard SQLite database. The user saves it wherever
they want — local machine, cloud storage, wherever.

### Import (`POST /admin/backups/import`)

```go
func (h *BackupHandler) Import(w http.ResponseWriter, r *http.Request)
```

Steps:
1. Parse the uploaded file from the multipart form (field name: `file`).
   Limit upload size to 100MB via `r.Body = http.MaxBytesReader(w, r.Body, 100<<20)`.
2. Write the uploaded bytes to a temp file.
3. **Validate** the temp file is a real SQLite database:
   - Open it with `modernc.org/sqlite`
   - Run `PRAGMA integrity_check` — must return `ok`
   - Confirm the expected tables exist: `users`, `forms`, `submissions`
   - If any check fails → delete temp file, redirect back with error flash
4. **Atomic swap:**
   - Close the current DB connection in the Store
   - `os.Rename(tempFile, cfg.DBPath)` — atomic on Linux (same filesystem)
   - Re-open the DB connection and re-run migrations
   - Replace the Store pointer in all handlers with the new one
5. Delete temp file.
6. Redirect to `/admin/backups` with success flash: "Database restored successfully."

### Atomic swap detail

The Store needs a `Reopen(path string) error` method that closes the existing
`*sql.DB`, opens a new one at the given path, and re-runs the migration SQL.
All handlers hold a pointer to the Store — since they hold `*store.Store` (a
pointer), re-opening the underlying `*sql.DB` inside the struct is sufficient.
No need to replace handler instances.

```go
func (s *Store) Reopen(path string) error  // closes old db, opens new one at path
```

### `backups.html` template

Clean, two-section page:

**Section 1 — Export**
```
Export Database
───────────────────────────────────────────────
Download a complete snapshot of all your data:
forms, submissions, and users.

[ Download backup ]
```
"Download backup" is a plain `<a href="/admin/backups/export">` — no JS needed,
browser triggers a file download naturally from the GET response headers.

**Section 2 — Import / Restore**
```
Restore Database
───────────────────────────────────────────────
⚠ This will replace ALL current data with the
  contents of the uploaded file. This cannot
  be undone.

[ Choose .db file ]  [ Restore ]
```
Standard `<form method="POST" enctype="multipart/form-data">`.
Show any flash messages (error or success) at the top of the page.

### Flash messages

Implement a minimal one-time flash message system using a signed cookie:
- On redirect after import: set a `dsforms_flash` cookie with the message
  and a `type` (success or error), signed with `SECRET_KEY`
- On next page load: read and display the cookie, then clear it
- Reuse this flash mechanism for other admin actions that need feedback
  (e.g. "Password updated", "User deleted")

```go
// internal/flash/flash.go
func Set(w http.ResponseWriter, secretKey, msgType, message string)
func Get(r *http.Request, w http.ResponseWriter, secretKey string) (msgType, message string)
// Get also clears the cookie after reading
```

### Error cases

| Situation | Response |
|---|---|
| Uploaded file is not SQLite | Flash error: "Invalid file: not a SQLite database." |
| `integrity_check` fails | Flash error: "Invalid file: database integrity check failed." |
| Missing expected tables | Flash error: "Invalid file: missing required tables." |
| File too large (>100MB) | Flash error: "File too large. Maximum size is 100MB." |
| Rename fails (different filesystem) | Flash error: show the OS error |

### No scheduler, no automation

This is intentional. The user downloads a backup manually when they want one.
For automated backups on Dokploy, the user can:
- Use Dokploy's built-in volume snapshot feature on the `/data` volume
- Set up a cron job on the host that runs `docker compose exec dsforms ./dsforms backup create`
  if they want a CLI-triggered clean snapshot (see Section 16 CLI)

The CLI `backup create` command is kept as a lightweight complement — it writes
a `VACUUM INTO` snapshot to `/data/backups/<timestamp>.db` and exits. No S3,
no scheduler, just the file. Useful for scripting outside the container.

```
backup create   — write a snapshot to /data/backups/, print filename, exit
```

This means `BACKUP_LOCAL_DIR` remains in the config for the CLI path, but
it is the only backup-related env var. All S3 config, scheduler config, and
`BACKUP_KEEP_LOCAL` are removed entirely.

---

## 18. README.md

Sections:
1. **What it is** — one paragraph
2. **Quick start** — copy .env.example, fill in values, `docker compose up -d`
3. **Default credentials** — warn that default is `admin`/`admin`, link to account settings
4. **Managing users from the container** — show the `docker compose exec` CLI commands
5. **Using with your static site** — the HTML snippet, mention `_redirect`, `_honeypot`, `_subject`
6. **Backups** — explain the UI export/import, mention the CLI `backup create` command for scripting
7. **Reverse proxy** — nginx snippet for TLS termination
8. **Development** — `go run .` locally
9. **License** — MIT

---

## 19. Build Order for Claude Code

Execute phases in this order. Each phase should be fully working before starting the next.

### Phase 1 — Skeleton
- `go.mod` with module name and Go version
- `internal/config/config.go`
- `main.go` that loads config, panics on missing required vars, starts HTTP server on `ListenAddr`, serves a `GET /healthz` that returns `200 OK`
- Verify: `go build ./...` succeeds

### Phase 2 — Database
- `internal/store/store.go` with schema migration (including `users` table), default user seed, all model types, all methods
- Verify: write a `_test.go` that opens `:memory:`, confirms default user exists with username `admin`, creates a form, creates a submission, lists submissions

### Phase 3 — Submit endpoint
- `internal/mail/mail.go`
- `internal/handler/submit.go`
- `internal/ratelimit/ratelimit.go`
- Wire `POST /f/{formID}` in `main.go`
- Wire rate limiter, `MaxBytesReader`, and security headers middleware
- Verify: `curl -X POST http://localhost:8080/f/nonexistent` returns 404

### Phase 4 — Auth
- `internal/auth/auth.go` (session cookie encodes userID, middleware loads User from DB into context)
- `internal/handler/auth.go` (login looks up by username + bcrypt verify, logout)
- `templates/login.html` (username + password fields)
- Wire auth routes in `main.go`
- Verify: `GET /admin/forms` redirects to `/admin/login` without a valid session

### Phase 5 — Admin UI
- All remaining templates including `users.html` and `account.html`
- `internal/handler/admin.go`
- `internal/handler/users.go`
- Wire all admin + user routes
- Embed templates with `//go:embed`
- Verify: full flow — login as `admin`/`admin` → warning banner visible → change password → banner gone → create form → submit via curl → see submission

### Phase 6 — Backup UI + Flash + CLI
- `internal/flash/flash.go` — signed cookie flash messages (`Set`, `Get`)
- `internal/backup/backup.go` — `Export` (VACUUM INTO temp, stream), `Import` (validate, atomic swap via `Store.Reopen`)
- `handler/backup.go` — Page, Export, Import handlers
- `templates/backups.html`
- Add backup routes to router
- Add `backup create` CLI subcommand to `main.go` (writes snapshot to `BACKUP_LOCAL_DIR` if set)
- Wire flash messages into account password change and user delete confirmations
- Verify: click "Download backup" → receives a valid `.db` file
- Verify: upload that same file via "Restore" → app continues working, data intact
- Verify: uploading a non-SQLite file → flash error shown, nothing broken

### Phase 7 — Docker
- `Dockerfile`
- `docker-compose.yml`
- `.env.example`
- Verify: `docker compose build && docker compose up` — app reachable at localhost:8080
- Verify: `docker compose exec dsforms ./dsforms user list` works

### Phase 8 — Polish
- `README.md`
- Export CSV handler
- Mark all read handler
- Make sure all mutating actions use POST + confirm dialogs, never GET
- Add `<meta name="viewport">` and mobile-friendly layout to base.html

### Phase 9 — Landing site
- `docs/index.html`
- Verify locally, push, enable GitHub Pages

---

## 20. Key Constraints & Decisions (do not deviate)

- **No CGO.** `modernc.org/sqlite` only. `CGO_ENABLED=0` in Dockerfile.
- **No ORM.** Raw `database/sql` queries only.
- **No JS framework.** Vanilla JS only, and only for the clipboard copy button. Everything else is server-rendered HTML forms with POST.
- **No external CSS.** Inline `<style>` in `base.html` only.
- **No session store.** Session is a signed, timestamped cookie. No Redis, no DB table.
- **Templates are embedded** in the binary via `//go:embed`. Do not read from filesystem at runtime.
- **All admin-mutating actions use POST**, never GET (to avoid CSRF via link). No CSRF token needed for a single-user app where `SameSite=Lax` is set on the session cookie.
- **Email sending is async** (goroutine) so a slow SMTP server doesn't delay the form redirect.
- **Submissions are never deleted on form read** — they accumulate until explicitly deleted.
- **`/f/:formID` accepts any POST fields** — no schema enforcement. This is intentional.
- **Rate limiting and abuse protection** — see Section 21. No external dependency; all in-process with `sync.Mutex`.
- **No AWS SDK.** No S3 integration. Backup is UI-driven export/import only.
- **No cron library.** No scheduler. Backups are manual from the UI or CLI.
- **Backup export never affects live requests** — `VACUUM INTO` is an online operation.
- **Backup import is atomic** — `os.Rename` swaps the file in one syscall, then DB is reopened.

---

## 21. Security & Abuse Protection

This is open source and self-hosted. Anyone can read the code and know exactly how it
works. The protections below are all stdlib + `sync` — zero extra dependencies.

### 19.1 Rate Limiting on `POST /f/:formID` (in-process token bucket)

Implement a simple per-IP token bucket limiter in `internal/ratelimit/ratelimit.go`.

```go
type Limiter struct {
    mu      sync.Mutex
    buckets map[string]*bucket
}

type bucket struct {
    tokens    float64
    lastSeen  time.Time
}
```

**Parameters (tune via env vars with these defaults):**

| Env var | Default | Meaning |
|---|---|---|
| `RATE_BURST` | `5` | Max requests an IP can fire in a burst |
| `RATE_PER_MINUTE` | `6` | Sustained rate (tokens refilled per minute) |

Algorithm: on each request, compute elapsed time since last seen, refill
`elapsed * (RATE_PER_MINUTE / 60.0)` tokens (capped at `RATE_BURST`), then
consume 1 token. If tokens < 1 → reject.

**On rejection:** return HTTP 429 with body `Too many requests`. If the original
request expected JSON (`Accept: application/json`), return `{"error":"too many requests"}`.

**Cleanup:** run a goroutine every 10 minutes that removes buckets not seen in the
last 30 minutes. This prevents unbounded memory growth from unique IPs.

Apply this middleware **only** to `POST /f/{formID}`. Do not rate-limit the admin UI
(you are the only user behind a session cookie).

### 19.2 Admin Login Brute-Force Protection

Track failed login attempts in memory (not DB).

```go
type LoginGuard struct {
    mu       sync.Mutex
    attempts map[string]*loginState  // key: IP
}

type loginState struct {
    failures  int
    lockedUntil time.Time
}
```

**Rules:**
- After **5 consecutive failed** attempts from the same IP → lock that IP out of
  `/admin/login` for **15 minutes**.
- A successful login resets the failure counter for that IP.
- On lockout: return HTTP 429 and render the login page with message
  "Too many failed attempts. Try again in 15 minutes."
- Same cleanup goroutine pattern: purge stale entries every 30 minutes.

Add these two config values (with defaults) to `Config`:

```
RATE_BURST=5
RATE_PER_MINUTE=6
```

### 19.3 Request Size Limit

Wrap the server's handler with `http.MaxBytesReader` on every request body.
Set limit to **64KB**. This prevents someone POSTing a 100MB body to exhaust memory.

In `main.go`, apply a middleware that wraps `r.Body`:
```go
r.Use(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
        next.ServeHTTP(w, r)
    })
})
```

### 19.4 Honeypot Field

Already described in Section 6. Reiterated here for completeness:
- If `_honeypot` field is non-empty → silently return success (200 or redirect).
- Do NOT store the submission. Do NOT send email. Do NOT return an error.
- Bots filling all fields see a success response and move on.

### 19.5 Security Headers

Apply these response headers to **all** routes via a middleware in `main.go`:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: strict-origin-when-cross-origin
Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'
```

(`unsafe-inline` is needed for the inline `<style>` and the clipboard copy snippet.)

### 19.6 What We Deliberately Do NOT Do

- No IP allowlist/blocklist (overkill for personal use, easy to add later).
- No CAPTCHA (honeypot + rate limiting is sufficient at this scale).
- No submission content scanning.
- No Fail2Ban integration (the in-process guard covers the login endpoint).

### 19.7 Updated Repository Layout

Add the new package:

```
internal/
    ratelimit/
        ratelimit.go    # token bucket + login guard
```

### 19.8 Updated Build Order

Phase 3 now includes building `internal/ratelimit/ratelimit.go` and wiring:
- `Limiter` middleware onto `POST /f/{formID}`
- `LoginGuard` into the login POST handler
- `MaxBytesReader` middleware onto all routes
- Security headers middleware onto all routes

---

## 22. GitHub Pages Landing Site (`/docs` folder)

A static marketing/documentation site hosted on GitHub Pages.
Configured in the repo by setting GitHub Pages source to the `/docs` folder on `main`.

No build step. No npm. No framework. Pure HTML + CSS + a tiny bit of vanilla JS.
One file: `docs/index.html`. Self-contained — everything inline.

---

### 20.1 Purpose of the Page

Three audiences land here:

1. **A developer** evaluating whether to self-host DSForms — needs to see what it does,
   how simple the HTML snippet is, and how to get started in under 5 minutes.
2. **Someone you're hosting it for** — needs a pointer to contact you.
3. **A curious passerby** from GitHub — needs to understand the project at a glance.

---

### 20.2 File Structure

```
docs/
└── index.html     # the entire site — no external dependencies except Google Fonts
```

That's it. One file. GitHub Pages serves it automatically.

---

### 20.3 Aesthetic Direction

**Industrial-utilitarian with a terminal edge.**

DSForms is a developer tool. It's honest, no-nonsense, self-hosted infrastructure.
The design should feel like something a backend developer built on a weekend and
is proud of — not a SaaS startup trying to look friendly.

- **Color palette:** Near-black background (`#0d1117` — GitHub's own dark),
  off-white text (`#e6edf3`), electric green accent (`#3fb950`) for highlights
  and the code snippet, muted border color (`#30363d`).
- **Typography:**
  - Display/headings: `"DM Mono"` from Google Fonts — monospaced, reads like a terminal
  - Body: `"Inter"` from Google Fonts — clean, legible contrast to the mono headers
  - Code blocks: `"DM Mono"` naturally
- **Layout:** Single-column, full-width sections, generous vertical rhythm.
  Max content width `760px`, centered. Feels like a well-formatted README rendered
  in a browser, but better.
- **Motion:** Minimal. One subtle fade-in + translate-up on page load for the hero text
  (`animation: fadeUp 0.5s ease both`). The code snippet gets a blinking cursor.
  Nothing more.
- **No images.** No illustrations. No hero graphic. The code speaks for itself.

---

### 20.4 Page Sections (top to bottom)

#### Header / Nav

```
dsforms                              GitHub ↗   Self-host ↓
```

- Left: `dsforms` in DM Mono, electric green, ~20px. Links to `#`.
- Right: two plain text links — "GitHub" (opens repo in new tab) and "Self-host"
  (smooth-scrolls to the deploy section).
- No background on the nav. Just floats above the hero. `position: sticky; top: 0`
  with a very subtle `backdrop-filter: blur(8px)` and `background: rgba(13,17,23,0.85)`.

#### Hero

Large, punchy. No fluff.

```
Form submissions
for static sites.
─────────────────────────────
Self-hosted. One binary. No SaaS.
```

- H1: two lines, DM Mono, ~56px on desktop / 36px mobile, off-white.
  The two words on line 1 ("Form submissions") animate in with `fadeUp` stagger.
- Subheading: Inter, ~18px, muted color (`#8b949e`). Three short claims separated
  by ` · ` — "Self-hosted · One binary · No SaaS."
- Two CTA buttons side by side:
  - Primary: "Self-host in 5 min →" — electric green background, black text,
    smooth-scrolls to the deploy section.
  - Secondary: "View on GitHub →" — transparent background, green border, green text.
- Below the buttons: a faint horizontal rule (`border-top: 1px solid #30363d`).

#### How It Works

Section title: `// how it works` in DM Mono, electric green, small caps feel.

Three numbered steps in a horizontal row on desktop, stacked on mobile:

```
01                   02                   03
──────────────────   ──────────────────   ──────────────────
Deploy DSForms        Create a form        Paste the snippet
on your server       in the admin UI      into your HTML
with Docker Compose  and get your         and you're done.
                     endpoint URL.
```

Each step is a card: `background: #161b22`, `border: 1px solid #30363d`,
`border-radius: 6px`, `padding: 24px`. The number is large, DM Mono, muted green,
top-left of the card.

#### The HTML Snippet

Section title: `// drop this in your html`

Show the actual HTML snippet in a styled code block:

```html
<form action="https://your-server.com/f/YOUR_FORM_ID" method="POST">
  <input type="hidden" name="_redirect" value="https://yoursite.com/thanks">
  <input type="text"   name="name"    placeholder="Your name"    required>
  <input type="email"  name="email"   placeholder="Your email"   required>
  <textarea            name="message" placeholder="Your message" required></textarea>
  <button type="submit">Send</button>
</form>
```

Styling:
- Dark card `#161b22`, border `#30363d`, `border-radius: 6px`.
- Top bar of the code block: fake "terminal" chrome — three colored dots
  (red/yellow/green, `8px` circles) on the left, text `"contact.html"` on the right
  in DM Mono muted color.
- Syntax highlight manually in HTML using `<span>` tags:
  - Tags (`<form>`, `<input>`, etc.): muted blue `#79c0ff`
  - Attribute names: light purple `#d2a8ff`
  - Attribute values (strings): electric green `#3fb950`
  - `required` keyword: orange `#ffa657`
- A "Copy" button top-right of the code block. On click: copies the snippet,
  button text changes to "Copied ✓" for 2 seconds, then back.

Below the snippet, three special fields explained as a small table:

| Field | Purpose |
|---|---|
| `_redirect` | Where to send the user after submission |
| `_subject` | Custom email subject line |
| `_honeypot` | Hidden spam trap — bots fill it, humans don't |

Table style: minimal, `border-collapse: collapse`, row borders `#30363d`,
DM Mono for field names in green, Inter for descriptions.

#### Features

Section title: `// what you get`

Two-column grid of feature bullets (4 left, 4 right) on desktop, single column mobile.
Each bullet: a `✓` in electric green + Inter body text.

```
✓ Email notifications on every submission
✓ Submissions stored in SQLite
✓ Simple admin UI to view & manage
✓ CSV export

✓ Honeypot + rate limiting built in
✓ Single Docker Compose deploy
✓ Open source — MIT license
✓ No per-submission fees, ever
```

No cards. Just the list. Clean.

#### Self-Host in 5 Minutes

Section title: `// self-host in 5 min`

A numbered terminal-style walkthrough. Each step is a command block:

**Step 1 — Clone & configure**
```bash
git clone https://github.com/youruser/dsforms
cd dsforms
cp .env.example .env
# Edit .env with your SMTP credentials and admin password
```

**Step 2 — Generate your password hash**
```bash
go run ./cmd/hashpw mysecretpassword
# Copy the output into ADMIN_PASSWORD in .env
```

**Step 3 — Start**
```bash
docker compose up -d
```

**Step 4 — Reverse proxy (nginx example)**
```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

Each command block: same dark card style as the snippet section, but simpler
(no terminal chrome, just a `$` prompt prefix on shell lines in muted green).

Below the steps, a small note in muted color:
"Requires Docker + Docker Compose. A $5 VPS is enough."

#### Footer

```
dsforms · MIT License · Made by [your name]

GitHub ↗   Issues ↗
```

Minimal. Same dark background. Thin top border `#30363d`. Center-aligned.
DM Mono for "dsforms", Inter for everything else.

---

### 20.5 Responsive Breakpoints

| Breakpoint | Changes |
|---|---|
| `> 768px` | Three-column "how it works", two-column features grid, hero 56px |
| `≤ 768px` | Everything single column, hero 36px, nav links collapse to just icons |

Use `@media (max-width: 768px)` only. No other breakpoints needed.

---

### 20.6 Performance Constraints

- **Only two external resources:** Google Fonts (`DM Mono` + `Inter`), loaded in `<head>`
  with `rel="preconnect"` and `display=swap`.
- **No JavaScript libraries.** The only JS is ~20 lines for the copy button.
- **No images or SVGs** beyond the three terminal-dot circles (pure CSS `border-radius`).
- Page should load and be fully readable with JavaScript disabled.
- Target: Lighthouse score > 95 on performance, 100 on accessibility.

---

### 20.7 Accessibility

- All color contrast ratios must meet WCAG AA (the green `#3fb950` on `#0d1117` passes).
- Semantic HTML: `<header>`, `<main>`, `<section>`, `<footer>`, proper heading hierarchy
  (`h1` → `h2` → `h3`), no skipped levels.
- The copy button must have `aria-label="Copy code snippet"` and update
  `aria-label` to "Copied" on success.
- All links must have descriptive text (no "click here").

---

### 20.8 GitHub Pages Configuration

In the repo, set Pages source to **Deploy from branch → `main` → `/docs` folder**.
No GitHub Actions workflow needed.

Add to README.md (Section 7 — new entry):
```
## Landing Page
The project landing page lives in `/docs/index.html` and is served via GitHub Pages.
To update it, edit the file and push to main.
```

---

### 20.9 Build Phase for the Landing Site

Add as **Phase 8** (after Docker phase):

- Create `docs/index.html` with all sections above
- Test locally by opening the file directly in a browser (`file://`) — no server needed
- Verify: all internal anchor links work, copy button works, page is readable on mobile
  (resize browser to 375px width)
- Verify: no console errors, no mixed content warnings
- Push to GitHub, enable Pages in repo settings, confirm the live URL loads correctly
