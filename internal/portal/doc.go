// SPDX-License-Identifier: AGPL-3.0-only

// Package portal is the public product portal (AEON-125).
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
// one project linked on the pace screen, and only after that link turns
// release history on. A pace link alone publishes nothing. llms.txt and
// catalog.json repeat only that public document.
package portal
