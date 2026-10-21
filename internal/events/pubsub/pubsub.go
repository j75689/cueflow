// Package pubsub is an events.Transport over Google Cloud Pub/Sub. It speaks the REST API directly
// (no SDK). Pub/Sub delivers a message once per subscription, so broadcasting to every instance
// takes a subscription per instance: New creates one on the topic, Close deletes it, and an
// expiration policy removes the ones left by instances that were killed before they could.
package pubsub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/events"
)

// DefaultEndpoint is the Pub/Sub REST API origin.
const DefaultEndpoint = "https://pubsub.googleapis.com"

// Subscription settings: an instance's subscription goes 1 day (the minimum) after the last pull,
// and keeps what it missed for 10 minutes (the minimum); events are only "refetch" hints, so
// anything older is useless anyway.
const (
	expireAfter = "86400s"
	retention   = "600s"
	ackDeadline = 10
	maxMessages = 100
)

// Config configures a Transport.
type Config struct {
	// Topic is "projects/P/topics/T"; it must exist.
	Topic string
	// Client is the authenticated HTTP client; defaults to Google application default credentials.
	Client *http.Client
	// Endpoint overrides DefaultEndpoint (the emulator, tests).
	Endpoint string
}

// Transport implements events.Transport.
type Transport struct {
	cfg Config
	sub string // projects/P/subscriptions/S, this instance's own
}

var _ events.Transport = (*Transport)(nil)

// New creates this instance's subscription on the topic.
func New(ctx context.Context, cfg Config) (*Transport, error) {
	project, topicID, ok := strings.Cut(strings.TrimPrefix(cfg.Topic, "projects/"), "/topics/")
	if !strings.HasPrefix(cfg.Topic, "projects/") || !ok || project == "" || topicID == "" {
		return nil, fmt.Errorf("pubsub: topic must be projects/PROJECT/topics/TOPIC, got %q", cfg.Topic)
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Client == nil {
		c, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/pubsub")
		if err != nil {
			return nil, fmt.Errorf("pubsub: default credentials: %w", err)
		}
		cfg.Client = c
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	t := &Transport{cfg: cfg, sub: fmt.Sprintf("projects/%s/subscriptions/%s-%s", project, topicID, hex.EncodeToString(b))}
	body := map[string]any{
		"topic":                    cfg.Topic,
		"ackDeadlineSeconds":       ackDeadline,
		"messageRetentionDuration": retention,
		"expirationPolicy":         map[string]any{"ttl": expireAfter},
	}
	if err := t.call(ctx, http.MethodPut, "/v1/"+t.sub, body, nil); err != nil {
		return nil, fmt.Errorf("pubsub: create subscription: %w", err)
	}
	return t, nil
}

// Subscription is this instance's subscription name.
func (t *Transport) Subscription() string { return t.sub }

// Close deletes this instance's subscription.
func (t *Transport) Close(ctx context.Context) error {
	return t.call(ctx, http.MethodDelete, "/v1/"+t.sub, nil, nil)
}

func (t *Transport) Publish(ctx context.Context, payload []byte) error {
	body := map[string]any{"messages": []map[string]any{{"data": base64.StdEncoding.EncodeToString(payload)}}}
	return t.call(ctx, http.MethodPost, "/v1/"+t.cfg.Topic+":publish", body, nil)
}

func (t *Transport) Receive(ctx context.Context, deliver func([]byte)) error {
	for {
		var out struct {
			ReceivedMessages []struct {
				AckID   string `json:"ackId"`
				Message struct {
					Data string `json:"data"`
				} `json:"message"`
			} `json:"receivedMessages"`
		}
		start := time.Now()
		if err := t.call(ctx, http.MethodPost, "/v1/"+t.sub+":pull", map[string]any{"maxMessages": maxMessages}, &out); err != nil {
			return err
		}
		ids := make([]string, 0, len(out.ReceivedMessages))
		for _, m := range out.ReceivedMessages {
			ids = append(ids, m.AckID)
			if data, err := base64.StdEncoding.DecodeString(m.Message.Data); err == nil {
				deliver(data)
			}
		}
		if len(ids) > 0 {
			// Unacked messages come back after the deadline; at worst a view refetches twice.
			if err := t.call(ctx, http.MethodPost, "/v1/"+t.sub+":acknowledge", map[string]any{"ackIds": ids}, nil); err != nil {
				return err
			}
		} else if time.Since(start) < 200*time.Millisecond {
			// A server that answers an empty pull at once (the emulator does) mustn't spin.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (t *Transport) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.cfg.Endpoint+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("decode response: " + err.Error())
		}
	}
	return nil
}
