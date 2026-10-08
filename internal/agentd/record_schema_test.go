// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/inspr-at/paimos/internal/localjournal"
)

// Risk: additive Record/nested-type changes can silently break rollback when
// the writer keeps the same schema version. Keep historical pins immutable;
// append a new version/hash and migration when this guard fails.
func TestRecordSchemaGolden(t *testing.T) {
	var describe func(reflect.Type) any
	marshaler := reflect.TypeFor[json.Marshaler]()
	describe = func(typ reflect.Type) any {
		out := map[string]any{"type": typ.String(), "kind": typ.Kind().String()}
		if typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler) {
			out["encoding"] = "json.Marshaler"
			return out
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			out["element"] = describe(typ.Elem())
			if typ.Kind() == reflect.Array {
				out["length"] = typ.Len()
			}
		case reflect.Map:
			out["key"] = describe(typ.Key())
			out["element"] = describe(typ.Elem())
		case reflect.Struct:
			fields := []any{}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if field.PkgPath != "" || field.Tag.Get("json") == "-" {
					continue
				}
				fields = append(fields, map[string]any{"name": field.Name, "json": field.Tag.Get("json"), "anonymous": field.Anonymous, "schema": describe(field.Type)})
			}
			out["fields"] = fields
		}
		return out
	}
	schema, err := json.Marshal(describe(reflect.TypeFor[Record]()))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(schema)
	got := hex.EncodeToString(digest[:])
	raw, err := os.ReadFile("testdata/record-schema-hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	if pins[strconv.Itoa(RecordSchemaVersion)] != got {
		t.Fatalf("Record schema changed: version %d hash %s; bump RecordSchemaVersion, add a migration and append a golden pin without changing older pins", RecordSchemaVersion, got)
	}
}

// Risk: 123 and 124 shared version 2. Forward migration must keep their evidence
// and leave persisted PIDs unowned through both startup and offline inspection.
func TestRecordSchemaForwardRecoveryAndDowngrade(t *testing.T) {
	for _, release := range []string{"123", "124"} {
		for _, offline := range []bool{false, true} {
			t.Run(release+"/offline="+strconv.FormatBool(offline), func(t *testing.T) {
				s, api, proc := testSupervisor(t)
				root, workspace := s.journalDir(), s.workspace
				if err := s.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile("testdata/checkpoint-release-" + release + ".json")
				if err != nil {
					t.Fatal(err)
				}
				checkpointPath := filepath.Join(root, "aeon-agentd-daemon.checkpoint.json")
				journalPath := filepath.Join(root, "aeon-agentd-daemon.journal")
				if err := os.WriteFile(checkpointPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
				// The release-124 reader opens the 123 checkpoint before adding
				// its durable rejection fields, still under legacy version 2.
				oldConfig := localjournal.Config[Record]{Directory: root, Prefix: "aeon-agentd-daemon", Version: 2, MaxBytes: 4 << 20, MaxRecords: 4096,
					Key: func(r Record) (string, error) { return r.RunID, nil }, Validate: func(r Record) error { return validateLaunchRecord(r) }}
				old, err := localjournal.Open(oldConfig)
				if err != nil {
					t.Fatal(err)
				}
				want := old.Snapshot()[0]
				if release == "123" {
					want.DeadLetters = []Telemetry{{Sequence: 2, Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"}}
					want.ReportRejections = 1
					if err := old.Put(want); err != nil {
						t.Fatal(err)
					}
				}
				if offline {
					if err := PersistFence(root, "daemon", ""); err != nil {
						t.Fatal(err)
					}
					status, err := OfflineLifecycle(root, "daemon", "tenant", "agent", "")
					if err != nil || !reflect.DeepEqual(status.UnconfirmedRunIDs, []string{"run"}) || !reflect.DeepEqual(status.SettlementPendingRunIDs, []string{"run"}) {
						t.Fatalf("offline migration lost process/settlement uncertainty: %+v %v", status, err)
					}
				} else {
					next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: "daemon", Workspace: workspace,
						Adapters: []Adapter{&fakeAdapter{proc: proc}}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_ = next.lock.Close()
						_ = next.state.Close()
					})
					want.State = "ownership_lost"
					if got := next.journal.Snapshot(); !reflect.DeepEqual(got, []Record{want}) {
						t.Fatalf("startup lost retained evidence: %+v", got)
					}
					_, err = next.Control(t.Context(), ControlRequest{TenantID: "tenant", PrincipalID: "agent", RunID: "run", Generation: next.Generation(), CorrelationID: "stop", Operation: "stop"})
					if !errors.Is(err, ErrNotOwned) || proc.calls != 0 {
						t.Fatalf("migration adopted a persisted PID: %v", err)
					}
					if err := next.Close(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
						t.Fatal("migration incorrectly authorized process cleanup", err)
					}
				}
				currentConfig := oldConfig
				currentConfig.Version = RecordSchemaVersion
				current, err := localjournal.Open(currentConfig) // No migration on reopen.
				if err != nil || !reflect.DeepEqual(current.Snapshot(), []Record{want}) {
					t.Fatal("migrated checkpoint did not round trip", err)
				}
				beforeCheckpoint, err := os.ReadFile(checkpointPath)
				if err != nil {
					t.Fatal(err)
				}
				beforeJournal, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = localjournal.Open(oldConfig)
				var versionErr *localjournal.SchemaVersionError
				if !errors.As(err, &versionErr) || versionErr.Stored != RecordSchemaVersion || versionErr.Supported != 2 {
					t.Fatal("downgrade failed for wrong reason", err)
				}
				afterCheckpoint, err := os.ReadFile(checkpointPath)
				if err != nil || !bytes.Equal(beforeCheckpoint, afterCheckpoint) {
					t.Fatal("downgrade damaged checkpoint", err)
				}
				afterJournal, err := os.ReadFile(journalPath)
				if err != nil || !bytes.Equal(beforeJournal, afterJournal) {
					t.Fatal("downgrade damaged WAL", err)
				}
			})
		}
	}
}
