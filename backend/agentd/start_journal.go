// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/localjournal"
)

var ErrStartOutcomeUnknown = errors.New("managed start outcome unknown; reconcile the original generation before any new request")
var ErrStartReplayConflict = errors.New("managed start key was reused with different input")
var ErrStartRejected = errors.New("original managed start was rejected before spawn")

type startRecord struct {
	Generation  string   `json:"generation"`
	Key         string   `json:"key"`
	Fingerprint string   `json:"fingerprint"`
	Outcome     string   `json:"outcome"`
	Session     *Session `json:"session,omitempty"`
}
type startJournal struct {
	journal *localjournal.Journal[startRecord]
}

func openStartJournal(root, instance string) (*startJournal, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	j, err := localjournal.Open(localjournal.Config[startRecord]{Directory: dir, Prefix: "starts", Version: 1, MaxBytes: 8 << 20, MaxRecords: 4096,
		Key: func(r startRecord) (string, error) { return r.Key, nil },
		Validate: func(r startRecord) error {
			if uuid.Validate(r.Generation) != nil {
				return errors.New("invalid start generation")
			}
			if len(r.Key) != 64 || len(r.Fingerprint) != 64 {
				return errors.New("invalid start digest")
			}
			if _, e := hex.DecodeString(r.Key); e != nil {
				return e
			}
			if _, e := hex.DecodeString(r.Fingerprint); e != nil {
				return e
			}
			switch r.Outcome {
			case "unknown", "rejected":
				if r.Session != nil {
					return errors.New("unexpected start session")
				}
			case "started":
				if r.Session == nil || r.Session.ID != r.Generation {
					return errors.New("missing start session")
				}
				return validateRegistryRecord(registryRecord{Session: *r.Session})
			default:
				return errors.New("invalid start outcome")
			}
			return nil
		}})
	if err != nil {
		return nil, err
	}
	return &startJournal{j}, nil
}

// Start serializes reservation and spawn. The content-free intent is synced
// before calling an adapter. An ambiguous attempt is never executed again,
// including after daemon restart. Records are bounded and never silently evicted.
func (s *Supervisor) Start(ctx context.Context, request StartRequest) (Session, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	attempted := false
	if request.IdempotencyKey == "" {
		return s.startOnce(ctx, request, &attempted, "")
	}
	if len(request.IdempotencyKey) > 512 || strings.ContainsAny(request.IdempotencyKey, "\x00\r\n") {
		return Session{}, errors.New("invalid managed start key")
	}
	if s.starts == nil {
		return Session{}, errors.New("durable managed start journal unavailable")
	}
	key := sha256.Sum256([]byte(request.IdempotencyKey))
	raw, err := json.Marshal(request)
	if err != nil {
		return Session{}, errors.New("invalid managed start request")
	}
	fingerprint := sha256.Sum256(raw)
	record := startRecord{Generation: uuid.NewString(), Key: hex.EncodeToString(key[:]), Fingerprint: hex.EncodeToString(fingerprint[:]), Outcome: "unknown"}
	for _, previous := range s.starts.journal.Snapshot() {
		if previous.Key != record.Key {
			continue
		}
		if previous.Fingerprint != record.Fingerprint {
			return Session{}, ErrStartReplayConflict
		}
		switch previous.Outcome {
		case "rejected":
			return Session{}, ErrStartRejected
		case "started":
			// Return current evidence for that same generation, never another spawn.
			s.mu.RLock()
			entry := s.sessions[previous.Session.ID]
			s.mu.RUnlock()
			if entry != nil {
				entry.mu.Lock()
				snapshot := entry.snapshotLocked()
				entry.mu.Unlock()
				return snapshot, nil
			}
			snapshot := *previous.Session
			snapshot.State = StateOwnershipLost
			snapshot.PID = 0
			snapshot.Steerable = false
			snapshot.Capabilities = []Capability{CapabilityInbox, CapabilityStatus}
			return snapshot, nil
		default:
			return s.recoverStartedGeneration(previous.Generation)
		}
	}
	if err := s.starts.journal.Put(record); err != nil {
		return Session{}, errors.New("managed start reservation could not be saved")
	}
	session, startErr := s.startOnce(ctx, request, &attempted, record.Generation)
	if startErr == nil {
		record.Outcome = "started"
		saved := session
		saved.PID = 0
		saved.Steerable = false
		record.Session = &saved
	} else if !attempted {
		record.Outcome = "rejected"
	}
	if err := s.starts.journal.Put(record); err != nil {
		return Session{}, ErrStartOutcomeUnknown
	}
	return session, startErr
}

// LookupStart reconciles a lost response without re-submitting any spawn input.
func (s *Supervisor) LookupStart(ctx context.Context, key string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.starts == nil {
		return Session{}, ErrStartOutcomeUnknown
	}
	digest := sha256.Sum256([]byte(key))
	lookup := hex.EncodeToString(digest[:])
	for _, r := range s.starts.journal.Snapshot() {
		if r.Key != lookup {
			continue
		}
		if r.Outcome == "rejected" {
			return Session{}, ErrStartRejected
		}
		if r.Outcome != "started" {
			return s.recoverStartedGeneration(r.Generation)
		}
		s.mu.RLock()
		entry := s.sessions[r.Session.ID]
		s.mu.RUnlock()
		if entry != nil {
			entry.mu.Lock()
			defer entry.mu.Unlock()
			return entry.snapshotLocked(), nil
		}
		lost := *r.Session
		lost.State = StateOwnershipLost
		lost.PID = 0
		lost.Steerable = false
		lost.Capabilities = []Capability{CapabilityInbox, CapabilityStatus}
		return lost, nil
	}
	return Session{}, ErrStartOutcomeUnknown
}

// A crash after the session journal sync but before the outcome sync can still
// be reconciled to the reserved generation. Recovered sessions retain the normal
// ownership_lost fence; a pending reservation alone proves no process ownership.
func (s *Supervisor) recoverStartedGeneration(id string) (Session, error) {
	s.mu.RLock()
	entry := s.sessions[id]
	s.mu.RUnlock()
	if entry == nil {
		return Session{}, ErrStartOutcomeUnknown
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.session.State == StateStarting {
		return Session{}, ErrStartOutcomeUnknown
	}
	return entry.snapshotLocked(), nil
}
