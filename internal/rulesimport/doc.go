// SPDX-License-Identifier: AGPL-3.0-only

// Package rulesimport previews explicit known doctrine files without discovering
// home, config or credential trees. The standalone CLI entry is rules-import:
//
//	aeon rules-import --context project --file /absolute/repo/AGENTS.md
//	aeon rules-import --context project --file /absolute/repo/AGENTS.md --apply --set UUID --revision N
//
// Preview is offline. Apply uses the existing CLI instance client and the AR1
// GET set / PUT draft contract, with server authorization and the caller's exact
// expected revision. No layer/set creation, publication, restore or retry occurs.
// Output is a local JSON proposal, then a separate JSON receipt on success. A
// failure leaves that local proposal intact; HTTP response bodies are not logged.
// Identical imported identities are idempotent; different existing identities
// require explicit resolution. Unrelated existing draft rules are preserved.
//
// AR1 has no on-demand placement or unresolved-alternative fields. Those cases,
// mixed layer/set groups, missing why, date-only expiry, unsupported selectors
// and oversized values are refused without truncating the proposal. To apply,
// supply a single group with explicit why and RFC3339 expiry (if any). Source
// lineage is retained as JSON in details; file hashes identify raw bytes, while
// source ranges and their hashes use BOM-stripped, LF-normalized parsing lines.
//
// Generic AGENTS.md and CLAUDE.md require an explicit local context. Public
// templates additionally need an aeon-context: template HTML comment; detected
// private/personal markers prohibit template output even with that annotation.
// Re-run with the appropriate local context to retain private proposals. Section
// selection never bypasses this check. Arbitrary symlinks are refused; only exact
// Darwin /var and /tmp system aliases are supported. Other platforms fail closed.
//
// AR1 owns common CLI dispatch; tmp/ar3-cli-registration.patch is the coordinator
// registration step. These local wire types follow AR1's frozen AEON-248/249 DTO
// and can be reconciled with internal/rules after coordinator integration.
package rulesimport
