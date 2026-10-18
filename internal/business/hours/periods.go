// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const periodColumns = `id::text,principal_id::text,starts_at,ends_at,state,revision`

func scanPeriod(row pgx.Row) (Period, error) {
	var v Period
	err := row.Scan(&v.ID, &v.PrincipalID, &v.StartsAt, &v.EndsAt, &v.State, &v.Revision)
	v.StartsAt = utc(v.StartsAt)
	v.EndsAt = utc(v.EndsAt)
	return v, err
}
func loadPeriod(ctx context.Context, tx pgx.Tx, id string) (Period, error) {
	return scanPeriod(tx.QueryRow(ctx, `SELECT `+periodColumns+` FROM time_periods WHERE id=$1 FOR UPDATE`, id))
}
func hydratePeriod(ctx context.Context, tx pgx.Tx, v *Period) error {
	if v.State == "approved" {
		var a Approval
		err := tx.QueryRow(ctx, `SELECT approved_by_principal_id::text,entries_sha256,total_seconds,event_id FROM time_period_approvals WHERE period_id=$1`, v.ID).Scan(&a.ApprovedBy, &a.EntriesSHA256, &a.TotalSeconds, &a.EventID)
		if err != nil {
			return err
		}
		v.Approval = &a
		v.digest = a.EntriesSHA256
		return nil
	}
	entries, err := entriesFor(ctx, tx, v.ID, "", "")
	if err != nil {
		return err
	}
	v.digest, _, err = entryDigest(entries)
	return err
}
func (m *Module) listPeriods(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	principal, err := filter(r, "principal_id")
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent {
		if principal != "" && principal != p.ID {
			return nil, fail(403, "own periods only")
		}
		principal = p.ID
	}
	limit, after, since, until, err := listBounds(r)
	if err != nil {
		return nil, err
	}
	if since == nil {
		v := time.Now().UTC().Truncate(24*time.Hour).AddDate(-1, 0, 0)
		since = &v
	}
	// Keep newest-first order. The cursor row supplies the ordering tuple;
	// the caller's tenant and principal visibility still constrain the page.
	var cursorAt *time.Time
	if after != "" {
		var at time.Time
		if err := tx.QueryRow(r.Context(), `SELECT starts_at FROM time_periods WHERE id=$1 AND ($2::uuid IS NULL OR principal_id=$2)`, after, nullable(principal)).Scan(&at); err != nil {
			return nil, fail(400, "invalid period cursor")
		}
		cursorAt = &at
	}
	rows, err := tx.Query(r.Context(), `SELECT p.id::text,p.principal_id::text,p.starts_at,p.ends_at,p.state,p.revision,
        a.approved_by_principal_id::text,a.entries_sha256,a.total_seconds,a.event_id
        FROM time_periods p LEFT JOIN time_period_approvals a ON a.tenant_id=p.tenant_id AND a.period_id=p.id
        WHERE ($1::uuid IS NULL OR p.principal_id=$1)
        AND ($2::timestamptz IS NULL OR p.ends_at >= $2) AND ($3::timestamptz IS NULL OR p.starts_at < $3)
        AND ($4::timestamptz IS NULL OR p.starts_at<$4 OR (p.starts_at=$4 AND p.id>$5::uuid))
        ORDER BY p.starts_at DESC,p.id LIMIT $6`, nullable(principal), since, until, cursorAt, nullable(after), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := listPage[Period]{items: []Period{}}
	for rows.Next() {
		var v Period
		var who, digest *string
		var seconds, event *int64
		if err := rows.Scan(&v.ID, &v.PrincipalID, &v.StartsAt, &v.EndsAt, &v.State, &v.Revision, &who, &digest, &seconds, &event); err != nil {
			return nil, err
		}
		v.StartsAt, v.EndsAt = utc(v.StartsAt), utc(v.EndsAt)
		if v.State == "approved" {
			if who == nil || digest == nil || seconds == nil || event == nil {
				return nil, fail(500, "approved period has no approval")
			}
			v.Approval = &Approval{ApprovedBy: *who, EntriesSHA256: *digest, TotalSeconds: *seconds, EventID: *event}
		}
		if len(out.items) == limit {
			out.next = out.items[limit-1].ID
			break
		}
		out.items = append(out.items, v)
	}
	return out, rows.Err()
}

func (m *Module) getPeriod(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	v, err := loadPeriod(r.Context(), tx, r.PathValue("periodId"))
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && v.PrincipalID != p.ID {
		return nil, fail(403, "own periods only")
	}
	if err := hydratePeriod(r.Context(), tx, &v); err != nil {
		return nil, err
	}
	return v, nil
}
func validInterval(start, end time.Time) bool {
	return !start.IsZero() && !end.IsZero() && start.Year() >= 1 && end.Year() <= 9999 && end.After(start) && start.Nanosecond()%1000 == 0 && end.Nanosecond()%1000 == 0
}
func (m *Module) createPeriod(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in PeriodWrite
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	in.PrincipalID = strings.ToLower(in.PrincipalID)
	if !workorders.UUID(in.PrincipalID) || !validInterval(in.StartsAt, in.EndsAt) {
		return nil, fail(400, "principal and a valid period interval required")
	}
	if in.PrincipalID != p.ID && !admin(r.Context(), tx, p) {
		return nil, fail(403, "only an admin person can open another principal's period")
	}
	// Serialize same-principal creates to prevent duplicate/overlapping periods.
	var kind string
	if err := tx.QueryRow(r.Context(), `SELECT kind FROM principals WHERE id=$1 FOR UPDATE`, in.PrincipalID).Scan(&kind); err != nil {
		return nil, err
	}
	var overlap bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM time_periods WHERE principal_id=$1 AND starts_at<$3 AND ends_at>$2)`, in.PrincipalID, in.StartsAt, in.EndsAt).Scan(&overlap); err != nil {
		return nil, err
	}
	if overlap {
		return nil, fail(409, "period overlaps an existing period")
	}
	v, err := scanPeriod(tx.QueryRow(r.Context(), `INSERT INTO time_periods(tenant_id,principal_id,starts_at,ends_at) VALUES($1,$2,$3,$4) RETURNING `+periodColumns, p.TenantID, in.PrincipalID, in.StartsAt, in.EndsAt))
	if err != nil {
		return nil, err
	}
	if _, err = m.writer.Append(r.Context(), tx, p, events.Change{Type: "hours.period_created", After: v}); err != nil {
		return nil, err
	}
	if err := hydratePeriod(r.Context(), tx, &v); err != nil {
		return nil, err
	}
	return v, nil
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Module) approve(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if !admin(r.Context(), tx, p) {
		return nil, fail(403, "admin person required")
	}
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ExpectedDigest   string `json:"expected_entries_sha256"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.ExpectedRevision < 1 || !digestPattern.MatchString(in.ExpectedDigest) {
		return nil, fail(400, "revision and exact entries digest required")
	}
	v, err := loadPeriod(r.Context(), tx, r.PathValue("periodId"))
	if err != nil {
		return nil, err
	}
	if err = m.gate(r.Context(), tx, p, "period_approve"); err != nil {
		return nil, err
	}
	if err = hydratePeriod(r.Context(), tx, &v); err != nil {
		return nil, err
	}
	if v.Revision != in.ExpectedRevision || v.digest != in.ExpectedDigest {
		return nil, fail(409, "period changed; reload before approving")
	}
	if v.State == "approved" {
		if v.Approval.ApprovedBy != p.ID {
			return nil, fail(409, "period already approved by another person")
		}
		return v, nil
	}
	entries, err := entriesFor(r.Context(), tx, v.ID, "", "")
	if err != nil {
		return nil, err
	}
	_, seconds, err := entryDigest(entries)
	if err != nil {
		return nil, err
	}
	a := Approval{ApprovedBy: p.ID, EntriesSHA256: v.digest, TotalSeconds: seconds}
	event, err := m.writer.Append(r.Context(), tx, p, events.Change{Type: "hours.period_approved", Before: v, After: struct {
		PeriodID string   `json:"period_id"`
		Revision int64    `json:"revision"`
		Approval Approval `json:"approval"`
	}{v.ID, v.Revision, a}})
	if err != nil {
		return nil, err
	}
	a.EventID = event.ID
	_, err = tx.Exec(r.Context(), `INSERT INTO time_period_approvals(tenant_id,period_id,revision,approved_by_principal_id,entries_sha256,total_seconds,event_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, v.ID, v.Revision, p.ID, v.digest, seconds, event.ID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(r.Context(), `UPDATE time_periods SET state='approved' WHERE id=$1`, v.ID)
	if err != nil {
		return nil, err
	}
	v.State = "approved"
	v.Approval = &a
	return v, nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Retain exact JSON number text when moving SQL numeric values to the wire.
func number(s string) json.Number { return json.Number(s) }
