// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"strings"

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

// Revision names registry pins, Models orders, never rules and pauses.
func Revision(ctx context.Context, tx pgx.Tx) (string, error) {
	var setup bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles)`).Scan(&setup); err != nil {
		return "", err
	}
	if !setup {
		return "", nil
	}
	revision, err := registryRevision(ctx, tx)
	if err != nil {
		return "", err
	}
	return revision[:8], nil
}
