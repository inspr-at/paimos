// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const DaemonHeader = "X-Aeon-Daemon-ID"
const GenerationHeader = "X-Aeon-Daemon-Generation"

// Telemetry contains only bounded identifiers and counters, never vendor text.
type Telemetry struct {
	Sequence   int64       `json:"sequence"`
	Kind       string      `json:"kind"`
	Status     string      `json:"status,omitempty"`
	Input      int64       `json:"input_tokens_delta"`
	Output     int64       `json:"output_tokens_delta"`
	Cost       int64       `json:"cost_micros_delta"`
	Tools      int32       `json:"tool_count_delta"`
	Turns      int32       `json:"turn_count_delta"`
	Model      string      `json:"effective_model,omitempty"`
	Evidence   string      `json:"model_evidence,omitempty"`
	ErrorCode  string      `json:"error_code,omitempty"`
	GitCommits []GitCommit `json:"git_commits,omitempty"`
}

// GitCommit is one commit this run introduced after its launch revision.
type GitCommit struct {
	SHA             string `json:"sha"`
	Subject         string `json:"subject"`
	Parents         int    `json:"parents,omitempty"`
	OnDefaultBranch bool   `json:"on_default_branch,omitempty"`
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == ':' || c == '/') {
			return false
		}
	}
	return true
}
func terminal(s string) bool {
	switch s {
	case "completed", "failed", "cancelled", "ownership_lost":
		return true
	}
	return false
}
func (t Telemetry) validate() error {
	if t.Sequence < 1 || t.Input < 0 || t.Output < 0 || t.Cost < 0 || t.Tools < 0 || t.Turns < 0 {
		return workorders.Fail(400, "positive sequence and nonnegative counters required")
	}
	switch t.Kind {
	case "started", "heartbeat", "turn", "tool", "usage", "status", "finished":
	default:
		return workorders.Fail(400, "invalid telemetry kind")
	}
	switch t.Status {
	case "", "starting", "running", "waiting", "completed", "failed", "cancelled", "ownership_lost":
	default:
		return workorders.Fail(400, "invalid run status")
	}
	if t.Kind == "status" && t.Status == "" {
		return workorders.Fail(400, "status report requires status")
	}
	if t.Kind == "started" && t.Status != "" && t.Status != "running" {
		return workorders.Fail(400, "started report requires running status")
	}
	if t.Kind == "finished" && t.Status != "" && !terminal(t.Status) {
		return workorders.Fail(400, "finished report requires terminal status")
	}
	if t.Model != "" && !identifier(t.Model) {
		return workorders.Fail(400, "effective_model must be a bounded model identifier")
	}
	if t.Evidence != "" && t.Evidence != "unverified" && t.Evidence != "vendor_reported" {
		return workorders.Fail(400, "invalid model evidence")
	}
	if t.Evidence == "vendor_reported" && t.Model == "" {
		return workorders.Fail(400, "vendor evidence requires effective_model")
	}
	switch t.ErrorCode {
	case "", "event_stream_bound", "app_server_protocol", "child_exit_failed", "turn_failed", "child_stop_failed", "ownership_lost", "reporter_unavailable", "workspace_conflict", "decision_refused":
	default:
		return workorders.Fail(400, "invalid error code")
	}
	if len(t.GitCommits) > 20 {
		return workorders.Fail(400, "too many git commits")
	}
	for _, c := range t.GitCommits {
		if !commitSHA(c.SHA) || !commitSubject(c.Subject) || c.Parents < 0 || c.Parents > 64 {
			return workorders.Fail(400, "invalid git commit")
		}
	}
	return nil
}

func sameTelemetry(a, b Telemetry) bool {
	if a.Sequence != b.Sequence || a.Kind != b.Kind || a.Status != b.Status || a.Input != b.Input || a.Output != b.Output || a.Cost != b.Cost || a.Tools != b.Tools || a.Turns != b.Turns || a.Model != b.Model || a.Evidence != b.Evidence || a.ErrorCode != b.ErrorCode || len(a.GitCommits) != len(b.GitCommits) {
		return false
	}
	for i := range a.GitCommits {
		if a.GitCommits[i] != b.GitCommits[i] {
			return false
		}
	}
	return true
}
func (m *module) telemetry(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var t Telemetry
	if err := workorders.Decode(r, &t); err != nil {
		return nil, err
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	daemon, generation := r.Header.Get(DaemonHeader), r.Header.Get(GenerationHeader)
	if !identifier(daemon) || !identifier(generation) {
		return nil, workorders.Fail(400, "daemon fencing headers required")
	}
	ctx := r.Context()
	v, o, err := lockRun(ctx, tx, r.PathValue("runId"))
	if err != nil {
		return nil, err
	}
	if v.DaemonID == nil || v.Generation == nil || *v.DaemonID != daemon || *v.Generation != generation {
		return nil, workorders.Fail(409, "daemon generation conflict")
	}
	if err = claimPermission(ctx, tx, p, v); err != nil {
		return nil, err
	}
	if v.AccountID != nil {
		if err = agentpairing.AccountFence(ctx, tx, *v.AccountID, true); err != nil {
			return nil, err
		}
	}
	var owner, accountDaemon string
	var accountGeneration *string
	// Serialize against account probes and settlement. A generation change must
	// not race between ownership verification and committing a running report.
	if err = tx.QueryRow(ctx, `SELECT registered_by_principal_id::text,daemon_id,last_daemon_generation
	 FROM agent_accounts WHERE id=$1 FOR UPDATE`, v.AccountID).Scan(&owner, &accountDaemon, &accountGeneration); err != nil {
		return nil, err
	}
	if owner != p.ID || accountDaemon != daemon || accountGeneration == nil || *accountGeneration != generation {
		return nil, workorders.Fail(403, "current daemon account owner required")
	}
	// 0201 stores only numeric telemetry. The immutable R1 event preserves the
	// complete canonical report for replay of status/model/error fields as well.
	canonical, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var previous json.RawMessage
	err = tx.QueryRow(ctx, `SELECT e.after->'report' FROM events e WHERE e.node_id=$1 AND e.type='run.telemetry'
	 AND e.after->>'run_id'=$2 AND e.after->>'sequence'=$3 ORDER BY e.id DESC LIMIT 1`, o.NodeID, v.ID, strconv.FormatInt(t.Sequence, 10)).Scan(&previous)
	if err == nil {
		var old Telemetry
		if err = json.Unmarshal(previous, &old); err != nil {
			return nil, err
		}
		if !sameTelemetry(old, t) {
			return nil, workorders.Fail(409, "divergent telemetry replay")
		}
		return v, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var last int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0) FROM run_telemetry WHERE run_id=$1`, v.ID).Scan(&last); err != nil {
		return nil, err
	}
	if t.Sequence <= last {
		return nil, workorders.Fail(409, "telemetry sequence is not monotonic")
	}
	if v.Status == "queued" || terminal(v.Status) {
		return nil, workorders.Fail(409, "run is not live")
	}
	status := v.Status
	if t.Status != "" {
		status = t.Status
	} else if t.Kind == "started" {
		status = "running"
	} else if t.Kind == "finished" {
		status = "completed"
	}
	if status == "starting" && v.Status != "starting" {
		return nil, workorders.Fail(409, "run cannot return to starting")
	}
	if t.Input > math.MaxInt64-v.InputTokens || t.Output > math.MaxInt64-v.OutputTokens || t.Cost > math.MaxInt64-v.Cost {
		return nil, workorders.Fail(400, "usage counter overflow")
	}
	before := v
	model, evidence := v.EffectiveModel, v.ModelEvidence
	if t.Model != "" {
		model = &t.Model
		evidence = "unverified"
	}
	if t.Evidence != "" {
		evidence = t.Evidence
	}
	if _, err = tx.Exec(ctx, `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,input_tokens_delta,output_tokens_delta,cost_micros_delta,tool_count_delta,turn_count_delta,error_code,status)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,''),nullif($11,''))`, p.TenantID, v.ID, t.Sequence, t.Kind, t.Input, t.Output, t.Cost, t.Tools, t.Turns, t.ErrorCode, t.Status); err != nil {
		return nil, err
	}
	v, err = scan(tx.QueryRow(ctx, `UPDATE agent_runs SET status=$2,input_tokens=input_tokens+$3,output_tokens=output_tokens+$4,cost_micros=cost_micros+$5,
	 effective_model=$6,model_evidence=$7,ended_at=CASE WHEN $8 THEN clock_timestamp() ELSE ended_at END WHERE id=$1 RETURNING `+columns, v.ID, status, t.Input, t.Output, t.Cost, model, evidence, terminal(status)))
	if err != nil {
		return nil, err
	}
	if err = applyRunUsage(ctx, tx, &v, t); err != nil {
		return nil, err
	}
	if m.usage != nil {
		if err = m.usage(ctx, tx, p, v, t); err != nil {
			return nil, err
		}
	}
	if terminal(v.Status) && v.AccountID != nil {
		if err = agentpairing.FinishDrain(ctx, tx, *v.AccountID); err != nil {
			return nil, err
		}
	}
	if err = workorders.BlockBudget(ctx, tx, p, o); err != nil {
		return nil, err
	}
	after := struct {
		RunID    string          `json:"run_id"`
		Sequence int64           `json:"sequence"`
		Report   json.RawMessage `json:"report"`
		Run      Run             `json:"run"`
	}{v.ID, t.Sequence, canonical, v}
	return v, workorders.Record(ctx, tx, p, o.NodeID, "run.telemetry", before, after)
}
