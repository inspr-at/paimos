// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type workLevel struct {
	Name string `json:"name"`
	Icon string `json:"icon"`
}
type workVocabulary struct {
	Revision int64       `json:"revision"`
	Leaf     workLevel   `json:"leaf"`
	Levels   []workLevel `json:"levels"`
}

var workIcons = map[string]bool{"": true, "ticket": true, "epic": true, "task": true, "layers": true, "tree": true, "folder": true, "check": true, "box": true}

func (v workVocabulary) validate() error {
	if v.Revision < 0 || len(v.Levels) > 32 {
		return badRequest("invalid vocabulary")
	}
	for _, level := range append([]workLevel{v.Leaf}, v.Levels...) {
		if !utf8.ValidString(level.Name) || utf8.RuneCountInString(level.Name) > 60 || strings.TrimSpace(level.Name) != level.Name || !workIcons[level.Icon] {
			return badRequest("invalid work name or icon")
		}
		for _, r := range level.Name {
			if unicode.IsControl(r) {
				return badRequest("invalid work name")
			}
		}
	}
	return nil
}
func loadVocabulary(ctx context.Context, tx pgx.Tx) (workVocabulary, error) {
	v := workVocabulary{Levels: []workLevel{}}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,vocabulary FROM work_vocabulary WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&v.Revision, &raw)
	if err == pgx.ErrNoRows {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	rev := v.Revision
	err = json.Unmarshal(raw, &v)
	v.Revision = rev
	if v.Levels == nil {
		v.Levels = []workLevel{}
	}
	return v, err
}
func (m *Module) handleGetVocabulary(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	var v workVocabulary
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = loadVocabulary(ctx, tx)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (m *Module) handlePutVocabulary(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "person required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in workVocabulary
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, 400, "invalid vocabulary")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, 400, "invalid vocabulary")
		return
	}
	if in.Levels == nil {
		writeError(w, 400, "levels must be an array")
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		// db.InTenant already enters pairing -> tree -> tenant for derivation.
		// This tenant fence is also used by access changes. No locks follow events.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return &httpError{status: 403, msg: "permission denied"}
		}
		prior, err := loadVocabulary(ctx, tx)
		if err != nil {
			return err
		}
		if prior.Revision != in.Revision {
			return conflict("work vocabulary changed; reload before saving")
		}
		in.Revision++
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO work_vocabulary(tenant_id,revision,vocabulary) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET revision=EXCLUDED.revision,vocabulary=EXCLUDED.vocabulary`, p.TenantID, in.Revision, raw); err != nil {
			return err
		}
		before, _ := json.Marshal(prior)
		return m.events.WriteEvent(ctx, tx, Event{ActorPrincipalID: p.ID, Type: "tenant.work_vocabulary_updated", Before: before, After: raw})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, in)
}
