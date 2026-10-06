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

// leadNames is the word people read for a project's lead (AEON-791). Inside
// PAIMOS it is always the lead; blank names use Lead and Leads.
type leadNames struct {
	Singular string `json:"singular"`
	Plural   string `json:"plural"`
}
type workVocabulary struct {
	Revision int64       `json:"revision"`
	Leaf     workLevel   `json:"leaf"`
	Levels   []workLevel `json:"levels"`
	Lead     *leadNames  `json:"lead,omitempty"`
}

var workIcons = map[string]bool{"": true, "ticket": true, "epic": true, "task": true, "layers": true, "tree": true, "folder": true, "check": true, "box": true}

func validName(name string, max int) bool {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > max || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (v workVocabulary) validate() error {
	if v.Revision < 0 || len(v.Levels) > 32 {
		return badRequest("invalid vocabulary")
	}
	for _, level := range append([]workLevel{v.Leaf}, v.Levels...) {
		if !validName(level.Name, 60) || !workIcons[level.Icon] {
			return badRequest("invalid work name or icon")
		}
	}
	if v.Lead != nil && (!validName(v.Lead.Singular, 40) || !validName(v.Lead.Plural, 40)) {
		return badRequest("invalid agent name")
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
	var input struct {
		Revision *int64 `json:"revision"`
		Leaf     *struct {
			Name *string `json:"name"`
			Icon *string `json:"icon"`
		} `json:"leaf"`
		Levels []struct {
			Name *string `json:"name"`
			Icon *string `json:"icon"`
		} `json:"levels"`
		Lead *struct {
			Singular *string `json:"singular"`
			Plural   *string `json:"plural"`
		} `json:"lead"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeError(w, 400, "invalid vocabulary")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, 400, "invalid vocabulary")
		return
	}
	if input.Revision == nil || input.Leaf == nil || input.Leaf.Name == nil || input.Leaf.Icon == nil || input.Levels == nil {
		writeError(w, 400, "revision, leaf name/icon and levels are required")
		return
	}
	in := workVocabulary{Revision: *input.Revision, Leaf: workLevel{Name: *input.Leaf.Name, Icon: *input.Leaf.Icon}, Levels: []workLevel{}}
	for _, level := range input.Levels {
		if level.Name == nil || level.Icon == nil {
			writeError(w, 400, "level name and icon are required")
			return
		}
		in.Levels = append(in.Levels, workLevel{Name: *level.Name, Icon: *level.Icon})
	}
	keepLead := input.Lead == nil
	if !keepLead {
		if input.Lead.Singular == nil || input.Lead.Plural == nil {
			writeError(w, 400, "lead singular and plural are required")
			return
		}
		if *input.Lead.Singular != "" || *input.Lead.Plural != "" {
			in.Lead = &leadNames{Singular: *input.Lead.Singular, Plural: *input.Lead.Plural}
		}
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
		// A writer that omits lead (older clients, the Work vocabulary card)
		// keeps the workspace's agent names.
		if keepLead {
			in.Lead = prior.Lead
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
