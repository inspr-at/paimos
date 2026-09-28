// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

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
	j, err := localjournal.Open(localjournal.Config[Record]{Directory: root, Prefix: prefix, Version: 2, MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r Record) (string, error) {
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
		if !noLocalProcess(r) {
			v.State = "unconfirmed"
			v.UnconfirmedRunIDs = append(v.UnconfirmedRunIDs, r.RunID)
		}
		if len(r.Pending) > 0 || r.SettlementGap || r.State == "claim_pending" {
			v.SettlementPendingRunIDs = append(v.SettlementPendingRunIDs, r.RunID)
		}
	}
	return v, nil
}
