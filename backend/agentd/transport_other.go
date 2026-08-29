//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
)

type Client struct{}

func NewClient(string) (*Client, error) {
	return nil, errors.New("agentd Unix transport is unsupported")
}
func Serve(context.Context, string, *Supervisor) error {
	return errors.New("agentd Unix transport is unsupported")
}
func (*Client) Status(context.Context) (Status, error) {
	return Status{}, errors.New("agentd Unix transport is unsupported")
}
func (*Client) Start(context.Context, StartRequest) (Session, error) {
	return Session{}, errors.New("agentd Unix transport is unsupported")
}
func (*Client) Steer(context.Context, string, ControlRequest) (Receipt, error) {
	return Receipt{}, errors.New("agentd Unix transport is unsupported")
}
func (*Client) Interrupt(context.Context, string, ControlRequest) (Receipt, error) {
	return Receipt{}, errors.New("agentd Unix transport is unsupported")
}
func (*Client) Stop(context.Context, string, ControlRequest) (Receipt, error) {
	return Receipt{}, errors.New("agentd Unix transport is unsupported")
}
