// SPDX-License-Identifier: AGPL-3.0-only
package parentbenefits

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/fieldschema"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const failureProvider = "Summary generation failed. Check the workspace model provider and retry."
const failureConsent = "Enable parent benefits in the workspace model provider with permission to edit this parent, then retry."
const failureChanged = "The parent or its leaves changed during generation. Review the leaves and retry."

type job struct {
	target
	tenant  string
	config  modelprovider.Config
	actor   tenant.Principal
	leaves  []leaf
	hash    string
	problem string
}

// Run processes one job per tenant per pass with a bounded tenant cursor and
// bounded provider call. Database leases survive restart and fence other servers.
func (m *Module) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := m.pass(ctx); err != nil && ctx.Err() == nil {
			slog.Error("parent benefit worker unavailable")
		}
		timer.Reset(5 * time.Second)
	}
}
func (m *Module) pass(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id::text LIMIT 10`, m.afterTenant)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		m.afterTenant = ""
		return nil
	}
	var firstError error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := m.Once(ctx, id); err != nil && firstError == nil {
			firstError = err
		}
		m.afterTenant = id
	}
	return firstError
}

// Once is also the integration-test boundary: no sleeps or running goroutine are
// needed to prove completion, restart recovery or a paused external request.
func (m *Module) Once(ctx context.Context, tid string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	j, err := m.claim(ctx, tid)
	if err != nil || j == nil {
		return false, err
	}
	var texts Texts
	if j.problem == "" {
		if m.chat == nil {
			j.problem = failureProvider
		} else {
			external, cancel := context.WithTimeout(ctx, 30*time.Second)
			result, err := m.chat(external, tid, j.config.ProviderID, j.config.Revision, messages(j.leaves))
			cancel()
			if err != nil {
				j.problem = failureProvider
			} else if texts, err = parse(result.Text); err != nil {
				j.problem = failureProvider
			}
		}
	}
	return true, m.finish(ctx, *j, texts)
}
func consent(ctx context.Context, tx pgx.Tx, tid, project string) (modelprovider.Config, tenant.Principal, error) {
	var p tenant.Principal
	p.TenantID = tid
	p.Kind = tenant.Person
	c, err := modelprovider.Load(ctx, tx)
	if err != nil {
		return c, p, err
	}
	if !c.Enabled || !c.Features.ParentBenefits {
		return c, p, modelprovider.ErrDisabled
	}
	// Consent belongs to the person who last saved this exact provider revision.
	// Recheck that person's current grants inside the final write, under the
	// tenant access fence. System is only the audit actor, never a blanket grant.
	err = tx.QueryRow(ctx, `SELECT actor_principal_id::text FROM events WHERE type='tenant.model_provider_updated' AND after->>'provider_id'=$1 AND after->>'revision'=$2 ORDER BY id DESC LIMIT 1`, c.ProviderID, jsonInt(c.Revision)).Scan(&p.ID)
	if err != nil {
		return c, p, err
	}
	if err = authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
		return c, p, err
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return c, p, err
	}
	err = authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project})
	return c, p, err
}
func jsonInt(v int64) string { raw, _ := json.Marshal(v); return string(raw) }
func (m *Module) claim(ctx context.Context, tid string) (*job, error) {
	ctx = db.AllProjects(ctx, "parent benefit generation: scoped workspace automation")
	var j *job
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := fence(ctx, tx, tid); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE deleted_at IS NULL AND (benefit_generation->>'status'='queued' OR (benefit_generation->>'status'='running' AND (benefit_generation->>'lease_until')::timestamptz<=clock_timestamp())) ORDER BY id LIMIT 1`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		t, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if !t.IsParent {
			return writeMeta(ctx, tx, id, `benefit_generation || jsonb_build_object('status','cancelled')`)
		}
		j = &job{target: t, tenant: tid}
		j.config, j.actor, err = consent(ctx, tx, tid, t.project)
		if err != nil {
			j.problem = failureConsent
		} else {
			j.leaves, j.hash, err = sources(ctx, tx, id)
			if err != nil {
				if errors.Is(err, errSources) {
					j.problem = errSources.Error()
				} else {
					return err
				}
			}
		}
		// A new token fences the previous lease owner after process restart.
		if err := writeMeta(ctx, tx, id, `benefit_generation || jsonb_build_object('status','running','generation',gen_random_uuid()::text,'lease_until',clock_timestamp()+interval '90 seconds','error','')`); err != nil {
			return err
		}
		j.target, err = load(ctx, tx, id)
		return err
	})
	return j, err
}
func (m *Module) finish(ctx context.Context, j job, texts Texts) error {
	ctx = db.AllProjects(ctx, "parent benefit generation: fenced result")
	return db.InTenant(ctx, m.pool, j.tenant, func(tx pgx.Tx) error {
		if err := fence(ctx, tx, j.tenant); err != nil {
			return err
		}
		now, err := load(ctx, tx, j.id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if now.Generation != j.Generation || now.State != "running" {
			return nil
		}
		// Provider saves also take the tenant fence and this lock. Nothing can
		// disable consent after this check but before commit.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "model-provider/"+j.tenant); err != nil {
			return err
		}
		c, p, authErr := consent(ctx, tx, j.tenant, now.project)
		if authErr != nil || p.ID != j.actor.ID || c.ProviderID != j.config.ProviderID || c.Revision != j.config.Revision {
			j.problem = failureConsent
		}
		var current bool
		if err := tx.QueryRow(ctx, `SELECT aeon_work_status_is_parent(n.id) AND aeon_work_status_category(n.state,k.field_schema) IN ('done','accepted','delivered') AND (n.benefit_generation->>'lease_until')::timestamptz>clock_timestamp() AND aeon_benefit_texts(n.fields)=aeon_benefit_texts($2::jsonb) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, j.id, j.fields).Scan(&current); err != nil {
			return err
		}
		if !current || now.project != j.project {
			j.problem = failureChanged
		}
		if j.problem == "" {
			_, hash, err := sources(ctx, tx, j.id)
			if err != nil || hash != j.hash {
				j.problem = failureChanged
			}
		}
		if j.problem == "" {
			raw, _ := json.Marshal(texts)
			var schema, merged []byte
			if err := tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(k.field_schema::text)<=1048576 THEN k.field_schema ELSE NULL END,CASE WHEN octet_length(n.fields::text)<=1048576 THEN n.fields||$2::jsonb ELSE NULL END FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, j.id, raw).Scan(&schema, &merged); err != nil {
				return err
			}
			if len(schema) == 0 || len(schema) > 1048576 || len(merged) == 0 || len(merged) > 1048576 {
				j.problem = "The parent fields exceed the generation limit. Edit its benefits directly."
			} else {
				compiled, err := fieldschema.Compile(schema)
				value, decodeErr := fieldschema.Decode(merged)
				if err != nil || decodeErr != nil || compiled.Validate(value) != nil {
					j.problem = "The summary does not match the parent field rules. Edit its benefits directly."
				}
			}
		}
		actor, err := systemactor.Ensure(ctx, tx, j.tenant)
		if err != nil {
			return err
		}
		if j.problem != "" {
			if err := writeMeta(ctx, tx, j.id, `benefit_generation || jsonb_build_object('status','failed','error',$2::text)`, j.problem); err != nil {
				return err
			}
			_, err = events.Append(ctx, tx, actor, events.Change{NodeID: &j.id, Type: "parent_benefits.failed", After: map[string]any{"generation": j.Generation, "error": j.problem}})
			return err
		}
		raw, _ := json.Marshal(texts)
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.parent_benefit_writer','on',true)`); err != nil {
			return err
		}
		// Merge only the four texts into the current record; preserve unrelated edits.
		if _, err := tx.Exec(ctx, `UPDATE nodes SET fields=fields||$2::jsonb,benefit_generation=jsonb_build_object('status','generated','generation',$3::text,'generated',true),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1`, j.id, raw, j.Generation); err != nil {
			return err
		}
		after, err := load(ctx, tx, j.id)
		if err != nil {
			return err
		}
		beforeSnap := map[string]any{"id": j.id, "fields": json.RawMessage(now.fields), "updated_at": now.Revision}
		afterSnap := map[string]any{"id": j.id, "fields": json.RawMessage(after.fields), "updated_at": after.Revision}
		meta, _ := json.Marshal(map[string]any{"job": "parent-benefits", "generated": true, "generation": j.Generation, "source_sha256": j.hash})
		_, err = events.Append(ctx, tx, actor, events.Change{NodeID: &j.id, Type: "node.benefits_generated", Before: beforeSnap, After: afterSnap, Metadata: meta})
		return err
	})
}
