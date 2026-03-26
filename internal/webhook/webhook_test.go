package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/youruser/dsforms/internal/store"
)

// --- Generic payload tests ---

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
}

// --- Slack payload tests ---

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
	if len(result.Blocks) < 2 {
		t.Fatalf("blocks = %d, want at least 2", len(result.Blocks))
	}
	if result.Blocks[0].Type != "header" {
		t.Errorf("first block type = %q, want header", result.Blocks[0].Type)
	}
	// Second block should be section with fields (both values are short)
	found := false
	for _, b := range result.Blocks {
		if b.Type == "section" && len(b.Fields) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected a section with fields for short values")
	}
}

func TestBuildSlack_LongFields(t *testing.T) {
	t.Parallel()
	form := store.Form{ID: "f1", Name: "Contact"}
	longValue := strings.Repeat("x", 200) // > 128 threshold
	sub := store.Submission{
		Data: map[string]string{"message": longValue},
	}
	payload, err := buildSlack(form, sub)
	if err != nil {
		t.Fatalf("buildSlack error: %v", err)
	}
	var result slackPayload
	json.Unmarshal(payload, &result)
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
	longName := strings.Repeat("a", 200)
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

// --- Discord payload tests ---

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
	longValue := strings.Repeat("x", 100) // > 64 threshold
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
	longValue := strings.Repeat("x", 1100) // > 1024 limit
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

// --- Send HTTP tests ---

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

func TestSend_SlackFormat(t *testing.T) {
	t.Parallel()
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "slack"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	if err := sender.Send(form, sub); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	// Verify it's a Slack payload (has "blocks" key)
	var result map[string]interface{}
	if err := json.Unmarshal(receivedBody, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, ok := result["blocks"]; !ok {
		t.Error("expected Slack payload with 'blocks' key")
	}
	if _, ok := result["text"]; !ok {
		t.Error("expected Slack payload with 'text' key")
	}
}

func TestSend_DiscordFormat(t *testing.T) {
	t.Parallel()
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewSender()
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "discord"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	if err := sender.Send(form, sub); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(receivedBody, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, ok := result["embeds"]; !ok {
		t.Error("expected Discord payload with 'embeds' key")
	}
	if result["username"] != "DSForms" {
		t.Errorf("username = %v, want DSForms", result["username"])
	}
}

func TestSend_Timeout(t *testing.T) {
	t.Parallel()
	// unblock is closed by the test after Send returns to let the handler finish.
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	defer srv.Close()

	sender := &Sender{
		Client: &http.Client{Timeout: 50 * time.Millisecond},
	}
	form := store.Form{ID: "f1", Name: "Test", WebhookURL: srv.URL, WebhookFormat: "generic"}
	sub := store.Submission{Data: map[string]string{"name": "Jane"}}
	err := sender.Send(form, sub)
	close(unblock) // let handler goroutine exit so srv.Close() doesn't block
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
