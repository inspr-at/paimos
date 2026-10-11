// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/modelprefs"
)

// Label is the short display name of a profile: the harness, the model
// without its vendor prefix, and the effort ("Codex astra · xhigh"). It is
// derived at display time and never stored on a ticket.
func (p Profile) Label() string {
	harness := p.Harness
	if harness != "" {
		harness = strings.ToUpper(harness[:1]) + harness[1:]
	}
	model := strings.TrimSpace(p.Model)
	switch p.Harness {
	case "codex":
		model = strings.TrimPrefix(model, "gpt-6-")
	case "pi":
		model = strings.TrimPrefix(model, "anthropic/claude-")
	case "cursor":
		// Cursor ids carry the effort (grok-4.7-xhigh); it follows the dot instead.
		model = strings.TrimSuffix(strings.TrimSuffix(model, "-fast"), "-"+p.Effort)
		if strings.HasSuffix(p.Model, "-fast") {
			model += " fast"
		}
	}
	label := strings.TrimSpace(harness + " " + model)
	if effort := strings.TrimSpace(p.Effort); effort != "" && effort != "default" {
		label += " · " + effort
	}
	return label
}

// ModelKey is the comparable model name behind a profile or a reported
// session: lower case, and for Anthropic models the family word (opus,
// sonnet, haiku, fable), so the harness alias "opus" and the API id
// "claude-opus-5-5" count as the same route.
func ModelKey(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(key, "claude") || !strings.Contains(key, "-") {
		for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
			if strings.Contains(key, family) {
				return family
			}
		}
	}
	return key
}

// Revision names the Models state routing reads: registry pins and
// retirements (enabled pins only), preference profiles and orders, rules, row constraints and
// pauses. It digests content (slugs, owners, lines, words), never row ids or
// write stamps, so equal state has an equal revision in every tenant. Empty
// before the registry is set up; ErrBoardBounds beyond the Models bounds.
func Revision(ctx context.Context, tx pgx.Tx) (string, error) {
	var setup bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles)`).Scan(&setup); err != nil {
		return "", err
	}
	if !setup {
		return "", nil
	}
	if err := registryBounds(ctx, tx, true); err != nil {
		return "", err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `WITH owner AS (SELECT id,coalesce(person_id::text,'workspace') AS key FROM model_pref_profiles)
 SELECT jsonb_build_array(
 coalesce((SELECT jsonb_agg(jsonb_build_array(p.slug,p.version,p.harness,p.family,p.model,p.effort,p.tier,p.enabled) ORDER BY p.slug,p.version,p.harness,p.model,p.effort,p.tier,p.enabled) FROM model_profiles p WHERE p.enabled),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(p.slug,p.version,r.retire_at) ORDER BY p.slug,p.version,r.retire_at) FROM model_profile_retirements r JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id WHERE p.enabled),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(o.key,p.template,p.thinking,p.usage,p.residency,p.hidden_kinds,p.dismissed_lines) ORDER BY o.key) FROM model_pref_profiles p JOIN owner o ON o.id=p.id),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(w.key,o.column_key,o.situation,o.rank,o.not_allowed,o.thinking,o.effort,o.effort_level,o.end_mode,o.efforts) ORDER BY w.key,o.column_key,o.situation) FROM (`+modelprefs.OrdersSQL+`) o JOIN owner w ON w.id=o.profile_id),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(r.scope,coalesce(r.project_id::text,''),r.column_key,r.line,r.lock,r.position) ORDER BY r.scope,coalesce(r.project_id::text,''),r.column_key,r.line) FROM model_rules r WHERE r.moved_to_models IS NOT TRUE),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(r.scope,coalesce(r.project_id::text,''),r.column_key,r.line,r.kind,r.position) ORDER BY r.scope,coalesce(r.project_id::text,''),r.column_key,r.line) FROM model_row_constraints r),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(jsonb_build_array(p.harness,p.model,p.until_at,p.reason) ORDER BY p.harness,p.model) FROM model_pauses p),'[]'::jsonb))`).Scan(&raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:8], nil
}
