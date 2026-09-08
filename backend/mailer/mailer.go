// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// Package mailer is a small injectable SMTP send seam. Password reset keeps
// its own caller; this package never logs recipients or message bodies.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/secretinput"
)

var (
	ErrNotConfigured = errors.New("smtp_unconfigured")
	ErrRejected      = errors.New("smtp_rejected")
	ErrAmbiguous     = errors.New("smtp_ambiguous")
)

type Message struct {
	From    string
	To      []string
	Subject string
	Raw     []byte
}

type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

type senderAddresser interface {
	SenderAddress() string
}

type Unconfigured struct{}

func (Unconfigured) Send(context.Context, Message) error { return ErrNotConfigured }

func (Unconfigured) SenderAddress() string { return strings.TrimSpace(brand.Default.EmailFrom) }

type SMTP struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

func FromEnv() Mailer {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host == "" {
		return Unconfigured{}
	}
	port := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	pass, err := secretinput.Optional("SMTP_PASS")
	if err != nil {
		return Unconfigured{}
	}
	return SMTP{
		Host: host,
		Port: port,
		User: os.Getenv("SMTP_USER"),
		Pass: pass,
		From: strings.TrimSpace(brand.Default.EmailFrom),
	}
}

func (s SMTP) SenderAddress() string { return strings.TrimSpace(s.From) }

func SenderAddress(m Mailer) string {
	if m == nil {
		return strings.TrimSpace(brand.Default.EmailFrom)
	}
	if s, ok := m.(senderAddresser); ok {
		if from := strings.TrimSpace(s.SenderAddress()); from != "" {
			return from
		}
	}
	return strings.TrimSpace(brand.Default.EmailFrom)
}

func (s SMTP) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return classifyPreSend(err)
	}
	if strings.TrimSpace(s.Host) == "" {
		return ErrNotConfigured
	}
	from := strings.TrimSpace(msg.From)
	if from == "" {
		from = s.From
	}
	from = strings.TrimSpace(from)
	if from == "" {
		return ErrNotConfigured
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("%w: from", ErrRejected)
	}
	if len(msg.To) == 0 || len(msg.Raw) == 0 {
		return fmt.Errorf("%w: empty message", ErrRejected)
	}
	to := make([]string, 0, len(msg.To))
	for _, raw := range msg.To {
		addr, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil || addr.Address == "" || strings.ContainsAny(addr.Address, "\r\n") {
			return fmt.Errorf("%w: recipient", ErrRejected)
		}
		to = append(to, addr.Address)
	}
	addr := net.JoinHostPort(s.Host, s.Port)
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return classifyPreSend(err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return classifyPreSend(err)
	}
	defer client.Close()
	if err := client.Hello("localhost"); err != nil {
		return classifyPreSend(err)
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		cfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
		if err := client.StartTLS(cfg); err != nil {
			return classifyPreSend(err)
		}
	}
	if s.User != "" {
		if err := client.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return classifyPreSend(err)
		}
	}
	if err := client.Mail(from); err != nil {
		return classifyPreSend(err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return classifyPreSend(err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return classifyPreSend(err)
	}
	if _, err := writer.Write(msg.Raw); err != nil {
		_ = writer.Close()
		return classifyAfterDATA(err)
	}
	if err := writer.Close(); err != nil {
		return classifyAfterDATA(err)
	}
	_ = client.Quit()
	return nil
}

func classifyPreSend(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrRejected) || errors.Is(err, ErrAmbiguous) {
		return err
	}
	var smtpErr *textproto.Error
	if errors.As(err, &smtpErr) && smtpErr.Code >= 500 {
		return fmt.Errorf("%w: %s", ErrRejected, smtpErr.Msg)
	}
	return fmt.Errorf("%w: %v", ErrRejected, err)
}

func classifyAfterDATA(err error) error {
	if err == nil {
		return nil
	}
	var smtpErr *textproto.Error
	if errors.As(err, &smtpErr) && smtpErr.Code >= 500 && smtpErr.Code < 600 {
		return fmt.Errorf("%w: %s", ErrRejected, smtpErr.Msg)
	}
	return fmt.Errorf("%w: %v", ErrAmbiguous, err)
}

func ErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConfigured):
		return "smtp_unconfigured"
	case errors.Is(err, ErrRejected):
		return "smtp_rejected"
	case errors.Is(err, ErrAmbiguous):
		return "smtp_ambiguous"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "smtp_ambiguous"
	default:
		return "smtp_ambiguous"
	}
}
