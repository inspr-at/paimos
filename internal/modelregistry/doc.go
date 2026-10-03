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
//
// Boundary integration: the present checkout has no preference GET or placement
// HTTP resolver. Their owners must call PrepareCatalog(CatalogRead) before their
// read snapshot, with an adapter repeating current target/canonical-person
// restrictions. Existing model list/legacy resolve/profile/whole-tenant replace
// and both review entries are adapted here; ladder GET never initializes.
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
// A harness query skips other harnesses and is rejected
// when the ladder has none of that harness. When the tenant already has an
// account for a harness, candidates of that harness are also skipped for a
// stale or missing probe, a full parallel slot, or no pace headroom. The
// command template is rendered from the built-in harness text and is never
// executed. Nothing is selected when every candidate was skipped; review-gate
// then sets owner_required.
//
// ResolveTicketRoute is the read-only helper for a ticket's role and area.
// The ladder is still keyed by role. A missing selection returns nil.
package modelregistry
