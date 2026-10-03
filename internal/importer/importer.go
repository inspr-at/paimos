// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Report describes one snapshot. All fetched work fields are mapped or retained.
type Report struct {
	Counts         map[string]int `json:"counts"`
	Skipped        []SkippedItem  `json:"skipped"`
	UnmappedFields []string       `json:"unmapped_fields"`
	Created        int            `json:"created"`
	Updated        int            `json:"updated"`
	// Writes counts projection rows inserted or updated while applying classic
	// relations (node links, parents, journey release membership). A replay
	// that finds those rows already applied returns zero.
	Writes int `json:"writes"`
	// Conflicts are source revisions withheld because Aeon changed the same
	// imported node after its last import. No conflicting row is overwritten.
	Conflicts []ImportConflict `json:"conflicts"`
}

type ImportConflict struct {
	ClassicID     int64  `json:"classic_id"`
	Key           string `json:"key"`
	Reason        string `json:"reason"`
	CurrentKind   string `json:"current_kind,omitempty"`
	RequestedKind string `json:"requested_kind,omitempty"`
}

type Writer interface {
	Write(context.Context, Snapshot, string) (Report, error)
}

type Importer struct {
	Source Source
	Writer Writer
}

func (i Importer) Run(ctx context.Context, tenant, project string, dryRun bool) (Report, error) {
	if i.Source == nil {
		return Report{}, fmt.Errorf("source is required")
	}
	snap, err := i.Source.Read(ctx, project)
	if err != nil {
		return Report{}, err
	}
	if snap.SourceID == "" || snap.SourceID != i.Source.InstanceID() {
		return Report{}, fmt.Errorf("source instance identity mismatch")
	}
	report := Analyze(snap)
	if dryRun {
		return report, nil
	}
	if i.Writer == nil {
		return report, fmt.Errorf("writer is required")
	}
	return i.Writer.Write(ctx, snap, tenant)
}

// RunDelta compares source provenance against the last imported revision.
// Source.Read still takes a complete GET-only snapshot: classic timestamps are
// not reliable for comments, relations, or deleted items. The writer applies
// only changed records and reports Aeon-side edits as conflicts. Replays are
// idempotent, including after an interrupted run.
func (i Importer) RunDelta(ctx context.Context, tenant, project string) (Report, error) {
	report, _, err := i.RunDeltaSnapshot(ctx, tenant, project)
	return report, err
}

// RunDeltaSnapshot returns the fetched snapshot for an attachment-byte delta
// pass without taking a second, potentially different classic snapshot.
func (i Importer) RunDeltaSnapshot(ctx context.Context, tenant, project string) (Report, Snapshot, error) {
	if i.Source == nil || i.Writer == nil {
		return Report{}, Snapshot{}, errors.New("source and writer are required")
	}
	snap, err := i.Source.Read(ctx, project)
	if err != nil {
		return Report{}, Snapshot{}, err
	}
	if snap.SourceID == "" || snap.SourceID != i.Source.InstanceID() {
		return Report{}, Snapshot{}, errors.New("source instance identity mismatch")
	}
	report, err := i.Writer.Write(ctx, snap, tenant)
	return report, snap, err
}

func Analyze(s Snapshot) Report {
	r := Report{Counts: map[string]int{"users": len(s.Users), "projects": len(s.Projects), "skipped": len(s.Skipped), "skipped_projects": 0, "skipped_issues": 0}, Skipped: append([]SkippedItem{}, s.Skipped...), UnmappedFields: []string{}}
	for _, item := range s.Skipped {
		r.Counts["skipped_"+item.Type+"s"]++
	}
	for _, p := range s.Projects {
		for _, issue := range p.Issues {
			typ := stringField(issue, "type")
			if typ == "" {
				typ = "unknown"
			}
			r.Counts[typ]++
		}
	}
	for _, issue := range s.Orphans {
		typ := stringField(issue, "type")
		if typ == "" {
			typ = "unknown"
		}
		r.Counts[typ]++
	}
	relations := map[string]bool{}
	for _, d := range s.Details {
		r.Counts["comments"] += len(d.Comments)
		r.Counts["history"] += len(d.History)
		r.Counts["attachments_metadata"] += len(d.Attachments)
		for _, rel := range d.Relations {
			sourceID, _ := intField(rel, "source_id")
			targetID, _ := intField(rel, "target_id")
			ref := fmt.Sprintf("%s:%d:%d", stringField(rel, "type"), sourceID, targetID)
			relations[ref] = true
		}
	}
	r.Counts["relations"] = len(relations)
	return r
}

func jsonValue(v any) ([]byte, error) { return json.Marshal(v) }
func canonicalType(t string) string   { return strings.ReplaceAll(t, "-", "_") }

func decodeExactJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}
