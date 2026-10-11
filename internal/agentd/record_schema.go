// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// RecordSchemaVersion is the schema of Record and its nested persisted types,
// in both the checkpoint and WAL envelopes. Bump it for every schema change,
// retain the old golden schema, and add an explicit forward migration.
// Version 1 belongs to classic and is never opened implicitly. Releases 123
// and 124 both wrote version 2; version 3 accepts their known additive fields.
// Version 4 adds private ledger attempts and process-group exit evidence.
// Version 5 adds immutable routine workspace/grant provenance. Earlier records
// retain their launch/process/ledger evidence and acquire no routine authority.
// Version 6 separates retained checkout lineage from a continuation's launch.
const RecordSchemaVersion = 6

func recordMigrations() map[int]func(json.RawMessage) (Record, error) {
	return map[int]func(json.RawMessage) (Record, error){2: migrateRecordV2, 3: migrateRecordV2, 4: migrateRecordV2, 5: migrateRecordV5}
}

func migrateRecordV2(raw json.RawMessage) (Record, error) {
	record, err := decodeLegacyRecord(raw)
	if err != nil {
		return Record{}, err
	}
	if record.RoutineWorkspace != nil {
		return Record{}, errors.New("legacy Record cannot carry routine authority")
	}
	// Missing additive fields keep their zero values. In particular an empty
	// launch_state never proves that no child was forked; startup still fences
	// persisted PIDs as ownership_lost and never adopts or signals them.
	return record, nil
}

func migrateRecordV5(raw json.RawMessage) (Record, error) {
	record, err := decodeLegacyRecord(raw)
	if err != nil {
		return Record{}, err
	}
	if record.RoutineWorkspace != nil && record.RoutineWorkspace.CheckoutLaunchID != "" {
		return Record{}, errors.New("version 5 Record cannot carry continuation lineage")
	}
	return record, nil
}

func decodeLegacyRecord(raw json.RawMessage) (Record, error) {
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("legacy Record has trailing data")
	}
	return record, nil
}
