// SPDX-License-Identifier: AGPL-3.0-only
// AEON — agent runtime primitives
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package localjournal implements the bounded, private append journal and
// atomic checkpoint shared by local process supervisors.
package localjournal

import (
	"bufio"
	"bytes"
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

type Config[T any] struct {
	Directory  string
	Prefix     string
	Version    int
	MaxBytes   int64
	MaxRecords int
	Key        func(T) (string, error)
	Validate   func(T) error
	// Migrations decode explicitly supported older schemas into the current
	// record. Open validates the entire checkpoint and WAL before persisting
	// the migrated state. Callers must serialize Open with their instance lock.
	Migrations map[int]func(json.RawMessage) (T, error)
}

// SchemaVersionError distinguishes an incompatible schema from damaged data.
// Open returns it before changing either state file.
type SchemaVersionError struct {
	Stored    int
	Supported int
}

func (e *SchemaVersionError) Error() string {
	if e.Stored > e.Supported {
		return fmt.Sprintf("local journal schema version %d is newer than supported version %d; use a newer daemon or, after stopping the daemon, restore the matching pre-upgrade checkpoint and journal backups; state files were left unchanged", e.Stored, e.Supported)
	}
	return fmt.Sprintf("local journal schema version %d cannot be migrated to version %d; use a daemon that supports this schema; state files were left unchanged", e.Stored, e.Supported)
}

type event[T any] struct {
	Version int    `json:"version"`
	Op      string `json:"op"`
	Record  *T     `json:"record,omitempty"`
	Key     string `json:"key,omitempty"`
}

type checkpoint[T any] struct {
	Version int `json:"version"`
	Records []T `json:"records"`
}

type Journal[T any] struct {
	mu             sync.Mutex
	dir            string
	journalPath    string
	checkpointPath string
	version        int
	maxBytes       int64
	maxRecords     int
	key            func(T) (string, error)
	validate       func(T) error
	migrations     map[int]func(json.RawMessage) (T, error)
	migrated       bool
	records        map[string]T
}

func Open[T any](config Config[T]) (*Journal[T], error) {
	if strings.TrimSpace(config.Directory) != config.Directory || config.Directory == "" ||
		config.Prefix == "" || strings.ContainsAny(config.Prefix, "/\\\x00\r\n") ||
		config.Version < 1 || config.MaxBytes < 1024 || config.MaxRecords < 1 || config.Key == nil || config.Validate == nil {
		return nil, errors.New("local journal configuration is invalid")
	}
	migrations := make(map[int]func(json.RawMessage) (T, error), len(config.Migrations))
	for version, migrate := range config.Migrations {
		if version < 1 || version >= config.Version || migrate == nil {
			return nil, errors.New("local journal migration configuration is invalid")
		}
		migrations[version] = migrate
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create local journal state: %w", err)
	}
	info, err := os.Lstat(config.Directory)
	if err != nil {
		return nil, fmt.Errorf("stat local journal directory %s: %w", config.Directory, err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("local journal directory %s has unsafe mode or type", config.Directory)
	}
	j := &Journal[T]{dir: config.Directory, journalPath: filepath.Join(config.Directory, config.Prefix+".journal"),
		checkpointPath: filepath.Join(config.Directory, config.Prefix+".checkpoint.json"), version: config.Version,
		maxBytes: config.MaxBytes, maxRecords: config.MaxRecords, key: config.Key, validate: config.Validate,
		records: map[string]T{}, migrations: migrations}
	if err := j.loadCheckpoint(); err != nil {
		return nil, fmt.Errorf("load local journal checkpoint %s: %w", j.checkpointPath, err)
	}
	if err := j.replay(); err != nil {
		return nil, fmt.Errorf("replay local journal %s: %w", j.journalPath, err)
	}
	return j, nil
}

func (j *Journal[T]) JournalPath() string    { return j.journalPath }
func (j *Journal[T]) CheckpointPath() string { return j.checkpointPath }

func (j *Journal[T]) loadCheckpoint() error {
	raw, err := readBounded(j.checkpointPath, j.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := j.schemaVersion(raw); err != nil {
		return err
	}
	var state checkpoint[json.RawMessage]
	if strictJSON(raw, &state) != nil || len(state.Records) > j.maxRecords {
		return errors.New("local journal checkpoint is corrupt or unsupported")
	}
	for _, rawRecord := range state.Records {
		record, err := j.decodeRecord(state.Version, rawRecord)
		if err != nil {
			return err
		}
		key, keyErr := j.key(record)
		if keyErr != nil || j.validate(record) != nil {
			return errors.New("local journal checkpoint is corrupt or unsupported")
		}
		if _, exists := j.records[key]; exists {
			return errors.New("local journal checkpoint contains duplicate records")
		}
		j.records[key] = record
	}
	j.migrated = state.Version != j.version
	return nil
}

// Read the version before strict decoding of the envelope or records: future
// fields must produce a version refusal rather than an apparent corruption.
func (j *Journal[T]) schemaVersion(raw []byte) (int, error) {
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Version < 1 {
		return 0, errors.New("local journal is corrupt or unsupported")
	}
	if header.Version != j.version && j.migrations[header.Version] == nil {
		return 0, &SchemaVersionError{Stored: header.Version, Supported: j.version}
	}
	return header.Version, nil
}

func (j *Journal[T]) decodeRecord(version int, raw json.RawMessage) (T, error) {
	var record T
	var err error
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return record, errors.New("local journal record is corrupt or unsupported")
	}
	if version == j.version {
		err = strictJSON(raw, &record)
	} else {
		record, err = j.migrations[version](raw)
	}
	if err != nil {
		// Decoder/migration errors may contain private values or field names.
		return record, errors.New("local journal record is corrupt or unsupported")
	}
	return record, nil
}

func (j *Journal[T]) replay() error {
	raw, err := readBounded(j.journalPath, j.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		if j.migrated {
			return j.checkpoint()
		}
		return nil
	}
	if err != nil {
		return err
	}
	repairTail := len(raw) > 0 && raw[len(raw)-1] != '\n'
	if repairTail {
		boundary := bytes.LastIndexByte(raw, '\n')
		// A complete future entry without its newline is still evidence of
		// a newer writer. Do not erase it as a torn append on downgrade.
		if _, err := j.schemaVersion(raw[boundary+1:]); err != nil {
			var versionErr *SchemaVersionError
			if errors.As(err, &versionErr) {
				return err
			}
		}
		if boundary >= 0 {
			raw = raw[:boundary+1]
		} else {
			raw = nil
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	replayed := false
	for scanner.Scan() {
		replayed = true
		if _, err := j.schemaVersion(scanner.Bytes()); err != nil {
			return err
		}
		var entry event[json.RawMessage]
		if strictJSON(scanner.Bytes(), &entry) != nil {
			return errors.New("local journal is corrupt or unsupported")
		}
		switch entry.Op {
		case "put":
			if entry.Record == nil || entry.Key != "" {
				return errors.New("local journal is corrupt or unsupported")
			}
			record, err := j.decodeRecord(entry.Version, *entry.Record)
			if err != nil {
				return err
			}
			key, keyErr := j.key(record)
			if keyErr != nil || j.validate(record) != nil {
				return errors.New("local journal is corrupt or unsupported")
			}
			j.records[key] = record
		case "delete":
			if entry.Record != nil || entry.Key == "" {
				return errors.New("local journal is corrupt or unsupported")
			}
			delete(j.records, entry.Key)
		default:
			return errors.New("local journal is corrupt or unsupported")
		}
	}
	if scanner.Err() != nil || len(j.records) > j.maxRecords {
		return errors.New("local journal is corrupt or exceeds its bound")
	}
	if replayed || repairTail || j.migrated {
		return j.checkpoint()
	}
	return nil
}

func (j *Journal[T]) Put(record T) error {
	if j == nil || j.validate(record) != nil {
		return errors.New("local journal record is invalid")
	}
	key, err := j.key(record)
	if err != nil || key == "" {
		return errors.New("local journal record key is invalid")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.records[key]; !exists && len(j.records) >= j.maxRecords {
		return errors.New("local journal record limit reached")
	}
	prospective := make(map[string]T, len(j.records)+1)
	for existingKey, existingRecord := range j.records {
		prospective[existingKey] = existingRecord
	}
	prospective[key] = record
	if _, err := j.checkpointBody(prospective); err != nil {
		return err
	}
	if err := j.append(event[T]{Version: j.version, Op: "put", Record: &record}); err != nil {
		return err
	}
	j.records[key] = record
	return j.checkpoint()
}

func (j *Journal[T]) Delete(key string) error {
	if j == nil || strings.TrimSpace(key) != key || key == "" {
		return errors.New("local journal key is invalid")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.append(event[T]{Version: j.version, Op: "delete", Key: key}); err != nil {
		return err
	}
	delete(j.records, key)
	return j.checkpoint()
}

func (j *Journal[T]) Snapshot() []T {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *Journal[T]) snapshotLocked() []T {
	return snapshotRecords(j.records)
}

func snapshotRecords[T any](records map[string]T) []T {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, key := range keys {
		out = append(out, records[key])
	}
	return out
}

func (j *Journal[T]) append(entry event[T]) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if int64(len(body)) > j.maxBytes {
		return errors.New("local journal record exceeds size limit")
	}
	if info, statErr := os.Stat(j.journalPath); statErr == nil && info.Size()+int64(len(body)) > j.maxBytes {
		return errors.New("local journal size limit reached")
	}
	file, err := os.OpenFile(j.journalPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- fixed state path.
	if err != nil {
		return err
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

func (j *Journal[T]) checkpoint() (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("write local journal checkpoint %s: %w", j.checkpointPath, resultErr)
		}
	}()
	body, err := j.checkpointBody(j.records)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(j.dir, "."+filepath.Base(j.checkpointPath)+".*")
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
	directory, err := os.Open(j.dir) // #nosec G304 -- validated state directory.
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return j.resetJournal()
}

func (j *Journal[T]) checkpointBody(records map[string]T) ([]byte, error) {
	body, err := json.Marshal(checkpoint[T]{Version: j.version, Records: snapshotRecords(records)})
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > j.maxBytes {
		return nil, errors.New("local journal checkpoint exceeds size limit")
	}
	return body, nil
}

func (j *Journal[T]) resetJournal() (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("reset local journal %s: %w", j.journalPath, resultErr)
		}
	}()
	file, err := os.OpenFile(j.journalPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- fixed state path.
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readBounded(path string, maximum int64) (_ []byte, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("read local journal state %s: %w", path, resultErr)
		}
	}()
	file, err := os.Open(path) // #nosec G304 -- caller supplies fixed state path.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o600 || info.Size() > maximum {
		return nil, errors.New("local journal state has unsafe mode or size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("local journal state exceeds its bound")
	}
	return raw, nil
}

func strictJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("local journal state has trailing data")
	}
	return nil
}
