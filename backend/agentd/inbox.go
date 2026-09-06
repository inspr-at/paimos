// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// Inbox controls only an existing owned generation, serializing with stop and
// steer. The caller owns durable pre-effect reservation and completion replay.
func (s *Supervisor) Inbox(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	if request.Text == "" || len(request.Text) > maxTextBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) || !validCorrelationID(request.CorrelationID) {
		return Receipt{}, errors.New("agentd inbox request invalid")
	}
	entry, err := s.get(id)
	if err != nil {
		return Receipt{}, err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, request); err != nil {
		return Receipt{}, err
	}
	if receipt, ok, err := entry.replay("inbox", request); err != nil || ok {
		return receipt, err
	}
	entry.mu.Lock()
	process, ok := entry.process.(InboxProcess)
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	if !ok || !entry.capabilities[CapabilityInbox] {
		entry.mu.Unlock()
		return Receipt{}, ErrCapabilityMissing
	}
	identity, project := entry.session.Identity, entry.session.ProjectID
	entry.mu.Unlock()
	effect, err := process.Inbox(ctx, request)
	if err == nil {
		err = validateControlEffect(request, effect)
	}
	if err != nil {
		entry.remember("inbox", request, Receipt{}, err)
		return Receipt{}, err
	}
	receipt := s.effectReceipt("inbox", id, identity, project, effect)
	receipt.RequestedLevel = "simple"
	receipt.EffectiveLevel = "simple"
	entry.remember("inbox", request, receipt, nil)
	return receipt, nil
}

func (s *Supervisor) SupportsInbox(id string) bool {
	entry, err := s.get(id)
	if err != nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	_, supported := entry.process.(InboxProcess)
	return supported && entry.session.State == StateRunning && entry.capabilities[CapabilityInbox]
}
