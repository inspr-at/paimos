// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type ActionKind struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}

func defaultActionKinds() map[string]ActionKind {
	labels := map[string]string{"deploy": "Deployment", "trade": "Trade", "money": "Money transfer", "secrets": "Use existing secret authority", "production_change": "Production change", "external_message": "External message on someone's behalf"}
	kinds := make(map[string]ActionKind, len(labels))
	for key, label := range labels {
		kinds[key] = ActionKind{Kind: key, Label: label, Enabled: true}
	}
	return kinds
}

func actionKindTx(ctx context.Context, tx pgx.Tx, kind string) (ActionKind, error) {
	var out ActionKind
	err := tx.QueryRow(ctx, `SELECT kind,label,enabled,revision FROM action_approval_kinds WHERE kind=$1`, kind).Scan(&out.Kind, &out.Label, &out.Enabled, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		if value, ok := defaultActionKinds()[kind]; ok {
			return value, nil
		}
		return ActionKind{}, fail(400, "unknown action kind; configure it before requesting approval")
	}
	return out, err
}

func (m *Module) handleActionKinds(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := []ActionKind{}
	err := m.inTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := actionRequire(ctx, tx, p, "settings.read", ""); err != nil {
			return err
		}
		kinds := defaultActionKinds()
		rows, err := tx.Query(ctx, `SELECT kind,label,enabled,revision FROM action_approval_kinds ORDER BY kind LIMIT 65`)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var kind ActionKind
			if err = rows.Scan(&kind.Kind, &kind.Label, &kind.Enabled, &kind.Revision); err != nil {
				return err
			}
			count++
			if count > 64 {
				return fail(409, "action kind configuration exceeds the 64 override bound")
			}
			kinds[kind.Kind] = kind
		}
		if err = rows.Err(); err != nil {
			return err
		}
		for _, kind := range kinds {
			out = append(out, kind)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
		return nil
	})
	writeResult(w, 200, out, err)
}

func (m *Module) handlePutActionKind(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		writeError(w, 403, "action kind configuration requires a person session")
		return
	}
	var in struct {
		Label    string `json:"label"`
		Enabled  *bool  `json:"enabled"`
		Revision *int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	kind := r.PathValue("kind")
	in.Label = strings.TrimSpace(in.Label)
	if !actionKindPattern.MatchString(kind) || in.Label == "" || !utf8.ValidString(in.Label) || utf8.RuneCountInString(in.Label) > 120 || strings.ContainsRune(in.Label, 0) || in.Enabled == nil || in.Revision == nil || *in.Revision < 0 || *in.Revision == 1<<63-1 {
		writeError(w, 400, "invalid action kind configuration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := ActionKind{Kind: kind, Label: in.Label, Enabled: *in.Enabled, Revision: *in.Revision + 1}
	err := m.inTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, db.TenantFenceSQL); err != nil {
			return err
		}
		if err := requirePerson(ctx, tx, p, "action kind configuration requires a person"); err != nil {
			return err
		}
		if err := actionRequire(ctx, tx, p, "settings.manage", ""); err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRow(ctx, `SELECT revision FROM action_approval_kinds WHERE kind=$1`, kind).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != *in.Revision {
			return fail(409, "action kind revision changed")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT kind FROM action_approval_kinds LIMIT 64) k`).Scan(&count); err != nil {
				return err
			}
			if count >= 64 {
				return fail(409, "action kind configuration is limited to 64 overrides")
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO action_approval_kinds(tenant_id,kind,label,enabled,revision) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(tenant_id,kind) DO UPDATE SET label=excluded.label,enabled=excluded.enabled,revision=excluded.revision`, p.TenantID, kind, in.Label, *in.Enabled, out.Revision); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "action_request.kind_configured", After: out})
		return err
	})
	writeResult(w, 200, out, err)
}
