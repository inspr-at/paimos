// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// The browser's fallback imports this same document. The endpoint adds live
// workspace limits and the visible project's override; it never mutates states.
//
//go:embed status_definitions.json
var statusDefinitions []byte

type statusRule struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days,omitempty"`
}
type statusDefinition struct {
	State   string   `json:"state"`
	Label   string   `json:"label"`
	Meaning string   `json:"meaning"`
	Hint    string   `json:"hint"`
	SetBy   string   `json:"set_by"`
	Exit    bool     `json:"exit"`
	Rules   []string `json:"rules"`
}
type statusAutopilot struct {
	Enabled          bool                  `json:"enabled"`
	EffectiveEnabled bool                  `json:"effective_enabled"`
	ProjectMode      string                `json:"project_mode"`
	Rules            map[string]statusRule `json:"rules"`
}
type statusHelp struct {
	Definitions []statusDefinition `json:"definitions"`
	Queued      struct {
		Label    string `json:"label"`
		Meaning  string `json:"meaning"`
		IsStatus bool   `json:"is_status"`
	} `json:"queued"`
	Autopilot statusAutopilot `json:"autopilot"`
	Triage    struct {
		Mode      string `json:"mode"`
		Available bool   `json:"available"`
	} `json:"triage"`
	LimitsSource string `json:"limits_source"`
	ProjectID    string `json:"project_id,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
}

func defaultStatusHelp() statusHelp {
	var help statusHelp
	// Immutable, checked-in JSON validated in TestStatusDefinitions.
	if err := json.Unmarshal(statusDefinitions, &help); err != nil {
		panic(err)
	}
	return help
}

func resolveStatusHelp(help *statusHelp) {
	help.Autopilot.EffectiveEnabled = help.Autopilot.Enabled
	switch help.Autopilot.ProjectMode {
	case "on":
		help.Autopilot.EffectiveEnabled = true
	case "off":
		help.Autopilot.EffectiveEnabled = false
	}
	accept := help.Autopilot.Rules["accept"]
	for i := range help.Definitions {
		def := &help.Definitions[i]
		if def.State != "accepted" {
			continue
		}
		def.SetBy = "Person"
		def.Hint = "Confirmed by a person or customer"
		if help.Autopilot.EffectiveEnabled && accept.Enabled {
			unit := "days"
			if accept.Days == 1 {
				unit = "day"
			}
			def.SetBy = fmt.Sprintf("Person, or the %d-day rule", accept.Days)
			def.Hint = fmt.Sprintf("Confirmed by a person or customer, or automatically %d %s after delivery", accept.Days, unit)
		}
	}
}

func (m *Module) handleStatusHelp(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	help := defaultStatusHelp()
	projectID := r.URL.Query().Get("project_id")
	if r.URL.Query().Has("project_id") {
		var valid bool
		projectID, valid = parseUUID(projectID)
		if !valid {
			writeErr(w, badRequest("invalid project_id"))
			return
		}
	}
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if projectID != "" {
			// Project RLS prevents learning an inaccessible project's name or mode.
			if err := tx.QueryRow(ctx, `SELECT n.title FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1::uuid AND k.slug='project' AND n.deleted_at IS NULL`, projectID).Scan(&help.ProjectName); err != nil {
				if err == pgx.ErrNoRows {
					return notFound("project not found")
				}
				return err
			}
			help.ProjectID = projectID
		}
		return loadStatusHelpSettings(ctx, tx, &help)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	resolveStatusHelp(&help)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, help)
}

// Part B owns settings persistence. Before its migration exists this is the
// approved defaults; an error reading installed settings must not masquerade as
// defaults. The exact storage adapter is kept here at the package boundary.
func loadStatusHelpSettings(ctx context.Context, tx pgx.Tx, help *statusHelp) error {
	var installed bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('status_autopilot_settings') IS NOT NULL`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	help.LimitsSource = "workspace"
	var rules []byte
	err := tx.QueryRow(ctx, `SELECT enabled,rules FROM status_autopilot_settings WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&help.Autopilot.Enabled, &rules)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil {
		var live map[string]statusRule
		if err := json.Unmarshal(rules, &live); err != nil {
			return err
		}
		if len(live) != len(help.Autopilot.Rules) {
			return fmt.Errorf("invalid status autopilot rules")
		}
		for key := range help.Autopilot.Rules {
			rule, ok := live[key]
			if !ok || key == "publish" && rule.Days != 0 || key != "publish" && (rule.Days < 1 || rule.Days > 365) {
				return fmt.Errorf("invalid status autopilot rule %s", key)
			}
		}
		help.Autopilot.Rules = live
	}
	if help.ProjectID != "" {
		err := tx.QueryRow(ctx, `SELECT mode FROM status_autopilot_projects WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND project_id=$1::uuid`, help.ProjectID).Scan(&help.Autopilot.ProjectMode)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		switch help.Autopilot.ProjectMode {
		case "inherit", "on", "off":
		default:
			return fmt.Errorf("invalid status autopilot project mode")
		}
	}
	return nil
}
