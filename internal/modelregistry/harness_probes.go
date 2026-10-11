// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Paired accounts require current ongoing approval. A catalog never grants use,
// changes account health, or spends the one-shot verification allowance.
const harnessProbeEligible = `a.state='available' AND a.archived_at IS NULL
 AND a.harness IN ('codex','cursor','opencode','pi','claude')
 AND NOT EXISTS (SELECT 1 FROM agent_pairing_enrollments e
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
 WHERE e.account_id=a.id AND (e.state<>'connected' OR c.state<>'connected' OR q.state<>'redeemed' OR e.ongoing_approved_at IS NULL))`

func queueHarnessProbe(ctx context.Context, tx pgx.Tx, tenantID, account string, revision int64, at time.Time) error {
	id := modelreport.EvidenceID(account + "/catalog/" + at.UTC().Format(time.RFC3339Nano))
	_, err := tx.Exec(ctx, `INSERT INTO model_harness_probes(tenant_id,account_id,binding_revision,request_id,requested_at,state)
 VALUES($1,$2,$3,$4,$5,'pending') ON CONFLICT(tenant_id,account_id) DO UPDATE SET
 binding_revision=EXCLUDED.binding_revision,request_id=EXCLUDED.request_id,requested_at=EXCLUDED.requested_at,
 state='pending',lease_until=NULL,daemon_generation=NULL,completed_at=NULL,model_count=0,report_digest=NULL`, tenantID, account, revision, id, at)
	return err
}

// Called before events in the refresh reservation. Bounded queueing is honest
// about larger tenants; unqueued accounts still discover on their own schedule.
func queueHarnessProbes(ctx context.Context, tx pgx.Tx, tenantID string, at time.Time) (int, bool, error) {
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.link_revision FROM agent_accounts a WHERE `+harnessProbeEligible+` ORDER BY a.id LIMIT 65`)
	if err != nil {
		return 0, false, err
	}
	type binding struct {
		id  string
		rev int64
	}
	items := []binding{}
	for rows.Next() {
		var b binding
		if err = rows.Scan(&b.id, &b.rev); err != nil {
			rows.Close()
			return 0, false, err
		}
		items = append(items, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, false, err
	}
	limited := len(items) > 64
	if limited {
		items = items[:64]
	}
	for _, b := range items {
		if err = queueHarnessProbe(ctx, tx, tenantID, b.id, b.rev, at); err != nil {
			return 0, false, err
		}
	}
	return len(items), limited, nil
}

func (m *Module) harnessProbeTx(r *http.Request, p tenant.Principal, fn func(pgx.Tx, tenant.Principal) error) error {
	if p.Kind != tenant.Agent {
		return fail(403, "agent required")
	}
	return m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		// Tenant -> tree/pairing -> catalog -> account -> event counter last.
		if err := agentpairing.Lock(r.Context(), tx); err != nil {
			return err
		}
		if err := catalogLock(r.Context(), tx); err != nil {
			return err
		}
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "models.report")
		if err != nil {
			return err
		}
		if err = authz.RequireTx(r.Context(), tx, current, "models.report", authz.Scope{}); err != nil {
			return err
		}
		return fn(tx, current)
	})
}

func probeDaemon(id, generation string) bool {
	return len(id) <= 128 && modelRE.MatchString(id) && len(generation) <= 128 && modelRE.MatchString(generation)
}

func (m *Module) claimHarnessProbes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var in struct {
		Daemon     string `json:"daemon_id"`
		Generation string `json:"daemon_generation"`
		After      string `json:"cursor"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if !probeDaemon(in.Daemon, in.Generation) || in.After != "" && !uuidRE.MatchString(in.After) {
		writeErr(w, fail(400, "invalid probe binding"))
		return
	}
	out := struct {
		Items []modelreport.CatalogProbe `json:"items"`
		Next  string                     `json:"next_cursor"`
	}{Items: []modelreport.CatalogProbe{}}
	err := m.harnessProbeTx(r, p, func(tx pgx.Tx, current tenant.Principal) error {
		cfg, err := settings(ctx, tx)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if !cfg.AgentReportsEnabled {
			return nil
		}
		clock := m.validationClock
		if clock == nil {
			clock = validationNow
		}
		now, err := clock(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.account_key,a.harness,a.link_revision FROM agent_accounts a WHERE `+harnessProbeEligible+`
 AND a.registered_by_principal_id=$1
 AND a.daemon_id=$2 AND a.last_daemon_generation=$3 AND ($4='' OR a.id>NULLIF($4,'')::uuid)
 ORDER BY a.id LIMIT 17`, current.ID, in.Daemon, in.Generation, in.After)
		if err != nil {
			return err
		}
		accounts := []modelreport.CatalogProbe{}
		for rows.Next() {
			var a modelreport.CatalogProbe
			if err = rows.Scan(&a.AccountID, &a.AccountKey, &a.Harness, &a.BindingRevision); err != nil {
				rows.Close()
				return err
			}
			accounts = append(accounts, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(accounts) > 16 {
			accounts = accounts[:16]
			out.Next = accounts[15].AccountID
		}
		for _, a := range accounts {
			var requested time.Time
			var revision int64
			var state string
			var lease *time.Time
			err := tx.QueryRow(ctx, `SELECT requested_at,binding_revision,state,lease_until FROM model_harness_probes WHERE account_id=$1`, a.AccountID).Scan(&requested, &revision, &state, &lease)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			if err == pgx.ErrNoRows || revision != a.BindingRevision || state != "pending" && state != "probing" && !now.Before(requested.Add(time.Duration(cfg.IntervalMinutes)*time.Minute)) {
				if err = queueHarnessProbe(ctx, tx, p.TenantID, a.AccountID, a.BindingRevision, now); err != nil {
					return err
				}
				state = "pending"
				lease = nil
			}
			if state != "pending" && (state != "probing" || lease != nil && now.Before(*lease)) {
				continue
			}
			if err = tx.QueryRow(ctx, `UPDATE model_harness_probes SET state='probing',lease_until=$2,daemon_generation=$3 WHERE account_id=$1 RETURNING request_id::text,requested_at`, a.AccountID, now.Add(time.Minute), in.Generation).Scan(&a.RequestID, &a.RequestedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, a)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) reportHarnessProbe(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, modelreport.CatalogBytesMax)
	var in modelreport.CatalogReport
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	account := r.PathValue("accountId")
	if !uuidRE.MatchString(account) || !uuidRE.MatchString(in.RequestID) || in.BindingRevision < 0 || !probeDaemon(in.DaemonID, in.Generation) {
		writeErr(w, fail(400, "invalid probe binding"))
		return
	}
	if err := modelreport.ValidateCatalog(in.State, in.Models); err != nil {
		writeErr(w, fail(400, err.Error()))
		return
	}
	added, proposed, profilesTruncated := 0, 0, false
	err := m.harnessProbeTx(r, p, func(tx pgx.Tx, current tenant.Principal) error {
		cfg, err := settings(ctx, tx)
		if err != nil {
			return err
		}
		if !cfg.AgentReportsEnabled {
			return fail(403, "harness reports disabled")
		}
		var harness string
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT a.harness,`+harnessProbeEligible+` AND a.link_revision=$2 AND a.daemon_id=$3 AND a.last_daemon_generation=$4
 AND a.registered_by_principal_id=$5
 FROM agent_accounts a WHERE a.id=$1 FOR NO KEY UPDATE`, account, in.BindingRevision, in.DaemonID, in.Generation, current.ID).Scan(&harness, &valid); err == pgx.ErrNoRows {
			return fail(404, "account not found")
		}
		if err != nil {
			return err
		}
		if !valid {
			return fail(409, "account probe binding changed")
		}
		if err = agentpairing.AccountFence(ctx, tx, account, false); err != nil {
			return err
		}
		var request, generation, state string
		var digest *string
		var at time.Time
		if err = tx.QueryRow(ctx, `SELECT request_id::text,coalesce(daemon_generation,''),state,report_digest,requested_at FROM model_harness_probes WHERE account_id=$1 AND binding_revision=$2`, account, in.BindingRevision).Scan(&request, &generation, &state, &digest, &at); err == pgx.ErrNoRows {
			return fail(409, "probe request changed")
		}
		if err != nil {
			return err
		}
		if request != in.RequestID || generation != in.Generation {
			return fail(409, "probe request changed")
		}
		raw, _ := json.Marshal(in)
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		if digest != nil {
			if *digest == hash {
				return nil
			}
			return fail(409, "probe already completed with different evidence")
		}
		if state != "probing" {
			return fail(409, "probe is not claimed")
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		facts := []CatalogFact{}
		source := "harness:" + harness + ":" + account
		if in.State == "fresh" {
			// Only a complete list proves that this account stopped listing a route.
			if _, err = tx.Exec(ctx, `UPDATE model_catalog_facts SET value='false',fetched_at=$2 WHERE source=$1 AND fact='listed'`, source, now); err != nil {
				return err
			}
		}
		for _, model := range in.Models {
			provider, id := CatalogIdentity(model.Provider, harness, model.Model, model.Effort)
			if (harness == "codex" && provider != "openai") || (harness == "claude" && provider != "anthropic") {
				return fail(400, "harness provider mismatch")
			}
			base := CatalogFact{Provider: provider, ModelID: id, Route: harness + "/" + model.Model, Source: source, SourceKind: "harness", FetchedAt: now}
			// Missing current fields must not remain old, apparently fresh facts.
			if in.State == "fresh" {
				if _, err = tx.Exec(ctx, `DELETE FROM model_catalog_facts WHERE source=$1 AND route=$2 AND fact NOT IN ('model_id','listed')`, source, base.Route); err != nil {
					return err
				}
			}
			base.Fact, base.FieldPath = "model_id", "model"
			base.Value, _ = json.Marshal(model.Model)
			facts = append(facts, base)
			base.Fact, base.FieldPath, base.Value = "listed", "models", json.RawMessage("true")
			facts = append(facts, base)
			for name, f := range model.Facts {
				base.Fact, base.FieldPath, base.Value = name, f.Path, f.Value
				facts = append(facts, base)
			}
		}
		if err = RecordCatalogFacts(ctx, tx, p.TenantID, facts); err != nil {
			return err
		}
		// Only explicitly reported levels become advertised tuples. ID-only
		// models acquire no default pin, and discovery never grants activation.
		remaining := maxRefreshObservations
		for _, model := range in.Models {
			var levels []string
			if field, ok := model.Facts["native_levels"]; ok {
				if err = json.Unmarshal(field.Value, &levels); err != nil {
					return err
				}
			}
			for _, level := range levels {
				if remaining == 0 {
					profilesTruncated = true
					continue
				}
				remaining--
				o := Observation{Harness: harness, Model: model.Model, Effort: level, Status: "advertised"}
				if err = observe(ctx, tx, p.TenantID, o, source); err != nil {
					return err
				}
				var exists bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE harness=$1 AND model=$2 AND effort=$3)`, harness, model.Model, level).Scan(&exists); err != nil {
					return err
				}
				if exists {
					continue
				}
				pin, mapped := observedPin(o)
				if !cfg.AutoAddProfiles || !mapped || KnownInvalid(o.Model) {
					proposed++
					continue
				}
				pin.Source = "harness"
				if _, err = insertObservedProfile(ctx, tx, p.TenantID, pin); err != nil {
					return err
				}
				added++
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE model_harness_probes SET state=$2,completed_at=$3,model_count=$4,report_digest=$5,lease_until=NULL WHERE account_id=$1`, account, in.State, now, len(in.Models), hash); err != nil {
			return err
		}
		return writeEvent(ctx, tx, current, "model.harness_probed", nil, map[string]any{"account_id": account, "harness": harness, "request_id": request, "state": in.State, "models": len(in.Models), "added": added, "proposed": proposed, "profiles_truncated": profilesTruncated})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"recorded": true, "added": added, "proposed": proposed, "profiles_truncated": profilesTruncated})
}

func harnessProbeStatus(ctx context.Context, tx pgx.Tx) ([]map[string]any, bool, error) {
	rows, err := tx.Query(ctx, `SELECT p.account_id::text,a.harness,p.requested_at,p.completed_at,p.state,p.model_count FROM model_harness_probes p JOIN agent_accounts a ON a.tenant_id=p.tenant_id AND a.id=p.account_id ORDER BY p.account_id LIMIT 65`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, harness, state string
		var requested time.Time
		var completed *time.Time
		var count int
		if err = rows.Scan(&id, &harness, &requested, &completed, &state, &count); err != nil {
			return nil, false, err
		}
		out = append(out, map[string]any{"account_id": id, "harness": harness, "requested_at": requested, "completed_at": completed, "state": state, "seen": count})
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	limited := len(out) > 64
	if limited {
		out = out[:64]
	}
	return out, limited, nil
}
