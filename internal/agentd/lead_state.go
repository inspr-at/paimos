// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func nativeLeadStateName(id string) string { return "native-lead-v2-" + id + ".json" }

// The released 131 reader requires version == 1 but ignores additive fields.
// It loses native resume/input provenance, never the retained-process fence.
func legacyLeadProjection(record leadRecord) leadRecord {
	record.Version = 1
	if record.Native != nil {
		record.NativeStateMajor = leadRecordSchemaVersion
		if !record.ExitReported && !record.Abandoned {
			// Even a native reservation without a fork retains occupancy. A
			// legacy daemon cannot reconcile it and must refuse startup.
			record.Attempted = true
		}
	}
	record.Native = nil
	return record
}

func leadStateJSON(record leadRecord) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 32768 {
		return nil, ErrRoutineUnavailable
	}
	return raw, nil
}

func writeLeadState(store *agentsetup.Store, record leadRecord, exclusive bool) error {
	name := "lead-runtime-" + record.Pickup.Demand.ID + ".json"
	legacy := legacyLeadProjection(record)
	legacyRaw, err := leadStateJSON(legacy)
	if err != nil {
		return err
	}
	if record.Native == nil {
		return store.Write(name, legacyRaw, exclusive)
	}
	record.Version, record.NativeStateMajor = leadRecordSchemaVersion, 0
	raw, err := leadStateJSON(record)
	if err != nil {
		return err
	}
	// Fence first, provenance second, settlement last. A failed/interrupted
	// write retains a blocking legacy receipt and cannot conceal a writer.
	guard := legacy
	guard.Attempted, guard.ExitReported = true, false
	guardRaw, err := leadStateJSON(guard)
	if err != nil {
		return err
	}
	if err := store.Write(name, guardRaw, exclusive); err != nil {
		return err
	}
	if err := store.Write(nativeLeadStateName(record.Pickup.Demand.ID), raw, exclusive); err != nil {
		return err
	}
	if !bytes.Equal(guardRaw, legacyRaw) {
		return store.Write(name, legacyRaw, false)
	}
	return nil
}

// Reads never repair state. Startup may rewrite a released major-2 receipt
// only after checking its retained ownership. Both files remain bounded and
// use Store's physical-path, owner, mode and atomic-write checks.
func readLeadState(store *agentsetup.Store, name string) (leadRecord, int, error) {
	raw, err := store.Read(name, 32768)
	if err != nil {
		return leadRecord{}, 0, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return leadRecord{}, 0, ErrProcessesUnconfirmed
	}
	record, err := decodeLeadRecord(raw)
	if err != nil {
		return leadRecord{}, 0, err
	}
	if name != "lead-runtime-"+record.Pickup.Demand.ID+".json" {
		return leadRecord{}, 0, ErrProcessesUnconfirmed
	}
	if record.NativeStateMajor == 0 {
		return record, header.Version, nil
	}
	if header.Version != 1 || record.NativeStateMajor != leadRecordSchemaVersion {
		return leadRecord{}, 0, ErrProcessesUnconfirmed
	}
	raw, err = store.Read(nativeLeadStateName(record.Pickup.Demand.ID), 32768)
	if err != nil {
		return leadRecord{}, 0, ErrProcessesUnconfirmed
	}
	native, err := decodeLeadRecord(raw)
	if err != nil || native.Native == nil || native.NativeStateMajor != 0 {
		return leadRecord{}, 0, errors.Join(ErrProcessesUnconfirmed, err)
	}
	want := legacyLeadProjection(native)
	record.Version = 1
	// Minor revisions never change the major's interpretation of the fence.
	want.MinorVersion = record.MinorVersion
	wantRaw, wantErr := leadStateJSON(want)
	gotRaw, gotErr := leadStateJSON(record)
	if wantErr != nil || gotErr != nil || !bytes.Equal(wantRaw, gotRaw) {
		return leadRecord{}, 0, ErrProcessesUnconfirmed
	}
	return native, header.Version, nil
}
