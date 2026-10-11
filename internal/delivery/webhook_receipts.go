// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

const webhookAttemptTimeout = 4 * time.Minute

var webhookSteps = []string{"projection", "fanout", "check_audit", "merge_audit", "metrics", "flow", "completion"}

var errWebhookBusy = webhookClass("delivery_in_progress")

type webhookReceipt struct {
	id, event, action, repository, installation, hash string
	raw                                               []byte
	checkpoints                                       map[string]webhookOutcome
	runs                                              []metricRun
	attempts                                          int64
	next                                              time.Time
	finished                                          *time.Time
}

// receiveWebhook is the only HTTP acceptance boundary. Signatures and headers
// never enter storage; the bounded payload is retained only after authentication,
// unique-key validation and binding to this installation/repository/tenant.
func (m *Module) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := httpapi.ReadRequestBody(w, r, 2<<20, 5*time.Second)
	if err != nil || len(raw) > 2<<20 {
		class := webhookClass("body_too_large")
		var tooLarge *http.MaxBytesError
		if err != nil && !errors.As(err, &tooLarge) {
			class = "body_read"
		}
		closeAfterFailedRead(w)
		m.rejectWebhook(w, r, "unknown", "parse", 413, class)
		return
	}
	app := &crossreview.GitHubApp{Config: m.config}
	if m.github == nil || len(m.secret) < 32 || !app.Configured(m.config.TenantID, m.config.Repository) {
		m.rejectWebhook(w, r, "unknown", "audit", 404, webhookClass("not_configured"))
		return
	}
	name, id, signature := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-Hub-Signature-256")
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write(raw)
	want, decodeErr := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if decodeErr != nil || len(signature) != 71 || !strings.HasPrefix(signature, "sha256=") || !hmac.Equal(want, mac.Sum(nil)) {
		m.rejectWebhook(w, r, webhookLogAction(raw, name), "audit", 401, webhookClass("invalid_signature"))
		return
	}
	e, err := authenticate(name, id, signature, raw, m.secret)
	if err != nil || !validAction(name, e.Action) {
		m.rejectWebhook(w, r, webhookLogAction(raw, name), "audit", 400, webhookClass("invalid_envelope"))
		return
	}
	if strconv.FormatInt(e.Installation.ID, 10) != m.config.InstallationID || e.Repository.Name != m.config.Repository {
		m.rejectWebhook(w, r, e.Action, "audit", 404, webhookClass("installation_mismatch"))
		return
	}
	if name == "pull_request" && e.Pull.Base.Repo.FullName != m.config.Repository {
		m.rejectWebhook(w, r, e.Action, "audit", 404, webhookClass("repository_mismatch"))
		return
	}
	if err := webhookSubject(name, e); err != nil {
		m.rejectWebhook(w, r, e.Action, "projection", 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webhookAttemptTimeout)
	defer cancel()
	if err = m.saveWebhook(ctx, name, id, raw, e); err != nil {
		status := http.StatusBadGateway
		var refusal *apiError
		if errors.As(err, &refusal) {
			status = refusal.status
		}
		m.rejectWebhook(w, r, e.Action, "receipt", status, err)
		return
	}
	if err = m.processWebhook(ctx, id, true); err != nil {
		step, status := "receipt", http.StatusBadGateway
		var failed *webhookStepError
		if errors.As(err, &failed) {
			step = failed.step
		}
		var refusal *apiError
		if errors.As(err, &refusal) {
			status = refusal.status
		}
		m.rejectWebhook(w, r, e.Action, step, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func webhookSubject(name string, e envelope) error {
	switch name {
	case "pull_request":
		if e.Pull.Number < 1 || !reviewgate.ValidSHA(e.Pull.Head.SHA) || !reviewgate.ValidSHA(e.Pull.Base.SHA) {
			return webhookClass("invalid_pull")
		}
	case "check_run":
		if !reviewgate.ValidSHA(e.CheckRun.Head) || len(e.CheckRun.Pulls) > 100 {
			return webhookClass("invalid_check_subjects")
		}
	case "check_suite":
		if !reviewgate.ValidSHA(e.CheckSuite.Head) || len(e.CheckSuite.Pulls) > 100 {
			return webhookClass("invalid_check_subjects")
		}
	case "status":
		if !reviewgate.ValidSHA(e.SHA) {
			return webhookClass("invalid_check_subjects")
		}
	case "merge_group":
		if !reviewgate.ValidSHA(e.Group.Head) || !reviewgate.ValidSHA(e.Group.Base) || !queueRef.MatchString(e.Group.Ref) || strings.Contains(e.Group.Ref, "..") {
			return webhookClass("invalid_group")
		}
	case "push":
		if e.Repository.Default != "" && e.Ref == "refs/heads/"+e.Repository.Default && !reviewgate.ValidSHA(e.After) {
			return webhookClass("invalid_push")
		}
	}
	return nil
}

func (m *Module) saveWebhook(ctx context.Context, name, id string, raw []byte, e envelope) error {
	service := db.AllProjects(ctx, "verified delivery receipt")
	return db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO delivery_webhook_receipts(tenant_id,delivery_id,repository,installation_id,event,action,payload_sha256,payload,received_at,next_attempt_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT(tenant_id,delivery_id) DO NOTHING`, m.config.TenantID, id, m.config.Repository, m.config.InstallationID, name, e.Action, digest(raw), raw, m.now())
		if err != nil {
			return err
		}
		var matches bool
		if err = tx.QueryRow(ctx, `SELECT repository=$2 AND installation_id=$3 AND event=$4 AND action=$5 AND payload_sha256=$6 FROM delivery_webhook_receipts WHERE delivery_id=$1`, id, m.config.Repository, m.config.InstallationID, name, e.Action, digest(raw)).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fail(http.StatusConflict, "delivery identity conflict")
		}
		return nil
	})
}

func webhookBackoff(attempt int64) time.Duration {
	delay := 5 * time.Second
	for n := int64(1); n < attempt && delay < 5*time.Minute; n++ {
		delay *= 2
	}
	return min(delay, 5*time.Minute)
}

// Session locks serialize a receipt across HTTP requests and replicas without
// holding row locks during network reads. Disconnect/restart releases the lock;
// the durable due time and checkpoints retain every unfinished delivery.
func (m *Module) processWebhook(ctx context.Context, id string, immediate bool) error {
	ctx, cancel := context.WithTimeout(ctx, webhookAttemptTimeout)
	defer cancel()
	conn, release, err := db.Acquire(ctx, m.pool)
	if err != nil {
		return err
	}
	defer release()
	key := "aeon-delivery-receipt:" + m.config.TenantID + ":" + id
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errWebhookBusy
	}
	defer unlock(conn, key)
	ctx = db.WithConnection(ctx, m.pool, conn)
	service := db.AllProjects(ctx, "delivery receipt attempt")
	var receipt webhookReceipt
	receipt.id = id
	var checkpoints, runs []byte
	err = db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT event,action,repository,installation_id,payload_sha256,payload,checkpoints,flow_runs,attempts,next_attempt_at,finished_at FROM delivery_webhook_receipts
			WHERE delivery_id=$1 AND repository=$2 AND installation_id=$3`, id, m.config.Repository, m.config.InstallationID).Scan(&receipt.event, &receipt.action, &receipt.repository, &receipt.installation, &receipt.hash, &receipt.raw, &checkpoints, &runs, &receipt.attempts, &receipt.next, &receipt.finished)
	})
	if err != nil {
		return err
	}
	if receipt.finished != nil || !immediate && receipt.next.After(m.now()) {
		return nil
	}
	if len(receipt.raw) > 2<<20 || digest(receipt.raw) != receipt.hash || json.Unmarshal(checkpoints, &receipt.checkpoints) != nil || json.Unmarshal(runs, &receipt.runs) != nil || len(receipt.runs) > 2000 {
		return webhookClass("invalid_receipt")
	}
	var e envelope
	if json.Unmarshal(receipt.raw, &e) != nil || e.Repository.Name != receipt.repository || strconv.FormatInt(e.Installation.ID, 10) != receipt.installation || e.Action != receipt.action || !validAction(receipt.event, receipt.action) {
		return webhookClass("invalid_receipt")
	}
	receipt.attempts++
	err = db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_webhook_receipts SET attempts=$2,next_attempt_at=$3 WHERE delivery_id=$1`, id, receipt.attempts, m.now().Add(webhookAttemptTimeout+webhookBackoff(receipt.attempts)))
		return err
	})
	if err != nil {
		return err
	}
	for _, step := range webhookSteps {
		if _, done := receipt.checkpoints[step]; done {
			continue
		}
		outcome, err := m.webhookStep(ctx, step, &receipt, e)
		if err == nil {
			err = m.checkpointWebhook(ctx, step, outcome, &receipt)
		}
		if err != nil {
			failedStep := step
			var failed *webhookStepError
			if errors.As(err, &failed) {
				failedStep = failed.step
			}
			// Request cancellation cannot prevent recording the failed attempt.
			// Reuse only the same sequential session, bounded by a cleanup deadline.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			storeErr := db.InTenant(db.AllProjects(cleanup, "delivery receipt retry schedule"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTenant(cleanup, tx, m.config.TenantID); err != nil {
					return err
				}
				_, err := tx.Exec(cleanup, `UPDATE delivery_webhook_receipts SET next_attempt_at=$2,last_step=$3,last_error_class=$4 WHERE delivery_id=$1`, id, m.now().Add(webhookBackoff(receipt.attempts)), failedStep, webhookErrorClass(err))
				return err
			})
			if storeErr != nil {
				return &webhookStepError{step: "receipt", err: storeErr}
			}
			return &webhookStepError{step: failedStep, err: err}
		}
	}
	return db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_webhook_receipts SET finished_at=$2,last_step=NULL,last_error_class=NULL WHERE delivery_id=$1`, id, m.now())
		return err
	})
}

func (m *Module) checkpointWebhook(ctx context.Context, step string, outcome webhookOutcome, receipt *webhookReceipt) error {
	runs, err := json.Marshal(receipt.runs)
	if err != nil || len(runs) > 2<<20 {
		return errRead
	}
	return db.InTenant(db.AllProjects(ctx, "delivery receipt checkpoint"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_webhook_receipts SET checkpoints=jsonb_set(checkpoints,ARRAY[$2::text],to_jsonb($3::text)),flow_runs=$4 WHERE delivery_id=$1`, receipt.id, step, string(outcome), runs)
		return err
	})
}

func (m *Module) webhookStep(ctx context.Context, step string, receipt *webhookReceipt, e envelope) (webhookOutcome, error) {
	switch step {
	case "projection":
		return m.projectWebhook(ctx, receipt.event, receipt.id, receipt.raw, e)
	case "fanout":
		if receipt.event != "pull_request" || m.fanout == nil {
			return webhookNothingToRecord, nil
		}
		return webhookRecorded, m.fanoutRecord(ctx, receipt.id)
	case "check_audit":
		return m.checkAuditWebhook(ctx, receipt.event, receipt.id, receipt.raw)
	case "merge_audit":
		return m.mergeAuditWebhook(ctx, receipt.event, receipt.raw)
	case "metrics":
		outcome, runs, err := m.metricWebhookFacts(ctx, receipt.event, receipt.raw)
		receipt.runs = runs
		return outcome, err
	case "flow":
		if receipt.event == "workflow_run" {
			return webhookRecorded, m.flowFromWorkflowRun(ctx, receipt.raw)
		}
		if len(receipt.runs) == 0 {
			return webhookNothingToRecord, nil
		}
		return webhookRecorded, m.flowFromRuns(ctx, receipt.runs)
	case "completion":
		reader, enabled := m.github.(MergeReader)
		if !enabled {
			return webhookNotConfigured, nil
		}
		return webhookRecorded, m.completeWebhook(ctx, receipt.event, receipt.raw, reader)
	}
	return webhookFailed, errRead
}

// A bounded due page and per-delivery session claim keep each sweep finite.
// Failed attempts back off indefinitely: pending facts are never discarded.
func (m *Module) retryWebhooks(ctx context.Context) error {
	ctx, stop := context.WithTimeout(ctx, webhookAttemptTimeout)
	defer stop()
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ids := []string{}
	err := db.InTenant(db.AllProjects(queryCtx, "delivery receipt due page"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(queryCtx, `SELECT delivery_id FROM delivery_webhook_receipts WHERE repository=$1 AND installation_id=$2 AND finished_at IS NULL AND next_attempt_at<=$3 ORDER BY next_attempt_at,delivery_id LIMIT 20`, m.config.Repository, m.config.InstallationID, m.now())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := m.processWebhook(ctx, id, false); err != nil && !errors.Is(err, errWebhookBusy) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
