// SPDX-License-Identifier: AGPL-3.0-only
// AEON — agent runtime primitives
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package localjournal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Risk: a failed upgrade/downgrade must not compact the checkpoint or WAL,
// including when an old checkpoint precedes a future or malformed WAL entry.
func TestJournalSchemaMigrationAndRefusal(t *testing.T) {
	const old = `{"version":1,"records":[{"id":"one","state":"old"}]}`
	const put = `{"version":1,"op":"put","record":{"id":"two","state":"pending"}}` + "\n"
	const future = `{"version":3,"op":"put","future_envelope":true,"record":{"id":"two","future_field":"private"}}`
	for _, tc := range []struct {
		name, checkpoint, wal string
		version, refused      int
		migrate, corrupt      bool
	}{
		{name: "checkpoint and WAL forward", checkpoint: old, wal: put, version: 2, migrate: true},
		{name: "checkpoint only forward", checkpoint: old, version: 2, migrate: true},
		{name: "empty checkpoint forward", checkpoint: `{"version":1,"records":[]}`, version: 2, migrate: true},
		{name: "WAL only forward", wal: put, version: 2, migrate: true},
		{name: "mixed WAL after interrupted migration", checkpoint: `{"version":2,"records":[]}`, wal: put + `{"version":2,"op":"put","record":{"id":"one","state":"current"}}` + "\n", version: 2, migrate: true},
		{name: "future checkpoint before record decoding", checkpoint: `{"version":3,"records":[{"future_field":"private"}],"future_envelope":true}`, wal: put, version: 2, migrate: true, refused: 3},
		{name: "future WAL after old checkpoint", checkpoint: old, wal: put + future + "\n", version: 2, migrate: true, refused: 3},
		{name: "future WAL without newline", checkpoint: old, wal: future, version: 2, migrate: true, refused: 3},
		{name: "unsupported old schema", checkpoint: old, wal: put, version: 2, refused: 1},
		{name: "unknown legacy field", checkpoint: `{"version":1,"records":[{"id":"one","state":"old","unrecognized":"private"}]}`, wal: put, version: 2, migrate: true, corrupt: true},
		{name: "invalid migrated record", checkpoint: `{"version":1,"records":[{"id":"","state":"old"}]}`, wal: put, version: 2, migrate: true, corrupt: true},
		{name: "bad WAL blocks checkpoint migration", checkpoint: old, wal: put + "invalid private contents\n", version: 2, migrate: true, corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"schema.checkpoint.json": tc.checkpoint, "schema.journal": tc.wal}
			for name, body := range files {
				if body != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			config := Config[testRecord]{Directory: dir, Prefix: "schema", Version: tc.version, MaxBytes: 4096, MaxRecords: 4,
				Key: func(r testRecord) (string, error) { return r.ID, nil }, Validate: func(r testRecord) error {
					if r.ID == "" || r.State == "" {
						return errors.New("invalid fixture")
					}
					return nil
				}}
			if tc.migrate {
				config.Migrations = map[int]func(json.RawMessage) (testRecord, error){1: func(raw json.RawMessage) (testRecord, error) {
					var r testRecord
					err := strictJSON(raw, &r)
					return r, err
				}}
			}
			j, err := Open(config)
			if tc.refused != 0 || tc.corrupt {
				if err == nil {
					t.Fatal("incompatible state accepted")
				}
				var versionErr *SchemaVersionError
				if tc.refused != 0 {
					if !errors.As(err, &versionErr) || versionErr.Stored != tc.refused || versionErr.Supported != tc.version || !strings.Contains(err.Error(), "use a") {
						t.Fatalf("wrong refusal: %v", err)
					}
					if tc.refused > tc.version && !strings.Contains(err.Error(), "pre-upgrade checkpoint and journal backups") {
						t.Fatalf("downgrade lacks remedy: %v", err)
					}
				} else if errors.As(err, &versionErr) || !strings.Contains(err.Error(), "corrupt or unsupported") {
					t.Fatalf("wrong corruption failure: %v", err)
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatal("error disclosed record data")
				}
				for name, before := range files {
					after, err := os.ReadFile(filepath.Join(dir, name))
					if before == "" {
						if !errors.Is(err, os.ErrNotExist) {
							t.Fatal("refusal created state file")
						}
					} else if err != nil || !bytes.Equal(after, []byte(before)) {
						t.Fatal("refusal modified state file", name, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []testRecord{{ID: "one", State: "old"}}
			switch tc.name {
			case "checkpoint and WAL forward":
				want = append(want, testRecord{ID: "two", State: "pending"})
			case "empty checkpoint forward":
				want = []testRecord{}
			case "WAL only forward":
				want = []testRecord{{ID: "two", State: "pending"}}
			case "mixed WAL after interrupted migration":
				want = []testRecord{{ID: "one", State: "current"}, {ID: "two", State: "pending"}}
			}
			assertState := func(j *Journal[testRecord]) {
				t.Helper()
				got, _ := json.Marshal(j.Snapshot())
				expected, _ := json.Marshal(want)
				if !bytes.Equal(got, expected) {
					t.Fatalf("lost migrated state: %s", got)
				}
			}
			assertState(j)
			raw, err := os.ReadFile(j.CheckpointPath())
			var cp checkpoint[testRecord]
			if err != nil || json.Unmarshal(raw, &cp) != nil || cp.Version != 2 {
				t.Fatal("checkpoint schema not upgraded", err)
			}
			config.Migrations = nil
			reopened, err := Open(config)
			if err != nil {
				t.Fatal("upgraded state did not reopen without migration", err)
			}
			assertState(reopened)
			wal, err := os.ReadFile(j.JournalPath())
			if err != nil || len(wal) != 0 {
				t.Fatal("migration did not compact WAL", err)
			}
		})
	}
}

type testRecord struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func TestStartupErrorsIdentifyCheckpointAndJournal(t *testing.T) {
	for _, suffix := range []string{".checkpoint.json", ".journal"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "diagnostic"+suffix)
			// Invalid JSON stays out of the error even when it contains data.
			if err := os.WriteFile(path, []byte("private fixture contents\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Open(Config[testRecord]{Directory: dir, Prefix: "diagnostic", Version: 1, MaxBytes: 1024, MaxRecords: 4,
				Key: func(r testRecord) (string, error) { return r.ID, nil }, Validate: func(testRecord) error { return nil }})
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "corrupt or unsupported") || strings.Contains(err.Error(), "private fixture contents") {
				t.Fatal("corrupt startup state lacks safe file diagnostic", err)
			}
		})
	}
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
