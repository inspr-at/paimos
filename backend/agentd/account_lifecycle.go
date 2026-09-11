// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/localjournal"
)

const (
	accountLifecycleJournalVersion = 1
	publicRegistrationImmutable    = "public_runtime_registration_is_immutable_for_this_generation"
)

type accountLifecycleRecord struct {
	Key         string                  `json:"key"`
	Fingerprint string                  `json:"fingerprint"`
	Outcome     string                  `json:"outcome"`
	Result      *AccountLifecycleResult `json:"result,omitempty"`
	Reason      string                  `json:"reason,omitempty"`
}

type accountAttachmentSnapshot struct {
	ProjectID          int64    `json:"project_id"`
	Adapter            string   `json:"adapter"`
	Keys               []string `json:"keys"`
	Revision           int64    `json:"revision"`
	MutatingGeneration string   `json:"mutating_generation"`
}

type accountLifecycleStore struct {
	requests    *localjournal.Journal[accountLifecycleRecord]
	attachments *localjournal.Journal[accountAttachmentSnapshot]
}

func openAccountLifecycleStore(root, instance string) (*accountLifecycleStore, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	requests, err := localjournal.Open(localjournal.Config[accountLifecycleRecord]{
		Directory: dir, Prefix: "account-lifecycle", Version: accountLifecycleJournalVersion,
		MaxBytes: 2 << 20, MaxRecords: 4096,
		Key: func(r accountLifecycleRecord) (string, error) {
			if len(r.Key) != 64 {
				return "", errors.New("invalid account lifecycle digest")
			}
			return r.Key, nil
		},
		Validate: validateAccountLifecycleRecord,
	})
	if err != nil {
		return nil, err
	}
	attachments, err := localjournal.Open(localjournal.Config[accountAttachmentSnapshot]{
		Directory: dir, Prefix: "account-attachments", Version: accountLifecycleJournalVersion,
		MaxBytes: 256 << 10, MaxRecords: 64,
		Key: func(r accountAttachmentSnapshot) (string, error) {
			if r.ProjectID <= 0 || !supportedNamedAccountAdapter(r.Adapter) {
				return "", errors.New("invalid account attachment snapshot")
			}
			return attachmentKey(r.ProjectID, r.Adapter), nil
		},
		Validate: validateAccountAttachmentSnapshot,
	})
	if err != nil {
		return nil, err
	}
	return &accountLifecycleStore{requests: requests, attachments: attachments}, nil
}

func validateAccountLifecycleRecord(r accountLifecycleRecord) error {
	if len(r.Key) != 64 || len(r.Fingerprint) != 64 {
		return errors.New("invalid account lifecycle digest")
	}
	if _, err := hex.DecodeString(r.Key); err != nil {
		return err
	}
	if _, err := hex.DecodeString(r.Fingerprint); err != nil {
		return err
	}
	switch r.Outcome {
	case "applied":
		if r.Result == nil || r.Reason != "" {
			return errors.New("invalid applied account lifecycle record")
		}
		return validateAccountLifecycleResult(*r.Result)
	case "rejected":
		if r.Result != nil || r.Reason == "" {
			return errors.New("invalid rejected account lifecycle record")
		}
		return nil
	default:
		return errors.New("invalid account lifecycle outcome")
	}
}

func validateAccountLifecycleResult(r AccountLifecycleResult) error {
	if r.ProjectID <= 0 || !supportedNamedAccountAdapter(r.Adapter) || !validAccountKey(r.AccountKey) {
		return errors.New("invalid account lifecycle result")
	}
	if r.Operation != AccountLifecycleConnect && r.Operation != AccountLifecycleDisconnect {
		return errors.New("invalid account lifecycle operation")
	}
	if r.Mode != AccountLifecycleExplicit || r.Revision < 1 || uuid.Validate(r.RuntimeGeneration) != nil {
		return errors.New("invalid account lifecycle revision")
	}
	if r.Advertisement != AccountAdvertisementCommitted && r.Advertisement != AccountAdvertisementRestartRequired {
		return errors.New("invalid account advertisement")
	}
	if r.State != "connected" && r.State != "disconnected" {
		return errors.New("invalid account lifecycle state")
	}
	return validateAttachedKeys(r.AttachedKeys)
}

func validateAccountAttachmentSnapshot(r accountAttachmentSnapshot) error {
	if r.ProjectID <= 0 || !supportedNamedAccountAdapter(r.Adapter) || r.Revision < 1 || uuid.Validate(r.MutatingGeneration) != nil {
		return errors.New("invalid account attachment snapshot")
	}
	return validateAttachedKeys(r.Keys)
}

func validateAttachedKeys(keys []string) error {
	if keys == nil {
		return errors.New("invalid attached account keys")
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if !validAccountKey(key) || seen[key] {
			return errors.New("invalid attached account key")
		}
		seen[key] = true
	}
	return nil
}

func supportedNamedAccountAdapter(adapter string) bool {
	return adapter == AdapterCodex || adapter == AdapterPi || adapter == AdapterCursor
}

func attachmentKey(project int64, adapter string) string {
	return strings.TrimSpace(adapter) + ":" + strconv.FormatInt(project, 10)
}

func (s *Supervisor) ApplyAccountLifecycle(ctx context.Context, request AccountLifecycleRequest) (AccountLifecycleResult, error) {
	if err := ctx.Err(); err != nil {
		return AccountLifecycleResult{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	return s.applyAccountLifecycleLocked(request)
}

func (s *Supervisor) applyAccountLifecycleLocked(request AccountLifecycleRequest) (AccountLifecycleResult, error) {
	if s.accounts == nil {
		return AccountLifecycleResult{}, ErrAccountLifecycleUnavailable
	}
	normalized, err := validateAccountLifecycleRequest(request)
	if err != nil {
		return AccountLifecycleResult{}, err
	}
	key := sha256.Sum256([]byte(normalized.IdempotencyKey))
	raw, err := json.Marshal(fingerprintAccountLifecycle(normalized))
	if err != nil {
		return AccountLifecycleResult{}, errors.New("invalid account lifecycle request")
	}
	fingerprint := sha256.Sum256(raw)
	record := accountLifecycleRecord{
		Key: hex.EncodeToString(key[:]), Fingerprint: hex.EncodeToString(fingerprint[:]),
	}
	for _, previous := range s.accounts.requests.Snapshot() {
		if previous.Key != record.Key {
			continue
		}
		if previous.Fingerprint != record.Fingerprint {
			return AccountLifecycleResult{}, ErrAccountLifecycleConflict
		}
		if previous.Outcome == "rejected" {
			return AccountLifecycleResult{}, rejectedAccountLifecycleError(previous.Reason)
		}
		out := *previous.Result
		out.Replayed = true
		return out, nil
	}
	result, applyErr := s.commitAccountLifecycle(normalized)
	if applyErr != nil {
		record.Outcome = "rejected"
		record.Reason = accountLifecycleReason(applyErr)
		if err := s.accounts.requests.Put(record); err != nil {
			return AccountLifecycleResult{}, ErrAccountLifecycleUnavailable
		}
		return AccountLifecycleResult{}, applyErr
	}
	record.Outcome = "applied"
	saved := result
	record.Result = &saved
	if err := s.accounts.requests.Put(record); err != nil {
		return AccountLifecycleResult{}, ErrAccountLifecycleUnavailable
	}
	return result, nil
}

type accountLifecycleFingerprint struct {
	Operation         string `json:"operation"`
	ProjectID         int64  `json:"project_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	AccountKey        string `json:"account_key"`
	Adapter           string `json:"adapter"`
	ExpectedRevision  int64  `json:"expected_revision"`
}

func fingerprintAccountLifecycle(request AccountLifecycleRequest) accountLifecycleFingerprint {
	return accountLifecycleFingerprint{
		Operation: request.Operation, ProjectID: request.ProjectID, RuntimeGeneration: request.RuntimeGeneration,
		AccountKey: request.AccountKey, Adapter: request.Adapter, ExpectedRevision: request.ExpectedRevision,
	}
}

func validateAccountLifecycleRequest(request AccountLifecycleRequest) (AccountLifecycleRequest, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.Operation = strings.TrimSpace(request.Operation)
	request.RuntimeGeneration = strings.TrimSpace(request.RuntimeGeneration)
	request.AccountKey = strings.TrimSpace(request.AccountKey)
	request.Adapter = strings.TrimSpace(request.Adapter)
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > 512 || strings.ContainsAny(request.IdempotencyKey, "\x00\r\n") {
		return AccountLifecycleRequest{}, errors.New("invalid account lifecycle key")
	}
	if request.Operation != AccountLifecycleConnect && request.Operation != AccountLifecycleDisconnect {
		return AccountLifecycleRequest{}, errors.New("invalid account lifecycle operation")
	}
	if request.ProjectID <= 0 {
		return AccountLifecycleRequest{}, errors.New("invalid account lifecycle project")
	}
	if uuid.Validate(request.RuntimeGeneration) != nil {
		return AccountLifecycleRequest{}, ErrAccountStale
	}
	if !validAccountKey(request.AccountKey) {
		return AccountLifecycleRequest{}, errors.New("invalid managed account selection")
	}
	if request.Adapter == AdapterClaude {
		return AccountLifecycleRequest{}, ErrAccountUnsupported
	}
	if !supportedNamedAccountAdapter(request.Adapter) {
		return AccountLifecycleRequest{}, ErrAccountUnsupported
	}
	if request.ExpectedRevision < 0 {
		return AccountLifecycleRequest{}, ErrAccountStale
	}
	return request, nil
}

func (s *Supervisor) BindAccountLifecycleProjects(ids []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bound := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			bound[id] = struct{}{}
		}
	}
	s.lifecycleProjects = bound
}

func (s *Supervisor) accountLifecycleBindingActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.lifecycleProjects) > 0
}

func (s *Supervisor) accountLifecycleProjectBound(project int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.lifecycleProjects) == 0 {
		return false
	}
	_, ok := s.lifecycleProjects[project]
	return ok
}

func (s *Supervisor) commitAccountLifecycle(request AccountLifecycleRequest) (AccountLifecycleResult, error) {
	if request.RuntimeGeneration != s.daemonID {
		return AccountLifecycleResult{}, ErrAccountStale
	}
	if !s.accountLifecycleProjectBound(request.ProjectID) {
		return AccountLifecycleResult{}, ErrAccountForeign
	}
	if !s.HasAccount(request.Adapter, request.AccountKey) {
		return AccountLifecycleResult{}, errors.New("managed account selection is unavailable")
	}
	current, explicit := s.attachmentSnapshot(request.ProjectID, request.Adapter)
	if request.ExpectedRevision != current.Revision {
		return AccountLifecycleResult{}, ErrAccountStale
	}
	keys := append([]string{}, current.Keys...)
	if !explicit {
		keys = s.enrolledAccountKeys(request.Adapter)
	}
	attached := slices.Contains(keys, request.AccountKey)
	if request.Operation == AccountLifecycleConnect {
		if !attached {
			keys = append(keys, request.AccountKey)
			slices.Sort(keys)
		}
	} else {
		if !attached {
			return AccountLifecycleResult{}, errors.New("managed account selection is unavailable")
		}
		if reason := s.accountDisconnectBlock(request.ProjectID, request.Adapter, request.AccountKey); reason != "" {
			if reason == "drain_required" {
				return AccountLifecycleResult{}, ErrAccountDrainRequired
			}
			return AccountLifecycleResult{}, ErrAccountInUse
		}
		filtered := make([]string, 0, len(keys)-1)
		for _, key := range keys {
			if key != request.AccountKey {
				filtered = append(filtered, key)
			}
		}
		keys = filtered
	}
	snapshot := accountAttachmentSnapshot{
		ProjectID: request.ProjectID, Adapter: request.Adapter, Keys: keys,
		Revision: current.Revision + 1, MutatingGeneration: s.daemonID,
	}
	if snapshot.Keys == nil {
		snapshot.Keys = []string{}
	}
	if err := s.accounts.attachments.Put(snapshot); err != nil {
		return AccountLifecycleResult{}, ErrAccountLifecycleUnavailable
	}
	state := "connected"
	if request.Operation == AccountLifecycleDisconnect || !slices.Contains(snapshot.Keys, request.AccountKey) {
		state = "disconnected"
	}
	if request.Operation == AccountLifecycleConnect {
		state = "connected"
	}
	advertisement, reason := AccountAdvertisementCommitted, ""
	if request.Operation == AccountLifecycleDisconnect || !attached {
		advertisement, reason = AccountAdvertisementRestartRequired, publicRegistrationImmutable
	}
	return AccountLifecycleResult{
		ProjectID: request.ProjectID, Adapter: request.Adapter, AccountKey: request.AccountKey,
		Operation: request.Operation, State: state, Mode: AccountLifecycleExplicit,
		Revision: snapshot.Revision, RuntimeGeneration: s.daemonID, AttachedKeys: append([]string{}, snapshot.Keys...),
		Advertisement: advertisement, AdvertisementReason: reason,
	}, nil
}

func (s *Supervisor) attachmentSnapshot(project int64, adapter string) (accountAttachmentSnapshot, bool) {
	if s.accounts == nil {
		return accountAttachmentSnapshot{ProjectID: project, Adapter: adapter, Keys: []string{}}, false
	}
	want := attachmentKey(project, adapter)
	for _, snapshot := range s.accounts.attachments.Snapshot() {
		if attachmentKey(snapshot.ProjectID, snapshot.Adapter) == want {
			keys := append([]string{}, snapshot.Keys...)
			slices.Sort(keys)
			snapshot.Keys = keys
			return snapshot, true
		}
	}
	return accountAttachmentSnapshot{ProjectID: project, Adapter: adapter, Keys: []string{}}, false
}

func (s *Supervisor) enrolledAccountKeys(adapter string) []string {
	s.mu.RLock()
	a := s.adapters[adapter]
	s.mu.RUnlock()
	catalog, ok := a.(enrolledAccountCatalog)
	if !ok {
		return []string{}
	}
	keys := append([]string{}, catalog.EnrolledAccountKeys()...)
	slices.Sort(keys)
	if keys == nil {
		return []string{}
	}
	return keys
}

func (s *Supervisor) AccountAttached(project int64, adapter, key string) bool {
	if key == "" || s.accounts == nil {
		return true
	}
	snapshot, explicit := s.attachmentSnapshot(project, adapter)
	if !explicit {
		return true
	}
	return slices.Contains(snapshot.Keys, key)
}

func (s *Supervisor) accountAttached(project int64, adapter, key string) bool {
	return s.AccountAttached(project, adapter, key)
}

func (s *Supervisor) AttachedAccountKeys(project int64, adapter string) (keys []string, mode string, revision int64) {
	snapshot, explicit := s.attachmentSnapshot(project, adapter)
	if !explicit {
		return s.enrolledAccountKeys(adapter), AccountLifecycleLegacy, 0
	}
	out := make([]string, 0, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		if s.HasAccount(adapter, key) {
			out = append(out, key)
		}
	}
	return out, AccountLifecycleExplicit, snapshot.Revision
}

func (s *Supervisor) AccountLifecycleStatus(project int64) []RuntimeAccountState {
	out := []RuntimeAccountState{}
	seen := map[string]bool{}
	var sessions []Session
	if project <= 0 {
		sessions = s.Status().Sessions
	}
	for _, adapter := range []string{AdapterCodex, AdapterPi, AdapterCursor} {
		if project > 0 {
			keys, mode, revision := s.AttachedAccountKeys(project, adapter)
			generation := s.daemonID
			if snapshot, explicit := s.attachmentSnapshot(project, adapter); explicit {
				generation = snapshot.MutatingGeneration
			}
			if mode == AccountLifecycleLegacy && len(keys) == 0 {
				continue
			}
			out = append(out, RuntimeAccountState{
				ProjectID: project, Adapter: adapter, Mode: mode, Revision: revision,
				Keys: keys, RuntimeGeneration: generation,
			})
			continue
		}
		if s.accounts != nil {
			for _, snapshot := range s.accounts.attachments.Snapshot() {
				if snapshot.Adapter != adapter || seen[attachmentKey(snapshot.ProjectID, snapshot.Adapter)] {
					continue
				}
				seen[attachmentKey(snapshot.ProjectID, snapshot.Adapter)] = true
				keys, mode, revision := s.AttachedAccountKeys(snapshot.ProjectID, adapter)
				out = append(out, RuntimeAccountState{
					ProjectID: snapshot.ProjectID, Adapter: adapter, Mode: mode, Revision: revision,
					Keys: keys, RuntimeGeneration: snapshot.MutatingGeneration,
				})
			}
		}
		for _, session := range sessions {
			if session.AccountKey == "" || session.Adapter != adapter || seen[attachmentKey(session.ProjectID, adapter)] {
				continue
			}
			seen[attachmentKey(session.ProjectID, adapter)] = true
			keys, mode, revision := s.AttachedAccountKeys(session.ProjectID, adapter)
			generation := s.daemonID
			if snapshot, explicit := s.attachmentSnapshot(session.ProjectID, adapter); explicit {
				generation = snapshot.MutatingGeneration
			}
			out = append(out, RuntimeAccountState{
				ProjectID: session.ProjectID, Adapter: adapter, Mode: mode, Revision: revision,
				Keys: keys, RuntimeGeneration: generation,
			})
		}
	}
	slices.SortFunc(out, func(a, b RuntimeAccountState) int {
		if a.ProjectID != b.ProjectID {
			return int(a.ProjectID - b.ProjectID)
		}
		return strings.Compare(a.Adapter, b.Adapter)
	})
	return out
}

func (s *Supervisor) accountDisconnectBlock(project int64, adapter, key string) string {
	for _, session := range s.Status().Sessions {
		if session.ProjectID != project || session.Adapter != adapter || session.AccountKey != key {
			continue
		}
		active := session.State == StateStarting || session.State == StateRunning || session.State == StateStopping || session.PID > 0
		unsettled := len(session.PendingDecisions) > 0 || session.LastEventKind == EventTurnStarted || session.LastEventKind == EventToolStarted
		if s.queue != nil && (s.queue.deliveryHeld(session.ID) || s.queue.hasAmbiguous(session.ID)) {
			return "drain_required"
		}
		if active || unsettled {
			return "account_in_use"
		}
	}
	return ""
}

func rejectedAccountLifecycleError(reason string) error {
	switch reason {
	case "conflict":
		return ErrAccountLifecycleConflict
	case "stale":
		return ErrAccountStale
	case "unsupported":
		return ErrAccountUnsupported
	case "in_use":
		return ErrAccountInUse
	case "drain_required":
		return ErrAccountDrainRequired
	case "unavailable":
		return ErrAccountLifecycleUnavailable
	case "foreign":
		return ErrAccountForeign
	default:
		return ErrAccountLifecycleRejected
	}
}

func accountLifecycleReason(err error) string {
	switch {
	case errors.Is(err, ErrAccountLifecycleConflict):
		return "conflict"
	case errors.Is(err, ErrAccountStale):
		return "stale"
	case errors.Is(err, ErrAccountUnsupported):
		return "unsupported"
	case errors.Is(err, ErrAccountInUse):
		return "in_use"
	case errors.Is(err, ErrAccountDrainRequired):
		return "drain_required"
	case errors.Is(err, ErrAccountLifecycleUnavailable):
		return "unavailable"
	case errors.Is(err, ErrAccountForeign):
		return "foreign"
	default:
		return "rejected"
	}
}
