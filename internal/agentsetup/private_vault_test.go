// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

type memoryVault struct {
	values map[string][]byte
	denied bool
}

func (v *memoryVault) Read(id string) ([]byte, error) {
	if v.denied {
		return nil, agentsecurity.ErrDenied
	}
	raw, ok := v.values[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(raw), nil
}
func (v *memoryVault) Write(id string, raw []byte, first bool) error {
	if v.denied {
		return agentsecurity.ErrDenied
	}
	if _, ok := v.values[id]; first && ok {
		return agentsecurity.ErrExists
	}
	v.values[id] = bytes.Clone(raw)
	return nil
}
func (v *memoryVault) Delete(id string) error {
	if v.denied {
		return agentsecurity.ErrDenied
	}
	delete(v.values, id)
	return nil
}

func TestPrivatePairingMigrationPersistsBeforeOverwriteAndUnlink(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := e.Store.Read(snapshotName, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	key, err := e.Store.Read("runtime.key", 4096)
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(filepath.Join(e.Store.Path(), snapshotName))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	v := &memoryVault{values: map[string][]byte{}, denied: true}
	e.Store.vault = v
	if _, err := e.load(); !errors.Is(err, agentsecurity.ErrDenied) {
		t.Fatal("ACL denial fell back to plaintext")
	}
	// Refusal preserves the source capability instead of wiping it prematurely.
	legacy, err := os.ReadFile(filepath.Join(e.Store.Path(), snapshotName))
	if err != nil || !bytes.Equal(legacy, before) {
		t.Fatal("failed migration changed legacy state")
	}
	v.denied = false
	if _, err := e.load(); err != nil {
		t.Fatal(err)
	}
	loaded, err := e.Store.Read("runtime.key", 4096)
	if err != nil || !bytes.Equal(loaded, key) {
		t.Fatal("runtime migration lost authority")
	}
	for _, name := range []string{snapshotName, "runtime.key"} {
		if _, err := os.Lstat(filepath.Join(e.Store.Path(), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("plaintext capability retained")
		}
	}
	overwritten, err := io.ReadAll(held)
	if err != nil || len(overwritten) != len(before) || !allZero(overwritten) {
		t.Fatal("source inode was not overwritten before unlink")
	}
	if !bytes.Equal(v.values[e.Store.vaultID(snapshotName)], before) {
		t.Fatal("pairing migration lost state")
	}
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	s.Phase = "connected"
	if err := e.save(s, false); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.RemoveExact("runtime.key", Hash([]byte("wrong"))); !errors.Is(err, ErrCollision) {
		t.Fatal("removed unbound authority")
	}
	if err := e.Store.RemoveExact("runtime.key", Hash(key)); err != nil {
		t.Fatal(err)
	}
}

func TestVaultMigrationRejectsConflictingOrLinkedLegacyState(t *testing.T) {
	for _, conflict := range []string{"keychain differs", "symlink", "hardlink", "crash after overwrite"} {
		t.Run(conflict, func(t *testing.T) {
			s := testStore(t)
			key := []byte("aeon_fixture_" + stringRepeat('a', 64))
			if err := s.Write("runtime.key", key, true); err != nil {
				t.Fatal(err)
			}
			v := &memoryVault{values: map[string][]byte{}}
			path := filepath.Join(s.Path(), "runtime.key")
			switch conflict {
			case "keychain differs":
				v.values[s.vaultID("runtime.key")] = []byte("different")
			case "hardlink":
				if err := os.Link(path, filepath.Join(s.Path(), "linked")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, filepath.Join(s.Path(), "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original", path); err != nil {
					t.Fatal(err)
				}
			case "crash after overwrite":
				v.values[s.vaultID("runtime.key")] = bytes.Clone(key)
				if err := os.WriteFile(path, make([]byte, len(key)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.vault = v
			raw, err := s.Read("runtime.key", 4096)
			if conflict == "crash after overwrite" {
				if err != nil || !bytes.Equal(raw, key) {
					t.Fatal("interrupted erase did not recover")
				}
			} else if err == nil {
				t.Fatal("unsafe migration accepted")
			}
		})
	}
}

func stringRepeat(b byte, n int) string { return string(bytes.Repeat([]byte{b}, n)) }

func TestRefusedLegacyMigrationDoesNotRotateOrCreateEnclaveKey(t *testing.T) {
	for _, conflict := range []string{"precreated", "hardlink", "denied"} {
		t.Run(conflict, func(t *testing.T) {
			e, _, _, opts, _ := engineFixture(t)
			signer := &fixtureEnclave{}
			e.Enclave = signer
			e.Store.vault = nil
			key := []byte("aeon_fixture_" + stringRepeat('b', 64))
			if err := e.Store.Write("runtime.key", key, true); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(e.Store.Path(), "runtime.key")
			v := &memoryVault{values: map[string][]byte{}}
			switch conflict {
			case "precreated":
				v.values[e.Store.vaultID("runtime.key")] = []byte("attacker-item")
			case "hardlink":
				if err := os.Link(path, filepath.Join(e.Store.Path(), "linked-key")); err != nil {
					t.Fatal(err)
				}
			case "denied":
				v.denied = true
			}
			e.Store.vault = v
			if _, err := e.Store.Read("runtime.key", 4096); err == nil {
				t.Fatal("refused migration returned legacy bytes")
			}
			kept, err := os.ReadFile(path)
			info, statErr := os.Stat(path)
			if err != nil || statErr != nil || !bytes.Equal(kept, key) || info.Mode().Perm() != 0600 {
				t.Fatal("refused migration changed or removed the legacy file")
			}
			if conflict == "precreated" && bytes.Equal(v.values[e.Store.vaultID("runtime.key")], key) {
				t.Fatal("refusal rotated the legacy secret into the pre-created item")
			}
			if _, err := e.Begin(t.Context(), opts); err == nil {
				t.Fatal("refused migration started a new pairing")
			}
			if signer.id != "" {
				t.Fatal("refused migration created an enclave key")
			}
		})
	}
}

func TestVaultedPairingBindsPublicRuntimeBeforeCredentialedRequests(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	e.Store.vault = &memoryVault{values: map[string][]byte{}}
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := readRuntimeConfig(e.Store)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"origin", "tenant", "computer", "principal", "daemon", "workspace", "key identity"} {
		t.Run(field, func(t *testing.T) {
			changed := c
			switch field {
			case "origin":
				changed.Origin = "https://attacker.example.test"
			case "tenant":
				changed.TenantID = otherAccount
			case "computer":
				changed.ComputerID = otherAccount
			case "principal":
				changed.PrincipalID = otherAccount
			case "daemon":
				changed.DaemonID += "-other"
			case "workspace":
				changed.Workspace += "/other"
			case "key identity":
				changed.LocalAuthKeyID = "other"
			}
			raw, _ := json.Marshal(changed)
			if err := e.Store.Write(RuntimeName, raw, false); err != nil {
				t.Fatal(err)
			}
			if _, err := readRuntimeConfig(e.Store); err == nil {
				t.Fatal("public state redirected protected pairing authority")
			}
		})
	}
	raw, _ := json.Marshal(c)
	if err := e.Store.Write(RuntimeName, raw, false); err != nil {
		t.Fatal(err)
	}
	e.Store.vault.(*memoryVault).denied = true
	if _, err := readRuntimeConfig(e.Store); !errors.Is(err, agentsecurity.ErrDenied) {
		t.Fatal("public runtime read bypassed Keychain denial")
	}
}

type fixtureEnclave struct {
	id     string
	public string
}

func (s *fixtureEnclave) Create(_ context.Context, id string) (string, error) {
	s.id = id
	return s.public, nil
}
func (*fixtureEnclave) Sign(context.Context, string, []byte, string) (string, error) { return "", nil }

func TestEnclaveKeyIsCreatedAtPairingAndPreservedForAddedHarness(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signer := &fixtureEnclave{public: base64.StdEncoding.EncodeToString(elliptic.Marshal(key.Curve, key.X, key.Y))}
	e.Enclave = signer
	e.Store.vault = &memoryVault{values: map[string][]byte{}}
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if api.request.LocalAuthPublicKey != signer.public || signer.id == "" {
		t.Fatal("pairing did not pin enclave identity")
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalAuthKeyID != signer.id {
		t.Fatal("runtime lost key identity")
	}
	// AddHarness changes the request UUID; it must retain the original key.
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	s.Request.RequestID = otherAccount
	if _, err := e.provision(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	c, err = ReadRuntimeConfig(e.Store.Path())
	if err != nil || c.LocalAuthKeyID != signer.id {
		t.Fatal("added harness changed key identity")
	}
}
