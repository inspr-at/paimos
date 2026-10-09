// SPDX-License-Identifier: AGPL-3.0-only

package events

// This allowlist is fail-closed for new event families. Adding a type requires
// naming the existing resource read permission; event snapshots are never read.
var subscriptionTopics = map[string]bool{
	"plan": true, "settings": true, "preferences": true, "policies": true,
	"approvals": true, "decisions": true, "delivery": true,
}

type subscriptionPolicy struct{ topic, permission string }

var subscriptionTypes = map[string]subscriptionPolicy{
	"model.preferences_changed":         {"preferences", "models.resolve"},
	"review_policy.changed":             {"policies", "reviewpolicy.read"},
	"approval.proposed":                 {"approvals", "approvals.read"},
	"approval.approved":                 {"approvals", "approvals.read"},
	"approval.denied":                   {"approvals", "approvals.read"},
	"approval.revoked":                  {"approvals", "approvals.read"},
	"question.asked":                    {"decisions", "questions.read"},
	"question.answered":                 {"decisions", "questions.read"},
	"question.corrected":                {"decisions", "questions.read"},
	"question.merged":                   {"decisions", "questions.read"},
	"question.outcome_applied":          {"decisions", "questions.read"},
	"delivery.state_changed":            {"delivery", "delivery.read"},
	"delivery.stall_alerted":            {"delivery", "delivery.read"},
	"delivery.held":                     {"delivery", "delivery.read"},
	"delivery.released":                 {"delivery", "delivery.read"},
	"delivery.step":                     {"delivery", "delivery.read"},
	"delivery.item":                     {"delivery", "delivery.read"},
	"delivery.incident":                 {"delivery", "delivery.read"},
	"status_autopilot.settings_changed": {"settings", "settings.manage"},
	"inbox.delivery_settings_changed":   {"settings", "settings.read"},
	"tenant.brand_updated":              {"settings", "settings.manage"},
	"tenant.model_provider_updated":     {"settings", "settings.manage"},
}
