// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// InspectWorkspace verifies the actual physical location using the same probe
// as Start. Explicit local configuration supplies path; browser handles do not.
func (s *Supervisor) InspectWorkspace(ctx context.Context, path, mode string) (WorkspaceProvenance, error) {
	physical, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !info.IsDir() || !filepath.IsAbs(path) || path != physical {
		return WorkspaceProvenance{}, ErrWorkspaceConflict
	}
	return s.inspectWorkspace(ctx, physical, mode)
}
func (s *Supervisor) ProbeAccount(ctx context.Context, adapter string) string {
	s.mu.RLock()
	a := s.adapters[adapter]
	s.mu.RUnlock()
	if p, ok := a.(AccountProber); ok {
		label := p.AccountLabel(ctx)
		if validAccountLabel(label) {
			return label
		}
	}
	return "unknown"
}

func (s *Supervisor) HasAccount(adapter, key string) bool {
	s.mu.RLock()
	a := s.adapters[adapter]
	s.mu.RUnlock()
	resolver, ok := a.(accountContextResolver)
	return ok && resolver.HasAccount(key)
}

// RepairReporter performs a fresh authenticated reporting pass on this exact
// daemon. It does not signal, replace or adopt another process.
func (s *Supervisor) RepairReporter(ctx context.Context) error {
	s.mu.RLock()
	available := s.reporter != nil && !s.closed
	s.mu.RUnlock()
	if !available {
		return errors.New("reporter repair unavailable")
	}
	s.report(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.reporterErrorCode != "" || s.reporterLastSuccess.IsZero() {
		return errors.New("reporter repair unconfirmed")
	}
	return nil
}

// CommitLifecycleBinding mirrors an already committed server binding. It never
// writes the legacy binding endpoint; the server's completion owns that CAS.
func (s *Supervisor) CommitLifecycleBinding(ctx context.Context, id, publicID string, parent string, ticket int64, shape string) error {
	entry, err := s.get(id)
	if err != nil {
		return err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	entry.mu.Lock()
	if entry.session.Reporter.PublicSessionID != publicID || entry.session.State != StateRunning || entry.session.LastEventKind != EventTurnCompleted {
		entry.mu.Unlock()
		return ErrSessionNotRunning
	}
	if ticket == 0 {
		shape = ""
	}
	entry.session.ParentSessionID = parent
	entry.session.TicketID = ticket
	entry.session.WorkShape = shape
	entry.mu.Unlock()
	return s.persist(entry)
}
