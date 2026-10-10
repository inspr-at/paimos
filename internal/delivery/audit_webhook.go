// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// The existing ingress remains the HMAC/installation boundary and projection
// owner. Delay its status until the additional audit work commits. A failed
// audit returns 502 even if projection ingestion succeeded; replay retries the
// audit after the original handler deduplicates its own ledger/fan-out.
type auditResponse struct {
	header http.Header
	status int
}

func (w *auditResponse) Header() http.Header { return w.header }
func (w *auditResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *auditResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(b), nil
}

func (m *Module) auditWebhook(w http.ResponseWriter, r *http.Request) {
	reader, enabled := m.github.(AuditGitHub)
	if !enabled {
		m.webhook(w, r)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		class := webhookClass("body_too_large")
		if err != nil {
			class = "body_read"
		}
		m.rejectWebhook(w, r, "unknown", "audit", 413, class)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	response := &auditResponse{header: w.Header()}
	m.webhook(response, r)
	if response.status != 204 {
		w.WriteHeader(response.status)
		return
	}
	name := r.Header.Get("X-GitHub-Event")
	// Authentication, duplicate-key validation and installation binding have
	// already succeeded. Extract only content-free audit facts.
	var e struct {
		Action  string `json:"action"`
		Ref     string `json:"ref"`
		After   string `json:"after"`
		SHA     string `json:"sha"`
		State   string `json:"state"`
		Context string `json:"context"`
		Sender  struct {
			Login string `json:"login"`
		} `json:"sender"`
		Repository struct {
			Default string `json:"default_branch"`
		} `json:"repository"`
		Pull     auditPull `json:"pull_request"`
		CheckRun struct {
			Head       string `json:"head_sha"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_run"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		m.rejectWebhook(w, r, webhookLogAction(raw, name), "audit", 400, webhookClass("invalid_json"))
		return
	}
	var f *MergeFact
	switch name {
	case "pull_request":
		if e.Action == "closed" && e.Pull.Merged && e.Repository.Default != "" {
			f, err = auditPullFact(e.Pull, m.config.Repository, e.Repository.Default)
		}
	case "push":
		if e.Repository.Default != "" && e.Ref == "refs/heads/"+e.Repository.Default {
			f, err = reader.AuditCommit(ctx, e.After)
			if err == nil && f != nil && f.PR == nil {
				// The authenticated push actor is not necessarily the commit
				// author/committer. Only the webhook can prove the pusher login.
				copy := *f
				copy.MergedBy = e.Sender.Login
				f = &copy
			}
		}
	case "check_run", "status":
		sha := e.CheckRun.Head
		c := Check{Name: e.CheckRun.Name, Status: e.CheckRun.Status, Conclusion: e.CheckRun.Conclusion}
		if name == "status" {
			sha = e.SHA
			c = Check{Name: e.Context, Status: "completed", Conclusion: e.State}
		}
		if !reviewgate.ValidSHA(sha) || c.Name == "" || len(c.Name) > 200 {
			err = errRead
			break
		}
		err = m.auditCheckEvent(ctx, r.Header.Get("X-GitHub-Delivery"), sha, c, raw)
	}
	if err == nil && f != nil {
		err = m.auditMerge(ctx, m.config.TenantID, *f, reader)
	}
	if err == nil {
		// AEON-993 timing facts; a failed read or write stays retryable.
		err = m.metricsEvent(ctx, name, raw)
	}
	if err == nil && name == "workflow_run" {
		// Live checks stay out of the metric tables. Completed suites still
		// write those facts, and the flow rows converge on redelivery.
		err = m.flowFromWorkflowRun(ctx, raw)
	}
	if err != nil {
		m.rejectWebhook(w, r, e.Action, "audit", 502, err)
		return
	}
	w.WriteHeader(204)
}

func (m *Module) auditCheckEvent(ctx context.Context, id, sha string, c Check, raw []byte) error {
	service := db.AllProjects(ctx, "authenticated merge audit check facts")
	return db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		facts, err := json.Marshal([]Check{c})
		if err != nil {
			return err
		}
		at := m.now()
		_, err = tx.Exec(ctx, `INSERT INTO delivery_github_events(tenant_id,delivery_id,event,repository,head_sha,received_at,payload_sha256,audit_checks,fanout_done,processed_at)
            VALUES($1,$2,'audit_checks',$3,$4,$5,$6,$7,true,$5) ON CONFLICT(tenant_id,delivery_id) DO NOTHING`, m.config.TenantID, "audit-checks-"+id, m.config.Repository, sha, at, digest(raw), facts)
		return err
	})
}
