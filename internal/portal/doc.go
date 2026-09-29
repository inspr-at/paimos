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
// tenant setting defaults off, and a closed portal answers the same 404 as an
// unknown address.
package portal
