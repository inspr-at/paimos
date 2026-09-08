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
	"errors"
	"fmt"
	"net/smtp"
	"os"
	"strings"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/secretinput"
)

var (
	ErrNotConfigured = errors.New("smtp_unconfigured")
	ErrRejected      = errors.New("smtp_rejected")
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

type Unconfigured struct{}

func (Unconfigured) Send(context.Context, Message) error { return ErrNotConfigured }

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
	from := strings.TrimSpace(brand.Default.EmailFrom)
	if from == "" {
		from = "noreply@localhost"
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
		From: from,
	}
}

func (s SMTP) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Host) == "" {
		return ErrNotConfigured
	}
	from := msg.From
	if from == "" {
		from = s.From
	}
	if len(msg.To) == 0 || len(msg.Raw) == 0 {
		return fmt.Errorf("%w: empty message", ErrRejected)
	}
	addr := s.Host + ":" + s.Port
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
	}
	if err := smtp.SendMail(addr, auth, from, msg.To, msg.Raw); err != nil {
		return fmt.Errorf("%w: transport", ErrRejected)
	}
	return nil
}

func ErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConfigured):
		return "smtp_unconfigured"
	case errors.Is(err, ErrRejected):
		return "smtp_rejected"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "smtp_retryable"
	default:
		return "smtp_retryable"
	}
}

func Retryable(err error) bool {
	return ErrorClass(err) == "smtp_retryable"
}
