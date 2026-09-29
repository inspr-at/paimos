// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// portalPace is the public delivery figure. Counts and medians only.
type portalPace struct {
	Releases30d          *int `json:"releases_30d,omitempty"`
	MedianReleaseGapDays *int `json:"median_release_gap_days,omitempty"`
	WishToLiveMedianDays *int `json:"wish_to_live_median_days,omitempty"`
}

func (p portalPace) empty() bool {
	return p.Releases30d == nil && p.MedianReleaseGapDays == nil && p.WishToLiveMedianDays == nil
}

type paceAdmin struct {
	ProjectID            string        `json:"project_id,omitempty"`
	ProjectTitle         string        `json:"project_title,omitempty"`
	Releases30d          *int          `json:"releases_30d,omitempty"`
	MedianReleaseGapDays *int          `json:"median_release_gap_days,omitempty"`
	WishToLiveMedianDays *int          `json:"wish_to_live_median_days,omitempty"`
	Fulfillments         []fulfillment `json:"fulfillments"`
}

type fulfillment struct {
	WishID    string `json:"wish_id"`
	FeatureID string `json:"feature_id"`
}

func (m *Module) readPace(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, _ tenant.Principal) (any, error) {
		return loadPaceAdmin(ctx, tx)
	})
}

func (m *Module) writePace(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		projectID, clear, err := decodeOptionalID(r, "project_id")
		if err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid pace"}
		}
		if clear {
			if _, err := tx.Exec(ctx, `DELETE FROM portal_pace`); err != nil {
				return nil, err
			}
		} else {
			var title string
			err := tx.QueryRow(ctx, `
				SELECT left(n.title, 300)
				FROM nodes n
				JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
				WHERE n.id = $1::uuid AND n.deleted_at IS NULL AND k.slug = 'project'`, projectID).Scan(&title)
			if errors.Is(err, pgx.ErrNoRows) || title == "" {
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				return nil, statusError{status: http.StatusBadRequest, msg: "Choose a project in this workspace."}
			}
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_pace(tenant_id, project_node_id)
				VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid)
				ON CONFLICT (tenant_id) DO UPDATE SET project_node_id = EXCLUDED.project_node_id`, projectID); err != nil {
				return nil, err
			}
		}
		if _, err := events.Append(ctx, tx, p, events.Change{
			Type:  "portal.pace_updated",
			After: map[string]any{"linked": !clear},
		}); err != nil {
			return nil, err
		}
		return loadPaceAdmin(ctx, tx)
	})
}

func (m *Module) writeFulfillment(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		wishID := r.PathValue("wishId")
		if !uuidPattern.MatchString(wishID) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid wish"}
		}
		featureID, clear, err := decodeOptionalID(r, "feature_id")
		if err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid wish"}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		var created time.Time
		var state string
		err = tx.QueryRow(ctx, `
			SELECT n.created_at, n.state
			FROM nodes n
			JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
			WHERE n.id = $1::uuid AND n.parent_id = $2::uuid AND n.deleted_at IS NULL AND k.slug = 'portal_wish'`,
			wishID, productID).Scan(&created, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, statusError{status: http.StatusNotFound, msg: "not found"}
		}
		if err != nil {
			return nil, err
		}
		if state != "published" && state != "hidden" {
			return nil, statusError{status: http.StatusBadRequest, msg: "Publish the wish before it counts."}
		}
		if clear {
			if _, err := tx.Exec(ctx, `DELETE FROM portal_fulfillments WHERE wish_id = $1::uuid`, wishID); err != nil {
				return nil, err
			}
		} else {
			var featureState string
			err = tx.QueryRow(ctx, `
				SELECT n.state
				FROM nodes n
				JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
				WHERE n.id = $1::uuid AND n.parent_id = $2::uuid AND n.deleted_at IS NULL AND k.slug = 'portal_feature'`,
				featureID, productID).Scan(&featureState)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, statusError{status: http.StatusNotFound, msg: "not found"}
			}
			if err != nil {
				return nil, err
			}
			if featureState != "live" {
				return nil, statusError{status: http.StatusBadRequest, msg: "That feature is not live."}
			}
			liveAt, ok, err := featureLiveAt(ctx, tx, featureID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, statusError{status: http.StatusBadRequest, msg: "That feature has no recorded live date."}
			}
			if liveAt.Before(created) {
				return nil, statusError{status: http.StatusBadRequest, msg: "This wish was opened after the feature went live."}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_fulfillments(tenant_id, wish_id, feature_id)
				VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2::uuid)
				ON CONFLICT (tenant_id, wish_id) DO UPDATE SET feature_id = EXCLUDED.feature_id`, wishID, featureID); err != nil {
				return nil, err
			}
		}
		if _, err := events.Append(ctx, tx, p, events.Change{
			NodeID: &wishID,
			Type:   "portal.fulfillment_updated",
			After:  map[string]any{"linked": !clear},
		}); err != nil {
			return nil, err
		}
		return loadPaceAdmin(ctx, tx)
	})
}

func attachPace(ctx context.Context, tx pgx.Tx, productID string, doc *portalDocument) error {
	admin, err := loadPaceAdminFor(ctx, tx, productID)
	if err != nil {
		return err
	}
	pace := portalPace{
		Releases30d:          admin.Releases30d,
		MedianReleaseGapDays: admin.MedianReleaseGapDays,
		WishToLiveMedianDays: admin.WishToLiveMedianDays,
	}
	if !pace.empty() {
		doc.Pace = &pace
	}
	return nil
}

func loadPaceAdmin(ctx context.Context, tx pgx.Tx) (paceAdmin, error) {
	productID, err := portalProductID(ctx, tx)
	if errors.Is(err, errNoProduct) {
		return paceAdmin{Fulfillments: []fulfillment{}}, nil
	}
	if err != nil {
		return paceAdmin{}, err
	}
	return loadPaceAdminFor(ctx, tx, productID)
}

func loadPaceAdminFor(ctx context.Context, tx pgx.Tx, productID string) (paceAdmin, error) {
	out := paceAdmin{Fulfillments: []fulfillment{}}
	var projectID, title string
	err := tx.QueryRow(ctx, `
		SELECT p.project_node_id::text, left(n.title, 300)
		FROM portal_pace p
		JOIN nodes n ON n.tenant_id = p.tenant_id AND n.id = p.project_node_id
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.deleted_at IS NULL AND k.slug = 'project'`).Scan(&projectID, &title)
	linked := err == nil && uuidPattern.MatchString(projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return out, err
	}
	var released []time.Time
	if linked {
		out.ProjectID = projectID
		out.ProjectTitle = publicLine(title, 300)
		rows, err := tx.Query(ctx, `
			SELECT released_at
			FROM journey_releases
			WHERE project_node_id = $1::uuid
			  AND state IN ('released', 'superseded')
			  AND released_at IS NOT NULL
			  AND released_at <= clock_timestamp()
			ORDER BY released_at DESC
			LIMIT 400`, projectID)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var at time.Time
			if err := rows.Scan(&at); err != nil {
				rows.Close()
				return out, err
			}
			released = append(released, at)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
	}
	rows, err := tx.Query(ctx, `
		SELECT w.created_at, live.at, w.id::text, f.id::text
		FROM portal_fulfillments pf
		JOIN nodes w ON w.tenant_id = pf.tenant_id AND w.id = pf.wish_id
		JOIN nodes f ON f.tenant_id = pf.tenant_id AND f.id = pf.feature_id
		JOIN node_kinds wk ON wk.tenant_id = w.tenant_id AND wk.id = w.kind_id
		JOIN node_kinds fk ON fk.tenant_id = f.tenant_id AND fk.id = f.kind_id
		JOIN LATERAL (
			SELECT max(e.at) AS at
			FROM events e
			WHERE e.node_id = f.id
			  AND e.undo_of IS NULL
			  AND NOT EXISTS (
			    SELECT 1 FROM events u WHERE u.tenant_id = e.tenant_id AND u.undo_of = e.id
			  )
			  AND e.after->>'state' = 'live'
			  AND coalesce(e.before->>'state', '') <> 'live'
		) live ON live.at IS NOT NULL
		WHERE w.parent_id = $1::uuid AND f.parent_id = $1::uuid
		  AND w.deleted_at IS NULL AND f.deleted_at IS NULL
		  AND wk.slug = 'portal_wish' AND fk.slug = 'portal_feature'
		  AND w.state IN ('published', 'hidden') AND f.state = 'live'
		ORDER BY w.created_at, w.id`, productID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var wishDays []int
	for rows.Next() {
		var created, liveAt time.Time
		var wishID, featureID string
		if err := rows.Scan(&created, &liveAt, &wishID, &featureID); err != nil {
			return out, err
		}
		out.Fulfillments = append(out.Fulfillments, fulfillment{WishID: wishID, FeatureID: featureID})
		if liveAt.Before(created) {
			continue
		}
		wishDays = append(wishDays, dayGap(created, liveAt))
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	pace := paceFrom(now, linked, released, wishDays)
	out.Releases30d = pace.Releases30d
	out.MedianReleaseGapDays = pace.MedianReleaseGapDays
	out.WishToLiveMedianDays = pace.WishToLiveMedianDays
	return out, nil
}

func featureLiveAt(ctx context.Context, tx pgx.Tx, featureID string) (time.Time, bool, error) {
	var at *time.Time
	err := tx.QueryRow(ctx, `
		SELECT max(e.at)
		FROM events e
		WHERE e.node_id = $1::uuid
		  AND e.undo_of IS NULL
		  AND NOT EXISTS (SELECT 1 FROM events u WHERE u.tenant_id = e.tenant_id AND u.undo_of = e.id)
		  AND e.after->>'state' = 'live'
		  AND coalesce(e.before->>'state', '') <> 'live'`, featureID).Scan(&at)
	if err != nil || at == nil {
		return time.Time{}, false, err
	}
	return *at, true, nil
}

// paceFrom folds release instants and wish-to-live day counts.
// A linked project with no releases still reports zero releases in 30 days.
// The median gap uses every consecutive pair in the sample, including long ones.
// The window is 30 times 24 hours, not a calendar month.
func paceFrom(now time.Time, linked bool, released []time.Time, wishDays []int) portalPace {
	var out portalPace
	if linked {
		n := countRecent(now, released)
		out.Releases30d = &n
		ordered := append([]time.Time(nil), released...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })
		var kept []time.Time
		for _, at := range ordered {
			if !at.After(now) {
				kept = append(kept, at)
			}
		}
		var gaps []int
		for i := 1; i < len(kept); i++ {
			gap := dayGap(kept[i-1], kept[i])
			if gap >= 0 {
				gaps = append(gaps, gap)
			}
		}
		if med, ok := medianInts(gaps); ok {
			out.MedianReleaseGapDays = &med
		}
	}
	if med, ok := medianInts(wishDays); ok {
		out.WishToLiveMedianDays = &med
	}
	return out
}

func countRecent(now time.Time, released []time.Time) int {
	cutoff := now.Add(-30 * 24 * time.Hour)
	n := 0
	for _, at := range released {
		if !at.Before(cutoff) && !at.After(now) {
			n++
		}
	}
	return n
}

func dayGap(from, to time.Time) int {
	return int(utcDate(to).Sub(utcDate(from)).Hours() / 24)
}

func utcDate(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func medianInts(values []int) (int, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2], true
	}
	sum := sorted[n/2-1] + sorted[n/2]
	if sum >= 0 {
		return (sum + 1) / 2, true
	}
	return (sum - 1) / 2, true
}

func decodeOptionalID(r *http.Request, key string) (id string, clear bool, err error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return "", false, errors.New("invalid JSON")
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, portalEditLimit+1))
	if err != nil || len(buf) == 0 || len(buf) > portalEditLimit {
		return "", false, errors.New("invalid JSON")
	}
	dec := json.NewDecoder(strings.NewReader(string(buf)))
	dec.DisallowUnknownFields()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", false, errors.New("invalid JSON")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return "", false, errors.New("invalid JSON")
	}
	value, ok := raw[key]
	if !ok || len(raw) != 1 {
		return "", false, errors.New("invalid JSON")
	}
	if strings.TrimSpace(string(value)) == "null" {
		return "", true, nil
	}
	if json.Unmarshal(value, &id) != nil || !uuidPattern.MatchString(id) {
		return "", false, errors.New("invalid JSON")
	}
	return id, false, nil
}
