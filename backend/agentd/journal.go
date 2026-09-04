// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/localjournal"
)

const (
	agentdJournalVersion = 1
	agentdJournalMax     = 2 << 20
)

type registryRecord struct {
	Session Session `json:"session"`
}

type registryJournal struct {
	journal *localjournal.Journal[registryRecord]
}

func instanceKey(instance string) (string, error) {
	if instance == "" || strings.TrimSpace(instance) != instance || len(instance) > 512 || strings.ContainsAny(instance, "\x00\r\n") {
		return "", errors.New("agentd PPM instance is invalid")
	}
	digest := sha256.Sum256([]byte(instance))
	return hex.EncodeToString(digest[:16]), nil
}

// InstanceStateDir guarantees that two PPM instances never share process
// history, even when their names contain path separators or URL syntax.
func InstanceStateDir(root, instance string) (string, error) {
	key, err := instanceKey(instance)
	if err != nil {
		return "", err
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("agentd state root must be absolute")
	}
	return filepath.Join(root, key), nil
}

func openRegistryJournal(root, instance string, maximum int) (*registryJournal, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	j, err := localjournal.Open(localjournal.Config[registryRecord]{
		Directory: dir, Prefix: "sessions", Version: agentdJournalVersion,
		MaxBytes: agentdJournalMax, MaxRecords: maximum,
		Key: func(record registryRecord) (string, error) {
			if !validOpaqueID(record.Session.ID) {
				return "", errors.New("invalid session id")
			}
			return record.Session.ID, nil
		},
		Validate: validateRegistryRecord,
	})
	if err != nil {
		return nil, err
	}
	return &registryJournal{journal: j}, nil
}

func validateRegistryRecord(record registryRecord) error {
	s := record.Session
	// ProjectID zero is accepted only for pre-PAI-870 history. Start rejects it,
	// and recovery strips control authority before the row becomes observable.
	if !validOpaqueID(s.ID) || !validOpaqueID(s.Identity) || s.ProjectID < 0 || !filepath.IsAbs(s.Workspace) || s.StartedAt.IsZero() || s.HeartbeatAt.IsZero() ||
		s.PID != 0 || !s.Managed {
		return errors.New("invalid agentd registry record")
	}
	if s.Role != "" && s.Role != "worker" && s.Role != "coordinator" {
		return errors.New("invalid agentd session role")
	}
	if s.ParentSessionID != "" && uuid.Validate(s.ParentSessionID) != nil {
		return errors.New("invalid agentd parent harness session id")
	}
	if s.TicketID < 0 {
		return errors.New("invalid agentd ticket id")
	}
	if s.WorkspaceProvenance.Identity == "" {
		if s.DispatchProfile != nil || s.AccountLabel != "" && s.AccountLabel != "unknown" {
			return errors.New("invalid legacy agentd execution provenance")
		}
	} else {
		workspace := s.WorkspaceProvenance
		if workspace.CanonicalPath != s.Workspace || !filepath.IsAbs(workspace.CanonicalPath) || filepath.Clean(workspace.CanonicalPath) != workspace.CanonicalPath ||
			len(workspace.Identity) != 64 || !validSafeLabel(workspace.Identity, 64) ||
			(workspace.Mode != WorkspaceExclusive && workspace.Mode != WorkspaceShared) {
			return errors.New("invalid agentd workspace provenance")
		}
		switch workspace.Kind {
		case WorkspaceDirectory:
			if workspace.GitTopLevel != "" || workspace.GitBranch != "" {
				return errors.New("invalid directory workspace provenance")
			}
		case WorkspacePrimary, WorkspaceWorktree:
			if !filepath.IsAbs(workspace.GitTopLevel) || filepath.Clean(workspace.GitTopLevel) != workspace.GitTopLevel || workspace.GitBranch == "" || strings.ContainsAny(workspace.GitBranch, "\x00\r\n") {
				return errors.New("invalid Git workspace provenance")
			}
		default:
			return errors.New("invalid agentd workspace kind")
		}
		if s.DispatchProfile != nil {
			profile := *s.DispatchProfile
			if dispatchprofile.ValidateSnapshot(profile) != nil || profile.Harness != s.Adapter || profile.WorkspaceMode != workspace.Mode {
				return errors.New("invalid agentd dispatch profile")
			}
		}
		if !validAccountLabel(s.AccountLabel) {
			return errors.New("invalid agentd account provenance")
		}
	}
	switch s.State {
	case StateStarting, StateRunning, StateStopping, StateStopped, StateExited, StateFailed, StateOwnershipLost:
	default:
		return errors.New("invalid agentd session state")
	}
	if _, err := canonicalCapabilities(s.Capabilities); err != nil {
		return err
	}
	if s.LastErrorCode != "" && !validErrorCode(s.LastErrorCode) {
		return errors.New("invalid agentd error code")
	}
	if s.Reporter.PublicSessionID != "" && !validOpaqueID(s.Reporter.PublicSessionID) {
		return errors.New("invalid agentd reporter session id")
	}
	if len(s.Reporter.Capabilities) > 0 {
		if _, err := canonicalCapabilities(s.Reporter.Capabilities); err != nil {
			return errors.New("invalid agentd reporter capabilities")
		}
	}
	if pending := s.Reporter.Pending; pending != nil {
		validOutcome := pending.Outcome == "applied" && pending.Reason == "applied"
		validRejection := pending.Outcome == "rejected" && (pending.Reason == "not_running" || pending.Reason == "unsupported" || pending.Reason == "ownership_lost" || pending.Reason == "failed")
		if s.Reporter.PublicSessionID == "" || !validOpaqueID(pending.ControlID) || (pending.Kind != "interrupt" && pending.Kind != "stop") || (!validOutcome && !validRejection) {
			return errors.New("invalid agentd reporter completion")
		}
	}
	if s.Reporter.RemoteClosed && (s.Reporter.PublicSessionID == "" || s.Reporter.Pending != nil) {
		return errors.New("invalid remote-closed agentd reporter state")
	}
	locallyRejected := s.Reporter.Closed && !s.Reporter.RemoteClosed && s.Reporter.PublicSessionID == "" &&
		s.State == StateFailed && s.LastErrorCode == ErrorWorkspaceConflict
	if s.Reporter.Closed && !s.Reporter.RemoteClosed && !locallyRejected {
		return errors.New("invalid closed agentd reporter state")
	}
	return nil
}

func (j *registryJournal) put(session Session) error {
	if j == nil {
		return nil
	}
	session.PID = 0
	session.Steerable = false
	return j.journal.Put(registryRecord{Session: session})
}

func (j *registryJournal) delete(sessionID string) error {
	if j == nil {
		return nil
	}
	return j.journal.Delete(sessionID)
}

func (j *registryJournal) recovered() []Session {
	if j == nil {
		return nil
	}
	records := j.journal.Snapshot()
	out := make([]Session, 0, len(records))
	now := time.Now().UTC()
	for _, record := range records {
		s := record.Session
		if s.AccountLabel == "" {
			s.AccountLabel = "unknown"
		}
		locallyRejected := s.Reporter.Closed && !s.Reporter.RemoteClosed && s.Reporter.PublicSessionID == "" &&
			s.State == StateFailed && s.LastErrorCode == ErrorWorkspaceConflict
		// A durable row or formerly observed PID cannot prove ownership after a
		// daemon restart. Preserve history, but make every control fail closed.
		if !locallyRejected {
			s.State = StateOwnershipLost
			s.LastErrorCode = ErrorOwnershipLost
		}
		s.PID = 0
		s.Steerable = false
		s.Capabilities = []Capability{CapabilityInbox, CapabilityStatus}
		s.HeartbeatAt = now
		out = append(out, s)
	}
	return out
}
