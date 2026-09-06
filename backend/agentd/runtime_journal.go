// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// ValidateRuntimeJournal validates copied, bounded bytes without opening or
// replaying the live journal. Doctor must never checkpoint or repair on read.
func ValidateRuntimeJournal(checkpoint, journal []byte) error {
	if len(checkpoint) > agentdJournalMax || len(journal) > agentdJournalMax {
		return errors.New("runtime journal exceeds bound")
	}
	strict := func(raw []byte, v any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(v) != nil {
			return errors.New("runtime journal corrupt")
		}
		if d.Decode(&struct{}{}) != io.EOF {
			return errors.New("runtime journal corrupt")
		}
		return nil
	}
	seen := map[string]bool{}
	if len(checkpoint) > 0 {
		var c struct {
			Version int              `json:"version"`
			Records []registryRecord `json:"records"`
		}
		if strict(checkpoint, &c) != nil || c.Version != agentdJournalVersion || len(c.Records) > 4096 {
			return errors.New("runtime checkpoint corrupt")
		}
		for _, r := range c.Records {
			if validateRegistryRecord(r) != nil || seen[r.Session.ID] {
				return errors.New("runtime checkpoint corrupt")
			}
			seen[r.Session.ID] = true
		}
	}
	if len(journal) > 0 && journal[len(journal)-1] != '\n' {
		return errors.New("runtime journal incomplete")
	}
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var e struct {
			Version int             `json:"version"`
			Op      string          `json:"op"`
			Record  *registryRecord `json:"record,omitempty"`
			Key     string          `json:"key,omitempty"`
		}
		if strict(scanner.Bytes(), &e) != nil || e.Version != agentdJournalVersion {
			return errors.New("runtime journal corrupt")
		}
		switch e.Op {
		case "put":
			if e.Record == nil || e.Key != "" || validateRegistryRecord(*e.Record) != nil {
				return errors.New("runtime journal corrupt")
			}
			seen[e.Record.Session.ID] = true
		case "delete":
			if e.Record != nil || e.Key == "" {
				return errors.New("runtime journal corrupt")
			}
			delete(seen, e.Key)
		default:
			return errors.New("runtime journal corrupt")
		}
		if len(seen) > 4096 {
			return errors.New("runtime journal exceeds bound")
		}
	}
	if scanner.Err() != nil {
		return errors.New("runtime journal corrupt")
	}
	return nil
}
