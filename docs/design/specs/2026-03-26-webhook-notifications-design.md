# Webhook Notifications — Design Spec

**Date:** 2026-03-26
**Status:** Approved
**Approach:** Separate `webhook.Sender` alongside existing `Notifier` (Approach A)

---

## Overview

Add per-form webhook notifications to DSForms. When a form submission arrives, POST a formatted payload to a configured webhook URL. Supports three formats: generic JSON, Slack (Block Kit), and Discord (Embeds). Email becomes optional — a form can have email, webhook, both, or neither.

No new dependencies. Uses `net/http` and `encoding/json` from stdlib.

---

## Data Model

### Form Struct

```go
type Form struct {
    ID            string
    Name          string
    EmailTo       string    // now optional, can be ""
    Redirect      string
    WebhookURL    string    // new
    WebhookFormat string    // new: "generic", "slack", "discord", or ""
    CreatedAt     time.Time
}
```

### Schema

`CREATE TABLE` updated to include:

```sql
webhook_url    TEXT NOT NULL DEFAULT '',
webhook_format TEXT NOT NULL DEFAULT ''
```

Migration for existing DBs: two `ALTER TABLE ADD COLUMN` statements that silently fail if columns already exist.

### Store Methods Updated

- `CreateForm` — INSERT includes `webhook_url`, `webhook_format`
- `GetForm` — SELECT and Scan include new columns
- `ListForms` — SELECT and Scan include new columns
- `UpdateForm` — UPDATE SET includes new columns

### email_to Becomes Optional

- Handler no longer rejects forms with empty `email_to`
- Only `name` remains required when creating/editing a form

---

## Webhook Package

**New package:** `internal/webhook/`

### Sender

```go
type Sender struct {
    Client *http.Client  // 5s timeout
}

func NewSender() *Sender
func (s *Sender) Send(form store.Form, sub store.Submission) error
```

`Send` flow:
1. If `form.WebhookURL` is empty, return nil (no-op)
2. Build payload by switching on `form.WebhookFormat`
3. POST with `Content-Type: application/json`
4. 2xx = success; anything else = return error with status code and body snippet
5. No retry — fire and forget

### Payload Formats

**Generic** — flat JSON for Zapier/n8n/custom endpoints. `submitted_at` uses `time.Now().UTC()` (the webhook fires immediately after submission):

```json
{
  "form_name": "Contact",
  "form_id": "abc123",
  "submitted_at": "2026-03-26T14:30:00Z",
  "fields": {
    "name": "Jane",
    "email": "jane@example.com",
    "message": "Hello"
  }
}
```

**Slack** — Block Kit with smart field layout:

```json
{
  "text": "New submission: Contact",
  "blocks": [
    {
      "type": "header",
      "text": {"type": "plain_text", "text": "New Submission: Contact"}
    },
    {
      "type": "section",
      "fields": [
        {"type": "mrkdwn", "text": "*name:*\nJane"},
        {"type": "mrkdwn", "text": "*email:*\njane@example.com"}
      ]
    },
    {
      "type": "section",
      "text": {"type": "mrkdwn", "text": "*message:*\nHello"}
    },
    {
      "type": "context",
      "elements": [
        {"type": "mrkdwn", "text": "via DSForms \u2022 Mar 26, 2026 14:30 UTC"}
      ]
    }
  ]
}
```

Layout rules:
- `text` field always present as notification fallback
- Fields <=128 chars: paired side-by-side in section `fields` array (max 10 per section)
- Fields >128 chars: own full-width section block
- Header text truncated at 150 chars with `...`

**Discord** — Embed with color and field layout:

```json
{
  "username": "DSForms",
  "embeds": [{
    "title": "New Submission: Contact",
    "color": 5793266,
    "timestamp": "2026-03-26T14:30:00.000Z",
    "footer": {"text": "DSForms"},
    "fields": [
      {"name": "name", "value": "Jane", "inline": true},
      {"name": "email", "value": "jane@example.com", "inline": true},
      {"name": "message", "value": "Hello", "inline": false}
    ]
  }]
}
```

Layout rules:
- Fields <=64 chars: `inline: true` (side-by-side, up to 3 per row)
- Fields >64 chars: `inline: false` (full width)
- Field values truncated at 1024 chars with `...`
- Max 25 fields per embed
- Total embed text capped at 6000 chars
- Color: `5793266` (Discord blurple #5865F2)
- Username: `"DSForms"`

### Go Structs (all unexported)

```go
// Slack
type slackPayload struct {
    Text   string       `json:"text"`
    Blocks []slackBlock `json:"blocks"`
}
type slackBlock struct {
    Type     string      `json:"type"`
    Text     *slackText  `json:"text,omitempty"`
    Fields   []slackText `json:"fields,omitempty"`
    Elements []slackText `json:"elements,omitempty"`
}
type slackText struct {
    Type string `json:"type"`
    Text string `json:"text"`
}

// Discord
type discordPayload struct {
    Username string         `json:"username"`
    Embeds   []discordEmbed `json:"embeds"`
}
type discordEmbed struct {
    Title     string         `json:"title"`
    Color     int            `json:"color"`
    Timestamp string         `json:"timestamp"`
    Footer    discordFooter  `json:"footer"`
    Fields    []discordField `json:"fields"`
}
type discordFooter struct {
    Text string `json:"text"`
}
type discordField struct {
    Name   string `json:"name"`
    Value  string `json:"value"`
    Inline bool   `json:"inline"`
}
```

---

## Submit Handler Changes

### SubmitHandler Struct

```go
type SubmitHandler struct {
    Store    *store.Store
    Notifier Notifier          // existing, now nullable
    Webhook  *webhook.Sender   // new
    BaseURL  string
}
```

### Notification Goroutine

```go
go func() {
    defer func() { /* recover */ }()
    if form.EmailTo != "" && h.Notifier != nil {
        if err := h.Notifier.SendNotification(form, sub); err != nil {
            log.Printf("submit: email failed for form %s submission %s: %v", formID, sub.ID, err)
        }
    }
    if form.WebhookURL != "" && h.Webhook != nil {
        if err := h.Webhook.Send(form, sub); err != nil {
            log.Printf("submit: webhook failed for form %s submission %s: %v", formID, sub.ID, err)
        }
    }
}()
```

---

## Admin Handler & UI Changes

### Handler Changes

- `CreateForm` and `EditForm`: parse `webhook_url` and `webhook_format` from POST body
- Validation: only `name` is required. If `webhook_url` is non-empty, `webhook_format` must be one of `generic`, `slack`, `discord` — if missing or invalid, default to `generic`. If URL is empty, format is cleared to `""`.
- `email_to` no longer required

### Test Webhook Endpoint

`POST /admin/forms/{id}/test-webhook`

- Requires admin auth
- Reads form from DB
- If `webhook_url` is empty: returns `{"success": false, "error": "No webhook configured"}`
- Builds test payload with dummy data:
  ```go
  testSub := store.Submission{
      ID:     "test",
      FormID: form.ID,
      Data:   map[string]string{
          "name":    "Test User",
          "email":   "test@example.com",
          "message": "This is a test from DSForms",
      },
      IP: "127.0.0.1",
  }
  ```
- Calls `webhook.Send(form, testSub)`
- Returns `{"success": true}` or `{"success": false, "error": "..."}`

### Template Changes

**`form_edit.html` and `form_new.html`** — after redirect field:

```html
<div class="form-group">
  <label for="webhook_url">Webhook URL</label>
  <input type="url" id="webhook_url" name="webhook_url"
         value="{{.Form.WebhookURL}}"
         placeholder="https://hooks.slack.com/services/...">
  <div class="form-hint">Optional. POST submission data to an external service.</div>
</div>

<div class="form-group">
  <label for="webhook_format">Webhook Format</label>
  <select id="webhook_format" name="webhook_format">
    <option value="">None</option>
    <option value="generic">Generic JSON</option>
    <option value="slack">Slack</option>
    <option value="discord">Discord</option>
  </select>
</div>
```

**`form_edit.html` only** — test button:

```html
<button type="button" id="test-webhook-btn" class="btn btn-secondary">
  Test Webhook
</button>
<span id="test-webhook-result"></span>
```

Vanilla JS fetch to `POST /admin/forms/{id}/test-webhook`. Shows success/error inline.

Format dropdown and test button always visible. If URL is empty, format is ignored on save.

---

## Config Changes

`SMTP_HOST` and `SMTP_FROM` change from `requireEnv()` to `os.Getenv()`. App starts without SMTP configured.

```go
SMTPHost: os.Getenv("SMTP_HOST"),
SMTPFrom: os.Getenv("SMTP_FROM"),
```

No new env vars. Webhooks are per-form via DB.

In `main.go`, mailer is only created if SMTP is configured:

```go
var mailer handler.Notifier
if cfg.SMTPHost != "" && cfg.SMTPFrom != "" {
    mailer = &mail.Mailer{...}
}
```

---

## Security Considerations

- Webhook URLs stored in plaintext in DB (same risk level as `email_to`)
- SSRF: admin could set URL to localhost/internal IPs. Acceptable since self-hosted and only admins configure forms.
- 5s HTTP client timeout prevents resource exhaustion
- Webhook fires in goroutine — never blocks submission response
- Existing rate limiter on `POST /f/{formID}` prevents webhook spam

---

## Testing Strategy (TDD)

### `internal/webhook/webhook_test.go` (new)

| Test | Verifies |
|------|----------|
| `TestBuildGeneric` | Correct JSON shape: form_name, form_id, submitted_at, fields |
| `TestBuildSlack_ShortFields` | Short values (<=128 chars) paired in section fields array |
| `TestBuildSlack_LongFields` | Long values get own full-width section block |
| `TestBuildSlack_MixedFields` | Mix of short and long values laid out correctly |
| `TestBuildSlack_HeaderTruncation` | Form name >150 chars truncated with `...` |
| `TestBuildSlack_FallbackText` | `text` field always present |
| `TestBuildDiscord_InlineFields` | Short values (<=64 chars) have inline: true |
| `TestBuildDiscord_NonInlineFields` | Long values have inline: false |
| `TestBuildDiscord_FieldTruncation` | Values >1024 chars truncated with `...` |
| `TestBuildDiscord_MaxFields` | >25 fields truncated to 25 |
| `TestBuildDiscord_EmbedMeta` | Color, username, timestamp, footer correct |
| `TestSend_Success` | httptest server returns 200, Send returns nil |
| `TestSend_ServerError` | Server returns 500, Send returns error |
| `TestSend_Timeout` | Server delays >5s, Send returns timeout error |
| `TestSend_EmptyURL` | No-op, returns nil |

### `internal/store/store_test.go` (update)

| Test | Verifies |
|------|----------|
| `TestCreateForm_WithWebhook` | New columns persisted and retrieved |
| `TestUpdateForm_WebhookFields` | Webhook URL and format update correctly |
| `TestGetForm_DefaultWebhookEmpty` | Migrated forms have empty webhook fields |

### `internal/handler/submit_test.go` (update)

| Test | Verifies |
|------|----------|
| `TestSubmit_WebhookFired` | Form with webhook URL triggers Send |
| `TestSubmit_NoWebhook` | Form without webhook URL skips Send |
| `TestSubmit_EmailAndWebhook` | Both configured, both fire |
| `TestSubmit_NoEmailNoWebhook` | Submission saved, no notifications |

Uses `MockWebhookSender` (same pattern as `MockMailer`).

### `internal/handler/admin_test.go` (update)

| Test | Verifies |
|------|----------|
| `TestCreateForm_WebhookFields` | Webhook fields parsed from POST and stored |
| `TestEditForm_WebhookFields` | Same for update |
| `TestEditForm_EmailOptional` | Form saves with empty email_to |
| `TestTestWebhook_Success` | Endpoint calls Send, returns success JSON |
| `TestTestWebhook_NoWebhook` | Returns error when no webhook configured |
| `TestTestWebhook_Unauthorized` | Requires auth |

### `internal/config/config_test.go` (update)

| Test | Verifies |
|------|----------|
| `TestLoad_NoSMTP` | App loads without panic when SMTP vars missing |

All tests use `t.Parallel()` where no shared mutable state. `go test -race ./...` must pass.

---

## Files to Create/Modify

| File | Action |
|------|--------|
| `internal/webhook/webhook.go` | New — Sender, payload builders, structs |
| `internal/webhook/webhook_test.go` | New — all webhook tests |
| `internal/store/store.go` | Modify — Form struct, schema, CRUD queries |
| `internal/store/store_test.go` | Modify — webhook field tests |
| `internal/handler/submit.go` | Modify — Webhook field, goroutine logic |
| `internal/handler/submit_test.go` | Modify — webhook notification tests |
| `internal/handler/admin.go` | Modify — parse webhook fields, test endpoint |
| `internal/handler/admin_test.go` | Modify — webhook form + test endpoint tests |
| `internal/config/config.go` | Modify — SMTP optional |
| `internal/config/config_test.go` | Modify — no-SMTP test |
| `templates/form_edit.html` | Modify — webhook fields + test button |
| `templates/form_new.html` | Modify — webhook fields |
| `main.go` | Modify — wire Sender, conditional mailer, new route |

---

## Rate Limits (External)

| Platform | Limit | Why it's fine |
|----------|-------|---------------|
| Slack | 1 req/sec per webhook | Our rate limiter caps submissions at 6/min |
| Discord | 5 req/2sec per webhook | Same reason |
| Generic | Varies | Not our concern |
