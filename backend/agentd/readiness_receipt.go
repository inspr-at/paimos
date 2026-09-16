// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/localjournal"
)

// ReadinessReceipt is the bounded server-accepted observation retained by its
// owning daemon. It contains no paths, credentials, leases, sessions, or raw
// probe output. The lifecycle client writes it only after validating the
// authority transition response.
type ReadinessReceipt struct {
	ContractVersion   string                  `json:"contract_version"`
	IntentID          string                  `json:"intent_id"`
	ProjectID         int64                   `json:"project_id"`
	RuntimeID         string                  `json:"runtime_id"`
	RuntimeGeneration string                  `json:"runtime_generation"`
	AccountLabel      string                  `json:"account_label"`
	AccountKey        string                  `json:"account_key,omitempty"`
	ProfileID         string                  `json:"dispatch_profile_id"`
	ProfileVersion    string                  `json:"dispatch_profile_version"`
	WorkspaceHandle   string                  `json:"workspace_handle"`
	WorkspaceIdentity string                  `json:"workspace_identity"`
	WorkspaceMode     string                  `json:"workspace_mode"`
	BaselineDigest    string                  `json:"baseline_digest"`
	HostKind          string                  `json:"host_kind"`
	Status            string                  `json:"status"`
	NextAction        string                  `json:"next_action"`
	ObservedAt        string                  `json:"observed_at"`
	ExpiresAt         string                  `json:"expires_at"`
	Checks            []ReadinessReceiptCheck `json:"checks"`
}

type ReadinessReceiptCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Digest string `json:"digest,omitempty"`
}

// ReadinessReceiptRequest deliberately requires the complete server-bound
// tuple. A local caller cannot ask for a broad project/runtime cache dump.
type ReadinessReceiptRequest struct {
	ProjectID         int64  `json:"project_id"`
	RuntimeID         string `json:"runtime_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	AccountLabel      string `json:"account_label"`
	AccountKey        string `json:"account_key,omitempty"`
	ProfileID         string `json:"dispatch_profile_id"`
	ProfileVersion    string `json:"dispatch_profile_version"`
	WorkspaceHandle   string `json:"workspace_handle"`
	WorkspaceIdentity string `json:"workspace_identity"`
	WorkspaceMode     string `json:"workspace_mode"`
	BaselineDigest    string `json:"baseline_digest"`
}

type readinessReceiptStore struct {
	journal *localjournal.Journal[ReadinessReceipt]
}

func openReadinessReceiptStore(root, instance string) (*readinessReceiptStore, error) {
	dir, err := InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	j, err := localjournal.Open(localjournal.Config[ReadinessReceipt]{
		Directory: dir, Prefix: "readiness-receipts", Version: 1, MaxBytes: 2 << 20, MaxRecords: 256,
		Key: func(in ReadinessReceipt) (string, error) {
			if err := validateReadinessReceipt(in, time.Time{}); err != nil {
				return "", err
			}
			return readinessReceiptKey(in), nil
		},
		Validate: func(in ReadinessReceipt) error { return validateReadinessReceipt(in, time.Time{}) },
	})
	if err != nil {
		return nil, err
	}
	return &readinessReceiptStore{journal: j}, nil
}

func readinessReceiptKey(in ReadinessReceipt) string {
	raw := []byte(strconv.FormatInt(in.ProjectID, 10) + "\x00" + in.RuntimeID + "\x00" + in.RuntimeGeneration + "\x00" + in.AccountLabel + "\x00" + in.AccountKey + "\x00" + in.ProfileID + "\x00" + in.ProfileVersion + "\x00" + in.WorkspaceHandle + "\x00" + in.WorkspaceIdentity + "\x00" + in.WorkspaceMode + "\x00" + in.BaselineDigest)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validateReadinessReceipt(in ReadinessReceipt, now time.Time) error {
	if in.ContractVersion != ReadinessContractVersion || uuid.Validate(in.IntentID) != nil || in.ProjectID <= 0 || uuid.Validate(in.RuntimeID) != nil || uuid.Validate(in.RuntimeGeneration) != nil ||
		!validAccountLabel(in.AccountLabel) || (in.AccountKey != "" && !validAccountKey(in.AccountKey)) || !validSafeLabel(in.ProfileID, 128) || !validSafeLabel(in.ProfileVersion, 128) ||
		!validSafeLabel(in.WorkspaceHandle, 128) || len(in.WorkspaceIdentity) != 64 || !validSafeLabel(in.WorkspaceIdentity, 64) || (in.WorkspaceMode != WorkspaceExclusive && in.WorkspaceMode != WorkspaceShared) || len(in.BaselineDigest) != 71 ||
		!validSafeLabel(in.HostKind, 64) || !validSafeLabel(in.NextAction, 64) || len(in.Checks) == 0 || len(in.Checks) > 8 {
		return errors.New("invalid accepted readiness receipt")
	}
	switch in.Status {
	case "ready", "needs_setup", "unavailable":
	default:
		return errors.New("invalid accepted readiness receipt")
	}
	observed, err := time.Parse(time.RFC3339Nano, in.ObservedAt)
	if err != nil || observed.IsZero() {
		return errors.New("invalid accepted readiness receipt")
	}
	expires, err := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if err != nil || !expires.After(observed) || (!now.IsZero() && !expires.After(now)) {
		return errors.New("accepted readiness receipt is stale")
	}
	seen := map[string]bool{}
	for _, check := range in.Checks {
		if !validSafeLabel(check.ID, 64) || !validSafeLabel(check.Reason, 64) || seen[check.ID] || (check.Digest != "" && len(check.Digest) != 71) {
			return errors.New("invalid accepted readiness receipt")
		}
		switch check.Status {
		case "pass", "fail", "warn", "unknown", "unsupported", "stale":
		default:
			return errors.New("invalid accepted readiness receipt")
		}
		seen[check.ID] = true
	}
	return nil
}

func (s *Supervisor) StoreReadinessReceipt(in ReadinessReceipt) error {
	if err := validateReadinessReceipt(in, time.Now()); err != nil {
		return err
	}
	if s.receipts == nil {
		return errors.New("durable readiness receipt store unavailable")
	}
	now := time.Now()
	for _, previous := range s.receipts.journal.Snapshot() {
		expires, parseErr := time.Parse(time.RFC3339Nano, previous.ExpiresAt)
		expired := parseErr != nil || !expires.After(now)
		// The private lookup accepts only this daemon generation, so receipts
		// from every prior generation are permanently unusable and can be
		// discarded before they consume the bounded journal.
		superseded := previous.RuntimeGeneration != in.RuntimeGeneration
		if expired || superseded {
			if err := s.receipts.journal.Delete(readinessReceiptKey(previous)); err != nil {
				return err
			}
		}
	}
	return s.receipts.journal.Put(in)
}

func (s *Supervisor) ReadinessReceipt(in ReadinessReceiptRequest) (ReadinessReceipt, error) {
	probe := ReadinessReceipt{ProjectID: in.ProjectID, RuntimeID: in.RuntimeID, RuntimeGeneration: in.RuntimeGeneration, AccountLabel: in.AccountLabel, AccountKey: in.AccountKey,
		ProfileID: in.ProfileID, ProfileVersion: in.ProfileVersion, WorkspaceHandle: in.WorkspaceHandle, WorkspaceIdentity: in.WorkspaceIdentity, WorkspaceMode: in.WorkspaceMode, BaselineDigest: in.BaselineDigest}
	if in.ProjectID <= 0 || uuid.Validate(in.RuntimeID) != nil || uuid.Validate(in.RuntimeGeneration) != nil || !validAccountLabel(in.AccountLabel) ||
		(in.AccountKey != "" && !validAccountKey(in.AccountKey)) || !validSafeLabel(in.ProfileID, 128) || !validSafeLabel(in.ProfileVersion, 128) || !validSafeLabel(in.WorkspaceHandle, 128) || len(in.WorkspaceIdentity) != 64 || !validSafeLabel(in.WorkspaceIdentity, 64) || (in.WorkspaceMode != WorkspaceExclusive && in.WorkspaceMode != WorkspaceShared) || len(in.BaselineDigest) != 71 {
		return ReadinessReceipt{}, errors.New("readiness receipt request is invalid")
	}
	if in.RuntimeGeneration != s.Status().DaemonID {
		return ReadinessReceipt{}, errors.New("accepted readiness receipt is not owned by this daemon generation")
	}
	if s.receipts == nil {
		return ReadinessReceipt{}, errors.New("durable readiness receipt store unavailable")
	}
	key := readinessReceiptKey(probe)
	for _, receipt := range s.receipts.journal.Snapshot() {
		if readinessReceiptKey(receipt) != key || receipt.ProjectID != in.ProjectID {
			continue
		}
		if err := validateReadinessReceipt(receipt, time.Now()); err != nil {
			return ReadinessReceipt{}, errors.New("accepted readiness receipt unavailable")
		}
		return receipt, nil
	}
	return ReadinessReceipt{}, errors.New("accepted readiness receipt unavailable")
}
