// SPDX-License-Identifier: AGPL-3.0-only
// Package pathpolicy contains the compiled routine path floor shared by the
// daemon and server. It has no database, model registry or execution dependency.
package pathpolicy

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

const MaxPathBytes = 1024

// SensitiveWords returns a fresh copy of the compiled recommended predicate.
// Callers cannot mutate the default used by another policy evaluation.
func SensitiveWords() []string {
	return []string{"password", "credential", "secret", "passwort", "zugangsdaten"}
}

// Validate bounds canonical repository-relative paths before matching.
func Validate(p string) error {
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00\r\n:%?#") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("guardrail paths must be bounded canonical repository-relative paths")
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." || segment == "." || strings.EqualFold(segment, ".git") {
			return fmt.Errorf("unsafe guardrail path")
		}
	}
	return nil
}

// Protected matches the compiled hard floor; callers validate paths first.
func Protected(p string) bool {
	p = strings.ToLower(p)
	for _, prefix := range []string{"internal/routineguard", "internal/routineactions", "internal/recurrences", "internal/agentd", "internal/auth", "internal/authz", "internal/db/migrations", "api/areas/recurrences.yaml", "scripts/ci", "scripts/ci-static.mjs", "scripts/check-migrations.mjs", "scripts/check-attached-policy-mutations.py", "claudeassets", "internal/recurrences/guardrails.go", "scripts/test-tiers", "scripts/audit", ".github/workflows", ".aeon", ".inspr"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "agents.md" || segment == "claude.md" || strings.Contains(segment, "guardrail") || strings.Contains(segment, "gate") || strings.Contains(segment, "guard") || segment == "rules" || strings.HasPrefix(segment, "rules.") {
			return true
		}
	}
	return false
}

// LocalWriteAllowed applies the default deterministic artifact path rules.
// It grants only a local file edit, never outward action or evaluation authority.
func LocalWriteAllowed(name string) bool {
	if Validate(name) != nil || Protected(name) {
		return false
	}
	lower := strings.ToLower(name)
	for _, word := range SensitiveWords() {
		if strings.Contains(lower, word) {
			return false
		}
	}
	return true
}
