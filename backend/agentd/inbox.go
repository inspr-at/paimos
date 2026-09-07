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

func (s *Supervisor) InboxReady(id string) bool {
	entry, err := s.get(id)
	if err != nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if ready, ok := entry.process.(interface{ InboxReady() bool }); ok {
		return ready.InboxReady()
	}
	return entry.session.State == StateRunning
}

// DeliveryHeld reports that the owned child has paused native delivery so
// queued instructions stay unconsumed instead of continuing after abort.
func (s *Supervisor) DeliveryHeld(id string) bool {
	entry, err := s.get(id)
	if err != nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if held, ok := entry.process.(interface{ DeliveryHeld() bool }); ok {
		return held.DeliveryHeld()
	}
	if s.queue != nil {
		return s.queue.deliveryHeld(entry.session.ID)
	}
	return false
}

func (s *Supervisor) HeldQueue(id string) (HeldQueue, error) {
	entry, err := s.get(id)
	if err != nil {
		return HeldQueue{}, err
	}
	if s.queue == nil {
		return HeldQueue{}, errPiDurableUnavailable
	}
	return s.queue.held(entry.session.ID)
}

func (s *Supervisor) ResumeQueue(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	if request.Text != "" || !validCorrelationID(request.CorrelationID) {
		return Receipt{}, errors.New("agentd resume request is invalid")
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
	if receipt, ok, err := entry.replay("resume-queue", request); err != nil || ok {
		return receipt, err
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	resumer, ok := entry.process.(interface {
		ResumeQueue(context.Context, ControlRequest) (ControlEffect, error)
	})
	if !ok || entry.process == nil {
		entry.mu.Unlock()
		return Receipt{}, errPiResumeUnavailable
	}
	identity, projectID := entry.session.Identity, entry.session.ProjectID
	entry.mu.Unlock()
	effect, err := resumer.ResumeQueue(ctx, request)
	if err != nil {
		entry.remember("resume-queue", request, Receipt{}, err)
		return Receipt{}, err
	}
	if err := validateControlEffect(request, effect); err != nil {
		entry.remember("resume-queue", request, Receipt{}, err)
		return Receipt{}, err
	}
	receipt := s.effectReceipt("resume-queue", id, identity, projectID, effect)
	entry.remember("resume-queue", request, receipt, nil)
	return receipt, nil
}
