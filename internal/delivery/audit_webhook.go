// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

type webhookOutcome string

const (
	webhookRecorded        webhookOutcome = "recorded"
	webhookNotConfigured   webhookOutcome = "not_configured"
	webhookNothingToRecord webhookOutcome = "nothing_to_record"
	webhookFailed          webhookOutcome = "failed"
)

// Preserve the original error for content-free classification while naming
// the failed downstream step instead of flattening it into the audit label.
type webhookStepError struct {
	step string
	err  error
}

func (e *webhookStepError) Error() string { return e.err.Error() }
func (e *webhookStepError) Unwrap() error { return e.err }

type auditEnvelope struct {
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

func (m *Module) checkAuditWebhook(ctx context.Context, name, id string, raw []byte) (webhookOutcome, error) {
	if _, enabled := m.github.(AuditGitHub); !enabled {
		return webhookNotConfigured, nil
	}
	if name != "check_run" && name != "status" {
		return webhookNothingToRecord, nil
	}
	var e auditEnvelope
	if json.Unmarshal(raw, &e) != nil {
		return webhookFailed, errRead
	}
	sha := e.CheckRun.Head
	c := Check{Name: e.CheckRun.Name, Status: e.CheckRun.Status, Conclusion: e.CheckRun.Conclusion}
	if name == "status" {
		sha = e.SHA
		c = Check{Name: e.Context, Status: "completed", Conclusion: e.State}
	}
	if !reviewgate.ValidSHA(sha) || c.Name == "" || len(c.Name) > 200 {
		return webhookFailed, errRead
	}
	if err := m.auditCheckEvent(ctx, id, sha, c, raw); err != nil {
		return webhookFailed, err
	}
	return webhookRecorded, nil
}

func (m *Module) mergeAuditWebhook(ctx context.Context, name string, raw []byte) (webhookOutcome, error) {
	reader, enabled := m.github.(AuditGitHub)
	if !enabled {
		return webhookNotConfigured, nil
	}
	var e auditEnvelope
	if json.Unmarshal(raw, &e) != nil {
		return webhookFailed, errRead
	}
	var f *MergeFact
	var err error
	switch name {
	case "pull_request":
		if e.Action == "closed" && e.Pull.Merged && e.Repository.Default != "" {
			f, err = auditPullFact(e.Pull, m.config.Repository, e.Repository.Default)
		}
	case "push":
		if e.Repository.Default != "" && e.Ref == "refs/heads/"+e.Repository.Default {
			f, err = reader.AuditCommit(ctx, e.After)
			if err == nil && f != nil && f.PR == nil {
				copy := *f
				copy.MergedBy = e.Sender.Login
				f = &copy
			}
		}
	}
	if err != nil {
		return webhookFailed, err
	}
	if f == nil {
		return webhookNothingToRecord, nil
	}
	if err = m.auditMerge(ctx, m.config.TenantID, *f, reader); err != nil {
		return webhookFailed, err
	}
	return webhookRecorded, nil
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
