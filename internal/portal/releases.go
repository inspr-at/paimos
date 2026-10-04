// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

// publicRelease carries Codename, the marketing name of a release this build's
// history knows (AEON-430); the portal shows it in front of the date.
type publicRelease struct {
	ReleasedAt string       `json:"released_at"`
	Version    string       `json:"version,omitempty"`
	Codename   string       `json:"codename,omitempty"`
	Notes      []publicNote `json:"notes"`
}

type publicReleasesDocument struct {
	Product  *portalProduct  `json:"product"`
	Releases []publicRelease `json:"releases"`
}

// portalPublishesReleases reports whether the linked project has turned
// release history on and still exists. A pace link without that choice, or a
// deleted project, publishes nothing.
func portalPublishesReleases(ctx context.Context, tx pgx.Tx) (bool, error) {
	productID, err := portalProductID(ctx, tx)
	if errors.Is(err, errNoProduct) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var on bool
	err = tx.QueryRow(ctx, `
		SELECT true
		FROM portal_product_pace p
		JOIN nodes proj ON proj.tenant_id = p.tenant_id AND proj.id = p.project_node_id AND proj.deleted_at IS NULL
		JOIN node_kinds pk ON pk.tenant_id = proj.tenant_id AND pk.id = proj.kind_id AND pk.slug = 'project'
		WHERE p.product_id=$1::uuid AND p.release_history`, productID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// loadPublicReleases reads frozen notes for the project linked on portal_pace
// once that link has turned release history on. The project title is not
// selected. A missing link, a link that has not opted in, or a deleted project
// yields an empty list. Planning and unpublished releases are not queried.
// A release with no public note is omitted.
type frozenSnapshot struct {
	version *string
	at      time.Time
	raw     []byte
}

func listFrozenSnapshots(ctx context.Context, tx pgx.Tx) ([]frozenSnapshot, error) {
	productID, err := portalProductID(ctx, tx)
	if errors.Is(err, errNoProduct) {
		return []frozenSnapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT r.version, r.released_at, s.snapshot
		FROM portal_product_pace p
		JOIN nodes proj ON proj.tenant_id = p.tenant_id AND proj.id = p.project_node_id AND proj.deleted_at IS NULL
		JOIN node_kinds pk ON pk.tenant_id = proj.tenant_id AND pk.id = proj.kind_id AND pk.slug = 'project'
		JOIN journey_releases r ON r.tenant_id = p.tenant_id AND r.project_node_id = p.project_node_id
		JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
		JOIN node_kinds rk ON rk.tenant_id = rn.tenant_id AND rk.id = rn.kind_id AND rk.slug = 'release'
		JOIN journey_release_note_snapshots s
		  ON s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id
		 AND s.snapshot->>'schema' = 'aeon.release-note-snapshot.v1'
		 AND s.snapshot->>'frozen' = 'true'
		WHERE p.product_id=$1::uuid AND p.release_history
		  AND r.state IN ('released', 'superseded')
		  AND r.released_at IS NOT NULL
		  AND r.released_at <= clock_timestamp()
		ORDER BY r.released_at DESC, r.number DESC
		LIMIT 100`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []frozenSnapshot{}
	for rows.Next() {
		var snap frozenSnapshot
		if err := rows.Scan(&snap.version, &snap.at, &snap.raw); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

func loadPublicReleases(ctx context.Context, tx pgx.Tx) ([]publicRelease, error) {
	snaps, err := listFrozenSnapshots(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := []publicRelease{}
	for _, snap := range snaps {
		rel, ok := projectPublicRelease(snap.version, snap.at, snap.raw)
		if !ok {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

type releaseSnapshot struct {
	Schema  string         `json:"schema"`
	Version string         `json:"version"`
	Frozen  bool           `json:"frozen"`
	Tickets []snapshotNote `json:"tickets"`
}

type snapshotNote struct {
	ID          string          `json:"id"`
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
	if rel.Version != "" {
		rel.Codename = publicCodename(rel.Version)
	}
	for _, ticket := range snap.Tickets {
		note, ok := visiblePublicNote(ticket)
		if !ok {
			continue
		}
		rel.Notes = append(rel.Notes, note)
	}
	if len(rel.Notes) == 0 {
		return publicRelease{}, false
	}
	return rel, true
}

var (
	historyOnce sync.Once
	history     releasehistory.History
)

// publicCodename names a version for the portal. Tests replace it.
var publicCodename = historyCodename

// historyCodename is the marketing name this build's release history gives a
// version, or "" for a version it does not know (another product's release).
func historyCodename(version string) string {
	historyOnce.Do(func() {
		if h, err := releasehistory.Embedded(); err == nil {
			history = h
		}
	})
	return releasehistory.CodenameOf(history, version)
}

// visiblePublicNote is the release-history rule: any non-empty public line
// after the completion check. One blank pill does not drop the benefit.
func visiblePublicNote(ticket snapshotNote) (publicNote, bool) {
	if strings.TrimSpace(ticket.Unavailable) != "" {
		return publicNote{}, false
	}
	if len(ticketbenefits.Issues(ticket.Fields)) > 0 {
		return publicNote{}, false
	}
	var fields struct {
		PillEN    string `json:"pill_en"`
		PillDE    string `json:"pill_de"`
		BenefitEN string `json:"benefit_en"`
		BenefitDE string `json:"benefit_de"`
		Hidden    bool   `json:"hide_from_release_notes"`
	}
	if json.Unmarshal(ticket.Fields, &fields) != nil || fields.Hidden {
		return publicNote{}, false
	}
	note := publicNote{
		PillEN:    publicLine(fields.PillEN, 80),
		PillDE:    publicLine(fields.PillDE, 80),
		BenefitEN: publicLine(fields.BenefitEN, 600),
		BenefitDE: publicLine(fields.BenefitDE, 600),
	}
	if note.PillEN == "" && note.PillDE == "" && note.BenefitEN == "" && note.BenefitDE == "" {
		return publicNote{}, false
	}
	return note, true
}
