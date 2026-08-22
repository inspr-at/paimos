// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

type runnerControlJournalEvent struct {
	Version int                         `json:"version"`
	Op      string                      `json:"op"`
	Record  *runnerControlJournalRecord `json:"record,omitempty"`
	Key     string                      `json:"key,omitempty"`
}

type runnerControlCheckpoint struct {
	Version int                          `json:"version"`
	Records []runnerControlJournalRecord `json:"records"`
}

type runnerControlJournal struct {
	mu             sync.Mutex
	dir            string
	journalPath    string
	checkpointPath string
	records        map[string]runnerControlJournalRecord
}

func openRunnerControlJournal(dir string) (*runnerControlJournal, error) {
	if strings.TrimSpace(dir) != dir || dir == "" {
		return nil, errors.New("runner control state directory is invalid")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create runner control state: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect runner control state: %w", err)
	}
	// MkdirAll applies 0700 to every directory it creates. Refuse an
	// existing broad or redirected directory instead of following a symlink
	// or silently changing permissions on a path we do not own.
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("runner control state directory has unsafe mode or type")
	}
	j := &runnerControlJournal{dir: dir, journalPath: filepath.Join(dir, "control.journal"),
		checkpointPath: filepath.Join(dir, "control.checkpoint.json"), records: map[string]runnerControlJournalRecord{}}
	if err := j.loadCheckpoint(); err != nil {
		return nil, err
	}
	if err := j.replayJournal(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *runnerControlJournal) loadCheckpoint() error {
	raw, err := readBoundedControlFile(j.checkpointPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var checkpoint runnerControlCheckpoint
	if err := strictControlJournalJSON(raw, &checkpoint); err != nil || checkpoint.Version != runnerControlJournalVersion ||
		len(checkpoint.Records) > runnerControlRecordMax {
		return errors.New("runner control checkpoint is corrupt or unsupported")
	}
	for _, record := range checkpoint.Records {
		if err := validateRunnerControlRecord(record); err != nil {
			return errors.New("runner control checkpoint is corrupt or unsupported")
		}
		if _, exists := j.records[record.CommandID]; exists {
			return errors.New("runner control checkpoint contains duplicate commands")
		}
		j.records[record.CommandID] = record
	}
	return nil
}

func (j *runnerControlJournal) replayJournal() error {
	file, err := os.Open(j.journalPath) // #nosec G304 -- path is fixed beneath the validated runner state directory.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open runner control journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > runnerControlJournalMax || info.Mode().Perm() != 0o600 {
		return errors.New("runner control journal has unsafe mode or size")
	}
	scanner := bufio.NewScanner(io.LimitReader(file, runnerControlJournalMax+1))
	scanner.Buffer(make([]byte, 4096), 32<<10)
	for scanner.Scan() {
		var event runnerControlJournalEvent
		if err := strictControlJournalJSON(scanner.Bytes(), &event); err != nil || event.Version != runnerControlJournalVersion {
			return errors.New("runner control journal is corrupt or unsupported")
		}
		switch event.Op {
		case "put":
			if event.Record == nil || event.Key != "" || validateRunnerControlRecord(*event.Record) != nil {
				return errors.New("runner control journal is corrupt or unsupported")
			}
			j.records[event.Record.CommandID] = *event.Record
		case "delete":
			if event.Record != nil || event.Key == "" {
				return errors.New("runner control journal is corrupt or unsupported")
			}
			delete(j.records, event.Key)
		default:
			return errors.New("runner control journal is corrupt or unsupported")
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("runner control journal is corrupt or unsupported")
	}
	if len(j.records) > runnerControlRecordMax {
		return errors.New("runner control journal record limit exceeded")
	}
	return nil
}

func (j *runnerControlJournal) put(record runnerControlJournalRecord) error {
	if j == nil || validateRunnerControlRecord(record) != nil {
		return errors.New("runner control journal record is invalid")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.records[record.CommandID]; !exists && len(j.records) >= runnerControlRecordMax {
		return errors.New("runner control journal record limit reached")
	}
	event := runnerControlJournalEvent{Version: runnerControlJournalVersion, Op: "put", Record: &record}
	if err := j.append(event); err != nil {
		return err
	}
	j.records[record.CommandID] = record
	return j.checkpoint()
}

func (j *runnerControlJournal) delete(commandID string) error {
	if j == nil || strings.TrimSpace(commandID) != commandID || commandID == "" {
		return errors.New("runner control journal key is invalid")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.append(runnerControlJournalEvent{Version: runnerControlJournalVersion, Op: "delete", Key: commandID}); err != nil {
		return err
	}
	delete(j.records, commandID)
	return j.checkpoint()
}

func (j *runnerControlJournal) snapshot() []runnerControlJournalRecord {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *runnerControlJournal) snapshotLocked() []runnerControlJournalRecord {
	out := make([]runnerControlJournalRecord, 0, len(j.records))
	for _, record := range j.records {
		out = append(out, record)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CommandID < out[b].CommandID })
	return out
}

func (j *runnerControlJournal) append(event runnerControlJournalEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	info, err := os.Stat(j.journalPath)
	if err == nil && info.Size()+int64(len(body)) > runnerControlJournalMax {
		return errors.New("runner control journal size limit reached")
	}
	file, err := os.OpenFile(j.journalPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- fixed state path.
	if err != nil {
		return fmt.Errorf("open runner control journal: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (j *runnerControlJournal) checkpoint() error {
	body, err := json.Marshal(runnerControlCheckpoint{Version: runnerControlJournalVersion, Records: j.snapshotLocked()})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(j.dir, ".control.checkpoint.*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tempName, j.checkpointPath); err != nil {
		cleanup()
		return err
	}
	directory, err := os.Open(j.dir) // #nosec G304 -- validated runner-owned state directory.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readBoundedControlFile(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- caller supplies one fixed runner-state path.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o600 || info.Size() > runnerControlJournalMax {
		return nil, errors.New("runner control state has unsafe mode or size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, runnerControlJournalMax+1))
	if err != nil || len(raw) > runnerControlJournalMax {
		return nil, errors.New("runner control state exceeds its bound")
	}
	return raw, nil
}

func strictControlJournalJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("runner control state has trailing data")
	}
	return nil
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
