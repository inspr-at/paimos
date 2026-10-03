// SPDX-License-Identifier: AGPL-3.0-only

// Package portal is the public product portal (AEON-125).
//
// Products, their catalogs and wishes are tenant-configured nodes. An explicit
// tenant default preserves old URLs; product slugs select independent catalogs,
// comparison and project links. portal_products settings default unpublished
// with participation disabled. Migration preserves existing published products
// in legacy mode and binds browser ballots to their original product. Policy,
// publication and default changes share tenant/tree fences with intake, voting
// and node moves. Registered activation is gated off until B3+B8+B7 are ready;
// its projection never counts browser ballots. A separate no-store participation
// endpoint exposes unavailable controls and anonymous history, preserving the
// exact existing catalog, roadmap and release JSON shapes for strict consumers.
// Each response resolves its product once, so a concurrent default change
// cannot pair one product's catalog with another product's project projection.
//
// A product, its catalog and its wishes are tenant-configured nodes. Anonymous
// visitors receive no principal and no project visibility. The public handler
// is a narrow service read: it returns a fixed column list for published
// portal nodes only. Votes keep a ballot hash, never a name, address or
// account. A wish intake stores title and summary on a pending node and
// nothing else; pending wishes stay out of the public catalog. Comparison
// cells stay unknown until a person approves a sourced fact. Pace figures are
// counts and medians, never a ticket title, a person or a private field. The
// public page omits a figure until five releases or five fulfilled wishes
// support it, and a smaller sample is absent rather than zero. The
// tenant setting defaults off, and a closed portal answers the same 404 as an
// unknown address. Published release notes are the frozen snapshot for the
// project linked to this product on the pace screen, and only after that link turns
// release history on. A pace link alone publishes nothing. The public roadmap
// is a separate whitelist (pill, benefit, target, status) for tickets a person
// approved. It does not wait for release history. A shipped ticket already
// visible there leaves the roadmap. llms.txt and catalog.json repeat only
// that public document, plus a roadmap link when an item exists.
package portal
