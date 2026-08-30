// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package localjournal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testRecord struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func TestJournalRejectsOversizedCheckpointBeforeAppending(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	j, err := Open(Config[testRecord]{Directory: dir, Prefix: "bounded", Version: 1, MaxBytes: 1024, MaxRecords: 4,
		Key: func(record testRecord) (string, error) { return record.ID, nil },
		Validate: func(record testRecord) error {
			if record.ID == "" || record.State == "" {
				return errors.New("record")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Put(testRecord{ID: "one", State: strings.Repeat("a", 600)}); err != nil {
		t.Fatal(err)
	}
	if err := j.Put(testRecord{ID: "two", State: strings.Repeat("b", 600)}); err == nil {
		t.Fatal("oversized checkpoint was accepted")
	}
	if info, err := os.Stat(j.JournalPath()); err != nil || info.Size() != 0 {
		t.Fatalf("rejected mutation reached WAL: info=%v err=%v", info, err)
	}
	reloaded, err := Open(Config[testRecord]{Directory: dir, Prefix: "bounded", Version: 1, MaxBytes: 1024, MaxRecords: 4,
		Key:      func(record testRecord) (string, error) { return record.ID, nil },
		Validate: func(testRecord) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("snapshot=%+v", got)
	}
}

func openTestJournal(t *testing.T, dir string) *Journal[testRecord] {
	t.Helper()
	j, err := Open(Config[testRecord]{Directory: dir, Prefix: "test", Version: 1, MaxBytes: 4096, MaxRecords: 4,
		Key: func(record testRecord) (string, error) {
			if record.ID == "" {
				return "", errors.New("id")
			}
			return record.ID, nil
		},
		Validate: func(record testRecord) error {
			if record.ID == "" || record.State == "" {
				return errors.New("record")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestJournalCheckpointsAndCompactsEveryMutation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	j := openTestJournal(t, dir)
	if err := j.Put(testRecord{ID: "one", State: "running"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(j.JournalPath())
	if err != nil || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal info=%v err=%v", info, err)
	}
	reloaded := openTestJournal(t, dir)
	if got := reloaded.Snapshot(); len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("snapshot=%+v", got)
	}
	if err := reloaded.Delete("one"); err != nil {
		t.Fatal(err)
	}
	if got := openTestJournal(t, dir).Snapshot(); len(got) != 0 {
		t.Fatalf("deleted snapshot=%+v", got)
	}
}

func TestJournalRepairsOnlyIncompleteFinalWALRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	j := openTestJournal(t, dir)
	if err := j.Put(testRecord{ID: "one", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.JournalPath(), []byte(`{"version":1,"op":"put"`), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded := openTestJournal(t, dir)
	if got := reloaded.Snapshot(); len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("snapshot=%+v", got)
	}
	info, err := os.Stat(j.JournalPath())
	if err != nil || info.Size() != 0 {
		t.Fatalf("tail was not compacted: info=%v err=%v", info, err)
	}
}
