// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type RuntimeConsumers interface {
	Run(context.Context)
	Snapshot() []runtimeconsumer.Evidence
}

// AttachConsumers starts exactly one supervisor under the daemon's lifecycle.
// Closing the runtime cancels and drains it before stopping owned children.
func (s *Supervisor) AttachConsumers(consumers RuntimeConsumers) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if consumers == nil || s.consumers != nil || s.closed {
		return errors.New("runtime consumers already bound or unavailable")
	}
	s.consumers = consumers
	s.consumersDone = make(chan struct{})
	go func() { defer close(s.consumersDone); consumers.Run(s.lifecycleCtx) }()
	return nil
}
