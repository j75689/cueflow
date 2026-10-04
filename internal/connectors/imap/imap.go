// Package imap is the mailbox source connector: newsletters and notes sent to a folder become
// agent material.
package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 mail
	"github.com/emersion/go-message/mail"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/connectors/rss"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "imap",
		Kind:        connector.KindSource,
		Name:        "Mailbox (IMAP)",
		Description: "Polls a mailbox folder; each message becomes an item (deduplicated by Message-ID). Read-only: messages are never modified.",
		Fields: []connector.Field{
			{Key: "host", Label: "IMAP host", Type: connector.FieldString, Required: true, Placeholder: "imap.gmail.com"},
			{Key: "port", Label: "Port", Type: connector.FieldNumber, Default: 993},
			{Key: "security", Label: "Security", Type: connector.FieldSelect, Options: []string{"tls", "starttls", "none"}, Default: "tls"},
			{Key: "username", Label: "Username", Type: connector.FieldString, Required: true},
			{Key: "password", Label: "Password / app password", Type: connector.FieldPassword, Required: true, Secret: true},
			{Key: "mailbox", Label: "Folder", Type: connector.FieldString, Default: "INBOX"},
			{Key: "from_filter", Label: "Only from (contains)", Type: connector.FieldString, Help: "Optional sender filter, e.g. newsletter@example.com"},
			{Key: "subject_filter", Label: "Subject contains", Type: connector.FieldString},
			{Key: "max_messages", Label: "Max messages per fetch", Type: connector.FieldNumber, Default: 30},
			{Key: "max_content_chars", Label: "Max content characters per message", Type: connector.FieldNumber, Default: 6000},
		},
	}, New)
}

type Source struct {
	cfg connector.Config
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	return &Source{cfg: cfg}, nil
}

func (s *Source) dial() (*client.Client, error) {
	addr := net.JoinHostPort(s.cfg.String("host"), fmt.Sprint(s.cfg.Int("port", 993)))
	tlsCfg := &tls.Config{ServerName: s.cfg.String("host"), MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	var (
		c   *client.Client
		err error
	)
	switch s.cfg.String("security") {
	case "tls":
		c, err = client.DialWithDialerTLS(dialer, addr, tlsCfg)
	default:
		c, err = client.DialWithDialer(dialer, addr)
		if err == nil && s.cfg.String("security") == "starttls" {
			err = c.StartTLS(tlsCfg)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("imap: connect: %w", err)
	}
	c.Timeout = 60 * time.Second
	if err := c.Login(s.cfg.String("username"), s.cfg.String("password")); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("imap: login: %w", err)
	}
	return c, nil
}

func (s *Source) Test(ctx context.Context) (*connector.TestResult, error) {
	c, err := s.dial()
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	defer c.Logout() //nolint:errcheck
	mbox, err := c.Select(s.mailbox(), true)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("%s: %d messages", mbox.Name, mbox.Messages)}, nil
}

func (s *Source) mailbox() string {
	if m := s.cfg.String("mailbox"); m != "" {
		return m
	}
	return "INBOX"
}

func (s *Source) Fetch(ctx context.Context, opts connector.FetchOptions) ([]connector.Item, error) {
	c, err := s.dial()
	if err != nil {
		return nil, err
	}
	defer c.Logout() //nolint:errcheck

	if _, err := c.Select(s.mailbox(), true); err != nil {
		return nil, fmt.Errorf("imap: select: %w", err)
	}
	crit := imap.NewSearchCriteria()
	since := opts.Since
	if since.IsZero() {
		since = time.Now().Add(-72 * time.Hour)
	}
	crit.Since = since
	if f := s.cfg.String("from_filter"); f != "" {
		crit.Header.Add("From", f)
	}
	if f := s.cfg.String("subject_filter"); f != "" {
		crit.Header.Add("Subject", f)
	}
	uids, err := c.UidSearch(crit)
	if err != nil {
		return nil, fmt.Errorf("imap: search: %w", err)
	}
	if len(uids) == 0 {
		return nil, nil
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	max := s.cfg.Int("max_messages", 30)
	if opts.Limit > 0 && opts.Limit < max {
		max = opts.Limit
	}
	if len(uids) > max {
		uids = uids[len(uids)-max:] // newest
	}

	set := new(imap.SeqSet)
	set.AddNum(uids...)
	section := &imap.BodySectionName{Peek: true} // Peek: never set \Seen
	msgs := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(set, []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope, imap.FetchUid}, msgs)
	}()

	maxChars := s.cfg.Int("max_content_chars", 6000)
	var items []connector.Item
	for m := range msgs {
		if ctx.Err() != nil {
			continue // drain
		}
		it, err := parseMessage(m, section, maxChars)
		if err != nil {
			continue
		}
		items = append(items, it)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("imap: fetch: %w", err)
	}
	return items, ctx.Err()
}

func parseMessage(m *imap.Message, section *imap.BodySectionName, maxChars int) (connector.Item, error) {
	it := connector.Item{}
	if env := m.Envelope; env != nil {
		it.Title = env.Subject
		it.ExternalID = env.MessageId
		if len(env.From) > 0 {
			it.Author = env.From[0].Address()
		}
		if !env.Date.IsZero() {
			d := env.Date.UTC()
			it.PublishedAt = &d
		}
	}
	if it.ExternalID == "" {
		it.ExternalID = fmt.Sprintf("uid:%d", m.Uid)
	}
	body := m.GetBody(section)
	if body == nil {
		return it, nil
	}
	mr, err := mail.CreateReader(body)
	if err != nil {
		return it, err
	}
	var plain, htmlText string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		h, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := h.ContentType()
		b, _ := io.ReadAll(io.LimitReader(p.Body, 1<<20))
		switch {
		case ct == "text/plain" && plain == "":
			plain = string(b)
		case ct == "text/html" && htmlText == "":
			htmlText = string(b)
		}
	}
	content := strings.TrimSpace(plain)
	if content == "" {
		content = rss.StripHTML(htmlText)
	}
	if r := []rune(content); maxChars > 0 && len(r) > maxChars {
		content = string(r[:maxChars]) + "…"
	}
	it.Content = content
	return it, nil
}
