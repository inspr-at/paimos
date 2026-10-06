// SPDX-License-Identifier: AGPL-3.0-only
//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package hooknote

import "context"

// Session is unused where the local peer transport is not implemented.
type Session struct{}

func Dial(context.Context, string, DaemonPin) (*Session, error) {
	return nil, ErrUnavailable
}

func (*Session) Offer(context.Context, Claim) (Note, string, error) {
	return Note{}, "", ErrUnavailable
}

func (*Session) Settle(context.Context, string, string) error { return ErrUnavailable }

func (*Session) Close() error { return nil }
