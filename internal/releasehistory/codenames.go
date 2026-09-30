// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import "github.com/inspr-at/paimos/internal/releasehistory/codename"

// WithCodenames names every release by its sequence (AEON-430), past releases
// included: the name is a pure function of the sequence, so it is the same in
// every build and never stored as the release's identity. A reservation keeps
// its sequence's name; when a published release took the same sequence (the
// first reservation of release 1), the published release owns the name and the
// reservation has none. The releases are copied, never changed in place.
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
		if r.ReleaseSequence > 0 && (r.State == StatePublished || !published[r.ReleaseSequence]) {
			r.Codename = codename.Codename(r.ReleaseSequence)
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
