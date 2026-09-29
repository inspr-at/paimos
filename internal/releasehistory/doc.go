// SPDX-License-Identifier: AGPL-3.0-only

// Package releasehistory builds, embeds and serves a product's release history
// in the schema inspr.release-history.v1. Aeon is the reference implementation:
// every INSPR product can produce the same manifest from its own repository and
// render it with the same face.
//
// # Where the data comes from
//
// The manifest is generated at build time (see the generate command in this
// package's generate directory) from the sources below; nothing is invented:
//
//   - Git: every annotated tag v<version> whose version is a calendar
//     coordinate. The tag message gives the headline and the release sequence,
//     the tagger date gives tagged_at, the tagged commit is the source commit,
//     and version.json at that tag gives reserved_at, the release channel and
//     the ticket. The commits between consecutive published tags (merges left
//     out) are the release's changes.
//   - version.json at the build: its unpublished_reservations are versions that
//     were reserved but never published. They stay in the history, marked
//     "reserved", so the sequence has no silent gaps.
//   - release-notes/<version>.json at the immutable tag: an explicit export
//     of journey_tickets membership and nodes.fields. Notes hold both languages,
//     a source digest and gaps; hidden text is excluded. Missing snapshots do
//     not turn Git headlines into benefits. See docs/RELEASE.md for the capture
//     and publication boundary. Older manifests without notes remain readable.
//   - data/product-notes.json: the reviewed, insert-only public projection of
//     Aeon's frozen snapshots. packnotes writes it during reservation or from
//     explicit historical exports. It contains no tenant IDs or hidden text.
//   - GitHub (optional, with a token): the release's published_at, the image
//     reference and digest the release workflow writes into the release notes
//     ("Container: …" and "Digest: …"), and the CI and Release workflow runs for
//     the source commit. Without GitHub, or when a value is missing, the
//     evidence says so in Unavailable instead of guessing.
//
// # Schema inspr.release-history.v1
//
// A History lists releases newest first. Each Release has:
//
//	version            the calendar coordinate without "v" (YYMMDDhhmmss.0.0)
//	tag                the git tag, "v" + version (empty for a reservation without a tag)
//	release_channel    "stable" unless version.json says otherwise
//	release_sequence   the sequence from the tag message or version.json
//	state              "published" or "reserved" (reserved, never published)
//	reserved_at        when the version was reserved (version.json reserved_at)
//	tagged_at          when the annotated tag was made
//	published_at       when it was published (GitHub release), or null
//	headline           the tag message's headline (source evidence)
//	notes              bilingual ticket benefits, snapshot provenance and gaps
//	tickets            ticket keys: version.json's ticket and keys in the headline
//	changes            commits since the previous published release, each with
//	                   commit, subject, type (feat, fix, test, docs, release,
//	                   refactor, chore, other), scope and the ticket keys it names.
//	                   When served, group is features, fixes or other: conventional
//	                   feat and fix prefixes win, and test, docs, refactor and
//	                   chore stay other. Otherwise a linked bug tag, type or kind
//	                   is fixes, a visible release-note pill or benefit is
//	                   features, and anything else is other. Several tickets take
//	                   the strongest group and the commit is listed once. The
//	                   version bump omits group. Older manifests without it stay
//	                   valid; clients then derive the group from type. Aeon uses
//	                   the selected frozen capture, never live ticket metadata.
//	                   A features or fixes change also
//	                   carries linked_tickets: key plus pill and benefit in
//	                   English and German, and the ticket's own group (fixes
//	                   for a bug, else features). Hidden tickets, and tickets with no
//	                   pill or benefit, are omitted. Later ticket edits cannot
//	                   change the captured text or classification.
//	changes_omitted    how many more changes there were beyond the listed ones
//	evidence           source_commit and its URL, the OCI image reference and
//	                   digest, the CI and Release runs (URL, conclusion), the
//	                   GitHub release URL, and Unavailable: what could not be
//	                   established, in plain words
//
// The History carries schema, product, repository, version_scheme,
// generated_at and source ("git+github", "git" or "none"). The HTTP face adds
// current, the version of the running build, and live_since, when this server
// started running it.
//
// # HTTP
//
//	GET /api/releases            the whole history plus the running version
//	GET /api/releases/{version}  one release (with or without the leading v)
//	PUT /api/releases/{version}/presentation     set theme, headline, intro
//	DELETE /api/releases/{version}/presentation  remove them
//
// The reads require an authenticated principal. The presentation writes
// (AEON-305) require releases.deploy on the served product project and record
// one event per change; aeon release-notes present is the offline equivalent.
// Reads attach the stored presentation as the additive presentation member. The manifest is embedded in the
// binary (data/history.json when generated, else data/empty.json). WithBackfills
// adds immutable database snapshots for the caller's tenant and visible product
// project. Native journey snapshots win, followed by explicit manifest backfills,
// then embedded tag/public notes; empty and hidden-only captures also win.
// Note reads never capture live ticket fields and never contact the network.
// Portable notes use the additive notes.public_items without tenant-local UUIDs;
// notes.items retains its original API contract. Other products can still opt
// into the legacy TicketSource annotation; Aeon's handler never calls it.
package releasehistory
