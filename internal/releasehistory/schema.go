// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schema is the identifier every manifest carries.
const Schema = "inspr.release-history.v1"

// History is a product's release history, newest release first.
type History struct {
	Schema        string    `json:"schema"`
	Product       string    `json:"product"`
	Repository    string    `json:"repository"`
	VersionScheme string    `json:"version_scheme"`
	GeneratedAt   time.Time `json:"generated_at"`
	// Source says what the manifest was built from: "git+github", "git" or "none".
	Source   string    `json:"source"`
	Releases []Release `json:"releases"`
}

// Release is one reserved or published version.
type Release struct {
	Notes *Notes `json:"notes,omitempty"`
	// Presentation is the release's theme, headline and intro (AEON-305),
	// read from the database per request. Absent when none was written.
	Presentation    *Presentation `json:"presentation,omitempty"`
	Version         string        `json:"version"`
	Tag             string        `json:"tag"`
	ReleaseChannel  string        `json:"release_channel"`
	ReleaseSequence int           `json:"release_sequence"`
	// Codename is the release's sci-fi name from its sequence (AEON-430),
	// presentation only. See WithCodenames.
	Codename    string     `json:"codename,omitempty"`
	State       string     `json:"state"`
	ReservedAt  *time.Time `json:"reserved_at"`
	TaggedAt    *time.Time `json:"tagged_at"`
	PublishedAt *time.Time `json:"published_at"`
	Headline    string     `json:"headline"`
	Tickets     []string   `json:"tickets"`
	Changes     []Change   `json:"changes"`
	// ChangesOmitted counts changes beyond MaxChanges that are not listed.
	ChangesOmitted int      `json:"changes_omitted"`
	Evidence       Evidence `json:"evidence"`
}

// Release states.
const (
	StatePublished = "published"
	StateReserved  = "reserved"
	StateWithdrawn = "withdrawn"
)

// Change is one commit of a release.
type Change struct {
	Commit  string   `json:"commit"`
	Subject string   `json:"subject"`
	Type    string   `json:"type"`
	Scope   string   `json:"scope"`
	Tickets []string `json:"tickets"`
	At      string   `json:"at"`
	// Group is the display group (features, fixes, other), set when the
	// history is served. Empty on the version bump and on an unannotated manifest.
	Group string `json:"group,omitempty"`
	// Linked is the frozen pill and benefit of each visible linked ticket for
	// Aeon; other products may opt into the legacy live TicketSource.
	// Hidden tickets are omitted. Empty on other changes, the version bump,
	// and when ticket lookup is off or failed.
	Linked []TicketNote `json:"linked_tickets,omitempty"`
}

// Evidence is what proves a release: its source, its image and its runs.
type Evidence struct {
	SourceCommit string `json:"source_commit"`
	SourceURL    string `json:"source_url"`
	Image        *Image `json:"image"`
	CI           *Run   `json:"ci"`
	ReleaseRun   *Run   `json:"release_run"`
	ReleaseURL   string `json:"release_url"`
	// Unavailable lists, in plain words, what could not be established.
	Unavailable []string `json:"unavailable"`
}

// Image is the published OCI image.
type Image struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

// Run is a CI workflow run for the source commit.
type Run struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

var (
	calendarVersion = regexp.MustCompile(`^[1-9]\d(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])([01]\d|2[0-3])[0-5]\d[0-5]\d\.0\.0$`)
	ticketKey       = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6}\b`)
	conventional    = regexp.MustCompile(`^([a-z]+)(?:\(([^)]*)\))?!?:\s*(.*)$`)
	sequenceField   = regexp.MustCompile(`^release_sequence\s+(\d+)$`)
)

// Version schemes (INSPR-CalVer3, AEON-309). Both calendar schemes share one
// coordinate, YYMMDDhhmmss.0.0, so versions keep sorting as time across them.
const (
	// SchemeCalVer3 is the current scheme; every new reservation declares it.
	SchemeCalVer3 = "inspr-calver-3"
	// SchemeCalVer2 is its predecessor: valid history, never a new reservation.
	SchemeCalVer2 = "inspr-calendar-v2"
	// LastCalVer2 is the last version Aeon reserved under SchemeCalVer2.
	LastCalVer2 = "260929113854.0.0"
)

// CalendarScheme reports whether scheme names the calendar coordinate
// (inspr-calver-3 or its predecessor inspr-calendar-v2).
func CalendarScheme(scheme string) bool {
	return scheme == SchemeCalVer3 || scheme == SchemeCalVer2
}

// SchemeOf returns the scheme an Aeon release version was reserved under:
// inspr-calendar-v2 up to LastCalVer2, inspr-calver-3 after it. This is
// Aeon's recorded switch-over, not a guess from the coordinate's shape.
func SchemeOf(version string) string {
	if version <= LastCalVer2 {
		return SchemeCalVer2
	}
	return SchemeCalVer3
}

// ValidVersion reports whether v is a calendar coordinate (inspr-calver-3 or
// inspr-calendar-v2, which share the grammar) without "v".
func ValidVersion(v string) bool {
	if !calendarVersion.MatchString(v) {
		return false
	}
	_, err := time.Parse("060102150405", strings.TrimSuffix(v, ".0.0"))
	return err == nil
}

// Tickets returns the distinct ticket keys named in the texts, in order of first mention.
func Tickets(texts ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, text := range texts {
		for _, key := range ticketKey.FindAllString(text, -1) {
			if !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	return out
}

var knownTypes = map[string]string{
	"feat": "feat", "feature": "feat", "fix": "fix", "hotfix": "fix", "test": "test", "tests": "test", "docs": "docs", "doc": "docs",
	"release": "release", "refactor": "refactor", "perf": "refactor", "chore": "chore", "ci": "chore", "build": "chore", "style": "chore",
}

// Classify types a commit subject by its conventional prefix ("feat(AEON-74): …").
// Subjects without a known prefix are "other"; the scope is kept either way.
func Classify(subject string) (kind, scope string) {
	m := conventional.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return "other", ""
	}
	if t, ok := knownTypes[m[1]]; ok {
		return t, m[2]
	}
	return "other", m[2]
}

// ParseTagMessage reads a release tag message in the INSPR form
// "<product> v<version> · <scheme> · <channel> · release_sequence <n> · <headline>"
// and returns the headline, channel and sequence. Other messages yield their
// first line as the headline.
func ParseTagMessage(message string) (headline, channel string, sequence int) {
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(message), "\n", 2)[0])
	parts := strings.Split(first, " · ")
	if len(parts) < 5 {
		return first, "", 0
	}
	for i, part := range parts {
		if m := sequenceField.FindStringSubmatch(strings.TrimSpace(part)); m != nil {
			n, _ := strconv.Atoi(m[1])
			if i >= 1 {
				channel = strings.TrimSpace(parts[i-1])
			}
			return strings.TrimSpace(strings.Join(parts[i+1:], " · ")), channel, n
		}
	}
	return first, "", 0
}

// Sort orders releases newest first by version (coordinates sort as time).
func Sort(releases []Release) {
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].Version > releases[j].Version })
}
