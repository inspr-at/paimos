// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestVerificationLogPrivateBoundedAndRejectsLinkedFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentsetup.OpenStore(filepath.Join(root, "daemon"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log := verificationLog(store)
	log("", "", "daemon_ready", "")
	initial, err := store.Read("verification.log", 256<<10)
	if err != nil || !bytes.Contains(initial, []byte("daemon_ready")) {
		t.Fatal("startup did not create the diagnostic destination", err)
	}
	// A full valid history drops complete old lines, and a second logger retains
	// it across restart. Unknown reasons cannot become a payload log.
	if err := store.Write("verification.log", bytes.Repeat([]byte("old\n"), (256<<10)/4), false); err != nil {
		t.Fatal(err)
	}
	log("", "", "poll_blocked", "probe_failed")
	log("run", "account", "refused", "local_binding_missing")
	verificationLog(store)("run-2", "account", "completed", "")
	log("run", "account", "failed", "fixture-private-payload")
	raw, err := store.Read("verification.log", 256<<10)
	if err != nil || !bytes.Contains(raw, []byte("probe_failed")) || !bytes.Contains(raw, []byte("local_binding_missing")) || !bytes.Contains(raw, []byte("run-2")) || bytes.Contains(raw, []byte("fixture-private-payload")) {
		t.Fatal("private bounded log lost evidence", err)
	}
	info, err := os.Stat(filepath.Join(store.Path(), "verification.log"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("log is not private", err)
	}
	other, err := agentsetup.OpenStore(filepath.Join(root, "other"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := os.Symlink(filepath.Join(store.Path(), "verification.log"), filepath.Join(other.Path(), "verification.log")); err != nil {
		t.Fatal(err)
	}
	verificationLog(other)("must-not-write", "account", "completed", "")
	after, err := store.Read("verification.log", 256<<10)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("linked destination was modified", err)
	}
}
