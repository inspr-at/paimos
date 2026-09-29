// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

// publicNote is the whitelisted benefit text from a frozen snapshot.
// Ticket ids, keys and hidden counts stay off this document.
type publicNote struct {
	PillEN    string `json:"pill_en"`
	PillDE    string `json:"pill_de"`
	BenefitEN string `json:"benefit_en"`
	BenefitDE string `json:"benefit_de"`
}

type publicRelease struct {
	ReleasedAt string       `json:"released_at"`
	Version    string       `json:"version,omitempty"`
	Notes      []publicNote `json:"notes"`
}

type publicReleasesDocument struct {
	Product  *portalProduct  `json:"product"`
	Releases []publicRelease `json:"releases"`
}

// loadPublicReleases reads frozen notes for the project linked on portal_pace.
// The project title is not selected. A missing link or a deleted project
// yields an empty list. Planning and unpublished releases are not queried.
func loadPublicReleases(ctx context.Context, tx pgx.Tx) ([]publicRelease, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.version, r.released_at, s.snapshot
		FROM portal_pace p
		JOIN nodes proj ON proj.tenant_id = p.tenant_id AND proj.id = p.project_node_id AND proj.deleted_at IS NULL
		JOIN node_kinds pk ON pk.tenant_id = proj.tenant_id AND pk.id = proj.kind_id AND pk.slug = 'project'
		JOIN journey_releases r ON r.tenant_id = p.tenant_id AND r.project_node_id = p.project_node_id
		JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
		JOIN node_kinds rk ON rk.tenant_id = rn.tenant_id AND rk.id = rn.kind_id AND rk.slug = 'release'
		JOIN journey_release_note_snapshots s
		  ON s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id
		 AND s.snapshot->>'schema' = 'aeon.release-note-snapshot.v1'
		 AND s.snapshot->>'frozen' = 'true'
		WHERE r.state IN ('released', 'superseded')
		  AND r.released_at IS NOT NULL
		  AND r.released_at <= clock_timestamp()
		ORDER BY r.released_at DESC, r.number DESC
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []publicRelease{}
	for rows.Next() {
		var version *string
		var at time.Time
		var raw []byte
		if err := rows.Scan(&version, &at, &raw); err != nil {
			return nil, err
		}
		rel, ok := projectPublicRelease(version, at, raw)
		if !ok {
			continue
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

type releaseSnapshot struct {
	Schema  string         `json:"schema"`
	Version string         `json:"version"`
	Frozen  bool           `json:"frozen"`
	Tickets []snapshotNote `json:"tickets"`
}

type snapshotNote struct {
	Fields      json.RawMessage `json:"fields"`
	Unavailable string          `json:"unavailable"`
}

// projectPublicRelease copies benefit lines that were complete and visible
// when the snapshot froze. A release whose stored version and row version
// both exist and differ is omitted. Invalid items are skipped.
func projectPublicRelease(version *string, at time.Time, raw []byte) (publicRelease, bool) {
	if len(raw) == 0 || len(raw) > 16<<20 || at.IsZero() {
		return publicRelease{}, false
	}
	var snap releaseSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		slog.Warn("portal release snapshot skipped")
		return publicRelease{}, false
	}
	if snap.Schema != releasehistory.SnapshotSchema || !snap.Frozen {
		return publicRelease{}, false
	}
	rowVersion := ""
	if version != nil {
		rowVersion = strings.TrimSpace(*version)
	}
	snapVersion := strings.TrimSpace(snap.Version)
	if rowVersion != "" && snapVersion != "" && rowVersion != snapVersion {
		return publicRelease{}, false
	}
	rel := publicRelease{ReleasedAt: at.UTC().Format(time.RFC3339), Notes: []publicNote{}}
	if releasehistory.ValidVersion(rowVersion) {
		rel.Version = rowVersion
	} else if rowVersion == "" && releasehistory.ValidVersion(snapVersion) {
		rel.Version = snapVersion
	}
	for _, ticket := range snap.Tickets {
		if strings.TrimSpace(ticket.Unavailable) != "" {
			continue
		}
		if len(ticketbenefits.Issues(ticket.Fields)) > 0 {
			continue
		}
		var fields struct {
			PillEN    string `json:"pill_en"`
			PillDE    string `json:"pill_de"`
			BenefitEN string `json:"benefit_en"`
			BenefitDE string `json:"benefit_de"`
			Hidden    bool   `json:"hide_from_release_notes"`
		}
		if json.Unmarshal(ticket.Fields, &fields) != nil || fields.Hidden {
			continue
		}
		note := publicNote{
			PillEN:    publicLine(fields.PillEN, 80),
			PillDE:    publicLine(fields.PillDE, 80),
			BenefitEN: publicLine(fields.BenefitEN, 600),
			BenefitDE: publicLine(fields.BenefitDE, 600),
		}
		if note.PillEN == "" && note.PillDE == "" && note.BenefitEN == "" && note.BenefitDE == "" {
			continue
		}
		rel.Notes = append(rel.Notes, note)
	}
	return rel, true
}
