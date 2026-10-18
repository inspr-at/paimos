// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

func TestStatusReadOnlyVaultDoesNotMigrateOrLock(t *testing.T) {
	e, _, _, opts, _ := engineFixture(t)
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	before, err := e.Store.Read(snapshotName, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryVault{values: map[string][]byte{}}
	e.Store.vault = vault
	reader, err := OpenStoreReadOnly(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.vault = vault
	status := &Engine{Store: reader}
	// Hold both writer locks, as a real sync/migration would. Read-only status
	// and its version lookup must neither contend nor create a third lock.
	guard, err := e.Store.LockNamed("keychain-migration.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	for _, migrated := range []bool{false, true} {
		if migrated {
			vault.values[reader.vaultID(snapshotName)] = bytes.Clone(before)
		}
		if _, err := status.Status(t.Context()); err != nil {
			t.Fatal("read-only status contended with writer", err)
		}
		if _, err := status.SavedOptions(); err != nil {
			t.Fatal("version lookup contended with writer", err)
		}
		raw, err := os.ReadFile(filepath.Join(e.Store.Path(), snapshotName))
		if err != nil || !bytes.Equal(raw, before) || !migrated && len(vault.values) != 0 {
			t.Fatal("status migrated, erased or changed legacy state")
		}
	}
	for _, err := range []error{reader.Lock(), reader.Write(snapshotName, before, false), reader.RemoveExact(snapshotName, Hash(before))} {
		if !errors.Is(err, ErrReadOnly) {
			t.Fatal("read-only opener permitted a mutation", err)
		}
	}
	if lock, err := reader.LockNamed("unexpected.lock"); lock != nil || !errors.Is(err, ErrReadOnly) {
		t.Fatal("read-only opener created a lock")
	}
	if _, err := os.Stat(filepath.Join(reader.Path(), "unexpected.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only lock attempt changed disk")
	}
	vault.denied = true
	if _, err := status.Status(t.Context()); !errors.Is(err, agentsecurity.ErrDenied) {
		t.Fatal("Keychain denial fell back to legacy state", err)
	}
	vault.denied = false
	vault.values[reader.vaultID(snapshotName)] = []byte("conflicting fixture")
	if _, err := status.Status(t.Context()); !errors.Is(err, ErrCollision) {
		t.Fatal("read-only status accepted conflicting authority", err)
	}
}

type pausedSnapshotVault struct {
	*memoryVault
	entered chan struct{}
	resume  chan struct{}
}

func (v *pausedSnapshotVault) Read(id string) ([]byte, error) {
	raw, err := v.memoryVault.Read(id)
	close(v.entered)
	<-v.resume
	return raw, err
}

func TestConcurrentStatusDoesNotBlockPairingWriter(t *testing.T) {
	e, _, _, opts, _ := engineFixture(t)
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	vault := &memoryVault{values: map[string][]byte{}}
	e.Store.vault = vault
	before, err := e.Store.Read(snapshotName, 1<<20) // Writer completes migration.
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStoreReadOnly(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	paused := &pausedSnapshotVault{memoryVault: vault, entered: make(chan struct{}), resume: make(chan struct{})}
	reader.vault = paused
	var old snapshot
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	wantStage := old.Phase
	old.Phase = "connected"
	after, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		p, err := (&Engine{Store: reader}).Status(t.Context())
		if err == nil && p.Stage != wantStage {
			err = errors.New("in-flight status lost its complete prior snapshot")
		}
		done <- err
	}()
	select {
	case <-paused.entered:
	case <-t.Context().Done():
		t.Fatal("status never reached Keychain read")
	}
	// The reader is provably in-flight. A writable read/save must still acquire
	// its migration lock and succeed; the reader will return its older snapshot.
	err = e.Store.Write(snapshotName, after, false)
	close(paused.resume)
	if readErr := <-done; readErr != nil || err != nil {
		t.Fatal("concurrent status blocked writer or lost snapshot", err, readErr)
	}
	if saved, err := e.Store.Read(snapshotName, 1<<20); err != nil || !bytes.Equal(saved, after) {
		t.Fatal("status overwrote the concurrent writer's newer state", err)
	}
}
