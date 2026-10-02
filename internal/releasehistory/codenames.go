// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import "github.com/inspr-at/paimos/internal/releasehistory/codename"

// WithCodenames names every release by its sequence (AEON-430), past releases
// included: the name is a pure function of the sequence, so it is the same in
// every build and never stored as the release's identity. A reservation keeps
// its name unless a published release took its sequence. Withdrawn attempts own
// no public name and surrender their sequence. The releases are copied.
func WithCodenames(h History) History {
	published := map[int]bool{}
	for _, r := range h.Releases {
		if r.State == StatePublished && r.ReleaseSequence > 0 {
			published[r.ReleaseSequence] = true
		}
	}
	out := h
	out.Releases = make([]Release, len(h.Releases))
	for i, r := range h.Releases {
		r.Codename = ""
		if r.ReleaseSequence > 0 && (r.State == StatePublished || (r.State == StateReserved && !published[r.ReleaseSequence])) {
			r.Codename = codename.Codename(r.ReleaseSequence)
		}
		if r.State == StateWithdrawn {
			r.ReleaseSequence = 0
		}
		out.Releases[i] = r
	}
	return out
}

// CodenameOf is the codename of the published or reserved release with this
// version in the served history, or "" when the build does not know it.
func (m *Module) CodenameOf(version string) string {
	return CodenameOf(m.history, version)
}

// CodenameOf is the codename of the release with this version in h, or "".
func CodenameOf(h History, version string) string {
	for _, r := range WithCodenames(h).Releases {
		if r.Version == version {
			return r.Codename
		}
	}
	return ""
}
