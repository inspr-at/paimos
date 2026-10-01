// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type hostLabel struct {
	Host  string `json:"host"`
	Label string `json:"label"`
}

func (m *Module) listHostLabels(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	rows, err := tx.Query(r.Context(), `SELECT host,label FROM person_host_labels WHERE person_id=$1 ORDER BY host`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hostLabel{}
	for rows.Next() {
		var label hostLabel
		if err := rows.Scan(&label.Host, &label.Label); err != nil {
			return nil, err
		}
		out = append(out, label)
	}
	return out, rows.Err()
}

func validHostText(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= 128 && !strings.ContainsFunc(value, unicode.IsControl)
}

func (m *Module) putHostLabel(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required")
	}
	var in struct {
		Host  string          `json:"host"`
		Label json.RawMessage `json:"label"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	var label *string
	if in.Label == nil || json.Unmarshal(in.Label, &label) != nil {
		return nil, workorders.Fail(400, "label is required; use null to reset")
	}
	if !validHostText(in.Host) {
		return nil, workorders.Fail(400, "host must be 1–128 characters without surrounding whitespace or control characters")
	}
	if label != nil {
		*label = strings.TrimSpace(*label)
		if !validHostText(*label) {
			return nil, workorders.Fail(400, "label must be 1–128 characters without control characters")
		}
	}
	// The session query observes project visibility through RLS. A guessed host
	// in another tenant or project cannot create an override or reveal its label.
	var visible bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE host=$1)`, in.Host).Scan(&visible); err != nil {
		return nil, err
	}
	if !visible {
		return nil, workorders.Fail(404, "host not found")
	}
	if label == nil {
		_, err := tx.Exec(r.Context(), `DELETE FROM person_host_labels WHERE person_id=$1 AND host=$2`, p.ID, in.Host)
		return hostLabel{in.Host, in.Host}, err
	}
	_, err := tx.Exec(r.Context(), `INSERT INTO person_host_labels(tenant_id,person_id,host,label) VALUES($1,$2,$3,$4)
		ON CONFLICT (tenant_id,person_id,host) DO UPDATE SET label=EXCLUDED.label`, p.TenantID, p.ID, in.Host, *label)
	return hostLabel{in.Host, *label}, err
}
