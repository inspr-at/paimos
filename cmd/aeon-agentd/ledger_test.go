// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Risk: status infers membership from service files, or rebuild forgets an
// uninstalled member. Both commands use only isolated fixture state.
func TestSharedLedgerCLIStatusAndRebuildKeepRemovedServiceMember(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "ledger")
	ledger, err := agentsetup.OpenSharedLedger(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	id, err := agentsetup.LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	member := agentsetup.LedgerMember{ID: id, Label: "cm.aeon.agentd.pma", Root: filepath.Join(root, "paired-pma"), JoinedAt: time.Unix(10, 0).UTC()}
	if err = ledger.Register(member); err != nil {
		t.Fatal(err)
	}
	before, _, err := ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = run([]string{"ledger", "status", "--ledger-root", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "member without service") || strings.Contains(out.String(), "paired-pma") {
		t.Fatal("status lost tombstone or exposed private root", out.String())
	}
	if err = os.Remove(filepath.Join(path, "ledger.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = run([]string{"ledger", "rebuild", "--ledger-root", path}, &out); err != nil {
		t.Fatal(err)
	}
	after, members, err := ledger.Snapshot()
	if err != nil || after.Generation == before.Generation || !after.Rebuilding || len(members) != 1 || members[0].ID != id {
		t.Fatal("CLI rebuild forgot removed service", err, after)
	}
	raw, err := json.Marshal(after)
	if err != nil || bytes.Contains(raw, []byte("paired-pma")) {
		t.Fatal("shared data exposed root", err)
	}
}

// Risk: installed capability discovery can guess from a release number, or an
// invalid instance can resolve outside its root. The version advert is explicit.
func TestSharedLedgerCLIAdvertisesCapabilityAndRejectsInvalidInstances(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--version"}, &out); err != nil || !strings.Contains(out.String(), "ledger-v1") {
		t.Fatal("missing capability", err)
	}
	if err := run([]string{"status", "--instance", "../pma"}, &out); err == nil {
		t.Fatal("unsafe instance accepted")
	}
	if err := run([]string{"ledger", "unknown"}, &out); err == nil {
		t.Fatal("unknown ledger command accepted")
	}
}
