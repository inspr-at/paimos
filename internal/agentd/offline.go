// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

// OfflineLifecycle inspects retained local evidence without an execution key.
// It requires an exclusive instance lock and a durable dispatch fence. A PID
// never proves exit, and a missing socket does not prove idle. Unknown prior
// processes remain unconfirmed; the function never adopts or signals them.
func OfflineLifecycle(root, daemon, tenant, principal, account string) (LifecycleStatus, error) {
	if daemon == "" || len(daemon) > 128 || strings.ContainsAny(daemon, "/\\\x00\r\n") || tenant == "" || principal == "" {
		return LifecycleStatus{}, ErrScope
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return LifecycleStatus{}, err
	}
	defer store.Close()
	lock, err := store.LockNamed("aeon-agentd-" + daemon + ".lock")
	if err != nil {
		return LifecycleStatus{}, ErrProcessesUnconfirmed
	}
	defer lock.Close()
	fenced := func(id string) bool {
		raw, e := store.Read(fenceName(id), 4096)
		if e != nil {
			return false
		}
		var v DrainRequest
		return json.Unmarshal(raw, &v) == nil && v.DaemonID == daemon && v.AccountID == id
	}
	all := fenced("")
	if !all && (account == "" || !fenced(account)) {
		return LifecycleStatus{}, ErrProcessesUnconfirmed
	}
	prefix := "aeon-agentd-" + daemon
	for _, suffix := range []string{".journal", ".checkpoint.json"} {
		if _, e := store.Read(prefix+suffix, 4<<20); e != nil && !errors.Is(e, os.ErrNotExist) {
			return LifecycleStatus{}, e
		}
	}
	j, err := localjournal.Open(localjournal.Config[Record]{Directory: root, Prefix: prefix, Version: RecordSchemaVersion, Migrations: recordMigrations(), MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r Record) (string, error) {
		if r.RunID == "" {
			return "", ErrScope
		}
		return r.RunID, nil
	}, Validate: func(r Record) error {
		if r.TenantID != tenant || r.PrincipalID != principal || r.Generation == "" {
			return ErrScope
		}
		return validateLaunchRecord(r)
	}})
	if err != nil {
		return LifecycleStatus{}, err
	}
	v := LifecycleStatus{DaemonID: daemon, State: "drained", AllFenced: all, ActiveRunIDs: []string{}, UnconfirmedRunIDs: []string{}, SettlementPendingRunIDs: []string{}, FencedAccountIDs: []string{}, VerificationResults: map[string]string{}}
	if account != "" {
		v.FencedAccountIDs = append(v.FencedAccountIDs, account)
	}
	for _, r := range j.Snapshot() {
		if account != "" && r.AccountID != "" && r.AccountID != account {
			continue
		}
		if r.LedgerGroup != "" && !noLocalProcess(r) && confirmedLedgerExit(r) {
			r.ExitObserved = true
			if err = j.Put(r); err != nil {
				return LifecycleStatus{}, err
			}
		}
		if !noLocalProcess(r) {
			v.State = "unconfirmed"
			v.UnconfirmedRunIDs = append(v.UnconfirmedRunIDs, r.RunID)
		}
		if len(r.Pending) > 0 || r.SettlementGap || r.State == "claim_pending" || r.State == "route_pending" {
			v.SettlementPendingRunIDs = append(v.SettlementPendingRunIDs, r.RunID)
		}
	}
	return v, nil
}

// OfflineLeaveLedger is the fenced owner's cleanup path after server revocation.
// It needs no runtime bearer key and cannot enrol or dispatch. Every uncertain
// intent, settlement and possible process remains a refusal. The caller must
// already have independently confirmed computer revocation through lifecycle.
func OfflineLeaveLedger(root, daemon, tenant, principal string) error {
	status, err := OfflineLifecycle(root, daemon, tenant, principal, "")
	if err != nil {
		return err
	}
	if status.State != "drained" || len(status.SettlementPendingRunIDs) > 0 {
		return ErrProcessesUnconfirmed
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		return err
	}
	defer store.Close()
	lock, err := store.LockNamed("aeon-agentd-" + daemon + ".lock")
	if err != nil {
		return ErrProcessesUnconfirmed
	}
	defer lock.Close()
	raw, err := store.Read("ledger-member.json", 8192)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var binding ledgerBinding
	if json.Unmarshal(raw, &binding) != nil || binding.Member.Root != filepath.Dir(root) || binding.Config.Root != binding.Member.Root {
		return ErrScope
	}
	j, err := localjournal.Open(localjournal.Config[Record]{Directory: root, Prefix: "aeon-agentd-" + daemon, Version: RecordSchemaVersion, Migrations: recordMigrations(), MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r Record) (string, error) { return r.RunID, nil }, Validate: func(r Record) error {
		if r.TenantID != tenant || r.PrincipalID != principal || r.Generation == "" || r.RunID == "" {
			return ErrScope
		}
		return validateLaunchRecord(r)
	}})
	if err != nil {
		return err
	}
	for _, r := range j.Snapshot() {
		if !noLocalProcess(r) || r.State == "claim_pending" || r.State == "route_pending" || len(r.Pending) > 0 || r.SettlementGap {
			return ErrProcessesUnconfirmed
		}
	}
	ledger, err := agentsetup.OpenSharedLedger(binding.Config.Path, false)
	if err != nil {
		return err
	}
	defer ledger.Close()
	data, members, err := ledger.Snapshot()
	if err != nil {
		return err
	}
	// Retrying after a confirmed prior leave is idempotent, but cannot invent or
	// delete another member. The private durable root binding supplies ownership.
	found := false
	for _, member := range members {
		if member.ID == binding.Member.ID {
			if member != binding.Member {
				return ErrScope
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	origin, err := agentsetup.CanonicalLedgerOrigin(binding.Config.Origin)
	if err != nil {
		return err
	}
	_, err = ledger.Import(binding.Member.ID, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(data.Generation, "instance", origin+"\x00"+tenant), PID: os.Getpid(), StartedAt: time.Now().UTC()}, nil)
	if err != nil {
		return err
	}
	return ledger.Leave(binding.Member.ID, data.Generation, true)
}
