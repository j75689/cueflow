// Package smtp is the email notifier connector (daily review reminders, run summaries).
package smtp

import (
	"context"
	"fmt"

	"github.com/wneessen/go-mail"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "smtp",
		Kind:        connector.KindNotifier,
		Name:        "Email (SMTP)",
		Description: "Sends review reminders and pipeline summaries by email.",
		Fields: []connector.Field{
			{Key: "host", Label: "SMTP host", Type: connector.FieldString, Required: true, Placeholder: "smtp.gmail.com"},
			{Key: "port", Label: "Port", Type: connector.FieldNumber, Default: 587},
			{Key: "security", Label: "Security", Type: connector.FieldSelect, Options: []string{"starttls", "tls", "none"}, Default: "starttls"},
			{Key: "username", Label: "Username", Type: connector.FieldString},
			{Key: "password", Label: "Password / app password", Type: connector.FieldPassword, Secret: true},
			{Key: "from", Label: "From address", Type: connector.FieldString, Required: true},
			{Key: "to", Label: "Recipients", Type: connector.FieldStringList, Required: true},
		},
	}, New)
}

type Notifier struct {
	cfg connector.Config
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	if len(cfg.StringList("to")) == 0 {
		return nil, fmt.Errorf("smtp: at least one recipient is required")
	}
	return &Notifier{cfg: cfg}, nil
}

func (n *Notifier) client() (*mail.Client, error) {
	opts := []mail.Option{mail.WithPort(n.cfg.Int("port", 587))}
	switch n.cfg.String("security") {
	case "tls":
		opts = append(opts, mail.WithSSL())
	case "none":
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	default:
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	if u := n.cfg.String("username"); u != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover), mail.WithUsername(u), mail.WithPassword(n.cfg.String("password")))
	}
	return mail.NewClient(n.cfg.String("host"), opts...)
}

func (n *Notifier) Test(ctx context.Context) (*connector.TestResult, error) {
	c, err := n.client()
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	if err := c.DialWithContext(ctx); err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	_ = c.Close()
	return &connector.TestResult{OK: true, Message: "SMTP login succeeded"}, nil
}

func (n *Notifier) Notify(ctx context.Context, note connector.Notification) error {
	m := mail.NewMsg()
	if err := m.From(n.cfg.String("from")); err != nil {
		return fmt.Errorf("smtp: from: %w", err)
	}
	if err := m.To(n.cfg.StringList("to")...); err != nil {
		return fmt.Errorf("smtp: to: %w", err)
	}
	m.Subject(note.Subject)
	m.SetBodyString(mail.TypeTextPlain, note.Text)
	if note.HTML != "" {
		m.AddAlternativeString(mail.TypeTextHTML, note.HTML)
	}
	c, err := n.client()
	if err != nil {
		return err
	}
	return c.DialAndSendWithContext(ctx, m)
}
