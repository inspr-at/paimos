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
	if s.Reporter.Closed && !s.Reporter.RemoteClosed {
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
		// A durable row or formerly observed PID cannot prove ownership after a
		// daemon restart. Preserve history, but make every control fail closed.
		s.State = StateOwnershipLost
		s.PID = 0
		s.Steerable = false
		s.Capabilities = []Capability{CapabilityInbox, CapabilityStatus}
		s.HeartbeatAt = now
		s.LastErrorCode = ErrorOwnershipLost
		out = append(out, s)
	}
	return out
}
