// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

func requirePathError(t *testing.T, err, cause error, path string) {
	t.Helper()
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected %v naming %s, got %v", cause, path, err)
	}
}

func TestStartupFileErrorsNamePathsAndPreserveCauses(t *testing.T) {
	s := testStore(t)
	s.vault = nil // No real Keychain access, even in an enclave-tagged test build.
	missingRoot := filepath.Join(s.Path(), "missing")
	_, err := OpenStore(missingRoot, false)
	requirePathError(t, err, os.ErrNotExist, missingRoot)
	for _, name := range []string{RuntimeName, snapshotName, "runtime.key", "control.json"} {
		_, err := s.Read(name, 4096)
		requirePathError(t, err, os.ErrNotExist, filepath.Join(s.Path(), name))
	}
	socket := filepath.Join(s.Path(), "agentd.sock")
	requirePathError(t, CheckSocket(socket), os.ErrNotExist, socket)
	if err := os.WriteFile(socket, []byte("fixture content must not appear"), 0600); err != nil {
		t.Fatal(err)
	}
	requirePathError(t, CheckSocket(socket), ErrUnsafePath, socket)
	unsafe := filepath.Join(s.Path(), "unsafe.json")
	if err := os.WriteFile(unsafe, []byte("fixture content must not appear"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = s.Read("unsafe.json", 4096)
	requirePathError(t, err, ErrUnsafePath, unsafe)
	if strings.Contains(err.Error(), "fixture content") {
		t.Fatal("file content leaked into the diagnostic")
	}
	lock, err := s.LockNamed("startup.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = s.LockNamed("startup.lock")
	requirePathError(t, err, ErrBusy, filepath.Join(s.Path(), "startup.lock"))
}

func TestMigratedPairingFailsAtNamedFileWithoutVault(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	vault := &memoryVault{values: map[string][]byte{}}
	e.Store.vault = vault
	for _, name := range []string{snapshotName, "runtime.key"} {
		if _, err := e.Store.Read(name, 1<<20); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(e.Store.Path(), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fixture migration did not remove disk source")
		}
	}
	if _, err := e.load(); err != nil {
		t.Fatal("vault-backed daemon cannot load migrated fixture", err)
	}
	e.Store.vault = nil // Same setup root, ordinary build's storage backend.
	if _, err := readRuntimeConfig(e.Store); err != nil {
		t.Fatal("public runtime should still be readable", err)
	}
	_, err := e.load()
	requirePathError(t, err, os.ErrNotExist, filepath.Join(e.Store.Path(), snapshotName))
	if runtime.GOOS == "darwin" && !strings.Contains(err.Error(), "signed aeon_enclave release daemon") {
		t.Fatal("missing macOS build-mode repair hint", err)
	}
	// status deliberately treats a missing snapshot as provisioning. A successful
	// status command therefore does not demonstrate daemon startup authority.
	status, err := e.Status(t.Context())
	if err != nil || status.Stage != "provisioning" {
		t.Fatal("unexpected ordinary-build status after migration", err)
	}
	vault.denied = true
	e.Store.vault = vault
	_, err = e.load()
	requirePathError(t, err, agentsecurity.ErrDenied, filepath.Join(e.Store.Path(), snapshotName))
	if !strings.Contains(err.Error(), "Keychain-backed state") {
		t.Fatal("vault refusal lacks storage context", err)
	}
}

func TestServiceExecutableLayoutDiagnostics(t *testing.T) {
	root := physicalTemp(t)
	dev := filepath.Join(root, "aeon-agentd")
	serviceBinary(t, dev)
	if got, err := ServiceExecutable(dev, root); err != nil || got != dev {
		t.Fatal("ordinary binary location unexpectedly refused", err)
	}
	missing := filepath.Join(root, "missing", "aeon-agentd")
	_, err := ServiceExecutable(missing, root)
	requirePathError(t, err, os.ErrNotExist, missing)
	for _, physical := range []string{
		filepath.Join(root, "brew", "Cellar", "aeon-agentd", "fixture", "bin", "aeon-agentd"),
		filepath.Join(root, ".local", "lib", "aeon", "fixture", "aeon-agentd"),
	} {
		serviceBinary(t, physical)
		stable := filepath.Join(root, ".local", "bin", "aeon-agentd")
		if strings.Contains(physical, "Cellar") {
			stable = filepath.Join(root, "brew", "opt", "aeon-agentd", "bin", "aeon-agentd")
		}
		_, err := ServiceExecutable(physical, root)
		requirePathError(t, err, os.ErrNotExist, stable)
	}
}
