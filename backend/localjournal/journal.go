// PAIMOS — Your Professional & Personal AI Project OS
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
	records        map[string]T
}

func Open[T any](config Config[T]) (*Journal[T], error) {
	if strings.TrimSpace(config.Directory) != config.Directory || config.Directory == "" ||
		config.Prefix == "" || strings.ContainsAny(config.Prefix, "/\\\x00\r\n") ||
		config.Version < 1 || config.MaxBytes < 1024 || config.MaxRecords < 1 || config.Key == nil || config.Validate == nil {
		return nil, errors.New("local journal configuration is invalid")
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create local journal state: %w", err)
	}
	info, err := os.Lstat(config.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("local journal state directory has unsafe mode or type")
	}
	j := &Journal[T]{dir: config.Directory, journalPath: filepath.Join(config.Directory, config.Prefix+".journal"),
		checkpointPath: filepath.Join(config.Directory, config.Prefix+".checkpoint.json"), version: config.Version,
		maxBytes: config.MaxBytes, maxRecords: config.MaxRecords, key: config.Key, validate: config.Validate,
		records: map[string]T{}}
	if err := j.loadCheckpoint(); err != nil {
		return nil, err
	}
	if err := j.replay(); err != nil {
		return nil, err
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
	var state checkpoint[T]
	if strictJSON(raw, &state) != nil || state.Version != j.version || len(state.Records) > j.maxRecords {
		return errors.New("local journal checkpoint is corrupt or unsupported")
	}
	for _, record := range state.Records {
		key, keyErr := j.key(record)
		if keyErr != nil || j.validate(record) != nil {
			return errors.New("local journal checkpoint is corrupt or unsupported")
		}
		if _, exists := j.records[key]; exists {
			return errors.New("local journal checkpoint contains duplicate records")
		}
		j.records[key] = record
	}
	return nil
}

func (j *Journal[T]) replay() error {
	raw, err := readBounded(j.journalPath, j.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	repairTail := len(raw) > 0 && raw[len(raw)-1] != '\n'
	if repairTail {
		if boundary := bytes.LastIndexByte(raw, '\n'); boundary >= 0 {
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
		var entry event[T]
		if strictJSON(scanner.Bytes(), &entry) != nil || entry.Version != j.version {
			return errors.New("local journal is corrupt or unsupported")
		}
		switch entry.Op {
		case "put":
			if entry.Record == nil || entry.Key != "" || j.validate(*entry.Record) != nil {
				return errors.New("local journal is corrupt or unsupported")
			}
			key, keyErr := j.key(*entry.Record)
			if keyErr != nil {
				return errors.New("local journal is corrupt or unsupported")
			}
			j.records[key] = *entry.Record
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
	if replayed || repairTail {
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

func (j *Journal[T]) checkpoint() error {
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

func (j *Journal[T]) resetJournal() error {
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

func readBounded(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- caller supplies fixed state path.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() > maximum {
		return nil, errors.New("local journal state has unsafe mode or size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
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
