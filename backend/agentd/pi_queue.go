// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/localjournal"
	"github.com/inspr-at/paimos/backend/pirpc"
)

const (
	piQueueKindSteer    = "steer"
	piQueueKindFollowUp = "follow_up"

	piQueuePending    = "pending_dispatch"
	piQueueQueued     = "queued"
	piQueueHoldIntent = "hold_intent"
	piQueueHeld       = "held"
	piQueueConsumed   = "consumed_running"
	piQueueTerminal   = "terminal"
	piQueueAmbiguous  = "ambiguous"
	piQueueRejected   = "rejected"
	piQueueResumed    = "resumed"

	piHeldOutcomeHeld      = "held"
	piHeldOutcomeTerminal  = "terminal"
	piHeldOutcomeAmbiguous = "ambiguous"
	piHeldOutcomeEmpty     = "empty"
)

var (
	errPiDurableUnavailable = errors.New("durable pi queue accounting is unavailable")
	errPiQueueAmbiguous     = errors.New("pi queue state is ambiguous")
	errPiQueueUnaccounted   = errors.New("pi native queue contained unaccounted instructions")
	errPiResumeUnavailable  = errors.New("pi queue resume is unavailable")
)

// HeldQueue is owner-private retention of exact queued Pi instructions.
// It is never copied into status, receipts, reporter events, or logs.
type HeldQueue struct {
	Generation string
	Outcome    string
	Steering   []string
	FollowUp   []string
}

type piQueueRecord struct {
	Key            string `json:"key"`
	Generation     string `json:"generation"`
	ProjectID      int64  `json:"project_id"`
	Identity       string `json:"identity"`
	AccountKey     string `json:"account_key,omitempty"`
	AccountLabel   string `json:"account_label"`
	ProfileID      string `json:"profile_id,omitempty"`
	ProfileVersion string `json:"profile_version,omitempty"`
	CorrelationID  string `json:"correlation_id"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	TextSHA256     string `json:"text_sha256"`
	TextBytes      int    `json:"text_bytes"`
	ControlID      string `json:"control_id,omitempty"`
}

type piQueueStore struct {
	mu       sync.Mutex
	journal  *localjournal.Journal[piQueueRecord]
	payloads string
}

func openPiQueueStore(root, instance string) (*piQueueStore, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	payloads := filepath.Join(dir, "pi-queue-payloads")
	if err := os.MkdirAll(payloads, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(payloads)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("pi queue payload directory has unsafe mode or type")
	}
	journal, err := localjournal.Open(localjournal.Config[piQueueRecord]{
		Directory: dir, Prefix: "pi-queue", Version: 1, MaxBytes: 8 << 20, MaxRecords: 4096,
		Key: func(r piQueueRecord) (string, error) {
			if r.Key == "" {
				return "", errors.New("missing pi queue key")
			}
			return r.Key, nil
		},
		Validate: validatePiQueueRecord,
	})
	if err != nil {
		return nil, err
	}
	return &piQueueStore{journal: journal, payloads: payloads}, nil
}

func validatePiQueueRecord(r piQueueRecord) error {
	if len(r.Key) != 64 || r.Generation == "" || !validOpaqueID(r.Generation) || r.ProjectID <= 0 ||
		!validOpaqueID(r.Identity) || !validAccountLabel(r.AccountLabel) ||
		(r.AccountKey != "" && !validAccountKey(r.AccountKey)) ||
		!validCorrelationID(r.CorrelationID) || r.TextBytes <= 0 || r.TextBytes > maxTextBytes ||
		len(r.TextSHA256) != 64 {
		return errors.New("invalid pi queue record")
	}
	switch r.Kind {
	case piQueueKindSteer, piQueueKindFollowUp:
	default:
		return errors.New("invalid pi queue kind")
	}
	switch r.State {
	case piQueuePending, piQueueQueued, piQueueHoldIntent, piQueueHeld, piQueueConsumed, piQueueTerminal, piQueueAmbiguous, piQueueRejected, piQueueResumed:
	default:
		return errors.New("invalid pi queue state")
	}
	if r.ControlID != "" && !validCorrelationID(r.ControlID) {
		return errors.New("invalid pi queue control id")
	}
	return nil
}

func piQueueKey(generation, correlation, kind string) string {
	sum := sha256.Sum256([]byte(generation + "\x00" + correlation + "\x00" + kind))
	return hex.EncodeToString(sum[:])
}

func (s *piQueueStore) payloadPath(key string) string {
	return filepath.Join(s.payloads, key+".txt")
}

func (s *piQueueStore) writePayload(key, text string) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || text == "" || len(text) > maxTextBytes {
		return errors.New("pi queue payload is invalid")
	}
	path := s.payloadPath(key)
	tmp, err := os.CreateTemp(s.payloads, "."+key+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	dir, err := os.Open(s.payloads) // #nosec G304 -- owner-only queue payload directory.
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	return err
}

func (s *piQueueStore) readPayload(key, digest string, size int) (string, error) {
	raw, err := os.ReadFile(s.payloadPath(key)) // #nosec G304 -- hashed owner-only payload name.
	if err != nil {
		return "", errPiQueueAmbiguous
	}
	if len(raw) != size || strings.ContainsRune(string(raw), 0) || !utf8.ValidString(string(raw)) {
		return "", errPiQueueAmbiguous
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return "", errPiQueueAmbiguous
	}
	return string(raw), nil
}

func piQueueMeta(scope piQueueScope, correlation, kind, text, state, controlID string) piQueueRecord {
	sum := sha256.Sum256([]byte(text))
	return piQueueRecord{
		Key:            piQueueKey(scope.Generation, correlation, kind),
		Generation:     scope.Generation,
		ProjectID:      scope.ProjectID,
		Identity:       scope.Identity,
		AccountKey:     scope.AccountKey,
		AccountLabel:   scope.AccountLabel,
		ProfileID:      scope.ProfileID,
		ProfileVersion: scope.ProfileVersion,
		CorrelationID:  correlation,
		Kind:           kind,
		State:          state,
		TextSHA256:     hex.EncodeToString(sum[:]),
		TextBytes:      len(text),
		ControlID:      controlID,
	}
}

type piQueueScope struct {
	Generation     string
	ProjectID      int64
	Identity       string
	AccountKey     string
	AccountLabel   string
	ProfileID      string
	ProfileVersion string
}

func (s *piQueueStore) reserve(scope piQueueScope, kind, text, correlation string) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	if kind != piQueueKindSteer && kind != piQueueKindFollowUp {
		return errors.New("pi queue kind is invalid")
	}
	if scope.Generation == "" || scope.ProjectID <= 0 {
		return errPiDurableUnavailable
	}
	record := piQueueMeta(scope, correlation, kind, text, piQueuePending, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.journal.Snapshot() {
		if existing.Key != record.Key {
			continue
		}
		if existing.TextSHA256 != record.TextSHA256 || existing.Kind != kind {
			return ErrControlReplayConflict
		}
		return nil
	}
	if err := s.writePayload(record.Key, text); err != nil {
		return err
	}
	return s.journal.Put(record)
}

func (s *piQueueStore) putState(key, state, controlID string) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putStateLocked(key, state, controlID)
}

func (s *piQueueStore) putStateLocked(key, state, controlID string) error {
	for _, record := range s.journal.Snapshot() {
		if record.Key != key {
			continue
		}
		record.State = state
		if controlID != "" {
			record.ControlID = controlID
		}
		return s.journal.Put(record)
	}
	return errPiQueueAmbiguous
}

func (s *piQueueStore) markQueued(generation, correlation, kind string) error {
	return s.putState(piQueueKey(generation, correlation, kind), piQueueQueued, "")
}

func (s *piQueueStore) markRejected(generation, correlation, kind string) error {
	return s.putState(piQueueKey(generation, correlation, kind), piQueueRejected, "")
}

func (s *piQueueStore) generationRecordsLocked(generation string) []piQueueRecord {
	if s == nil {
		return nil
	}
	var out []piQueueRecord
	for _, record := range s.journal.Snapshot() {
		if record.Generation == generation {
			out = append(out, record)
		}
	}
	return out
}

func (s *piQueueStore) hasAmbiguous(generation string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasAmbiguousLocked(generation)
}

func (s *piQueueStore) hasAmbiguousLocked(generation string) bool {
	for _, record := range s.generationRecordsLocked(generation) {
		switch record.State {
		case piQueuePending, piQueueAmbiguous:
			return true
		}
	}
	return false
}

func (s *piQueueStore) pauseAccounted(generation string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pauseAccountedLocked(generation)
}

func (s *piQueueStore) pauseAccountedLocked(generation string) bool {
	for _, record := range s.generationRecordsLocked(generation) {
		switch record.State {
		case piQueuePending, piQueueQueued, piQueueHoldIntent, piQueueAmbiguous:
			return false
		}
	}
	return true
}

func (s *piQueueStore) holdCommitted(generation string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sawHeld := false
	for _, record := range s.generationRecordsLocked(generation) {
		switch record.State {
		case piQueuePending, piQueueQueued, piQueueHoldIntent, piQueueAmbiguous:
			return false
		case piQueueHeld, piQueueTerminal:
			sawHeld = true
		}
	}
	return sawHeld
}

func (s *piQueueStore) deliveryHeld(generation string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.generationRecordsLocked(generation) {
		switch record.State {
		case piQueuePending, piQueueHoldIntent, piQueueHeld, piQueueAmbiguous:
			return true
		}
	}
	return false
}

func (s *piQueueStore) beginHold(generation, controlID string) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasAmbiguousLocked(generation) {
		return errPiQueueAmbiguous
	}
	for _, record := range s.journal.Snapshot() {
		if record.Generation != generation {
			continue
		}
		if record.State != piQueueQueued && record.State != piQueueHoldIntent && record.State != piQueueHeld {
			continue
		}
		record.State = piQueueHoldIntent
		record.ControlID = controlID
		if err := s.journal.Put(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *piQueueStore) rollbackHold(generation string) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.journal.Snapshot() {
		if record.Generation != generation || record.State != piQueueHoldIntent {
			continue
		}
		record.State = piQueueQueued
		record.ControlID = ""
		if err := s.journal.Put(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *piQueueStore) commitHold(generation, controlID string, native pirpc.ClearQueueData, terminal bool) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	steering := append([]string(nil), native.Steering...)
	follow := append([]string(nil), native.FollowUp...)
	accounted := true
	for _, record := range s.journal.Snapshot() {
		if record.Generation != generation || record.State != piQueueHoldIntent {
			continue
		}
		text, err := s.readPayload(record.Key, record.TextSHA256, record.TextBytes)
		if err != nil {
			accounted = false
			record.State = piQueueAmbiguous
			record.ControlID = controlID
			if err := s.journal.Put(record); err != nil {
				return err
			}
			continue
		}
		bag := &steering
		if record.Kind == piQueueKindFollowUp {
			bag = &follow
		}
		remaining, ok := takeExact(*bag, text)
		*bag = remaining
		if !ok {
			accounted = false
			record.State = piQueueAmbiguous
			record.ControlID = controlID
			if err := s.journal.Put(record); err != nil {
				return err
			}
			continue
		}
		if terminal {
			record.State = piQueueTerminal
		} else {
			record.State = piQueueHeld
		}
		record.ControlID = controlID
		if err := s.journal.Put(record); err != nil {
			return err
		}
	}
	for _, extra := range steering {
		if err := s.persistUnaccountedLocked(generation, controlID, piQueueKindSteer, extra); err != nil {
			return err
		}
		accounted = false
	}
	for _, extra := range follow {
		if err := s.persistUnaccountedLocked(generation, controlID, piQueueKindFollowUp, extra); err != nil {
			return err
		}
		accounted = false
	}
	if !accounted {
		return errPiQueueUnaccounted
	}
	return nil
}

func (s *piQueueStore) persistUnaccountedLocked(generation, controlID, kind, text string) error {
	sum := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(sum[:])
	correlation := "unaccounted-" + digest[:32]
	key := piQueueKey(generation, correlation, kind)
	record := piQueueRecord{
		Key:           key,
		Generation:    generation,
		ProjectID:     1,
		Identity:      "pi:unaccounted",
		AccountLabel:  AccountPiContext,
		CorrelationID: correlation,
		Kind:          kind,
		State:         piQueueAmbiguous,
		TextSHA256:    digest,
		TextBytes:     len(text),
		ControlID:     controlID,
	}
	for _, existing := range s.journal.Snapshot() {
		if existing.Generation == generation {
			record.ProjectID = existing.ProjectID
			record.Identity = existing.Identity
			record.AccountKey = existing.AccountKey
			record.AccountLabel = existing.AccountLabel
			record.ProfileID = existing.ProfileID
			record.ProfileVersion = existing.ProfileVersion
			break
		}
	}
	if err := s.writePayload(key, text); err != nil {
		return err
	}
	return s.journal.Put(record)
}

func (s *piQueueStore) markTerminal(generation, controlID string) error {
	if s == nil {
		return errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.journal.Snapshot() {
		if record.Generation != generation {
			continue
		}
		if record.State != piQueueHeld && record.State != piQueueHoldIntent && record.State != piQueueQueued {
			continue
		}
		record.State = piQueueTerminal
		record.ControlID = controlID
		if err := s.journal.Put(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *piQueueStore) held(generation string) (HeldQueue, error) {
	out := HeldQueue{Generation: generation, Outcome: piHeldOutcomeEmpty, Steering: []string{}, FollowUp: []string{}}
	if s == nil {
		return out, errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ambiguous, sawHeld, sawTerminal := false, false, false
	for _, record := range s.generationRecordsLocked(generation) {
		switch record.State {
		case piQueuePending, piQueueHoldIntent, piQueueAmbiguous:
			ambiguous = true
		case piQueueHeld:
			sawHeld = true
		case piQueueTerminal:
			sawTerminal = true
		default:
			continue
		}
		text, err := s.readPayload(record.Key, record.TextSHA256, record.TextBytes)
		if err != nil {
			ambiguous = true
			continue
		}
		if record.Kind == piQueueKindFollowUp {
			out.FollowUp = append(out.FollowUp, text)
		} else {
			out.Steering = append(out.Steering, text)
		}
	}
	switch {
	case ambiguous:
		out.Outcome = piHeldOutcomeAmbiguous
	case sawHeld:
		out.Outcome = piHeldOutcomeHeld
	case sawTerminal:
		out.Outcome = piHeldOutcomeTerminal
	}
	return out, nil
}

func (s *piQueueStore) liveHeld(generation string) (steering, followUp []piQueueRecord, err error) {
	if s == nil {
		return nil, nil, errPiDurableUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.generationRecordsLocked(generation) {
		if record.State != piQueueHeld {
			continue
		}
		switch record.Kind {
		case piQueueKindSteer:
			steering = append(steering, record)
		case piQueueKindFollowUp:
			followUp = append(followUp, record)
		}
	}
	return steering, followUp, nil
}

func (s *piQueueStore) markResumed(key, controlID string) error {
	return s.putState(key, piQueueResumed, controlID)
}

func takeExact(bag []string, want string) ([]string, bool) {
	for i, value := range bag {
		if value == want {
			out := append([]string(nil), bag[:i]...)
			return append(out, bag[i+1:]...), true
		}
	}
	return bag, false
}
