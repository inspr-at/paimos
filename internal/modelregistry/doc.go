// SPDX-License-Identifier: AGPL-3.0-only

// Package modelregistry serves /api/models for one tenant: an immutable
// profile catalog, an ordered role ladder, and role resolution.
//
// New returns an httpapi.Module. The coordinator mounts it; this package does
// not edit cmd/aeon. The first use of an empty registry seeds the classic
// static catalog and its default ladders (scout, mechanical, build,
// build-hard, review-gate) inside the caller's tenant transaction. Profiles
// are insert-only. Replacing routes never rewrites a profile, so an expired
// suppression leaves history intact and simply stops matching once valid_until
// has passed.
//
// Resolve walks the stored ladder. review-gate requires author_family and
// skips that family.
// Author families accept claude, codex and grok as aliases for anthropic,
// openai and xai; pi is ambiguous and requires an explicit family. Resolution
// responses echo the normalised author_family (empty when omitted).
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
