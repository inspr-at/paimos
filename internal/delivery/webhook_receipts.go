// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const (
	webhookAttemptTimeout = 4 * time.Minute
	webhookMaxAttempts    = 12
)

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
	lastStep, lastClass                               *string
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
	// Preserve the existing typed-envelope refusal before accepting a receipt.
	// This is input validation, not an audit read or effect.
	if _, enabled := m.github.(AuditGitHub); enabled {
		var audit auditEnvelope
		if json.Unmarshal(raw, &audit) != nil {
			m.rejectWebhook(w, r, e.Action, "audit", 400, webhookClass("invalid_json"))
			return
		}
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
		m.rejectWebhookStep(w, r, e.Action, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) rejectWebhookStep(w http.ResponseWriter, r *http.Request, action string, err error) {
	step, status := "receipt", http.StatusBadGateway
	var failed *webhookStepError
	if errors.As(err, &failed) {
		step = failed.step
	}
	var refusal *apiError
	if errors.As(err, &refusal) {
		status = refusal.status
	}
	m.rejectWebhook(w, r, action, step, status, err)
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
		// Main's history-worker wake timestamp and the verified receipt commit
		// together; an ingress write cannot advertise a receipt it did not save.
		return m.recordMetricEventTx(ctx, tx)
	})
}

func webhookBackoff(attempt int64) time.Duration {
	return webhookErrorBackoff(attempt, nil)
}

func webhookErrorBackoff(attempt int64, cause error) time.Duration {
	if crossreview.IsTransientGitHubError(cause) {
		return shortMetricRetry()
	}
	delay := metricRetryBase(metricError(cause, time.Time{}).Reason)
	for n := int64(1); n < attempt && delay < time.Hour; n++ {
		delay *= 2
	}
	return min(delay, time.Hour)
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
		return tx.QueryRow(ctx, `SELECT event,action,repository,installation_id,payload_sha256,payload,checkpoints,flow_runs,attempts,next_attempt_at,finished_at,last_step,last_error_class FROM delivery_webhook_receipts
			WHERE delivery_id=$1 AND repository=$2 AND installation_id=$3`, id, m.config.Repository, m.config.InstallationID).Scan(&receipt.event, &receipt.action, &receipt.repository, &receipt.installation, &receipt.hash, &receipt.raw, &checkpoints, &runs, &receipt.attempts, &receipt.next, &receipt.finished, &receipt.lastStep, &receipt.lastClass)
	})
	if err != nil {
		return err
	}
	if receipt.finished != nil || receipt.next.After(m.now()) && (!immediate || receipt.lastClass != nil && webhookLimited(*receipt.lastClass)) {
		return nil
	}
	if len(receipt.raw) > 2<<20 || len(checkpoints) > 4096 || len(runs) > 2<<20 || digest(receipt.raw) != receipt.hash || json.Unmarshal(checkpoints, &receipt.checkpoints) != nil || json.Unmarshal(runs, &receipt.runs) != nil || len(receipt.runs) > 2000 {
		return webhookClass("invalid_receipt")
	}
	var e envelope
	if json.Unmarshal(receipt.raw, &e) != nil || e.Repository.Name != receipt.repository || strconv.FormatInt(e.Installation.ID, 10) != receipt.installation || e.Action != receipt.action || !validAction(receipt.event, receipt.action) {
		return webhookClass("invalid_receipt")
	}
	// Permission waits only retry notice routing, never GitHub. An authenticated
	// redelivery of this exact payload explicitly resumes the unfinished step.
	if !immediate && receipt.lastClass != nil && webhookPermission(*receipt.lastClass) && receipt.lastStep != nil {
		return m.deferWebhook(ctx, &receipt, *receipt.lastStep, *receipt.lastStep, webhookClass(*receipt.lastClass))
	}
	// A crash after reserving the last attempt must not buy another network
	// attempt on restart. Fully checkpointed receipts can still finish safely.
	if receipt.attempts >= webhookMaxAttempts {
		for _, step := range webhookSteps {
			if _, done := receipt.checkpoints[step]; !done {
				return m.deferWebhook(ctx, &receipt, step, step, nil)
			}
		}
	} else {
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
	}
	for _, step := range webhookSteps {
		if _, done := receipt.checkpoints[step]; done {
			continue
		}
		outcome, err := m.webhookStep(ctx, step, &receipt, e)
		if err == nil && outcome != webhookPending {
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
			storeErr := m.deferWebhook(cleanup, &receipt, step, failedStep, err)
			if storeErr != nil {
				return &webhookStepError{step: "receipt", err: storeErr}
			}
			return &webhookStepError{step: failedStep, err: err}
		}
		if outcome == webhookPending {
			// The Needs You refusal, if any, remains durable even after the
			// automated retry budget ends; no completion gate is bypassed.
			return m.deferWebhook(ctx, &receipt, step, step, nil)
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

func (m *Module) deferWebhook(ctx context.Context, receipt *webhookReceipt, step, failedStep string, cause error) error {
	var class *string
	if cause != nil {
		value := webhookErrorClass(cause)
		class = &value
	}
	limited := class != nil && webhookLimited(*class)
	permission := class != nil && webhookPermission(*class)
	if limited {
		// Reservation still fences crashes; a confirmed rate-limit wait refunds
		// it atomically with the due time and never exhausts the retry budget.
		receipt.attempts = max(0, receipt.attempts-1)
	}
	exhausted := receipt.attempts >= webhookMaxAttempts && !permission
	if exhausted && (class == nil || *class != "http_5xx" && *class != "network" && *class != "http_404") {
		value := "retry_exhausted"
		class = &value
	}
	at := m.now()
	delay := webhookErrorBackoff(receipt.attempts, cause)
	if limited {
		delay = webhookLimitBackoff(cause, at)
	} else if permission {
		delay = time.Hour
	}
	var actor tenant.Principal
	if permission {
		var err error
		actor, err = m.auditActor(ctx, m.config.TenantID)
		if err != nil {
			return err
		}
	}
	err := db.InTenant(db.AllProjects(ctx, "delivery receipt retry schedule"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		// Notice routing rechecks the current linked project and lead under the
		// same tenant/tree access fence as the final receipt mutation.
		if permission {
			if err := db.LockTree(ctx, tx, m.config.TenantID); err != nil {
				return err
			}
		} else if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_webhook_receipts SET next_attempt_at=$2,last_step=$3,last_error_class=$4,
			finished_at=CASE WHEN $5 THEN $6 ELSE finished_at END,
			checkpoints=CASE WHEN $5 THEN jsonb_set(checkpoints,ARRAY[$7::text],to_jsonb($8::text)) ELSE checkpoints END,attempts=$9
			WHERE delivery_id=$1`, receipt.id, at.Add(delay), failedStep, class, exhausted, at, step, string(webhookGaveUp), receipt.attempts)
		if err != nil || !permission {
			return err
		}
		return m.webhookPermissionNoticeTx(ctx, tx, actor, receipt, failedStep, *class)
	})
	if err == nil && exhausted {
		m.logWebhookGaveUp(ctx, receipt, failedStep, cause)
	}
	return err
}

func (m *Module) webhookPermissionNoticeTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, receipt *webhookReceipt, step, class string) error {
	var e envelope
	if err := json.Unmarshal(receipt.raw, &e); err != nil {
		return err
	}
	fact := MergeFact{Head: e.SHA}
	switch receipt.event {
	case "pull_request":
		fact.PR, fact.Head, fact.Title, fact.Branch = &e.Pull.Number, e.Pull.Head.SHA, e.Pull.Title, e.Pull.Head.Ref
	case "check_run":
		fact.Head = e.CheckRun.Head
	case "check_suite":
		fact.Head = e.CheckSuite.Head
	case "merge_group":
		fact.Head = e.Group.Head
	case "push":
		fact.Head = e.After
	}
	o, err := auditLink(ctx, tx, receipt.repository, fact)
	if err != nil {
		return err
	}
	to, err := auditRecipientTx(ctx, tx, actor.TenantID, MergeAudit{Project: o.Project, Ticket: o.Ticket})
	if err != nil {
		return err
	}
	key := digest([]byte(actor.TenantID + "|" + receipt.repository + "|" + receipt.id + "|permission"))
	body := fmt.Sprintf("Delivery receipt permission: %s at %s. Automatic GitHub reads are paused. Check the GitHub App installation access, then redeliver the verified webhook to resume.", class, step)
	return deliveryNoticeTx(ctx, tx, actor, o.Project, to, key, body)
}

func (m *Module) checkpointWebhook(ctx context.Context, step string, outcome webhookOutcome, receipt *webhookReceipt) error {
	var runs []byte
	if step == "metrics" {
		var err error
		runs, err = json.Marshal(receipt.runs)
		if err != nil || len(runs) > 2<<20 {
			return errRead
		}
	}
	return db.InTenant(db.AllProjects(ctx, "delivery receipt checkpoint"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_webhook_receipts SET checkpoints=jsonb_set(checkpoints,ARRAY[$2::text],to_jsonb($3::text)),flow_runs=CASE WHEN $2='metrics' THEN $4::jsonb ELSE flow_runs END WHERE delivery_id=$1`, receipt.id, step, string(outcome), runs)
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
		return m.completeWebhook(ctx, receipt.event, receipt.raw, reader)
	}
	return webhookFailed, errRead
}

// A bounded due page and per-delivery session claim keep each sweep finite.
// Unresolved work backs off within its attempt budget. Exhausted receipts keep
// their payload and checkpoints, but leave the due page with a gave_up outcome.
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
