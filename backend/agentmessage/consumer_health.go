// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package agentmessage

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"time"
)

type RuntimeHealthInput struct {
	RuntimeID         string `json:"runtime_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	Sequence          int64  `json:"sequence"`
	Layer             string `json:"layer"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	FailureCount      int    `json:"failure_count"`
}
type RuntimeHealthResult struct {
	SchemaVersion int    `json:"schema_version"`
	State         string `json:"state"`
	Sequence      int64  `json:"sequence"`
	Published     bool   `json:"published"`
}

func (s *Service) PublishRuntimeHealth(ctx context.Context, c ConsumerCredentials, project int64, in RuntimeHealthInput) (RuntimeHealthResult, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return RuntimeHealthResult{}, ErrConsumerStorage
	}
	defer tx.Rollback()
	if _, _, e = consumerRuntime(ctx, tx, c, project, in.RuntimeID, in.RuntimeGeneration, true); e != nil {
		return RuntimeHealthResult{}, e
	}
	if in.Sequence <= 0 || in.FailureCount < 0 || in.FailureCount > 10 {
		return RuntimeHealthResult{}, ErrConsumerInvalid
	}
	switch in.Layer {
	case "reporter", "primary", "fallback", "attention":
	default:
		return RuntimeHealthResult{}, ErrConsumerInvalid
	}
	if in.State == "healthy" {
		if in.Reason != "recovered" || in.FailureCount != 0 {
			return RuntimeHealthResult{}, ErrConsumerInvalid
		}
	} else if in.State == "unhealthy" {
		if in.FailureCount == 0 {
			return RuntimeHealthResult{}, ErrConsumerInvalid
		}
		switch in.Reason {
		case "consumer_crash_loop", "consumer_authority_unavailable", "consumer_transport_failed":
		default:
			return RuntimeHealthResult{}, ErrConsumerInvalid
		}
	} else {
		return RuntimeHealthResult{}, ErrConsumerInvalid
	}
	var id, state, reason, publishedAt string
	var published bool
	var sequence, episode int64
	var count int
	e = tx.QueryRowContext(ctx, `SELECT id,state,reason,sequence,episode,failure_count,published,published_at FROM agent_runtime_health WHERE runtime_id=? AND layer=?`, in.RuntimeID, in.Layer).Scan(&id, &state, &reason, &sequence, &episode, &count, &published, &publishedAt)
	if e != nil && e != sql.ErrNoRows {
		return RuntimeHealthResult{}, ErrConsumerStorage
	}
	if e == nil && in.Sequence <= sequence {
		if in.Sequence != sequence || state != in.State || reason != in.Reason || count != in.FailureCount {
			return RuntimeHealthResult{}, ErrConsumerConflict
		}
		if tx.Commit() != nil {
			return RuntimeHealthResult{}, ErrConsumerStorage
		}
		return RuntimeHealthResult{SchemaVersion: 1, State: state, Sequence: sequence, Published: published}, nil
	}
	if id == "" {
		id = uuid.NewString()
	}
	// A recovered episode can never reactivate its immutable attention item.
	// New episodes get a new source sequence even when publication is throttled.
	if in.State == "unhealthy" && state != "unhealthy" {
		if episode >= 10000 {
			return RuntimeHealthResult{}, ErrConsumerConflict
		}
		episode++
	}
	var existing int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attention_items WHERE source_kind='runtime_health' AND source_id=? AND source_sequence=?`, id, episode).Scan(&existing) != nil {
		return RuntimeHealthResult{}, ErrConsumerStorage
	}
	publish := in.State == "unhealthy" && existing == 0
	if publish && publishedAt != "" {
		last, err := time.Parse(time.RFC3339Nano, publishedAt)
		if err != nil {
			return RuntimeHealthResult{}, ErrConsumerStorage
		}
		if time.Since(last) < time.Minute {
			publish = false
		}
	}
	if publish {
		// No configured receiver is a supported state; the typed health row
		// still reaches the browser and a later report can publish attention.
		_, err := resolveAttentionReceiver(ctx, tx)
		if err == sql.ErrNoRows {
			publish = false
		} else if err != nil {
			return RuntimeHealthResult{}, ErrConsumerStorage
		} else {
			publishedAt = consumerStamp(time.Now())
		}
	}
	now := consumerStamp(time.Now())
	_, e = tx.ExecContext(ctx, `INSERT INTO agent_runtime_health(id,runtime_id,project_id,layer,sequence,state,reason,failure_count,episode,published,published_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(runtime_id,layer) DO UPDATE SET sequence=excluded.sequence,state=excluded.state,reason=excluded.reason,failure_count=excluded.failure_count,episode=excluded.episode,published=excluded.published,published_at=excluded.published_at,updated_at=excluded.updated_at`, id, in.RuntimeID, project, in.Layer, in.Sequence, in.State, in.Reason, in.FailureCount, episode, publish, publishedAt, now)
	if e != nil {
		return RuntimeHealthResult{}, ErrConsumerStorage
	}
	if publish {
		receiver, err := resolveAttentionReceiver(ctx, tx)
		if err == sql.ErrNoRows {
			return RuntimeHealthResult{}, ErrConsumerUnavailable
		}
		if err != nil {
			return RuntimeHealthResult{}, ErrConsumerStorage
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at) VALUES(?,?,?,?,'runtime_health',?,?,'runtime_unhealthy',?,?)`, receiver.projectID, receiver.agentID, receiver.address, project, id, episode, in.Reason, now)
		if e != nil {
			return RuntimeHealthResult{}, ErrConsumerStorage
		}
	}
	if tx.Commit() != nil {
		return RuntimeHealthResult{}, ErrConsumerStorage
	}
	return RuntimeHealthResult{SchemaVersion: 1, State: in.State, Sequence: in.Sequence, Published: publish}, nil
}
