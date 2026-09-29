// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func settingKind(kind string) bool { return kind == "rename" || kind == "model" || kind == "effort" }
func validSetting(kind, value string) bool {
	if !settingKind(kind) {
		return value == ""
	}
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	return kind != "effort" || supportedEffort(value)
}
func supportedEffort(value string) bool {
	return slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, value)
}

type settingModel struct {
	Model   string   `json:"model"`
	Efforts []string `json:"efforts"`
}

func settingModels(ctx context.Context, tx pgx.Tx, s Session) ([]settingModel, error) {
	out := []settingModel{}
	if s.RunID == nil || s.Harness != "claude" {
		return out, nil
	}
	models, err := agentaccounts.ModelsForRun(ctx, tx, *s.RunID, s.Harness)
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		m := settingModel{Model: model.Model, Efforts: []string{}}
		for _, e := range model.Efforts {
			if supportedEffort(e.Effort) && !slices.Contains(m.Efforts, e.Effort) {
				m.Efforts = append(m.Efforts, e.Effort)
			}
		}
		if len(m.Efforts) > 0 {
			out = append(out, m)
		}
	}
	return out, nil
}
func validateSettingCatalog(ctx context.Context, tx pgx.Tx, s Session, kind, value string) error {
	if kind != "model" && kind != "effort" {
		return nil
	}
	models, err := settingModels(ctx, tx, s)
	if err != nil {
		return err
	}
	model, effort := "", ""
	if s.Model != nil {
		model = *s.Model
	}
	if s.ReasoningEffort != nil {
		effort = *s.ReasoningEffort
	}
	if kind == "model" {
		model = value
	} else {
		effort = value
	}
	for _, m := range models {
		if m.Model == model && slices.Contains(m.Efforts, effort) {
			return nil
		}
	}
	return workorders.Fail(400, "model and effort must be supported by this session's enrolled account catalog")
}
func (m *Module) managedSettings(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person may control a managed session")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.control", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil, workorders.Fail(403, "harness.control permission required")
		}
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	if s.Management != "managed" || s.Harness != "claude" {
		return nil, workorders.Fail(409, "managed settings unavailable for this adapter")
	}
	models, err := settingModels(r.Context(), tx, s)
	return map[string]any{"models": models}, err
}
