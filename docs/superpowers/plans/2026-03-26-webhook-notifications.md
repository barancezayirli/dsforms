# Webhook Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-form webhook notifications with ready-made Slack, Discord, and generic JSON payloads.

**Architecture:** New `internal/webhook/` package with pure-function payload builders and an HTTP sender. Form struct gains `WebhookURL` and `WebhookFormat` fields. Submit handler fires webhook in the same goroutine as email. SMTP config becomes optional.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `net/http/httptest`), no new dependencies.

**Spec:** `docs/superpowers/specs/2026-03-26-webhook-notifications-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/config/config.go` | Modify | Make SMTP_HOST and SMTP_FROM optional |
| `internal/config/config_test.go` | Modify | Add no-SMTP test, update existing panic tests |
| `internal/store/store.go` | Modify | Add WebhookURL/WebhookFormat to Form, schema migration, CRUD |
| `internal/store/store_test.go` | Modify | Test webhook fields in CRUD |
| `internal/webhook/webhook.go` | Create | Sender, payload builders (generic, slack, discord) |
| `internal/webhook/webhook_test.go` | Create | All webhook package tests |
| `internal/handler/submit.go` | Modify | Add Webhook field, conditional notification goroutine |
| `internal/handler/submit_test.go` | Modify | Test webhook firing behavior |
| `internal/handler/admin.go` | Modify | Parse webhook fields, test-webhook endpoint, relax email_to validation |
| `internal/handler/admin_test.go` | Modify | Test webhook form fields, test-webhook endpoint, optional email |
| `templates/form_edit.html` | Modify | Webhook URL, format dropdown, test button |
| `templates/form_new.html` | Modify | Webhook URL, format dropdown |
| `main.go` | Modify | Conditional mailer, wire webhook.Sender, new route |

---

### Task 1: Make SMTP config optional

**Files:**
- Modify: `internal/config/config.go:36-40`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test for no-SMTP config**

Add a new test case to the existing table-driven `TestLoad` in `internal/config/config_test.go`:

```go
{
    name: "missing SMTP_HOST does not panic",
    setup: func(t *testing.T) {
        t.Setenv("SECRET_KEY", "test-secret-key-32-chars-long!!")
        // No SMTP vars set at all
    },
    check: func(t *testing.T, cfg Config) {
        if cfg.SMTPHost != "" {
            t.Errorf("SMTPHost = %q, want empty", cfg.SMTPHost)
        }
        if cfg.SMTPFrom != "" {
            t.Errorf("SMTPFrom = %q, want empty", cfg.SMTPFrom)
        }
    },
},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/config/ -run TestLoad/missing_SMTP_HOST_does_not_panic -v`
Expected: FAIL — panics because `requireEnv("SMTP_HOST")` fires.

- [ ] **Step 3: Change SMTP_HOST and SMTP_FROM to optional**

In `internal/config/config.go`, change lines 36 and 40:

```go
SMTPHost:       os.Getenv("SMTP_HOST"),
```

```go
SMTPFrom:       os.Getenv("SMTP_FROM"),
```

(Replace `requireEnv("SMTP_HOST")` with `os.Getenv("SMTP_HOST")` and `requireEnv("SMTP_FROM")` with `os.Getenv("SMTP_FROM")`.)

- [ ] **Step 4: Update existing panic tests**

The existing test cases `"missing SMTP_HOST panics"` and `"missing SMTP_FROM panics"` must be removed or changed. Replace them:

Remove the test case `"missing SMTP_HOST panics"` (the one with `t.Setenv("SMTP_HOST", "")` and `wantPanic: true`).

Remove the test case `"missing SMTP_FROM panics"` (the one with `t.Setenv("SMTP_FROM", "")` and `wantPanic: true`).

Also update `setAllRequired` to no longer set SMTP vars, since they're not required:

```go
func setAllRequired(t *testing.T) {
    t.Helper()
    t.Setenv("SECRET_KEY", "test-secret-key-32-chars-long!!")
}
```

And add SMTP vars explicitly in the test cases that check them:

In `"all required vars set"`:
```go
setup: func(t *testing.T) {
    setAllRequired(t)
    t.Setenv("SMTP_HOST", "smtp.example.com")
    t.Setenv("SMTP_USER", "user@example.com")
    t.Setenv("SMTP_PASS", "password123")
    t.Setenv("SMTP_FROM", "DSForms <noreply@example.com>")
},
```

- [ ] **Step 5: Run all config tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/config/ -v`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: make SMTP config optional for webhook-only setups"
```

---

### Task 2: Add webhook fields to Form struct and store

**Files:**
- Modify: `internal/store/store.go:33-39,58-95,328-392`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Write failing tests for webhook fields**

Add to `internal/store/store_test.go`:

```go
func TestCreateFormWithWebhook(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := store.Form{
		ID:            "form-wh",
		Name:          "Webhook Form",
		EmailTo:       "me@example.com",
		WebhookURL:    "https://hooks.slack.com/services/T00/B00/xxx",
		WebhookFormat: "slack",
	}
	if err := s.CreateForm(f); err != nil {
		t.Fatalf("CreateForm error = %v", err)
	}
	got, err := s.GetForm("form-wh")
	if err != nil {
		t.Fatalf("GetForm error = %v", err)
	}
	if got.WebhookURL != "https://hooks.slack.com/services/T00/B00/xxx" {
		t.Errorf("WebhookURL = %q, want webhook URL", got.WebhookURL)
	}
	if got.WebhookFormat != "slack" {
		t.Errorf("WebhookFormat = %q, want slack", got.WebhookFormat)
	}
}

func TestUpdateFormWebhookFields(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := store.Form{ID: "form-wh2", Name: "Test", EmailTo: "a@b.com"}
	_ = s.CreateForm(f)
	f.WebhookURL = "https://discord.com/api/webhooks/123/abc"
	f.WebhookFormat = "discord"
	if err := s.UpdateForm(f); err != nil {
		t.Fatalf("UpdateForm error = %v", err)
	}
	got, _ := s.GetForm("form-wh2")
	if got.WebhookURL != "https://discord.com/api/webhooks/123/abc" {
		t.Errorf("WebhookURL = %q, want discord URL", got.WebhookURL)
	}
	if got.WebhookFormat != "discord" {
		t.Errorf("WebhookFormat = %q, want discord", got.WebhookFormat)
	}
}

func TestGetFormDefaultWebhookEmpty(t *testing.T) {
	t.Parallel()
	s := mustNew(t)
	f := store.Form{ID: "form-no-wh", Name: "NoWH", EmailTo: "a@b.com"}
	_ = s.CreateForm(f)
	got, _ := s.GetForm("form-no-wh")
	if got.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty", got.WebhookURL)
	}
	if got.WebhookFormat != "" {
		t.Errorf("WebhookFormat = %q, want empty", got.WebhookFormat)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/store/ -run "TestCreateFormWithWebhook|TestUpdateFormWebhookFields|TestGetFormDefaultWebhookEmpty" -v`
Expected: FAIL — `WebhookURL` and `WebhookFormat` fields don't exist on Form.

- [ ] **Step 3: Add fields to Form struct**

In `internal/store/store.go`, update the Form struct (lines 33-39):

```go
type Form struct {
	ID            string
	Name          string
	EmailTo       string
	Redirect      string
	WebhookURL    string
	WebhookFormat string
	CreatedAt     time.Time
}
```

- [ ] **Step 4: Update schema and add migration**

In `internal/store/store.go`, update the `forms` table in the `schema` constant:

```sql
CREATE TABLE IF NOT EXISTS forms (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    email_to       TEXT NOT NULL DEFAULT '',
    redirect       TEXT NOT NULL DEFAULT '',
    webhook_url    TEXT NOT NULL DEFAULT '',
    webhook_format TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);
```

Add a migration function after `runMigrations`. In `internal/store/store.go`, add this function and call it from `New` after `runMigrations`:

```go
func runAlterMigrations(db *sql.DB) {
	alters := []string{
		"ALTER TABLE forms ADD COLUMN webhook_url TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE forms ADD COLUMN webhook_format TEXT NOT NULL DEFAULT ''",
	}
	for _, q := range alters {
		_, _ = db.Exec(q) // silently ignore "duplicate column" errors
	}
}
```

In the `New` function, call it after `runMigrations`:

```go
if err := runMigrations(db); err != nil {
    return nil, err
}
runAlterMigrations(db)
```

Also call it from `Reopen` after `runMigrations`:

```go
if err := runMigrations(newDB); err != nil {
    newDB.Close()
    return fmt.Errorf("reopen: %w", err)
}
runAlterMigrations(newDB)
```

- [ ] **Step 5: Update CRUD queries**

**CreateForm** — change the query:

```go
func (s *Store) CreateForm(f Form) error {
	_, err := s.db.Exec(
		"INSERT INTO forms (id, name, email_to, redirect, webhook_url, webhook_format) VALUES (?, ?, ?, ?, ?, ?)",
		f.ID, f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat,
	)
	if err != nil {
		return fmt.Errorf("create form: %w", err)
	}
	return nil
}
```

**GetForm** — change the query and scan:

```go
func (s *Store) GetForm(id string) (Form, error) {
	var f Form
	err := s.db.QueryRow(
		"SELECT id, name, email_to, redirect, webhook_url, webhook_format, created_at FROM forms WHERE id = ?",
		id,
	).Scan(&f.ID, &f.Name, &f.EmailTo, &f.Redirect, &f.WebhookURL, &f.WebhookFormat, &f.CreatedAt)
	if err != nil {
		return Form{}, fmt.Errorf("get form: %w", err)
	}
	return f, nil
}
```

**ListForms** — change the query and scan:

```go
func (s *Store) ListForms() ([]FormSummary, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.name, f.email_to, f.redirect, f.webhook_url, f.webhook_format, f.created_at,
		       COUNT(CASE WHEN s.read = 0 THEN 1 END) as unread_count
		FROM forms f
		LEFT JOIN submissions s ON s.form_id = f.id
		GROUP BY f.id
		ORDER BY f.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list forms: %w", err)
	}
	defer rows.Close()

	var forms []FormSummary
	for rows.Next() {
		var fs FormSummary
		if err := rows.Scan(&fs.ID, &fs.Name, &fs.EmailTo, &fs.Redirect, &fs.WebhookURL, &fs.WebhookFormat, &fs.CreatedAt, &fs.UnreadCount); err != nil {
			return nil, fmt.Errorf("list forms: %w", err)
		}
		forms = append(forms, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list forms: %w", err)
	}
	return forms, nil
}
```

**UpdateForm** — change the query:

```go
func (s *Store) UpdateForm(f Form) error {
	_, err := s.db.Exec(
		"UPDATE forms SET name = ?, email_to = ?, redirect = ?, webhook_url = ?, webhook_format = ? WHERE id = ?",
		f.Name, f.EmailTo, f.Redirect, f.WebhookURL, f.WebhookFormat, f.ID,
	)
	if err != nil {
		return fmt.Errorf("update form: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run all store tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/store/ -race -v`
Expected: All PASS (new tests + existing tests).

- [ ] **Step 7: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add webhook_url and webhook_format to forms table and Form struct"
```

---

### Task 3: Webhook package — generic payload builder

**Files:**
- Create: `internal/webhook/webhook_test.go`
- Create: `internal/webhook/webhook.go`

- [ ] **Step 1: Write failing test for generic payload**

Create `internal/webhook/webhook_test.go`:

```go
package webhook

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

func TestBuildGeneric(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	sub := store.Submission{
		ID:     "s1",
		FormID: "f1",
		Data:   map[string]string{"name": "Jane", "email": "jane@example.com"},
	}

	payload, err := buildGeneric(form, sub)
	if err != nil {
		t.Fatalf("buildGeneric error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if result["form_name"] != "Contact" {
		t.Errorf("form_name = %v, want Contact", result["form_name"])
	}
	if result["form_id"] != "f1" {
		t.Errorf("form_id = %v, want f1", result["form_id"])
	}
	if _, ok := result["submitted_at"]; !ok {
		t.Error("submitted_at missing")
	}
	fields, ok := result["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("fields is not a map")
	}
	if fields["name"] != "Jane" {
		t.Errorf("fields.name = %v, want Jane", fields["name"])
	}
	if fields["email"] != "jane@example.com" {
		t.Errorf("fields.email = %v, want jane@example.com", fields["email"])
	}
}
```

- [ ] **Step 2: Create minimal webhook.go to make test compile, then run test**

Create `internal/webhook/webhook.go`:

```go
package webhook

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

// Sender sends webhook notifications via HTTP POST.
type Sender struct {
	Client *http.Client
}

// NewSender creates a Sender with a 5-second timeout.
func NewSender() *Sender {
	return &Sender{
		Client: &http.Client{Timeout: 5 * time.Second},
	}
}

type genericPayload struct {
	FormName    string            `json:"form_name"`
	FormID      string            `json:"form_id"`
	SubmittedAt string            `json:"submitted_at"`
	Fields      map[string]string `json:"fields"`
}

func buildGeneric(form store.Form, sub store.Submission) ([]byte, error) {
	p := genericPayload{
		FormName:    form.Name,
		FormID:      form.ID,
		SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		Fields:      sub.Data,
	}
	return json.Marshal(p)
}
```

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run TestBuildGeneric -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/webhook/webhook.go internal/webhook/webhook_test.go
git commit -m "feat: add webhook package with generic payload builder"
```

---

### Task 4: Webhook package — Slack payload builder

**Files:**
- Modify: `internal/webhook/webhook_test.go`
- Modify: `internal/webhook/webhook.go`

- [ ] **Step 1: Write failing tests for Slack payload**

Append to `internal/webhook/webhook_test.go`:

```go
func TestBuildSlack_ShortFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	sub := store.Submission{
		Data: map[string]string{"name": "Jane", "email": "jane@example.com"},
	}
	payload, err := buildSlack(form, sub)
	if err != nil {
		t.Fatalf("buildSlack error: %v", err)
	}
	var result slackPayload
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if result.Text != "New submission: Contact" {
		t.Errorf("text = %q, want fallback text", result.Text)
	}
	// Should have: header, section with fields, context = 3 blocks
	if len(result.Blocks) < 2 {
		t.Fatalf("blocks = %d, want at least 2", len(result.Blocks))
	}
	if result.Blocks[0].Type != "header" {
		t.Errorf("first block type = %q, want header", result.Blocks[0].Type)
	}
	// Second block should be section with fields (both values are short)
	if result.Blocks[1].Type != "section" {
		t.Errorf("second block type = %q, want section", result.Blocks[1].Type)
	}
	if len(result.Blocks[1].Fields) == 0 {
		t.Error("second block should have fields for short values")
	}
}

func TestBuildSlack_LongFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	longValue := string(make([]byte, 200)) // 200 chars > 128 threshold
	for i := range longValue {
		longValue = longValue[:i] + "x" + longValue[i+1:]
	}
	sub := store.Submission{
		Data: map[string]string{"message": longValue},
	}
	payload, err := buildSlack(form, sub)
	if err != nil {
		t.Fatalf("buildSlack error: %v", err)
	}
	var result slackPayload
	json.Unmarshal(payload, &result)
	// Long field should get its own section with .Text, not .Fields
	found := false
	for _, b := range result.Blocks {
		if b.Type == "section" && b.Text != nil && len(b.Fields) == 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected a full-width section for long field value")
	}
}

func TestBuildSlack_HeaderTruncation(t *testing.T) {
	t.Parallel()
	longName := ""
	for i := 0; i < 200; i++ {
		longName += "a"
	}
	form := store.Form{ID: "f1", Name: longName}
	sub := store.Submission{Data: map[string]string{"x": "y"}}
	payload, _ := buildSlack(form, sub)
	var result slackPayload
	json.Unmarshal(payload, &result)
	headerText := result.Blocks[0].Text.Text
	if len(headerText) > 150 {
		t.Errorf("header text len = %d, want <= 150", len(headerText))
	}
}

func TestBuildSlack_FallbackText(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	sub := store.Submission{Data: map[string]string{"x": "y"}}
	payload, _ := buildSlack(form, sub)
	var result slackPayload
	json.Unmarshal(payload, &result)
	if result.Text == "" {
		t.Error("text fallback is empty")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run "TestBuildSlack" -v`
Expected: FAIL — `buildSlack` function not defined.

- [ ] **Step 3: Implement Slack payload builder**

Add to `internal/webhook/webhook.go`:

```go
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

func buildSlack(form store.Form, sub store.Submission) ([]byte, error) {
	title := "New Submission: " + form.Name
	if len(title) > 150 {
		title = title[:147] + "..."
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{Type: "plain_text", Text: title},
		},
	}

	// Separate short and long fields
	var shortFields []slackText
	keys := sortedKeys(sub.Data)
	for _, key := range keys {
		val := sub.Data[key]
		if len(val) <= 128 {
			shortFields = append(shortFields, slackText{
				Type: "mrkdwn",
				Text: "*" + key + ":*\n" + val,
			})
		} else {
			blocks = append(blocks, slackBlock{
				Type: "section",
				Text: &slackText{Type: "mrkdwn", Text: "*" + key + ":*\n" + val},
			})
		}
	}

	// Add short fields in batches of 10
	for i := 0; i < len(shortFields); i += 10 {
		end := i + 10
		if end > len(shortFields) {
			end = len(shortFields)
		}
		blocks = append(blocks, slackBlock{
			Type:   "section",
			Fields: shortFields[i:end],
		})
	}

	// Context footer
	blocks = append(blocks, slackBlock{
		Type: "context",
		Elements: []slackText{
			{Type: "mrkdwn", Text: "via DSForms \u2022 " + time.Now().UTC().Format("Jan 2, 2006 15:04 UTC")},
		},
	})

	p := slackPayload{
		Text:   "New submission: " + form.Name,
		Blocks: blocks,
	}
	return json.Marshal(p)
}
```

Also add the `sortedKeys` helper:

```go
import "sort"

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 4: Run Slack tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run "TestBuildSlack" -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webhook/webhook.go internal/webhook/webhook_test.go
git commit -m "feat: add Slack Block Kit payload builder"
```

---

### Task 5: Webhook package — Discord payload builder

**Files:**
- Modify: `internal/webhook/webhook_test.go`
- Modify: `internal/webhook/webhook.go`

- [ ] **Step 1: Write failing tests for Discord payload**

Append to `internal/webhook/webhook_test.go`:

```go
func TestBuildDiscord_InlineFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	sub := store.Submission{
		Data: map[string]string{"name": "Jane", "email": "jane@example.com"},
	}
	payload, err := buildDiscord(form, sub)
	if err != nil {
		t.Fatalf("buildDiscord error: %v", err)
	}
	var result discordPayload
	json.Unmarshal(payload, &result)
	if result.Username != "DSForms" {
		t.Errorf("username = %q, want DSForms", result.Username)
	}
	if len(result.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(result.Embeds))
	}
	embed := result.Embeds[0]
	if embed.Color != 5793266 {
		t.Errorf("color = %d, want 5793266", embed.Color)
	}
	for _, f := range embed.Fields {
		if !f.Inline {
			t.Errorf("field %q should be inline (value len %d <= 64)", f.Name, len(f.Value))
		}
	}
}

func TestBuildDiscord_NonInlineFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	longValue := ""
	for i := 0; i < 100; i++ {
		longValue += "x"
	}
	sub := store.Submission{
		Data: map[string]string{"message": longValue},
	}
	payload, _ := buildDiscord(form, sub)
	var result discordPayload
	json.Unmarshal(payload, &result)
	for _, f := range result.Embeds[0].Fields {
		if f.Name == "message" && f.Inline {
			t.Error("long field should not be inline")
		}
	}
}

func TestBuildDiscord_FieldTruncation(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	longValue := ""
	for i := 0; i < 1100; i++ {
		longValue += "x"
	}
	sub := store.Submission{
		Data: map[string]string{"msg": longValue},
	}
	payload, _ := buildDiscord(form, sub)
	var result discordPayload
	json.Unmarshal(payload, &result)
	for _, f := range result.Embeds[0].Fields {
		if len(f.Value) > 1024 {
			t.Errorf("field value len = %d, want <= 1024", len(f.Value))
		}
	}
}

func TestBuildDiscord_MaxFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	data := make(map[string]string)
	for i := 0; i < 30; i++ {
		data[fmt.Sprintf("field_%02d", i)] = "value"
	}
	sub := store.Submission{Data: data}
	payload, _ := buildDiscord(form, sub)
	var result discordPayload
	json.Unmarshal(payload, &result)
	if len(result.Embeds[0].Fields) > 25 {
		t.Errorf("fields = %d, want <= 25", len(result.Embeds[0].Fields))
	}
}

func TestBuildDiscord_EmbedMeta(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	sub := store.Submission{Data: map[string]string{"x": "y"}}
	payload, _ := buildDiscord(form, sub)
	var result discordPayload
	json.Unmarshal(payload, &result)
	embed := result.Embeds[0]
	if embed.Title != "New Submission: Contact" {
		t.Errorf("title = %q", embed.Title)
	}
	if embed.Footer.Text != "DSForms" {
		t.Errorf("footer = %q, want DSForms", embed.Footer.Text)
	}
	if embed.Timestamp == "" {
		t.Error("timestamp is empty")
	}
}
```

Add `"fmt"` to the import block in the test file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run "TestBuildDiscord" -v`
Expected: FAIL — `buildDiscord` function not defined.

- [ ] **Step 3: Implement Discord payload builder**

Add to `internal/webhook/webhook.go`:

```go
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

func buildDiscord(form store.Form, sub store.Submission) ([]byte, error) {
	var fields []discordField
	keys := sortedKeys(sub.Data)
	for _, key := range keys {
		val := sub.Data[key]
		if len(val) > 1024 {
			val = val[:1021] + "..."
		}
		fields = append(fields, discordField{
			Name:   key,
			Value:  val,
			Inline: len(sub.Data[key]) <= 64,
		})
	}
	if len(fields) > 25 {
		fields = fields[:25]
	}

	p := discordPayload{
		Username: "DSForms",
		Embeds: []discordEmbed{
			{
				Title:     "New Submission: " + form.Name,
				Color:     5793266,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Footer:    discordFooter{Text: "DSForms"},
				Fields:    fields,
			},
		},
	}
	return json.Marshal(p)
}
```

- [ ] **Step 4: Run Discord tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run "TestBuildDiscord" -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webhook/webhook.go internal/webhook/webhook_test.go
git commit -m "feat: add Discord embed payload builder"
```

---

### Task 6: Webhook package — Send method with HTTP tests

**Files:**
- Modify: `internal/webhook/webhook_test.go`
- Modify: `internal/webhook/webhook.go`

- [ ] **Step 1: Write failing tests for Send**

Append to `internal/webhook/webhook_test.go`:

```go
import (
	"net/http/httptest"
	"io"
	"sync/atomic"
	"net/http"
	"time"
)

func TestSend_Success(t *testing.T) {
	t.Parallel()
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("empty body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "generic"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	err := sender.Send(form, sub)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if called.Load() != 1 {
		t.Errorf("server called %d times, want 1", called.Load())
	}
}

func TestSend_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "generic"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	err := sender.Send(form, sub)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestSend_EmptyURL(t *testing.T) {
	t.Parallel()
	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: "", WebhookFormat: "generic"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	err := sender.Send(form, sub)
	if err != nil {
		t.Fatalf("expected nil for empty URL, got: %v", err)
	}
}

func TestSend_Timeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "generic"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	err := sender.Send(form, sub)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -run "TestSend" -v -timeout 30s`
Expected: FAIL — `Send` method not defined.

- [ ] **Step 3: Implement Send method**

Add to `internal/webhook/webhook.go`:

```go
import (
	"bytes"
	"fmt"
	"io"
)

// Send posts a webhook payload to the form's webhook URL.
// Returns nil if WebhookURL is empty (no-op).
func (s *Sender) Send(form store.Form, sub store.Submission) error {
	if form.WebhookURL == "" {
		return nil
	}

	var payload []byte
	var err error
	switch form.WebhookFormat {
	case "slack":
		payload, err = buildSlack(form, sub)
	case "discord":
		payload, err = buildDiscord(form, sub)
	default:
		payload, err = buildGeneric(form, sub)
	}
	if err != nil {
		return fmt.Errorf("webhook build payload: %w", err)
	}

	resp, err := s.Client.Post(form.WebhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook response %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
```

- [ ] **Step 4: Run all webhook tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/webhook/ -race -v -timeout 30s`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webhook/webhook.go internal/webhook/webhook_test.go
git commit -m "feat: add webhook Send method with format dispatch"
```

---

### Task 7: Submit handler — wire webhook into notification goroutine

**Files:**
- Modify: `internal/handler/submit.go:17-27,116-125`
- Modify: `internal/handler/submit_test.go`

- [ ] **Step 1: Write failing tests for webhook firing**

Add to `internal/handler/submit_test.go`. First, we need imports and a mock. Add these imports and the mock type:

```go
import (
	"sync"
	"sync/atomic"
	"github.com/youruser/dsforms/internal/webhook"
)
```

Add mock webhook sender to the test file:

```go
type mockWebhookSender struct {
	mu    sync.Mutex
	calls []store.Form
	ch    chan struct{}
}

func newMockWebhookSender() *mockWebhookSender {
	return &mockWebhookSender{ch: make(chan struct{}, 10)}
}

func (m *mockWebhookSender) Send(form store.Form, sub store.Submission) error {
	m.mu.Lock()
	m.calls = append(m.calls, form)
	m.mu.Unlock()
	m.ch <- struct{}{}
	return nil
}

func (m *mockWebhookSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockWebhookSender) wait(timeout time.Duration) bool {
	select {
	case <-m.ch:
		return true
	case <-time.After(timeout):
		return false
	}
}
```

Now write the tests:

```go
func TestSubmitWebhookFired(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{
		ID: "wh-form", Name: "WH", EmailTo: "test@test.com",
		WebhookURL: "https://hooks.example.com", WebhookFormat: "generic",
	})
	m := mail.NewMockMailer()
	wh := newMockWebhookSender()
	h := &SubmitHandler{Store: s, Notifier: m, Webhook: wh, BaseURL: "https://example.com"}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/wh-form", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !wh.wait(2 * time.Second) {
		t.Fatal("webhook not called within timeout")
	}
	if wh.callCount() != 1 {
		t.Errorf("webhook calls = %d, want 1", wh.callCount())
	}
}

func TestSubmitNoWebhook(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{ID: "no-wh", Name: "NoWH", EmailTo: "test@test.com"})
	m := mail.NewMockMailer()
	wh := newMockWebhookSender()
	h := &SubmitHandler{Store: s, Notifier: m, Webhook: wh, BaseURL: "https://example.com"}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/no-wh", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Wait for email to fire (proves goroutine ran)
	if !m.Wait(2 * time.Second) {
		t.Fatal("email not sent")
	}
	// Webhook should NOT have been called
	if wh.callCount() != 0 {
		t.Errorf("webhook calls = %d, want 0", wh.callCount())
	}
}

func TestSubmitNoEmailNoWebhook(t *testing.T) {
	t.Parallel()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	_ = s.CreateForm(store.Form{ID: "silent", Name: "Silent"})
	h := &SubmitHandler{Store: s, Notifier: nil, Webhook: nil, BaseURL: "https://example.com"}
	r := chi.NewRouter()
	r.Post("/f/{formID}", h.Handle)

	form := url.Values{"name": {"Alice"}}
	req := httptest.NewRequest("POST", "/f/silent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	subs, _ := s.ListSubmissions("silent")
	if len(subs) != 1 {
		t.Errorf("submissions = %d, want 1", len(subs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -run "TestSubmitWebhook|TestSubmitNoWebhook|TestSubmitNoEmailNoWebhook" -v`
Expected: FAIL — `Webhook` field not on SubmitHandler, mockWebhookSender interface mismatch.

- [ ] **Step 3: Add WebhookSender interface and Webhook field to SubmitHandler**

In `internal/handler/submit.go`, add the interface and update the struct:

```go
// WebhookSender sends webhook notifications.
type WebhookSender interface {
	Send(form store.Form, sub store.Submission) error
}

// SubmitHandler handles form submissions via POST /f/{formID}.
type SubmitHandler struct {
	Store    *store.Store
	Notifier Notifier
	Webhook  WebhookSender
	BaseURL  string
}
```

- [ ] **Step 4: Update notification goroutine**

Replace the goroutine (lines 116-125) with:

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("submit: panic in notification for form %s submission %s: %v", formID, sub.ID, r)
        }
    }()
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

- [ ] **Step 5: Run all handler tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -race -v -timeout 30s`
Expected: All PASS (existing + new tests).

- [ ] **Step 6: Commit**

```bash
git add internal/handler/submit.go internal/handler/submit_test.go
git commit -m "feat: fire webhook in submit handler notification goroutine"
```

---

### Task 8: Admin handler — parse webhook fields and relax email validation

**Files:**
- Modify: `internal/handler/admin.go:136-181,216-278`
- Modify: `internal/handler/admin_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/handler/admin_test.go`:

```go
func TestCreateFormWebhookFields(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"Contact"},
		"email_to":       {"me@example.com"},
		"webhook_url":    {"https://hooks.slack.com/services/T00/B00/xxx"},
		"webhook_format": {"slack"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if forms[0].WebhookURL != "https://hooks.slack.com/services/T00/B00/xxx" {
		t.Errorf("WebhookURL = %q", forms[0].WebhookURL)
	}
	if forms[0].WebhookFormat != "slack" {
		t.Errorf("WebhookFormat = %q", forms[0].WebhookFormat)
	}
}

func TestEditFormWebhookFields(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{
		"name":           {"New"},
		"email_to":       {"new@example.com"},
		"webhook_url":    {"https://discord.com/api/webhooks/123/abc"},
		"webhook_format": {"discord"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.WebhookURL != "https://discord.com/api/webhooks/123/abc" {
		t.Errorf("WebhookURL = %q", f.WebhookURL)
	}
	if f.WebhookFormat != "discord" {
		t.Errorf("WebhookFormat = %q", f.WebhookFormat)
	}
}

func TestCreateFormEmailOptional(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{
		"name":           {"WebhookOnly"},
		"webhook_url":    {"https://hooks.slack.com/services/T00/B00/xxx"},
		"webhook_format": {"slack"},
	}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (email should be optional now)", w.Code)
	}
	forms, _ := s.ListForms()
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
}

func TestEditFormEmailOptional(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "old@example.com"})
	form := url.Values{"name": {"Test"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (email should be optional)", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.EmailTo != "" {
		t.Errorf("EmailTo = %q, want empty", f.EmailTo)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -run "TestCreateFormWebhookFields|TestEditFormWebhookFields|TestCreateFormEmailOptional|TestEditFormEmailOptional" -v`
Expected: FAIL — webhook fields not parsed, email still required.

- [ ] **Step 3: Update CreateForm handler**

In `internal/handler/admin.go`, replace the `CreateForm` method (lines 136-181):

```go
func (h *AdminHandler) CreateForm(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")
	webhookURL := r.FormValue("webhook_url")
	webhookFormat := r.FormValue("webhook_format")

	if name == "" {
		data := formNewData{
			Title:       "New Form",
			Active:      "forms",
			CurrentUser: user,
			Form: store.Form{
				Name:          name,
				EmailTo:       emailTo,
				Redirect:      redirect,
				WebhookURL:    webhookURL,
				WebhookFormat: webhookFormat,
			},
			Error: "Form name is required.",
		}
		if err := h.Templates["form_new.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_new template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Default webhook format to "generic" if URL provided but format empty/invalid
	if webhookURL != "" {
		switch webhookFormat {
		case "generic", "slack", "discord":
			// valid
		default:
			webhookFormat = "generic"
		}
	} else {
		webhookFormat = ""
	}

	f := store.Form{
		ID:            uuid.New().String(),
		Name:          name,
		EmailTo:       emailTo,
		Redirect:      redirect,
		WebhookURL:    webhookURL,
		WebhookFormat: webhookFormat,
	}
	if err := h.Store.CreateForm(f); err != nil {
		log.Printf("create form error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+f.ID+"/edit", http.StatusFound)
}
```

- [ ] **Step 4: Update EditForm handler**

In `internal/handler/admin.go`, replace the `EditForm` method (lines 216-278):

```go
func (h *AdminHandler) EditForm(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")

	name := r.FormValue("name")
	emailTo := r.FormValue("email_to")
	redirect := r.FormValue("redirect")
	webhookURL := r.FormValue("webhook_url")
	webhookFormat := r.FormValue("webhook_format")

	if name == "" {
		f, err := h.Store.GetForm(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "form not found", http.StatusNotFound)
				return
			}
			log.Printf("edit form: get form %s error: %v", id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		f.Name = name
		f.EmailTo = emailTo
		f.Redirect = redirect
		f.WebhookURL = webhookURL
		f.WebhookFormat = webhookFormat

		flashType, flashMsg := flash.Get(r, w, h.SecretKey)
		data := formEditData{
			Title:       "Edit Form",
			Active:      "forms",
			CurrentUser: user,
			Flash:       newFlash(flashType, flashMsg),
			Form:        f,
			BaseURL:     h.BaseURL,
			Error:       "Form name is required.",
		}
		if err := h.Templates["form_edit.html"].ExecuteTemplate(w, "base", data); err != nil {
			log.Printf("form_edit template error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Default webhook format to "generic" if URL provided but format empty/invalid
	if webhookURL != "" {
		switch webhookFormat {
		case "generic", "slack", "discord":
			// valid
		default:
			webhookFormat = "generic"
		}
	} else {
		webhookFormat = ""
	}

	f := store.Form{
		ID:            id,
		Name:          name,
		EmailTo:       emailTo,
		Redirect:      redirect,
		WebhookURL:    webhookURL,
		WebhookFormat: webhookFormat,
	}

	if err := h.Store.UpdateForm(f); err != nil {
		log.Printf("edit form: update %s error: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/forms/"+id+"/edit", http.StatusFound)
}
```

- [ ] **Step 5: Fix existing tests that expect email-required behavior**

The existing tests `TestCreateFormEmptyEmail` and `TestEditFormEmptyEmail` expect 200 (re-render with error). With email now optional, they should expect 302 (success). Update:

`TestCreateFormEmptyEmail`:
```go
func TestCreateFormEmptyEmail(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	form := url.Values{"name": {"Contact"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/new", form.Encode())
	// Email is now optional — should succeed
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
}
```

`TestEditFormEmptyEmail`:
```go
func TestEditFormEmptyEmail(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Old", EmailTo: "old@example.com"})
	form := url.Values{"name": {"New"}, "email_to": {""}}
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/edit", form.Encode())
	// Email is now optional — should succeed
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	f, _ := s.GetForm("f1")
	if f.EmailTo != "" {
		t.Errorf("EmailTo = %q, want empty", f.EmailTo)
	}
}
```

- [ ] **Step 6: Run all handler tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -race -v -timeout 30s`
Expected: All PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handler/admin.go internal/handler/admin_test.go
git commit -m "feat: parse webhook fields in admin handlers, make email optional"
```

---

### Task 9: Test webhook endpoint

**Files:**
- Modify: `internal/handler/admin.go`
- Modify: `internal/handler/admin_test.go`

- [ ] **Step 1: Write failing tests for test-webhook endpoint**

Add to `internal/handler/admin_test.go`:

```go
func TestTestWebhookSuccess(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{
		ID: "f1", Name: "Test", EmailTo: "a@b.com",
		WebhookURL: "https://hooks.example.com", WebhookFormat: "generic",
	})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/test-webhook", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	// The webhook will fail (fake URL) but we test the handler responds with JSON
	if _, ok := resp["success"]; !ok {
		t.Error("response missing 'success' key")
	}
}

func TestTestWebhookNoWebhook(t *testing.T) {
	t.Parallel()
	s, r := setupAdmin(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	w := doAdminRequest(t, s, r, "POST", "/admin/forms/f1/test-webhook", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["success"] != false {
		t.Error("expected success=false for form without webhook")
	}
	if _, ok := resp["error"]; !ok {
		t.Error("expected error message")
	}
}
```

Add `"encoding/json"` to the import block in `admin_test.go` if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -run "TestTestWebhook" -v`
Expected: FAIL — 404 because route doesn't exist.

- [ ] **Step 3: Add TestWebhook handler and WebhookSender field**

Add `WebhookSender` field to `AdminHandler` in `internal/handler/admin.go`:

```go
type AdminHandler struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	Templates map[string]*template.Template
	Webhook   WebhookSender
}
```

Add the `TestWebhook` handler method:

```go
// TestWebhook handles POST to test a form's webhook configuration.
func (h *AdminHandler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	w.Header().Set("Content-Type", "application/json")

	form, err := h.Store.GetForm(id)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "form not found",
		})
		return
	}

	if form.WebhookURL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "No webhook configured",
		})
		return
	}

	testSub := store.Submission{
		ID:     "test",
		FormID: form.ID,
		Data: map[string]string{
			"name":    "Test User",
			"email":   "test@example.com",
			"message": "This is a test from DSForms",
		},
		IP: "127.0.0.1",
	}

	if h.Webhook != nil {
		if err := h.Webhook.Send(form, testSub); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
```

- [ ] **Step 4: Wire the route in setupAdmin test helper**

In `admin_test.go`, update `setupAdmin` to add the route and set the Webhook field. After line 101 (`r.Post("/admin/submissions/{id}/delete", ah.DeleteSubmission)`), add:

```go
r.Post("/admin/forms/{id}/test-webhook", ah.TestWebhook)
```

- [ ] **Step 5: Run test-webhook tests**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -run "TestTestWebhook" -v`
Expected: All PASS.

- [ ] **Step 6: Run all handler tests to check no regressions**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test ./internal/handler/ -race -v -timeout 30s`
Expected: All PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handler/admin.go internal/handler/admin_test.go
git commit -m "feat: add test-webhook endpoint for admin forms"
```

---

### Task 10: Templates — webhook fields and test button

**Files:**
- Modify: `templates/form_edit.html`
- Modify: `templates/form_new.html`

- [ ] **Step 1: Update form_new.html**

In `templates/form_new.html`, after the redirect `form-group` div (before the closing `</div>` with the buttons), add:

```html
      <div class="form-group">
        <label for="webhook_url">Webhook URL</label>
        <input type="url" id="webhook_url" name="webhook_url" value="{{.Form.WebhookURL}}" placeholder="https://hooks.slack.com/services/...">
        <div class="form-hint">Optional. POST submission data to an external service.</div>
      </div>
      <div class="form-group">
        <label for="webhook_format">Webhook Format</label>
        <select id="webhook_format" name="webhook_format">
          <option value=""{{if eq .Form.WebhookFormat ""}} selected{{end}}>None</option>
          <option value="generic"{{if eq .Form.WebhookFormat "generic"}} selected{{end}}>Generic JSON</option>
          <option value="slack"{{if eq .Form.WebhookFormat "slack"}} selected{{end}}>Slack</option>
          <option value="discord"{{if eq .Form.WebhookFormat "discord"}} selected{{end}}>Discord</option>
        </select>
      </div>
```

Also change the `email_to` input to remove the `required` attribute:

```html
        <input type="email" id="email_to" name="email_to" value="{{.Form.EmailTo}}" placeholder="Where notifications go">
```

- [ ] **Step 2: Update form_edit.html**

In `templates/form_edit.html`, after the redirect `form-group` div (before the buttons div), add the same webhook fields:

```html
      <div class="form-group">
        <label for="webhook_url">Webhook URL</label>
        <input type="url" id="webhook_url" name="webhook_url" value="{{.Form.WebhookURL}}" placeholder="https://hooks.slack.com/services/...">
        <div class="form-hint">Optional. POST submission data to an external service.</div>
      </div>
      <div class="form-group">
        <label for="webhook_format">Webhook Format</label>
        <select id="webhook_format" name="webhook_format">
          <option value=""{{if eq .Form.WebhookFormat ""}} selected{{end}}>None</option>
          <option value="generic"{{if eq .Form.WebhookFormat "generic"}} selected{{end}}>Generic JSON</option>
          <option value="slack"{{if eq .Form.WebhookFormat "slack"}} selected{{end}}>Slack</option>
          <option value="discord"{{if eq .Form.WebhookFormat "discord"}} selected{{end}}>Discord</option>
        </select>
      </div>
```

Also change the `email_to` input to remove the `required` attribute:

```html
        <input type="email" id="email_to" name="email_to" value="{{.Form.EmailTo}}">
```

After the Save/Cancel buttons div (but still inside the card-body form), add the test button:

```html
      <div style="margin-top:12px">
        <button type="button" id="test-webhook-btn" class="btn" onclick="testWebhook()">Test Webhook</button>
        <span id="test-webhook-result" style="margin-left:8px"></span>
      </div>
```

At the bottom of the template (before `{{end}}`), add the JS:

```html
<script>
function testWebhook() {
  var btn = document.getElementById('test-webhook-btn');
  var result = document.getElementById('test-webhook-result');
  btn.disabled = true;
  result.textContent = 'Sending...';
  fetch('/admin/forms/{{.Form.ID}}/test-webhook', {method: 'POST'})
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if (data.success) {
        result.textContent = 'Success!';
        result.style.color = 'var(--color-success, green)';
      } else {
        result.textContent = 'Failed: ' + (data.error || 'unknown error');
        result.style.color = 'var(--color-danger, red)';
      }
      btn.disabled = false;
    })
    .catch(function(err) {
      result.textContent = 'Error: ' + err.message;
      result.style.color = 'var(--color-danger, red)';
      btn.disabled = false;
    });
}
</script>
```

- [ ] **Step 3: Commit**

```bash
git add templates/form_edit.html templates/form_new.html
git commit -m "feat: add webhook URL, format dropdown, and test button to form templates"
```

---

### Task 11: Wire everything in main.go

**Files:**
- Modify: `main.go:304-317,365-392`

- [ ] **Step 1: Update main.go — conditional mailer and webhook sender**

Replace the mailer creation (lines 304-311) with:

```go
	var mailer handler.Notifier
	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" {
		mailer = &mail.Mailer{
			Host:    cfg.SMTPHost,
			Port:    cfg.SMTPPort,
			User:    cfg.SMTPUser,
			Pass:    cfg.SMTPPass,
			From:    cfg.SMTPFrom,
			BaseURL: cfg.BaseURL,
		}
	}
```

Add the webhook import and sender creation after the mailer:

```go
	webhookSender := webhook.NewSender()
```

Update the import block to add `"github.com/youruser/dsforms/internal/webhook"`.

Update the submitHandler (lines 313-317):

```go
	submitHandler := &handler.SubmitHandler{
		Store:    s,
		Notifier: mailer,
		Webhook:  webhookSender,
		BaseURL:  cfg.BaseURL,
	}
```

Update the adminHandler to include the webhook sender:

```go
	adminHandler := &handler.AdminHandler{
		Store:     s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		Templates: templates,
		Webhook:   webhookSender,
	}
```

- [ ] **Step 2: Add the test-webhook route**

In the admin route group (after line 379 `r.Post("/admin/forms/{id}/read-all", adminHandler.MarkAllRead)`), add:

```go
		r.Post("/admin/forms/{id}/test-webhook", adminHandler.TestWebhook)
```

- [ ] **Step 3: Build to verify compilation**

Run: `cd /Users/barancezayirli/Projects/dsforms && go build ./...`
Expected: No errors.

- [ ] **Step 4: Run entire test suite**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test -race ./... -timeout 60s`
Expected: All PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: wire webhook sender and conditional mailer in main.go"
```

---

### Task 12: Final verification

- [ ] **Step 1: Full test suite with race detector**

Run: `cd /Users/barancezayirli/Projects/dsforms && go test -race -count=1 ./... -timeout 60s`
Expected: All PASS, no race conditions.

- [ ] **Step 2: Build the binary**

Run: `cd /Users/barancezayirli/Projects/dsforms && CGO_ENABLED=0 go build -o /dev/null .`
Expected: Clean build, no errors.

- [ ] **Step 3: Review git log**

Run: `cd /Users/barancezayirli/Projects/dsforms && git log --oneline -12`
Expected: See all feature commits in order.
