// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var legacyVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
var calendarV1 = regexp.MustCompile(`^[1-9][0-9]\.[0-9]{2}\.[0-9]{2}(?:\.[0-9]{2}\.[0-9]{2}\.[0-9]{2})?$`)

// ValidVersion preserves tagged historical coordinates. It grants no product
// reservation authority and deliberately does not change the strict history API.
func ValidVersion(scheme, version string) bool {
	if len(scheme) > 64 || len(version) > 64 || scheme == "" || version == "" {
		return false
	}
	switch scheme {
	case "legacy":
		return legacyVersion.MatchString(version)
	case "inspr-calendar-v1":
		if !calendarV1.MatchString(version) {
			return false
		}
		layout := "2006.01.02"
		if len(version) == 17 {
			layout += ".15.04.05"
		}
		_, err := time.Parse(layout, "20"+version)
		return err == nil
	case "inspr-calendar-v2", "inspr-calver-3":
		return releasehistory.ValidVersion(version)
	}
	return false
}

func sum(raw []byte) string { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }

type sourceRelease struct {
	ID                       string
	Number                   int
	State, Scheme, Version   string
	ReleasedAt               *time.Time
	Kind, Project, NodeState string
	Deleted                  bool
	Updated                  time.Time
	Revision                 int64
	DoneAt                   *time.Time
	DoneEvent                int64
	Title                    string
}
type sourceMember struct {
	ID, Release, Kind, Project, State, Priority string
	Deleted                                     bool
	Created, Updated                            time.Time
	Position                                    int
	Journey                                     json.RawMessage
}

// plan is called in a repeatable-read snapshot, or under the exclusive importer
// and tree/access fences in apply. Every detail page is capped before decoding.
func (s *Service) plan(ctx context.Context, tx pgx.Tx, identity Identity) (Report, error) {
	r := Report{Identity: identity, CheckedAt: s.now(), Eligible: true, NextSequence: 1, Releases: []ReleaseMapping{}, Members: []MemberMapping{}, Reasons: []Reason{}}
	refuse := func(code, id, fix string) {
		r.Eligible = false
		if len(r.Reasons) < 5200 {
			r.Reasons = append(r.Reasons, Reason{code, id, fix})
		} else {
			r.Incomplete = true
		}
	}
	var kind, key, state string
	var deleted bool
	var updated time.Time
	err := tx.QueryRow(ctx, `SELECT k.slug,coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1)),n.state,n.deleted_at IS NOT NULL,n.updated_at FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1`, identity.Project).Scan(&kind, &key, &state, &deleted, &updated)
	if err != nil {
		return r, err
	}
	if kind != "project" || deleted {
		refuse("project_unavailable", identity.Project, "Restore the project or convert it back to project.")
	}
	var adopted, openHandoff bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1),EXISTS(SELECT 1 FROM stage_handoffs h JOIN journey_releases j ON j.tenant_id=h.tenant_id AND j.release_node_id=h.release_node_id WHERE j.project_node_id=$1 AND h.state IN ('requested','active','blocked'))`, identity.Project).Scan(&adopted, &openHandoff); err != nil {
		return r, err
	}
	if adopted {
		refuse("already_adopted", identity.Project, "The project already plans with releases.")
	}
	if openHandoff {
		refuse("open_handoff", identity.Project, "Finish or revoke the open stage handoff.")
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM journey_releases WHERE project_node_id=$1 LIMIT 201) bounded`, identity.Project).Scan(&r.Counts.Releases); err != nil {
		return r, err
	}
	// ALL journey members with a release, plus EVERY open live permitted node
	// without such membership. Neither conversion nor deletion drops a member.
	memberSQL := `SELECT n.id::text,coalesce(j.release_node_id::text,''),k.slug,coalesce(n.project_id::text,''),n.state,n.deleted_at IS NOT NULL,n.created_at,n.updated_at,
	 left(coalesce(n.fields->>'priority','none'),16),coalesce(j.walker_position,0),coalesce(to_jsonb(j),'null'::jsonb)
	 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	 LEFT JOIN journey_tickets j ON j.tenant_id=n.tenant_id AND j.ticket_node_id=n.id AND j.project_node_id=$1
	 WHERE (j.release_node_id IS NOT NULL OR (n.project_id=$1 AND n.deleted_at IS NULL AND k.slug IN ('epic','ticket','task') AND n.state NOT IN ('done','accepted','delivered','cancelled','canceled') AND j.release_node_id IS NULL))`
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (`+memberSQL+` LIMIT 5001) bounded`, identity.Project).Scan(&r.Counts.Members); err != nil {
		return r, err
	}
	if r.Counts.Releases > 200 || r.Counts.Members > 5000 {
		r.Incomplete = true
		r.Counts.AtLeast = true
		refuse("v1_limits", identity.Project, "At least the measured cap is exceeded. Reduce the mapped population; larger projects require AEON-610.")
		return r, nil
	}
	sources := []sourceRelease{}
	cursor := "00000000-0000-0000-0000-000000000000"
	for {
		rows, e := tx.Query(ctx, `SELECT j.release_node_id::text,j.number,j.state,coalesce(j.version_scheme,''),coalesce(j.version,''),j.released_at,k.slug,coalesce(n.project_id::text,''),n.state,n.deleted_at IS NOT NULL,n.updated_at,j.revision,
		 completed.at,coalesce(completed.id,0),n.title
		 FROM journey_releases j JOIN nodes n ON n.tenant_id=j.tenant_id AND n.id=j.release_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		 LEFT JOIN LATERAL(SELECT e.at,e.id FROM events e WHERE e.node_id=n.id AND e.after->>'state' IN ('done','accepted','delivered') AND e.before->>'state' IS DISTINCT FROM e.after->>'state' ORDER BY e.id DESC LIMIT 1) completed ON true
		 WHERE j.project_node_id=$1 AND j.release_node_id>$2::uuid ORDER BY j.release_node_id LIMIT 200`, identity.Project, cursor)
		if e != nil {
			return r, e
		}
		n := 0
		for rows.Next() {
			var v sourceRelease
			if e = rows.Scan(&v.ID, &v.Number, &v.State, &v.Scheme, &v.Version, &v.ReleasedAt, &v.Kind, &v.Project, &v.NodeState, &v.Deleted, &v.Updated, &v.Revision, &v.DoneAt, &v.DoneEvent, &v.Title); e != nil {
				rows.Close()
				return r, e
			}
			sources = append(sources, v)
			cursor = v.ID
			n++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return r, e
		}
		if n < 200 {
			break
		}
		if len(sources) >= 200 {
			break
		}
	}
	members := []sourceMember{}
	cursor = "00000000-0000-0000-0000-000000000000"
	for {
		rows, e := tx.Query(ctx, `SELECT * FROM (`+memberSQL+`) mapped WHERE id>$2::text ORDER BY id LIMIT 200`, identity.Project, cursor)
		if e != nil {
			return r, e
		}
		n := 0
		for rows.Next() {
			var v sourceMember
			if e = rows.Scan(&v.ID, &v.Release, &v.Kind, &v.Project, &v.State, &v.Deleted, &v.Created, &v.Updated, &v.Priority, &v.Position, &v.Journey); e != nil {
				rows.Close()
				return r, e
			}
			members = append(members, v)
			cursor = v.ID
			n++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return r, e
		}
		if n < 200 {
			break
		}
		if len(members) >= 5000 {
			break
		}
	}
	product := s.cfg.Product.TenantID == identity.Tenant && s.cfg.Product.ProjectID == identity.Project
	// AEON is the explicitly configured product key in serve.go. An unresolved
	// product binding cannot make a would-be product quietly adopt local counters.
	if key == "AEON" && !product {
		refuse("product_binding", identity.Project, "Resolve the instance-local AEON tenant/project binding and pinned product inputs.")
	}
	high := 0
	history := map[int]bool{}
	if product {
		p := s.cfg.Product
		if key != "AEON" || p.Repository != "inspr-at/paimos" || !digestRE.MatchString(p.HistoryDigest) || !digestRE.MatchString(p.VersionDigest) || p.VersionSequence < 1 || len(p.HistorySequences) > 10000 {
			refuse("product_binding", identity.Project, "Verify the product/repository binding and immutable history/version digests.")
		}
		high = p.VersionSequence
		for _, n := range p.HistorySequences {
			if n < 1 {
				refuse("product_binding", identity.Project, "Repair invalid pinned history identity.")
			}
			history[n] = true
			if n > high {
				high = n
			}
		}
	}
	for _, v := range sources {
		if v.Number > high {
			high = v.Number
		}
	}
	if high >= math.MaxInt32-200 {
		refuse("sequence_exhausted", identity.Project, "The sequence counter has no remaining capacity.")
	} else {
		r.NextSequence = high + 1
	}
	counts := map[string]int{}
	for _, v := range members {
		if v.Project != identity.Project || !delivery.ItemKind(v.Kind) {
			refuse("member_guard", v.ID, "Move the member back to this project or convert it back to epic, ticket or task.")
		}
		if v.Release != "" {
			counts[v.Release]++
		}
	}
	versions := map[string]bool{}
	for _, v := range sources {
		if v.Kind != "release" || v.Project != identity.Project || v.Deleted {
			refuse("release_guard", v.ID, "Restore the release, move it back or convert it back to release.")
		}
		m := ReleaseMapping{ID: v.ID, Sequence: v.Number, OriginalSequence: v.Number, State: "planned", Origin: "adopted_planned", Scheme: v.Scheme, Version: v.Version, ReleasedAt: v.ReleasedAt, DefaultTitle: v.Title == fmt.Sprintf("Release %d", v.Number)}
		switch v.State {
		case "released", "superseded":
			m.State = "released"
			m.Origin = "adopted_released"
			m.CompletionBasis = "journey.released_at"
		case "planning":
			if delivery.Completed(v.NodeState) {
				m.State = "released"
				m.Origin = "adopted_released"
				m.ReleasedAt = v.DoneAt
				m.CompletionBasis = "events.state_changed"
				if m.ReleasedAt == nil {
					at := v.Updated
					m.ReleasedAt = &at
					m.CompletionBasis = "nodes.updated_at"
				}
				m.Scheme = ""
				m.Version = ""
			}
		default:
			refuse("journey_in_flight", v.ID, "Finish the journey build, candidate, deploy or access gate.")
		}
		if v.Version != "" || v.Scheme != "" {
			if !ValidVersion(v.Scheme, v.Version) {
				refuse("version_pair", v.ID, "Repair the malformed or unsupported tagged version without rewriting published history.")
			}
			if versions[v.Version] {
				refuse("duplicate_version", v.ID, "Resolve the duplicate historical version in this project.")
			}
			versions[v.Version] = true
		}
		if m.State == "planned" {
			r.Counts.Active++
		}
		if counts[v.ID] > 1000 {
			refuse("release_capacity", v.ID, "Move members out; a release can hold at most 1,000 rows, including tombstones.")
		}
		if product && history[v.Number] {
			if m.State == "planned" && counts[v.ID] == 0 {
				m.Sequence = r.NextSequence
				r.NextSequence++
			} else {
				refuse("sequence_collision", v.ID, "Only an empty planned product release can be renumbered around pinned history.")
			}
		}
		r.Releases = append(r.Releases, m)
	}
	if r.Counts.Active > 40 {
		refuse("active_release_capacity", identity.Project, "Reduce mapped non-terminal releases to 40 before adoption.")
	}
	sort.Slice(r.Releases, func(i, j int) bool { return r.Releases[i].Sequence < r.Releases[j].Sequence })
	keys, err := delivery.SeedRanks(len(r.Releases))
	if err != nil {
		return r, err
	}
	for i := range r.Releases {
		r.Releases[i].Rank = keys[i]
	}
	priority := func(p string) int {
		switch p {
		case "urgent":
			return 0
		case "high":
			return 1
		case "medium":
			return 2
		case "low":
			return 3
		default:
			return 4
		}
	}
	sort.Slice(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if a.Release != b.Release {
			return a.Release < b.Release
		}
		if a.Release != "" {
			if a.Position != b.Position {
				return a.Position < b.Position
			}
		} else {
			if priority(a.Priority) != priority(b.Priority) {
				return priority(a.Priority) < priority(b.Priority)
			}
			if !a.Created.Equal(b.Created) {
				return a.Created.Before(b.Created)
			}
		}
		return a.ID < b.ID
	})
	for start := 0; start < len(members); {
		end := start + 1
		for end < len(members) && members[end].Release == members[start].Release {
			end++
		}
		keys, err = delivery.SeedRanks(end - start)
		if err != nil {
			return r, err
		}
		for i := start; i < end; i++ {
			v := members[i]
			source := "seed"
			if v.Release != "" {
				source = "adopted"
			}
			r.Members = append(r.Members, MemberMapping{v.ID, v.Release, keys[i-start], source, v.Deleted})
		}
		start = end
	}
	// Fingerprint excludes attempt and wall-clock metadata, but binds every
	// copied/eligibility value and product identity in this one snapshot.
	pinned := Product{TenantID: s.cfg.Product.TenantID, ProjectID: s.cfg.Product.ProjectID}
	if product {
		pinned = s.cfg.Product
	}
	raw, err := json.Marshal(struct {
		Instance, Tenant, Project, Migration, Kind, Key, State string
		Deleted, Adopted, Handoff                              bool
		Updated                                                time.Time
		Releases                                               []sourceRelease
		Members                                                []sourceMember
		Product                                                Product
	}{identity.Instance, identity.Tenant, identity.Project, identity.Migration, kind, key, state, deleted, adopted, openHandoff, updated, sources, members, pinned})
	if err != nil {
		return r, err
	}
	if len(raw) > MaxReportBytes {
		return r, fmt.Errorf("source metadata exceeds bounded report size")
	}
	r.Fingerprint = sum(raw)
	return r, ctx.Err()
}
