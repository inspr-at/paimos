// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/backend/localjournal"
)

const (
	runnerControlJournalVersion = 1
	runnerControlJournalMax     = 1 << 20
	runnerControlRecordMax      = 256
)

type runnerControlJournalRecord struct {
	CommandID      string               `json:"command_id"`
	LeaseID        string               `json:"lease_id"`
	LeaseRevision  int64                `json:"lease_revision"`
	EffectSequence int64                `json:"effect_sequence"`
	ClaimSequence  int64                `json:"claim_sequence"`
	ResultSequence int64                `json:"result_sequence"`
	RequestDigest  string               `json:"request_digest"`
	Outcome        string               `json:"outcome"`
	Reason         string               `json:"reason"`
	State          string               `json:"state"`
	Effect         *runnerControlEffect `json:"effect,omitempty"`
}

type runnerControlJournal struct {
	journal                          *localjournal.Journal[runnerControlJournalRecord]
	dir, journalPath, checkpointPath string
}

func openRunnerControlJournal(dir string) (*runnerControlJournal, error) {
	j, err := localjournal.Open(localjournal.Config[runnerControlJournalRecord]{
		Directory: dir, Prefix: "control", Version: runnerControlJournalVersion,
		MaxBytes: runnerControlJournalMax, MaxRecords: runnerControlRecordMax,
		Key: func(record runnerControlJournalRecord) (string, error) {
			if strings.TrimSpace(record.CommandID) != record.CommandID || record.CommandID == "" {
				return "", errors.New("invalid runner control key")
			}
			return record.CommandID, nil
		}, Validate: validateRunnerControlRecord,
	})
	if err != nil {
		return nil, err
	}
	return &runnerControlJournal{journal: j, dir: dir, journalPath: j.JournalPath(), checkpointPath: j.CheckpointPath()}, nil
}

func (j *runnerControlJournal) put(record runnerControlJournalRecord) error {
	if j == nil {
		return errors.New("runner control journal is unavailable")
	}
	return j.journal.Put(record)
}
func (j *runnerControlJournal) delete(key string) error {
	if j == nil {
		return errors.New("runner control journal is unavailable")
	}
	return j.journal.Delete(key)
}
func (j *runnerControlJournal) snapshot() []runnerControlJournalRecord {
	if j == nil {
		return nil
	}
	return j.journal.Snapshot()
}

func validateRunnerControlRecord(record runnerControlJournalRecord) error {
	if strings.TrimSpace(record.CommandID) != record.CommandID || record.CommandID == "" ||
		strings.TrimSpace(record.LeaseID) != record.LeaseID || record.LeaseID == "" ||
		record.LeaseRevision <= 0 || record.EffectSequence != 1 || record.ClaimSequence != 1 ||
		record.ResultSequence != 1 || (record.State != "claimed" && record.State != "completed") {
		return errors.New("invalid runner control record")
	}
	if record.State == "claimed" && (record.Outcome != "outcome_unknown" || record.Reason != "") {
		return errors.New("invalid runner control reason")
	}
	if record.State == "completed" && ((record.Outcome == "applied" && record.Reason != "") ||
		(record.Outcome == "rejected" && record.Reason != "natural_exit" && record.Reason != "process_termination_failed" &&
			record.Reason != "effect_rejected" && record.Reason != "unsupported_platform") ||
		(record.Outcome != "applied" && record.Outcome != "rejected")) {
		return errors.New("invalid runner control reason")
	}
	if record.Effect != nil && (record.Effect.CommandID != record.CommandID || record.Effect.LeaseID != record.LeaseID ||
		record.Effect.LeaseRevision != record.LeaseRevision || record.Effect.EffectSequence != record.EffectSequence ||
		!record.Effect.Target.validForRun(record.Effect.Target.RunID) ||
		!record.Effect.validForLease(runnerControlLease{Actions: []string{record.Effect.Action}})) {
		return errors.New("invalid runner control effect")
	}
	digest, err := hex.DecodeString(record.RequestDigest)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid runner control request digest")
	}
	digestInput := record.CommandID + "\x00" + record.Outcome + "\x00" + record.Reason
	if record.State == "claimed" {
		digestInput = record.CommandID + "\x00claimed"
	}
	want := sha256.Sum256([]byte(digestInput))
	if !strings.EqualFold(record.RequestDigest, hex.EncodeToString(want[:])) {
		return errors.New("runner control request digest does not match its closed result")
	}
	return nil
}
