// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RefreshSettings struct {
	AgentReportsEnabled bool `json:"agent_reports_enabled"`
	AutoAddProfiles     bool `json:"auto_add_profiles"`
	APIEnabled          bool `json:"api_enabled"`
	IntervalMinutes     int  `json:"interval_minutes"`
}
type DiscoverySource struct {
	AccountID string `json:"account_id"`
	Vendor    string `json:"vendor"`
}
type SourceResult struct {
	DiscoverySource
	State string `json:"state"`
	Seen  int    `json:"seen"`
}
type RefreshResult struct {
	At             time.Time      `json:"at"`
	NewLines       []string       `json:"new_lines"`
	Added          int            `json:"added"`
	AcceptedModels []string       `json:"accepted_models,omitempty"`
	Proposed       int            `json:"proposed"`
	Sources        []SourceResult `json:"sources"`
	LadderChanged  bool           `json:"ladder_changed"`
}

// NewWithVault uses an independent derivation of the existing server key.
// Without a stable key API credential storage is unavailable, not plaintext.
func NewWithVault(pool *pgxpool.Pool, serverKey []byte) *Module {
	m := &Module{pool: pool, discovery: discoveryClient()}
	if len(serverKey) >= 32 {
		mac := hmac.New(sha256.New, serverKey)
		mac.Write([]byte("aeon-model-discovery-v1"))
		m.vaultKey = mac.Sum(nil)
	}
	return m
}

func settings(ctx context.Context, tx pgx.Tx) (RefreshSettings, error) {
	var out RefreshSettings
	err := tx.QueryRow(ctx, `SELECT agent_reports_enabled,auto_add_profiles,api_enabled,interval_minutes FROM model_refresh_settings`).Scan(&out.AgentReportsEnabled, &out.AutoAddProfiles, &out.APIEnabled, &out.IntervalMinutes)
	return out, err
}

func (m *Module) authorized(r *http.Request, p tenant.Principal, perm string) error {
	if authz.Require(authz.BindPool(r.Context(), m.pool), perm, authz.Scope{}) != nil {
		return fail(403, "permission denied")
	}
	return nil
}

func (m *Module) reports(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.authorized(r, p, "models.report"); err != nil {
		writeErr(w, err)
		return
	}
	var in []Observation
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out ReportResult
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "models.report", authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		var err error
		out, err = recordReports(r.Context(), tx, p, in, "agent", "")
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) refresh(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.authorized(r, p, "models.refresh"); err != nil {
		writeErr(w, err)
		return
	}
	// No arbitrary writes can be smuggled into a refresh trigger.
	if r.Body != nil {
		var in map[string]json.RawMessage
		err := decodeJSON(w, r, &in)
		if err != nil && r.ContentLength != 0 {
			writeErr(w, err)
			return
		}
		if len(in) > 0 {
			writeErr(w, fail(400, "refresh accepts no fields"))
			return
		}
	}
	out, err := m.runRefresh(r.Context(), p, false)
	if err != nil {
		var cooldown *refreshCooldown
		if errors.As(err, &cooldown) {
			w.Header().Set("Retry-After", fmt.Sprint(cooldown.RetryAfter))
			httpapi.WriteJSON(w, 429, map[string]any{"error": "model refresh cooldown", "retry_after": cooldown.RetryAfter})
			return
		}
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

const maxRefreshObservations = 200

type discoveryInput struct {
	DiscoverySource
	cipher []byte
}

type discoveredSource struct {
	input discoveryInput
	ids   []string
	fresh bool
}

// Reserve before making network calls. Only short catalog transactions hold
// the tenant lock; both workers and the scheduler obey the same interval.
func (m *Module) runRefresh(ctx context.Context, p tenant.Principal, scheduled bool) (RefreshResult, error) {
	out := RefreshResult{Sources: []SourceResult{}, NewLines: []string{}}
	inputs := []discoveryInput{}
	reserved := false
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) (err error) {
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if !scheduled {
			if err := authz.RequireTx(ctx, tx, p, "models.refresh", authz.Scope{}); err != nil {
				return fail(403, "permission denied")
			}
		}
		pending, err := prepareCatalogDeferred(ctx, tx, p)
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				err = flushCatalogChanges(ctx, tx, p, pending)
			}
		}()
		if err := catalogLock(ctx, tx); err != nil {
			return err
		}
		cfg, err := settings(ctx, tx)
		if err != nil {
			return err
		}
		clock := m.validationClock
		if clock == nil {
			clock = validationNow
		}
		if out.At, err = clock(ctx, tx); err != nil {
			return err
		}
		due, err := applyScheduledRetirements(ctx, tx, out.At)
		if err != nil {
			return err
		}
		pending = append(pending, due...)
		var last, manual *time.Time
		if err := tx.QueryRow(ctx, `SELECT last_run_at,last_manual_run_at FROM model_refresh_settings FOR NO KEY UPDATE`).Scan(&last, &manual); err != nil {
			return err
		}
		if !scheduled && p.Kind == tenant.Person && manual != nil && out.At.Sub(*manual) < 5*time.Minute {
			return &refreshCooldown{RetryAfter: max(1, int(math.Ceil((5*time.Minute - out.At.Sub(*manual)).Seconds())))}
		}
		if (scheduled || p.Kind != tenant.Person) && last != nil && out.At.Sub(*last) < time.Duration(cfg.IntervalMinutes)*time.Minute {
			if scheduled {
				return nil
			}
			return fail(429, "model refresh interval has not elapsed")
		}
		if cfg.APIEnabled {
			rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c.vendor) c.account_id::text,c.vendor,c.ciphertext FROM model_discovery_credentials c JOIN agent_accounts a ON a.tenant_id=c.tenant_id AND a.id=c.account_id WHERE a.state='available' ORDER BY c.vendor,c.account_id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var c discoveryInput
				if err := rows.Scan(&c.AccountID, &c.Vendor, &c.cipher); err != nil {
					return err
				}
				inputs = append(inputs, c)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE model_refresh_settings SET last_run_at=$1,last_manual_run_at=CASE WHEN $2 THEN $1 ELSE last_manual_run_at END`, out.At, !scheduled && p.Kind == tenant.Person)
		reserved = err == nil
		return err
	})
	if err != nil || !reserved {
		return out, err
	}

	// One deadline covers all vendors and pages, never one timeout per page.
	fetchCtx, cancel := context.WithTimeout(ctx, discoveryDeadline)
	defer cancel()
	fetched := make([]discoveredSource, 0, len(inputs))
	client := m.discovery
	if client == nil {
		client = discoveryClient()
	}
	for _, input := range inputs {
		result := discoveredSource{input: input}
		var allowed bool
		// A person may disable discovery or revoke a key while a preceding
		// vendor is responding. Recheck before starting the next vendor.
		err := m.in(fetchCtx, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(fetchCtx, `SELECT api_enabled AND EXISTS(SELECT 1 FROM model_discovery_credentials c JOIN agent_accounts a ON a.tenant_id=c.tenant_id AND a.id=c.account_id WHERE c.account_id=$1 AND c.vendor=$2 AND c.ciphertext=$3 AND a.state='available') FROM model_refresh_settings`, input.AccountID, input.Vendor, input.cipher).Scan(&allowed)
		})
		if err == nil && allowed && fetchCtx.Err() == nil {
			key, err := linkvault.Decrypt(m.vaultKey, p.TenantID, "models/"+input.AccountID+"/"+input.Vendor, input.cipher)
			if err == nil {
				result.ids, err = listVendorModels(fetchCtx, client, input.Vendor, key)
				result.fresh = err == nil
			}
		}
		fetched = append(fetched, result)
	}

	err = m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		if err := catalogLock(ctx, tx); err != nil {
			return err
		}
		if !scheduled {
			if err := authz.RequireTx(ctx, tx, p, "models.refresh", authz.Scope{}); err != nil {
				return fail(403, "permission denied")
			}
		}
		cfg, err := settings(ctx, tx)
		if err != nil {
			return err
		}
		var current time.Time
		if err := tx.QueryRow(ctx, `SELECT last_run_at FROM model_refresh_settings FOR UPDATE`).Scan(&current); err != nil {
			return err
		}
		if !current.Equal(out.At) {
			return fail(409, "model refresh superseded")
		}
		remaining := maxRefreshObservations
		for _, fetched := range fetched {
			input := fetched.input
			result := SourceResult{DiscoverySource: input.DiscoverySource, State: "stale"}
			// Do not publish results from a revoked key/account or disabled job.
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_discovery_credentials c JOIN agent_accounts a ON a.tenant_id=c.tenant_id AND a.id=c.account_id WHERE c.account_id=$1 AND c.vendor=$2 AND c.ciphertext=$3 AND a.state='available')`, input.AccountID, input.Vendor, input.cipher).Scan(&valid); err != nil {
				return err
			}
			if cfg.APIEnabled && valid && fetched.fresh {
				observations := discoveryObservations(input.Vendor, out.At.UTC().Format(time.RFC3339Nano)+"/"+input.AccountID, fetched.ids)
				result.State, result.Seen = "fresh", len(fetched.ids)
				if cfg.AutoAddProfiles {
					var limited bool
					var err error
					observations, limited, err = successorObservations(ctx, tx, observations, remaining)
					if err != nil {
						return err
					}
					if limited {
						result.State = "limited"
					}
				}
				if len(observations) > remaining {
					result.State = "limited"
					observations = observations[:remaining]
				}
				remaining -= len(observations)
				for _, o := range observations {
					if err := observe(ctx, tx, p.TenantID, o, "api:"+input.Vendor); err != nil {
						return err
					}
					if KnownInvalid(o.Model) {
						continue
					}
					pin, mapped := observedPin(o)
					if cfg.AutoAddProfiles && mapped {
						accepted, err := acceptUsedSuccessor(ctx, tx, p, pin)
						if err != nil {
							return err
						}
						if accepted {
							out.Added++
							// added counts profiles, one per effort. Acceptance is the
							// distinct model, so four efforts of one successor are one version.
							id := boardLineID(Profile{Family: pin.Family, Harness: pin.Harness, Model: pin.Model})
							if !slices.Contains(out.AcceptedModels, id) {
								out.AcceptedModels = append(out.AcceptedModels, id)
							}
							continue
						}
					}
					var exists bool
					if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE harness=$1 AND model=$2 AND effort=$3)`, o.Harness, o.Model, o.Effort).Scan(&exists); err != nil {
						return err
					}
					if exists {
						continue
					}
					pin, mapped = observedPin(o)
					if cfg.AutoAddProfiles && mapped {
						family, line, _ := ProfileLine(Profile{Family: pin.Family, Harness: pin.Harness, Model: pin.Model})
						id := family + ":" + line
						var knownLine bool
						if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE aeon_model_board_line(family,harness,model)=$1)`, id).Scan(&knownLine); err != nil {
							return err
						}
						if _, err := insertObservedProfile(ctx, tx, p.TenantID, pin); err != nil {
							return err
						}
						if !knownLine {
							out.NewLines = append(out.NewLines, id)
						}
						out.Added++
					} else {
						out.Proposed++
					}
				}
			}
			// Never persist vendor error bodies, raw headers, URLs or key material.
			out.Sources = append(out.Sources, result)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE model_refresh_settings SET last_result=$1::jsonb`, string(raw)); err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, "model.catalog_refreshed", nil, out)
	})
	return out, err
}

func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in *struct {
		RefreshSettings
		AccountUseRevision *int64 `json:"account_use_revision,omitempty"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in == nil || in.IntervalMinutes < 60 || in.IntervalMinutes > 43200 {
		writeErr(w, fail(400, "interval_minutes must be from 60 to 43200"))
		return
	}
	if in.APIEnabled && len(m.vaultKey) == 0 {
		writeErr(w, fail(503, "model discovery vault unavailable"))
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) (err error) {
		if err := db.LockTree(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		pending, err := prepareCatalogDeferred(r.Context(), tx, p)
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				err = flushCatalogChanges(r.Context(), tx, p, pending)
			}
		}()
		if err := catalogLock(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "models.manage", authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		before, err := settings(r.Context(), tx)
		if err != nil {
			return err
		}
		if before == in.RefreshSettings {
			return nil
		}
		if before.AutoAddProfiles != in.AutoAddProfiles {
			if p.Kind != tenant.Person || authz.RequireTx(r.Context(), tx, p, accountuse.Permission, authz.Scope{}) != nil {
				return authz.ErrForbidden
			}
			if in.AccountUseRevision == nil {
				return fail(409, "account_use_revision_conflict")
			}
			rules, err := accountuse.ReadRules(r.Context(), tx)
			if err != nil {
				return err
			}
			if in.AutoAddProfiles {
				rules.NewModels = "allow"
			} else {
				rules.NewModels = "shipped_only"
			}
			if _, err := accountuse.WriteRules(r.Context(), tx, p, *in.AccountUseRevision, rules.RuleValues); err != nil {
				var conflict *accountuse.Error
				if errors.As(err, &conflict) {
					return fail(conflict.Status, conflict.Message)
				}
				return err
			}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE model_refresh_settings SET agent_reports_enabled=$1,api_enabled=$2,interval_minutes=$3`, in.AgentReportsEnabled, in.APIEnabled, in.IntervalMinutes); err != nil {
			return err
		}
		stored, err := settings(r.Context(), tx)
		if err != nil {
			return err
		}
		in.RefreshSettings = stored
		return writeEvent(r.Context(), tx, p, "model.refresh_settings_changed", before, stored)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, in.RefreshSettings)
}

func (m *Module) putCredential(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	if len(m.vaultKey) == 0 {
		writeErr(w, fail(503, "model discovery vault unavailable"))
		return
	}
	var in struct {
		Vendor string `json:"vendor"`
		Key    string `json:"api_key"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) || vendorURLs[in.Vendor] == "" || len(in.Key) > 4096 || strings.ContainsAny(in.Key, "\r\n\x00") {
		writeErr(w, fail(400, "invalid discovery credential"))
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := catalogLock(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "models.manage", authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		var harness string
		if err := tx.QueryRow(r.Context(), `SELECT harness FROM agent_accounts WHERE id=$1`, id).Scan(&harness); err != nil {
			if err == pgx.ErrNoRows {
				return fail(404, "account not found")
			}
			return err
		}
		expected := map[string]string{"openai": "codex", "xai": "grok", "anthropic": "claude", "openrouter": "pi"}[in.Vendor]
		if harness != expected {
			return fail(400, "vendor must match account harness")
		}
		if in.Key == "" {
			if _, err := tx.Exec(r.Context(), `DELETE FROM model_discovery_credentials WHERE account_id=$1`, id); err != nil {
				return err
			}
		} else {
			cipher, err := linkvault.Encrypt(m.vaultKey, p.TenantID, "models/"+id+"/"+in.Vendor, in.Key)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO model_discovery_credentials(tenant_id,account_id,vendor,ciphertext) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,account_id) DO UPDATE SET vendor=EXCLUDED.vendor,ciphertext=EXCLUDED.ciphertext`, p.TenantID, id, in.Vendor, cipher); err != nil {
				return err
			}
		}
		return writeEvent(r.Context(), tx, p, "model.discovery_credential_changed", nil, map[string]any{"account_id": id, "vendor": in.Vendor, "configured": in.Key != ""})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]bool{"configured": in.Key != ""})
}

func (m *Module) refreshStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.authorized(r, p, "models.read"); err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"sources": []DiscoverySource{}, "observations": []json.RawMessage{}}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) (err error) {
		pending, err := prepareCatalogDeferred(r.Context(), tx, p)
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				err = flushCatalogChanges(r.Context(), tx, p, pending)
			}
		}()
		cfg, err := settings(r.Context(), tx)
		if err != nil {
			return err
		}
		out["settings"] = cfg
		var at *time.Time
		var raw json.RawMessage
		if err := tx.QueryRow(r.Context(), `SELECT last_run_at,last_result FROM model_refresh_settings`).Scan(&at, &raw); err != nil {
			return err
		}
		out["last_run_at"] = at
		out["last_result"] = raw
		rows, err := tx.Query(r.Context(), `SELECT account_id::text,vendor FROM model_discovery_credentials ORDER BY vendor,account_id`)
		if err != nil {
			return err
		}
		sources := []DiscoverySource{}
		for rows.Next() {
			var s DiscoverySource
			if err := rows.Scan(&s.AccountID, &s.Vendor); err != nil {
				rows.Close()
				return err
			}
			sources = append(sources, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		out["sources"] = sources
		rows, err = tx.Query(r.Context(), `SELECT jsonb_build_object('harness',o.harness,'model',o.model,'effort',o.effort,'last_seen_at',o.last_seen_at,'last_working_at',o.last_working_at,'last_failing_at',o.last_failing_at,'failures',o.failures,'suppressed_until',o.suppressed_until,'source',o.source,'pending',NOT EXISTS(SELECT 1 FROM model_profiles p WHERE p.harness=o.harness AND p.model=o.model AND p.effort=o.effort AND p.enabled)) FROM model_observations o ORDER BY o.last_seen_at DESC,o.harness,o.model,o.effort LIMIT 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		observations := []json.RawMessage{}
		for rows.Next() {
			var o json.RawMessage
			if err := rows.Scan(&o); err != nil {
				return err
			}
			observations = append(observations, o)
		}
		out["observations"] = observations
		return rows.Err()
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Run upgrades all existing tenants on startup, then refreshes on each tenant's
// configured interval. A minute poll also picks up tenants created after startup.
func (m *Module) Run(ctx context.Context) {
	m.sweep(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.sweep(ctx)
		}
	}
}
func (m *Module) sweep(ctx context.Context) {
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		slog.Warn("model refresh tenant scan failed")
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		work, cancel := context.WithTimeout(ctx, 90*time.Second)
		var actor string
		err := db.InTenant(db.NoProjects(work, "model catalog scheduler"), m.pool, id, func(tx pgx.Tx) error {
			return tx.QueryRow(work, `SELECT aeon_authz_system_actor($1::uuid)::text`, id).Scan(&actor)
		})
		if err == nil {
			p := tenant.Principal{TenantID: id, ID: actor, Kind: tenant.Agent}
			_, err = m.runRefresh(db.NoProjects(work, "model catalog scheduler"), p, true)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("model catalog refresh failed", "tenant_id", id)
		}
	}
}
