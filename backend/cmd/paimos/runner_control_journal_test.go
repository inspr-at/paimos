// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runnerJournalFixture() runnerControlJournalRecord {
	digest := sha256.Sum256([]byte("command-opaque\x00applied\x00"))
	return runnerControlJournalRecord{CommandID: "command-opaque", LeaseID: "lease-opaque", LeaseRevision: 2,
		EffectSequence: 1, ClaimSequence: 1, ResultSequence: 1, RequestDigest: hex.EncodeToString(digest[:]),
		Outcome: "applied", State: "completed"}
}

func TestRunnerControlJournalIsDurableBoundedAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runner-state")
	journal, err := openRunnerControlJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := runnerJournalFixture()
	if err := journal.put(record); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, journal.journalPath, journal.checkpointPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if path == dir {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", path, info.Mode().Perm(), want)
		}
	}
	reloaded, err := openRunnerControlJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.snapshot(); len(got) != 1 || got[0] != record {
		t.Fatalf("reloaded=%+v", got)
	}
	raw, _ := os.ReadFile(journal.checkpointPath)
	for _, forbidden := range []string{"Bearer ", "api_key", "provider", "prompt", "pid", "pgid", "environment", "Idempotency-Key"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(forbidden)) {
			t.Fatalf("checkpoint leaked forbidden field %q: %s", forbidden, raw)
		}
	}
	if err := reloaded.delete(record.CommandID); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.snapshot()) != 0 {
		t.Fatal("deleted record survived checkpoint")
	}
}

func TestRunnerControlJournalFailsClosedOnCorruptionAndVersion(t *testing.T) {
	journal, err := openRunnerControlJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tampered := runnerJournalFixture()
	tampered.Outcome = "rejected"
	tampered.Reason = "natural_exit"
	if err := journal.put(tampered); err == nil {
		t.Fatal("journal accepted a result whose digest names different closed fields")
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "corrupt", body: "not-json\n"},
		{name: "unknown version", body: `{"version":2,"op":"delete","key":"x"}` + "\n"},
		{name: "unknown field", body: `{"version":1,"op":"delete","key":"x","secret":"x"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "control.journal")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := openRunnerControlJournal(dir); err == nil {
				t.Fatal("unsafe journal was accepted")
			}
		})
	}
}

func TestRunnerControlJournalSerializesPumpAndResultWriters(t *testing.T) {
	journal, err := openRunnerControlJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := runnerJournalFixture()
	var group sync.WaitGroup
	for index := 0; index < 24; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			record := base
			record.CommandID = base.CommandID + "-" + string(rune('a'+index))
			digest := sha256.Sum256([]byte(record.CommandID + "\x00applied\x00"))
			record.RequestDigest = hex.EncodeToString(digest[:])
			if err := journal.put(record); err != nil {
				t.Errorf("put: %v", err)
				return
			}
			if index%2 == 0 {
				if err := journal.delete(record.CommandID); err != nil {
					t.Errorf("delete: %v", err)
				}
			}
		}()
	}
	group.Wait()
	if got := len(journal.snapshot()); got != 12 {
		t.Fatalf("records=%d want=12", got)
	}
}
