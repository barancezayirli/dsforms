package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// Send dispatches a webhook for the given form and submission.
// It selects the payload format based on form.WebhookFormat.
func (s *Sender) Send(form store.Form, sub store.Submission) error {
	if form.WebhookURL == "" {
		return nil
	}

	var (
		payload []byte
		err     error
	)
	switch form.WebhookFormat {
	case "slack":
		payload, err = buildSlack(form, sub)
	case "discord":
		payload, err = buildDiscord(form, sub)
	default:
		payload, err = buildGeneric(form, sub)
	}
	if err != nil {
		return fmt.Errorf("build webhook payload: %w", err)
	}

	resp, err := s.Client.Post(form.WebhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("send webhook: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// sortedKeys returns the map keys in sorted order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- Generic payload ---

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

// --- Slack payload ---

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
	// Build header text, truncating at 150 chars.
	headerText := "New Submission: " + form.Name
	if len(headerText) > 150 {
		headerText = headerText[:147] + "..."
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: headerText,
			},
		},
	}

	// Separate short (<=128) and long (>128) fields.
	keys := sortedKeys(sub.Data)
	var shortPairs []slackText // pairs: label then value
	for _, k := range keys {
		v := sub.Data[k]
		if len(v) <= 128 {
			shortPairs = append(shortPairs, slackText{Type: "mrkdwn", Text: "*" + k + "*"})
			shortPairs = append(shortPairs, slackText{Type: "mrkdwn", Text: v})
		} else {
			// Long field gets its own full-width section block.
			fullText := "*" + k + "*\n" + v
			blocks = append(blocks, slackBlock{
				Type: "section",
				Text: &slackText{Type: "mrkdwn", Text: fullText},
			})
		}
	}

	// Emit short fields in groups of max 10 (5 key-value pairs).
	// Slack section fields accept up to 10 items.
	for i := 0; i < len(shortPairs); i += 10 {
		end := i + 10
		if end > len(shortPairs) {
			end = len(shortPairs)
		}
		blocks = append(blocks, slackBlock{
			Type:   "section",
			Fields: shortPairs[i:end],
		})
	}

	// Context footer.
	date := time.Now().UTC().Format("2006-01-02")
	blocks = append(blocks, slackBlock{
		Type: "context",
		Elements: []slackText{
			{Type: "mrkdwn", Text: "via DSForms \u2022 " + date},
		},
	})

	p := slackPayload{
		Text:   "New submission: " + form.Name,
		Blocks: blocks,
	}
	return json.Marshal(p)
}

// --- Discord payload ---

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
	keys := sortedKeys(sub.Data)
	if len(keys) > 25 {
		keys = keys[:25]
	}

	fields := make([]discordField, 0, len(keys))
	for _, k := range keys {
		v := sub.Data[k]
		// Truncate values at 1024 chars.
		if len(v) > 1024 {
			v = v[:1021] + "..."
		}
		fields = append(fields, discordField{
			Name:   k,
			Value:  v,
			Inline: len(v) <= 64,
		})
	}

	embed := discordEmbed{
		Title:     "New Submission: " + form.Name,
		Color:     5793266,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Footer:    discordFooter{Text: "DSForms"},
		Fields:    fields,
	}

	p := discordPayload{
		Username: "DSForms",
		Embeds:   []discordEmbed{embed},
	}
	return json.Marshal(p)
}
