// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

var deliveryCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Delivery writes require the paired run content capability as well as the
// exact harness lease. A general worker key cannot impersonate agentd.
func deliveryWorker(r *http.Request, tx pgx.Tx, p tenant.Principal, run, generation string) (Session, error) {
	ctx := r.Context()
	if p.RunID != run || p.RunGeneration != generation || p.TransportComputerID == "" || p.RunComputerID != p.TransportComputerID {
		return Session{}, workorders.Fail(403, "rules delivery run binding rejected")
	}
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return Session{}, err
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		return Session{}, err
	}
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return s, err
	}
	if s.Management != "managed" || s.RunID == nil || *s.RunID != run || s.WorkOrderID == nil || s.Harness != "claude" {
		return s, workorders.Fail(403, "rules delivery run binding rejected")
	}
	var live bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r
 JOIN agent_pairing_computers c ON c.tenant_id=r.tenant_id AND c.daemon_id=r.daemon_id AND c.principal_id=r.agent_principal_id
 JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
 WHERE r.tenant_id=$1 AND r.id=$2 AND r.agent_principal_id=$3 AND r.daemon_generation=$4
 AND r.status IN ('starting','running') AND r.work_order_id=$5
 AND c.id=$6 AND c.state='connected' AND c.key_id=$7 AND c.content_daemon_generation=$4
 AND a.last_daemon_generation=$4 AND a.daemon_id=r.daemon_id AND a.registered_by_principal_id=$3)`,
		p.TenantID, run, p.ID, generation, *s.WorkOrderID, p.TransportComputerID, p.KeyID).Scan(&live)
	if err != nil {
		return s, err
	}
	if !live {
		return s, workorders.Fail(403, "rules delivery computer or generation rejected")
	}
	return s, nil
}

func (m *Module) prepareRulesDelivery(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in rules.RulesDeliveryPrepare
	r.Body = httpapi.RequestBodyReader(nil, r, 1<<20)
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RunID) || len(in.Generation) < 1 || len(in.Generation) > 128 || !provenanceSHA256.MatchString(in.BodySHA256) || len(in.Repository.Body) > doctrine.ManagedFileMaxBytes || !utf8.ValidString(in.Repository.Body) || (in.Repository.Commit == "" && in.Repository.Body != "") || (in.Repository.Commit != "" && !deliveryCommit.MatchString(in.Repository.Commit)) {
		return nil, workorders.Fail(400, "invalid rules delivery manifest")
	}
	s, err := deliveryWorker(r, tx, p, in.RunID, in.Generation)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	requestDigest := sha256.Sum256(raw)
	var oldRaw, oldDigest []byte
	var expires, now time.Time
	err = tx.QueryRow(r.Context(), `SELECT manifest,request_digest,expires_at,clock_timestamp() FROM harness_rules_deliveries WHERE session_id=$1`, s.ID).Scan(&oldRaw, &oldDigest, &expires, &now)
	if err == nil {
		if string(oldDigest) != string(requestDigest[:]) {
			return nil, workorders.Fail(409, "rules delivery manifest already bound")
		}
		if !now.Before(expires) {
			return nil, workorders.Fail(409, "rules delivery manifest expired")
		}
		var out rules.RulesDeliveryManifest
		err = json.Unmarshal(oldRaw, &out)
		return out, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Independently render the authorized pin and all database layers. The repo
	// delta is agentd's run-commit attestation; the server does not fetch a checkout.
	merged, err := rules.ForManagedFiles(r.Context(), tx, p, s.ProjectID, *s.WorkOrderID, "claude-code", doctrine.ManagedPackMaxBytes)
	if err != nil {
		return nil, err
	}
	body, err := rules.AssembleManagedPack(merged, in.Repository)
	if err != nil {
		return nil, workorders.Fail(422, "rules delivery pack rejected")
	}
	sum := sha256.Sum256([]byte(body))
	if hex.EncodeToString(sum[:]) != in.BodySHA256 {
		return nil, workorders.Fail(409, "rules delivery served manifest hash mismatch")
	}
	out := rules.RulesDeliveryManifest{RunID: in.RunID, SessionID: s.ID, ComputerID: p.TransportComputerID, Generation: in.Generation, BodySHA256: in.BodySHA256, ByteSize: len(body), Harness: s.Harness, RepositoryCommit: in.Repository.Commit, Pins: []rules.DeliveryPin{}}
	for _, f := range merged.PinnedFiles {
		out.Pins = append(out.Pins, rules.DeliveryPin{Repository: f.Repository, Commit: f.Commit, Path: f.Path, BlobSHA: f.BlobSHA})
	}
	if err = tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text,clock_timestamp()`).Scan(&out.ID, &out.IssuedAt); err != nil {
		return nil, err
	}
	out.ExpiresAt = out.IssuedAt.Add(5 * time.Minute)
	manifest, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO harness_rules_deliveries(tenant_id,id,session_id,run_id,computer_id,daemon_generation,manifest,request_digest,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.TenantID, out.ID, s.ID, in.RunID, p.TransportComputerID, in.Generation, manifest, requestDigest[:], out.IssuedAt, out.ExpiresAt)
	return out, err
}

func (m *Module) acknowledgeRulesDelivery(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in rules.RulesDeliveryAck
	r.Body = httpapi.RequestBodyReader(nil, r, 2048)
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.ManifestID) || !workorders.UUID(in.RunID) || len(in.Generation) < 1 || len(in.Generation) > 128 || !provenanceSHA256.MatchString(in.BodySHA256) || !provenanceSHA256.MatchString(in.BridgeSHA256) || in.AcknowledgedAt.IsZero() {
		return nil, workorders.Fail(400, "invalid rules delivery acknowledgement")
	}
	s, err := deliveryWorker(r, tx, p, in.RunID, in.Generation)
	if err != nil {
		return nil, err
	}
	var manifest, ack []byte
	var expires, now time.Time
	var recorded *time.Time
	err = tx.QueryRow(r.Context(), `SELECT manifest,acknowledgement,expires_at,clock_timestamp(),recorded_at FROM harness_rules_deliveries WHERE id=$1 AND session_id=$2 AND run_id=$3 AND computer_id=$4 AND daemon_generation=$5`, in.ManifestID, s.ID, in.RunID, p.TransportComputerID, in.Generation).Scan(&manifest, &ack, &expires, &now, &recorded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(403, "rules delivery manifest binding rejected")
	}
	if err != nil {
		return nil, err
	}
	var served rules.RulesDeliveryManifest
	if err = json.Unmarshal(manifest, &served); err != nil {
		return nil, err
	}
	if !now.Before(expires) {
		return nil, workorders.Fail(409, "rules delivery manifest expired")
	}
	if in.BodySHA256 != served.BodySHA256 || in.BridgeSHA256 != served.BodySHA256 {
		return nil, workorders.Fail(409, "rules delivery served manifest hash mismatch")
	}
	if in.AcknowledgedAt.Before(served.IssuedAt.Add(-30*time.Second)) || in.AcknowledgedAt.After(now.Add(30*time.Second)) {
		return nil, workorders.Fail(409, "rules delivery acknowledgement time rejected")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(ack) > 0 {
		var old rules.RulesDeliveryAck
		if err = json.Unmarshal(ack, &old); err != nil {
			return nil, err
		}
		if old != in {
			return nil, workorders.Fail(409, "rules delivery acknowledgement already bound")
		}
		return rules.RulesDeliveryEvidence{Manifest: served, Evidence: rules.RulesDeliveryAcknowledged, AcknowledgedAt: old.AcknowledgedAt, RecordedAt: *recorded}, nil
	}
	if err = tx.QueryRow(r.Context(), `UPDATE harness_rules_deliveries SET acknowledgement=$2,acknowledged_at=$3,recorded_at=clock_timestamp() WHERE id=$1 AND acknowledgement IS NULL RETURNING recorded_at`, in.ManifestID, raw, in.AcknowledgedAt).Scan(&recorded); err != nil {
		return nil, err
	}
	out := rules.RulesDeliveryEvidence{Manifest: served, Evidence: rules.RulesDeliveryAcknowledged, AcknowledgedAt: in.AcknowledgedAt, RecordedAt: *recorded}
	// No locks are acquired after this event append.
	if err = record(r.Context(), tx, p, s, "rules_delivery_acknowledged", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) readRulesDelivery(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	if len(r.URL.Query()) != 0 {
		return nil, workorders.Fail(400, "invalid rules delivery query")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	var raw []byte
	var out rules.RulesDeliveryEvidence
	err = tx.QueryRow(r.Context(), `SELECT manifest,acknowledged_at,recorded_at FROM harness_rules_deliveries WHERE session_id=$1 AND acknowledgement IS NOT NULL`, s.ID).Scan(&raw, &out.AcknowledgedAt, &out.RecordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"evidence": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &out.Manifest); err != nil {
		return nil, err
	}
	out.Evidence = rules.RulesDeliveryAcknowledged
	return out, nil
}
