// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory/codename"
)

// Withdrawal records an immutable failed image. It is never a release or a
// reusable coordinate; only its unconsumed sequence may be reserved again.
type Withdrawal struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Ticket  string `json:"ticket"`
	Reason  string `json:"reason"`
}

var imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validateWithdrawals(v versionFile) error {
	seen := map[string]bool{}
	for _, w := range v.WithdrawnReleases {
		if !ValidVersion(w.Version) || !imageDigest.MatchString(w.Digest) || ticketKey.FindString(w.Ticket) != w.Ticket || w.Ticket == "" || strings.TrimSpace(w.Reason) == "" || seen[w.Version] {
			return fmt.Errorf("invalid or duplicate withdrawn release")
		}
		seen[w.Version] = true
	}
	for _, version := range v.UnpublishedReservations {
		version = strings.TrimPrefix(version, "v")
		if !ValidVersion(version) || seen[version] {
			return fmt.Errorf("invalid or duplicate unpublished reservation")
		}
		seen[version] = true
	}
	return nil
}

// PublishedOnly keeps the shipped releases that consume allocation sequences.
func PublishedOnly(h History) History {
	out := h
	out.Releases = []Release{}
	for _, r := range h.Releases {
		if r.State == StatePublished {
			out.Releases = append(out.Releases, r)
		}
	}
	return out
}

// PublicHistory retains reservations for opt-in history rows, deep links and
// statistics. Withdrawn attempts remain exclusively in the offline manifest.
func PublicHistory(h History) History {
	out := h
	out.Releases = []Release{}
	for _, r := range h.Releases {
		if r.State == StatePublished || r.State == StateReserved {
			out.Releases = append(out.Releases, r)
		}
	}
	return out
}

// NextSequence allocates from published releases in this channel only.
// Failed attempts never advance it. Published ordering cannot be repaired by
// returning slots below its maximum (the one-time 115 -> 118 gap, AEON-530).
func NextSequence(h History, channel string) (int, error) {
	if channel == "" {
		return 0, fmt.Errorf("release channel required")
	}
	published := PublishedOnly(h).Releases
	Sort(published)
	maximum, previous := 0, 0
	for _, r := range published {
		if r.ReleaseChannel != channel {
			continue
		}
		if r.ReleaseSequence < 1 || (previous != 0 && r.ReleaseSequence >= previous) {
			return 0, fmt.Errorf("published release sequences must strictly follow coordinate order in channel %s", channel)
		}
		if maximum == 0 {
			maximum = r.ReleaseSequence
		}
		previous = r.ReleaseSequence
	}
	return maximum + 1, nil
}

// ReserveFile prepares version.json only; it creates no tag or artifact.
// The previous attempt must be published, or explicitly recorded as abandoned
// by the coordinator after checking distribution and immutable image evidence.
func ReserveFile(ctx context.Context, repo, version, ticket string) (string, error) {
	if !ValidVersion(version) || SchemeOf(version) != SchemeCalVer3 || ticket == "" || ticketKey.FindString(ticket) != ticket || !strings.HasPrefix(ticket, "AEON-") {
		return "", fmt.Errorf("reserve requires a fresh CalVer3 coordinate and AEON ticket")
	}
	shallow, err := runGit(ctx, repo, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(shallow) != "false" {
		return "", fmt.Errorf("reservation requires a full checkout with all release tags fetched")
	}
	path := filepath.Join(repo, "version.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var v versionFile
	if json.Unmarshal(raw, &v) != nil || v.Product != "PAIMOS AEON" || !ValidVersion(v.Version) {
		return "", fmt.Errorf("invalid version.json")
	}
	h, err := Build(ctx, Options{Repo: repo})
	if err != nil {
		return "", err
	}
	known := false
	for _, r := range h.Releases {
		if r.Version == v.Version {
			known = true
		}
		if version <= r.Version {
			return "", fmt.Errorf("coordinate must be later than every published or failed attempt")
		}
	}
	if !known {
		return "", fmt.Errorf("previous coordinate must be published or explicitly classified as unpublished/withdrawn before retry")
	}
	if version <= v.Version {
		return "", fmt.Errorf("coordinate must be later than version.json")
	}
	sequence, err := NextSequence(h, v.ReleaseChannel)
	if err != nil {
		return "", err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	name := codename.Codename(sequence)
	fields["version_scheme"], fields["version"] = SchemeCalVer3, version
	fields["release_sequence"], fields["codename"] = sequence, name
	fields["reserved_at"], fields["ticket"] = coordinateTime(version).Format(time.RFC3339), ticket
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return "", err
	}
	// One atomic replacement keeps unrelated metadata and the denial ledger.
	f, err := os.CreateTemp(repo, ".reservation-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.Write(append(out, '\n')); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return name, os.Rename(f.Name(), path)
}
