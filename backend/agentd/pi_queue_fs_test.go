// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPiQueueKeyRejectsMalformedAndTraversal(t *testing.T) {
	generation := uuid.NewString()
	base := piQueueRecord{
		Generation: generation, ProjectID: 957, Identity: "pi:worker", AccountKey: "operator-pi",
		AccountLabel: AccountPiContext, CorrelationID: "follow-1", Kind: piQueueKindFollowUp,
		State: piQueueHeld, TextSHA256: strings.Repeat("ab", 32), TextBytes: 4,
	}
	for _, key := range []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64),
		strings.Repeat("g", 64),
		"../" + strings.Repeat("a", 61),
		strings.Repeat("../", 21) + "a",
		strings.Repeat("0", 63) + "/",
		strings.Repeat("0", 62) + "..",
	} {
		record := base
		record.Key = key
		if err := validatePiQueueRecord(record); err == nil {
			t.Fatalf("accepted malformed key %q", key)
		}
	}
	record := base
	record.Key = strings.Repeat("ab", 32)
	record.TextSHA256 = strings.Repeat("AB", 32)
	if err := validatePiQueueRecord(record); err == nil {
		t.Fatal("accepted uppercase payload digest")
	}
}

func TestPiQueuePayloadRejectsTraversalAndForeignFiles(t *testing.T) {
	store, err := openPiQueueStore(t.TempDir(), "ppm-pi-fs")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "private.txt")
	secret := "operator-private-not-for-queue"
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(store.payloads.Name())
	before, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	bad := "../" + strings.Repeat("a", 61)
	if err := store.writePayload(bad, "hello"); err == nil {
		t.Fatal("writePayload accepted a traversal key")
	}
	if _, err := store.readPayload(bad, strings.Repeat("ab", 32), 5); !errors.Is(err, errPiQueueAmbiguous) {
		t.Fatalf("readPayload traversal err=%v", err)
	}
	after, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("malformed key created files outside the payload root")
	}
	if _, err := os.Stat(filepath.Join(outside, "hello")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("traversal write escaped into an unrelated directory")
	}
}

func TestPiQueuePayloadRejectsSymlinkAndSizeMismatch(t *testing.T) {
	store, err := openPiQueueStore(t.TempDir(), "ppm-pi-fs-link")
	if err != nil {
		t.Fatal(err)
	}
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "private.txt")
	secret := "do-not-read-me"
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("ab", 32)
	name := filepath.Join(store.payloads.Name(), key+".txt")
	if err := os.Symlink(secretPath, name); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(secret))
	got, err := store.readPayload(key, hex.EncodeToString(sum[:]), len(secret))
	if err == nil || got == secret {
		t.Fatalf("symlink payload leaked unrelated file: got=%q err=%v", got, err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", 4096)
	if err := os.WriteFile(name, []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}
	claim := []byte("keep")
	claimSum := sha256.Sum256(claim)
	got, err = store.readPayload(key, hex.EncodeToString(claimSum[:]), len(claim))
	if err == nil || got == string(claim) || got == oversized {
		t.Fatalf("size mismatch was not rejected: got=%q err=%v", got, err)
	}
}

func TestPiQueueCorruptJournalKeyRefusesOpen(t *testing.T) {
	root := t.TempDir()
	dir, err := InstanceStateDir(root, "ppm-pi-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := piQueueRecord{
		Key: "../" + strings.Repeat("a", 61), Generation: uuid.NewString(), ProjectID: 957,
		Identity: "pi:worker", AccountLabel: AccountPiContext, CorrelationID: "follow-1",
		Kind: piQueueKindFollowUp, State: piQueueHeld, TextSHA256: strings.Repeat("ab", 32), TextBytes: 4,
	}
	body, err := json.Marshal(map[string]any{"version": 1, "op": "put", "record": record})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi-queue.journal"), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openPiQueueStore(root, "ppm-pi-corrupt"); err == nil {
		t.Fatal("opened a journal whose key could traverse")
	}
}

func TestPiQueueReserveReplayKeepsExactBytes(t *testing.T) {
	store, err := openPiQueueStore(t.TempDir(), "ppm-pi-replay")
	if err != nil {
		t.Fatal(err)
	}
	generation := uuid.NewString()
	scope := piQueueScope{
		Generation: generation, ProjectID: 957, Identity: "pi:worker", AccountKey: "operator-pi",
		AccountLabel: AccountPiContext, ProfileID: "pi-anthropic-sonnet-high", ProfileVersion: "1",
	}
	want := "keep\u2028me"
	if err := store.reserve(scope, piQueueKindFollowUp, want, "follow-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.reserve(scope, piQueueKindFollowUp, want, "follow-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.reserve(scope, piQueueKindFollowUp, "other", "follow-1"); !errors.Is(err, ErrControlReplayConflict) {
		t.Fatalf("replay conflict err=%v", err)
	}
	key := piQueueKey(generation, "follow-1", piQueueKindFollowUp)
	sum := sha256.Sum256([]byte(want))
	got, err := store.readPayload(key, hex.EncodeToString(sum[:]), len(want))
	if err != nil || got != want {
		t.Fatalf("exact bytes lost: got=%q err=%v", got, err)
	}
}
