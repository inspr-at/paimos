// SPDX-License-Identifier: AGPL-3.0-only

// Package modelregistry serves /api/models for one tenant: an immutable
// profile catalog, an ordered role ladder, and role resolution.
//
// New returns an httpapi.Module. The coordinator mounts it; this package does
// not edit cmd/aeon. The first use of an empty registry seeds the versioned
// static catalog and its default ladders (scout, mechanical, build,
// build-hard, review-gate) in a separately committed preparation transaction,
// authorized under tenant/tree/pairing and registry fences for its initiating
// operation. Missing required profiles are added without replacing saved policy;
// intentionally empty role ladders stay empty. Injected resolvers are read-only
// and return ErrCatalogNotReady for incomplete setup. Profiles
// are insert-only. Replacing routes never rewrites a profile, so an expired
// suppression leaves history intact and simply stops matching once valid_until
// has passed.
// Route priorities must fit PostgreSQL's positive integer range; structural
// validation rejects overflow before catalog preparation can commit.
//
// Boundary integration: model list, legacy and placement resolution, preference
// GET, profile/whole-tenant replacement and both review entries prepare before
// their final transaction. Read adapters repeat current target/project/key and
// canonical-person restrictions. Preference/placement documents use read-only
// repeatable-read snapshots after preparation; ladder GET never initializes.
//
// Editor contract (AEON-633a/b/i): role-qualified PUT replaces only that role
// and requires the GET's quoted full SHA-256 If-Match token. The read's rows and
// token come from one statement; truncated reads have no token. Whole-tenant
// PUT keeps its legacy semantics. Both writers reauthorize models.manage under
// tenant then registry fences, bound rows to 50/role and 250/tenant (including
// the complete audit snapshots), and sample clock_timestamp after the fences.
// Unchanged stored expired holds survive reordering; expiry_policy=clear in
// conditional compensation normalizes expired desired holds to available.
// Confirmation is the actual stored array and ETag; equal content is a no-op.
// Package 633g owns D1 B ordering-mode persistence, token/GET/audit extension
// and managed-dispatch activation. This slice rejects order_mode until that
// integration lands; it does not claim to activate saved managed order.
//
// Preference writes validate selectors with targeted existence queries (one
// bounded result row, no catalog/ladder decoding). Per-request checks reuse
// selector results, with a separate review-floor key, and validated replacement
// kinds are reused inside the same tenant fence. SQL latest-line matching is
// tested against ProfileLine across the catalog and concrete harness forms.
// Preference GET bounds catalog preview reads (including retired revisions) and
// each role ladder to 256 entries plus one overflow sentinel before decoding,
// reusing them across levels and selectors in the repeatable-read snapshot.
// Picker review membership uses bounded profile IDs rather than all routes.
// Incomplete resolution returns no profile, an unavailable_reason/row warning
// and resolution_truncated on its view; choices_truncated remains independent.
// The document has a 30-second work deadline. Live dispatch remains unchanged.
//
// Every preference person-level PUT/DELETE now requires If-Prefs-Person from
// the coherent GET. This is a client compatibility change: all You writers,
// including Reset/Undo, must supply it before enablement. The header only
// compares the active canonical caller; it never chooses the mutation target.
// Default/Project accept omitted headers for their explicit scope; supplied
// headers are checked. All confirmations include person_id and level revision.
// Full GET still needs workspace models.read plus supplied project nodes.read.
// Project-only model_prefs.manage plus nodes.read retains direct scoped writes,
// with no model catalog/work-kind read grant. Agent middleware ceilings remain.
//
// Policies' closed mutation set is row PUT, row DELETE, scalar-only level PUT.
// Scalar bodies carry residency/residency_locked/prefs_locked and omit rows;
// row bodies preserve both buckets and lock. Row Reset/Undo touches one active
// kind, never level DELETE or visible-row replacement. Archived kinds stay in
// storage but outside visible source audit projections; kind retirement shares
// the preference fence and refuses a stale row action with unknown_kind.
// Legacy whole-level API writes remain destructive and are not Policies Undo.
// Residency changes retain stricter run stamps; restamp events flush only after
// resource writes/response loading, before the final preference event.
//
// Caller inventory: searches of web/src and internal/cli for model-preferences
// and work-kinds find no writer in this stacked base. origin/main's model prefs
// CLI is read-only. New controller/editor callers belong to 633c/e and must
// implement the above header and row/scalar payload constraints; existing API
// and fixture callers must migrate You mutations. No whole-level Reset is
// implicitly enabled by a reused component.
//
// Resolve walks the stored ladder. review-gate requires author_family and
// skips that family.
// Author families accept claude, codex and grok as aliases for anthropic,
// openai and xai; pi is ambiguous and requires an explicit family. Resolution
// responses echo the normalised author_family (empty when omitted).
// Responses also include the starter's residency trace; optional project_id
// selects that project's override. The trace retains every loosened lock.
// This endpoint keeps its role-dispatch ladder; qualified review requests use
// ResolveReviewFor and persist the complete preference trace with the review.
// project_id alone does not enable placement resolution. mode=placement opts
// in explicitly, as do ticket, area, complexity and person_id placement inputs.
// A harness query skips other harnesses and is rejected
// when the ladder has none of that harness. When the tenant already has an
// account for a harness, candidates of that harness are also skipped for a
// stale or missing probe, a full parallel slot, or no pace headroom. The
// command template is rendered from the built-in harness text and is never
// executed. Nothing is selected when every candidate was skipped; review-gate
// then sets owner_required.
// Placement resolution also accepts review-gate-security with author_family;
// an unavailable security ladder returns owner_required and its explanation.
//
// ResolveTicketRoute is the read-only helper for a ticket's role and area.
// The ladder is still keyed by role. A missing selection returns nil.
package modelregistry
