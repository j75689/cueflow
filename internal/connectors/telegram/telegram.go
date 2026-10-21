// Package telegram is the notifier connector that messages a Telegram chat through a bot.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/textutil"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "telegram",
		Kind:        connector.KindNotifier,
		Name:        "Telegram",
		Description: "Messages a Telegram chat through a bot when a task needs you.",
		Fields: []connector.Field{
			{Key: "bot_token", Label: "Bot token", Type: connector.FieldPassword, Required: true, Secret: true,
				Help: "From @BotFather, e.g. 123456:ABC-DEF..."},
			{Key: "chat_id", Label: "Chat ID", Type: connector.FieldString, Required: true,
				Help: "A user, group or channel id (e.g. -1001234567890) or @channelname. Message the bot first."},
			{Key: "message_thread_id", Label: "Topic ID", Type: connector.FieldNumber,
				Help: "Optional: post into one topic of a forum group."},
			{Key: "endpoint_url", Label: "API base URL", Type: connector.FieldString, Default: "https://api.telegram.org"},
		},
	}, New)
}

// maxText keeps a message under Telegram's 4096-character limit (which counts UTF-16 units, so an
// emoji takes two), with room for the clip marker.
const maxText = 4000

type Notifier struct {
	token, chat, base string
	thread            int
	http              *http.Client
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	n := &Notifier{
		token:  strings.TrimSpace(cfg.String("bot_token")),
		chat:   strings.TrimSpace(cfg.String("chat_id")),
		thread: cfg.Int("message_thread_id", 0),
		base:   strings.TrimRight(cfg.String("endpoint_url"), "/"),
		http:   env.HTTPClient,
	}
	if n.token == "" || n.chat == "" {
		return nil, fmt.Errorf("telegram: bot token and chat id are required")
	}
	if n.base == "" {
		n.base = "https://api.telegram.org"
	}
	if n.http == nil {
		n.http = http.DefaultClient
	}
	return n, nil
}

func (n *Notifier) Test(ctx context.Context) (*connector.TestResult, error) {
	if err := n.send(ctx, "CueFlow can reach this chat. Task alerts will arrive here."); err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: "test message sent"}, nil
}

func (n *Notifier) Notify(ctx context.Context, note connector.Notification) error {
	text := strings.TrimSpace(note.Text)
	if s := strings.TrimSpace(note.Subject); s != "" {
		text = strings.TrimSpace(s + "\n\n" + text)
	}
	return n.send(ctx, textutil.Clip(text, maxText))
}

// send posts plain text (no parse_mode, so task titles need no escaping).
func (n *Notifier) send(ctx context.Context, text string) error {
	payload := map[string]any{"chat_id": n.chat, "text": text, "link_preview_options": map[string]any{"is_disabled": true}}
	if n.thread > 0 {
		payload["message_thread_id"] = n.thread
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.base+"/bot"+n.token+"/sendMessage", bytes.NewReader(raw))
	if err != nil {
		return errors.New("telegram: bad API base URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		// The request URL holds the bot token; url.Error prints it, so report only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(b, &body)
	if resp.StatusCode != http.StatusOK || !body.OK {
		msg := body.Description
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, textutil.Clip(msg, 300))
	}
	return nil
}
