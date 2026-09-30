// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

// Revision names the current registry state: a short digest over every
// route step and its profile pin. It changes whenever PUT /api/models/routes
// or a new profile changes what a role can resolve to. Empty when the
// registry has no routes (not seeded).
func Revision(ctx context.Context, tx pgx.Tx) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.role, r.priority, r.state, r.valid_until, p.slug, p.version, p.enabled
		FROM model_role_routes r
		JOIN model_profiles p ON p.tenant_id = r.tenant_id AND p.id = r.profile_id
		ORDER BY r.role, r.priority`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	sum := sha256.New()
	n := 0
	for rows.Next() {
		var role, state, slug, version string
		var priority int
		var until *time.Time
		var enabled bool
		if err := rows.Scan(&role, &priority, &state, &until, &slug, &version, &enabled); err != nil {
			return "", err
		}
		stamp := ""
		if until != nil {
			stamp = until.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(sum, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%t\n", role, priority, state, stamp, slug, version, enabled)
		n++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	return hex.EncodeToString(sum.Sum(nil))[:8], nil
}
