// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/localjournal"
)

const (
	cursorDecisionJournalPrefix = "cursor-decisions"
	maxCursorEvidenceRecords    = 4096
)

type cursorEvidenceRecord struct {
	Key        string    `json:"key"`
	Generation string    `json:"generation"`
	RequestID  string    `json:"request_id,omitempty"`
	Method     string    `json:"method"`
	Digest     string    `json:"digest,omitempty"`
	OptionID   string    `json:"option_id,omitempty"`
	Authority  string    `json:"authority,omitempty"`
	Outcome    string    `json:"outcome"`
	ToolKind   string    `json:"tool_kind,omitempty"`
	OutputSHA  string    `json:"output_digest,omitempty"`
	OutputLen  int       `json:"output_bytes,omitempty"`
	At         time.Time `json:"at"`
}

type cursorEvidenceStore struct {
	mu      sync.Mutex
	journal *localjournal.Journal[cursorEvidenceRecord]
	records []cursorEvidenceRecord
}

func openCursorEvidenceStore(root, instance string) (*cursorEvidenceStore, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	journal, err := localjournal.Open(localjournal.Config[cursorEvidenceRecord]{
		Directory: dir, Prefix: cursorDecisionJournalPrefix, Version: 1,
		MaxBytes: 2 << 20, MaxRecords: maxCursorEvidenceRecords,
		Key: func(record cursorEvidenceRecord) (string, error) {
			if record.Key == "" {
				return "", errors.New("cursor evidence key is invalid")
			}
			return record.Key, nil
		},
		Validate: validateCursorEvidenceRecord,
	})
	if err != nil {
		return nil, err
	}
	store := &cursorEvidenceStore{journal: journal}
	for _, record := range journal.Snapshot() {
		store.records = append(store.records, record)
	}
	return store, nil
}

func validateCursorEvidenceRecord(record cursorEvidenceRecord) error {
	if record.Key == "" || len(record.Key) > 64 || record.Generation == "" || record.Method == "" || record.Outcome == "" || record.At.IsZero() {
		return errors.New("cursor evidence record is invalid")
	}
	switch record.Outcome {
	case "pending", "selected", "cancelled", "refused", "expired", "unknown_method", "output":
	default:
		return errors.New("cursor evidence outcome is invalid")
	}
	if record.Authority != "" && record.Authority != DecisionAuthorityLocalOperator {
		return errors.New("cursor evidence authority is invalid")
	}
	return nil
}

func (s *cursorEvidenceStore) append(record cursorEvidenceRecord) {
	if s == nil {
		return
	}
	if record.At.IsZero() {
		record.At = time.Now().UTC()
	}
	if record.Key == "" {
		sum := sha256.Sum256([]byte(record.Generation + "\x00" + record.RequestID + "\x00" + record.Outcome + "\x00" + record.At.UTC().Format(time.RFC3339Nano)))
		record.Key = hex.EncodeToString(sum[:16])
	}
	if validateCursorEvidenceRecord(record) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal != nil {
		if err := s.journal.Put(record); err != nil {
			return
		}
	}
	s.records = append(s.records, record)
	if len(s.records) > maxCursorEvidenceRecords {
		s.records = s.records[len(s.records)-maxCursorEvidenceRecords:]
	}
}

func (s *cursorEvidenceStore) snapshot() []cursorEvidenceRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cursorEvidenceRecord(nil), s.records...)
}

func (s *Supervisor) Answer(ctx context.Context, id string, request DecisionAnswer) (Receipt, error) {
	if err := validateDecisionAnswer(request); err != nil {
		return Receipt{}, err
	}
	entry, err := s.get(id)
	if err != nil {
		return Receipt{}, err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, ControlRequest{
		Instance: request.Instance, ProjectID: request.ProjectID, Identity: request.Identity, CorrelationID: request.CorrelationID,
	}); err != nil {
		return Receipt{}, err
	}
	replayText := request.RequestID + "\x00" + request.Digest + "\x00" + request.OptionID + "\x00" + request.Authority
	replayRequest := ControlRequest{Instance: request.Instance, ProjectID: request.ProjectID, Identity: request.Identity, CorrelationID: request.CorrelationID, Text: replayText}
	if receipt, ok, err := entry.replay("answer", replayRequest); err != nil || ok {
		return receipt, err
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	process, ok := entry.process.(DecisionProcess)
	if !ok || entry.process == nil {
		entry.mu.Unlock()
		return Receipt{}, ErrCapabilityMissing
	}
	identity, project := entry.session.Identity, entry.session.ProjectID
	generation := entry.session.ID
	entry.mu.Unlock()
	if request.Generation != generation {
		entry.remember("answer", replayRequest, Receipt{}, ErrDecisionMismatch)
		return Receipt{}, ErrDecisionMismatch
	}
	effect, err := process.Answer(ctx, request)
	if err == nil {
		err = validateControlEffect(ControlRequest{CorrelationID: request.CorrelationID}, effect)
	}
	if err != nil {
		entry.remember("answer", replayRequest, Receipt{}, err)
		return Receipt{}, err
	}
	_ = s.persist(entry)
	s.scheduleReport()
	receipt := s.effectReceipt("answer", id, identity, project, effect)
	entry.remember("answer", replayRequest, receipt, nil)
	return receipt, nil
}

func validateDecisionAnswer(request DecisionAnswer) error {
	if !validCorrelationID(request.CorrelationID) || !validOpaqueID(request.RequestID) || len(request.RequestID) > 64 ||
		len(request.Digest) != 64 || !validOpaqueID(request.OptionID) || len(request.OptionID) > 64 ||
		!validOpaqueID(request.Generation) {
		return errors.New("agentd decision answer is invalid")
	}
	if request.Authority != DecisionAuthorityLocalOperator {
		return ErrDecisionAuthority
	}
	if request.Digest != strings.ToLower(request.Digest) || !utf8.ValidString(request.Digest) {
		return errors.New("agentd decision digest is invalid")
	}
	for _, b := range request.Digest {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return errors.New("agentd decision digest is invalid")
		}
	}
	return nil
}
