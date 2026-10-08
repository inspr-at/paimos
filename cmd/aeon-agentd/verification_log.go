// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// This private, bounded destination also works for existing launchd services
// without log paths. It never rewrites a service receipt or restarts a daemon.
func verificationLog(store *agentsetup.Store) func(string, string, string, string) {
	var mu sync.Mutex
	return func(run, account, stage, reason string) {
		if len(run) > 128 || len(account) > 128 {
			return
		}
		switch stage {
		case "daemon_ready", "poll_blocked", "starting", "refused", "ownership_lost", "completed", "failed", "cancelled", "settlement_pending":
		default:
			return
		}
		switch reason {
		case "", "queue_unavailable", "dispatch_not_allowed", "probe_failed", "probe_timeout", "adapter_unsupported", "binding_incomplete", "local_binding_missing", "start_unconfirmed", "child_exit_failed", "vendor_limit", "reporter_unavailable":
		default:
			return
		}
		line, _ := json.Marshal(map[string]string{"at": time.Now().UTC().Format(time.RFC3339), "run_id": run, "account_id": account, "stage": stage, "reason": reason})
		mu.Lock()
		defer mu.Unlock()
		const limit = 256 << 10
		prior, err := store.Read("verification.log", limit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("verification log unavailable")
			return
		}
		prior = append(prior, append(line, '\n')...)
		if len(prior) > limit {
			prior = prior[len(prior)-limit:]
			if cut := bytes.IndexByte(prior, '\n'); cut >= 0 {
				prior = prior[cut+1:]
			}
		}
		if err := store.Write("verification.log", prior, false); err != nil {
			slog.Warn("verification log unavailable")
		}
	}
}
