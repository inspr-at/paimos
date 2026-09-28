// SPDX-License-Identifier: AGPL-3.0-only

// Package portal is the public product portal (AEON-125).
//
// A product, its catalog and its wishes are tenant-configured nodes. Anonymous
// visitors receive no principal and no project visibility. The public handler
// is a narrow service read: it returns a fixed column list for published
// portal nodes only. Votes keep a ballot hash, never a name, address or
// account. The tenant setting defaults off, and a closed portal answers the
// same 404 as an unknown address.
package portal
