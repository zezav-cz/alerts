package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
)

// Telegram sends the alert's text as a plain message through the Bot API.
// No parse_mode is set, so the text needs no escaping.
type Telegram struct {
	name    string
	token   string
	chatID  string
	client  *http.Client
	baseURL string
}

// NewTelegram returns a Telegram notifier posting to api.telegram.org.
func NewTelegram(name, token, chatID string, client *http.Client) *Telegram {
	return &Telegram{name: name, token: token, chatID: chatID, client: client, baseURL: "https://api.telegram.org"}
}

// Name returns the notifier's configured name.
func (t *Telegram) Name() string { return t.name }

// Send posts one message.
func (t *Telegram) Send(ctx context.Context, a alert.Alert) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":              t.chatID,
		"text":                 a.Text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return t.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return t.redact(err)
	}
	defer resp.Body.Close()

	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply)

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RetryAfterError{
			After: time.Duration(reply.Parameters.RetryAfter) * time.Second,
			Err:   fmt.Errorf("telegram: HTTP 429: %s", reply.Description),
		}
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, reply.Description)
	case !reply.OK:
		return fmt.Errorf("telegram: request not ok: %s", reply.Description)
	}
	return nil
}

// redact strips the bot token, which net/http errors carry inside the URL.
func (t *Telegram) redact(err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), t.token, "<token>"))
}
