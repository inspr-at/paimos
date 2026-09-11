// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// RuntimeStatus is a content-free local ownership projection. It intentionally
// excludes target references, identities, workspace paths and reporter leases.
type RuntimeStatus struct {
	Consumers           []runtimeconsumer.Evidence `json:"consumers,omitempty"`
	DaemonID            string                     `json:"daemon_id"`
	Instance            string                     `json:"instance"`
	PID                 int                        `json:"pid"`
	ReporterConfigured  bool                       `json:"reporter_configured"`
	ReporterLastSuccess time.Time                  `json:"reporter_last_success"`
	ReporterUnavailable bool                       `json:"reporter_unavailable"`
	Sessions            []RuntimeSession           `json:"sessions"`
	Accounts            []RuntimeAccountState      `json:"accounts,omitempty"`
	Closed              bool                       `json:"closed"`
}
type RuntimeSession struct {
	ID                string       `json:"id"`
	PID               int          `json:"pid"`
	State             SessionState `json:"state"`
	Owned             bool         `json:"owned"`
	WorkspaceVerified bool         `json:"workspace_verified"`
	ReporterBound     bool         `json:"reporter_bound"`
}

func (s *Supervisor) RuntimeStatus(ctx context.Context) RuntimeStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status := s.Status()
	s.mu.Lock()
	out := RuntimeStatus{DaemonID: s.daemonID, Instance: s.instance, PID: os.Getpid(), ReporterConfigured: s.reporter != nil, ReporterLastSuccess: s.reporterLastSuccess, ReporterUnavailable: s.reporterErrorCode != "", Closed: s.closed, Sessions: []RuntimeSession{}}
	consumers := s.consumers
	s.mu.Unlock()
	out.Accounts = s.AccountLifecycleStatus(0)
	if consumers != nil {
		out.Consumers = consumers.Snapshot()
	}
	for _, session := range status.Sessions {
		owned := session.PID > 0 && session.State != StateOwnershipLost && session.State != StateExited && session.State != StateStopped
		verified := false
		if owned && session.WorkspaceProvenance.Identity != "" {
			physical, pathErr := filepath.EvalSymlinks(session.Workspace)
			info, statErr := os.Stat(session.Workspace)
			if pathErr == nil && statErr == nil && info.IsDir() && physical == session.Workspace {
				actual, err := s.inspectWorkspace(ctx, physical, session.WorkspaceProvenance.Mode)
				verified = err == nil && actual.Identity == session.WorkspaceProvenance.Identity && actual.CanonicalPath == session.WorkspaceProvenance.CanonicalPath && actual.Kind == session.WorkspaceProvenance.Kind
			}
		}

		out.Sessions = append(out.Sessions, RuntimeSession{ID: session.ID, PID: session.PID, State: session.State, Owned: owned, WorkspaceVerified: verified, ReporterBound: session.Reporter.PublicSessionID != "" && !session.Reporter.Closed})
	}
	return out
}

// QuiesceRuntime closes the spawn gate and reaps only this generation's actual
// children. A durable PID or an old daemon id never grants signal authority.
func (s *Supervisor) QuiesceRuntime(ctx context.Context, generation string, expectedSessions []string) error {
	for !s.startMu.TryLock() {
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		s.startMu.Unlock()
		return err
	}
	if generation == "" || generation != s.daemonID {
		s.startMu.Unlock()
		return errors.New("runtime generation mismatch")
	}
	current := []string{}
	for _, session := range s.Status().Sessions {
		if session.PID > 0 && session.State != StateOwnershipLost && session.State != StateStopped && session.State != StateExited {
			current = append(current, session.ID)
		}
	}
	expected := append([]string{}, expectedSessions...)
	sort.Strings(current)
	sort.Strings(expected)
	if !slices.Equal(current, expected) {
		s.startMu.Unlock()
		return errors.New("runtime process preview changed")
	}
	s.mu.Lock()
	s.runtimeQuiescing = true
	s.mu.Unlock()
	s.startMu.Unlock()
	if err := s.Close(ctx); err != nil {
		return err
	}
	for _, session := range s.Status().Sessions {
		if session.PID > 0 && session.State != StateOwnershipLost && session.State != StateStopped && session.State != StateExited {
			return errors.New("owned child reap unconfirmed")
		}
	}
	return nil
}
