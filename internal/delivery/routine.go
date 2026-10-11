// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// RoutinePipelineFacts is a qualified read, never worker-provided CI evidence.
// ArtifactSHA identifies the exact tested source commit (queue commit when
// queued). OPS installs an exact-artifact reader; absence remains unavailable.
type RoutinePipelineFacts struct {
	Facts       ShipFacts
	ArtifactSHA string
	ObservedAt  time.Time
	MergeSHA    string
}
type RoutineArtifactReader func(context.Context, string, string) (string, error)

func (m *Module) WithRoutineArtifacts(read RoutineArtifactReader) *Module {
	m.routineArtifacts = read
	return m
}
func (m *Module) RoutineSnapshot(ctx context.Context, tid, repo, branch, head string) (RoutinePipelineFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := RoutinePipelineFacts{}
	reader, ok := m.github.(shippingReader)
	if !ok || m.config.TenantID != tid || m.config.Repository != repo {
		return out, errRead
	}
	f, err := reader.Shipping(ctx, branch, head)
	if err != nil {
		return out, err
	}
	out.Facts = f
	if f.Merged && f.PR != nil {
		r, ok := m.github.(MergeReader)
		if !ok {
			return out, errRead
		}
		fact, err := r.MergedPull(ctx, *f.PR)
		if err != nil || fact == nil {
			return out, errRead
		}
		if fact.Head != head {
			return out, errRead
		}
		out.MergeSHA = fact.SHA
	} else {
		if m.routineArtifacts == nil {
			return out, errRead
		}
		candidate := head
		if f.Queued {
			candidate = f.QueueHead
		}
		out.ArtifactSHA, err = m.routineArtifacts(ctx, repo, candidate)
		if err != nil {
			return out, err
		}
	}
	out.ObservedAt = m.now()
	return out, nil
}

// RoutineReadyTx reuses the shipping policy and its required check names. A
// queue commit refreshes the proof: the PR's green rollup cannot stand in for
// the synthetic merge commit. Caller holds the standard final-write fence.
func RoutineReadyTx(ctx context.Context, tx pgx.Tx, project, repo, head string, f RoutinePipelineFacts) (Settings, bool, error) {
	if len(f.Facts.Checks) > 1000 || len(f.Facts.Runs) > 100 {
		return Settings{}, false, errRead
	}
	jobs := 0
	for _, r := range f.Facts.Runs {
		jobs += len(r.Jobs)
		if len(r.Jobs) > 1000 || jobs > 4000 {
			return Settings{}, false, errRead
		}
	}
	s, err := settingsTx(ctx, tx, &project)
	if err != nil {
		return s, false, err
	}
	if f.Facts.PushedHead != head || f.Facts.PullHead != head || !f.Facts.Open || f.Facts.Draft || f.Facts.Conflict || !f.Facts.MergeableKnown || f.Facts.Behind {
		return s, false, nil
	}
	candidate := head
	if f.Facts.Queued {
		candidate = f.Facts.QueueHead
	}
	if candidate == "" || f.ArtifactSHA != candidate {
		return s, false, nil
	}
	checks := f.Facts.Checks
	if f.Facts.Queued {
		checks = nil
		for _, r := range f.Facts.Runs {
			if r.Head == candidate && r.Event == "merge_group" && s.RequiredWorkflow != nil && r.Workflow == *s.RequiredWorkflow {
				if checks != nil || shipRunAction(r) != "none" {
					return s, false, nil
				}
				checks = r.Jobs
			}
		}
	} else {
		for _, r := range f.Facts.Runs {
			if r.Head == head && s.RequiredWorkflow != nil && r.Workflow == *s.RequiredWorkflow && (r.Event == "pull_request" || r.Event == "push") && shipRunAction(r) != "none" {
				return s, false, nil
			}
		}
	}
	var quarantine bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_queue_failures WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 AND kind='required_failure')`, repo, f.Facts.PR, head).Scan(&quarantine)
	if err != nil {
		return s, false, err
	}
	return s, !quarantine && allSuccess(Observation{Checks: checks, Settings: s}), nil
}

// RoutineMergeResultTx observes AEON-1008's receipt/refusal. It never writes
// node status or reproduces the responsible-person fallback/Done guards.
func RoutineMergeResultTx(ctx context.Context, tx pgx.Tx, ticket, repo, head, merge string, pr int64) (bool, string, error) {
	var done bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1 AND type='delivery.merge_done' AND metadata->>'repository'=$2 AND metadata->>'head_sha'=$3 AND metadata->>'merge_sha'=$4 AND metadata->>'pull_request'=$5)`, ticket, repo, head, merge, strconv.FormatInt(pr, 10)).Scan(&done)
	if err != nil || done {
		return done, "", err
	}
	return false, "merge_done_receipt_pending_or_refused", nil
}
