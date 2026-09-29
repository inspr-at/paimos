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
// Identical imported identities are idempotent and reported unchanged. A draft
// rule from an earlier import is updated when the source changed and the stored
// rule still matches the baseline recorded for that source revision. The
// baseline covers every user-editable field, including the draft identity and
// source revision. A rule marked edited here, or whose stored content diverged
// from that baseline, is left in place and reported as a conflict. Unannotated
// rules whose text changed are matched by source path, the heading path below
// the document title, and position or similarity; an ambiguous replacement is
// reported and not written. Unrelated existing draft rules are preserved.
// Publication is never requested.
//
// AR1 has no on-demand placement field. Packs are attached as draft details and
// stay out of the session-file projection. Unresolved choices, mixed layer/set
// groups, missing why, date-only expiry, unsupported selectors and oversized
// values are refused without truncating the proposal. To apply, supply a single
// group with explicit why and RFC3339 expiry (if any). Source lineage is
// retained as JSON in details: source path, heading path and content hash on
// every rule. File hashes identify raw bytes, while source ranges and their
// hashes use BOM-stripped, LF-normalized parsing lines. --layer
// PATH=company|project|person|agent overrides the classified layer. --report DIR
// writes contradictions.md and contradictions.json. Contradictions are
// same-identity differences and opposing directives (must/always versus
// never/must-not) on the same action across layers. Heading paths are
// provenance. Compatible tightening and disjoint role or harness selectors are
// not conflicts. Long packs stay in details and out of the always-on projection.
//
// Generic AGENTS.md and CLAUDE.md require an explicit local context. Public
// templates additionally need an aeon-context: template HTML comment; detected
// private/personal markers prohibit template output even with that annotation.
// Exact directory components doctrine-private and inspr-doctrine-private are
// private by themselves, case-insensitively, including marker-free text.
// Re-run with the appropriate local context to retain private proposals. Section
// selection never bypasses this check. Arbitrary symlinks are refused; only exact
// Darwin /var and /tmp system aliases are supported. Other platforms fail closed.
//
// AR1 owns common CLI dispatch; tmp/ar3-cli-registration.patch is the coordinator
// registration step. These local wire types follow AR1's frozen AEON-248/249 DTO
// and can be reconciled with internal/rules after coordinator integration.
package rulesimport
