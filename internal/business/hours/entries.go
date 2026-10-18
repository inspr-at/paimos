// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const entryColumns = `id::text,period_id::text,principal_id::text,node_id::text,cost_unit_node_id::text,source,agent_run_id::text,started_at,ended_at,duration_seconds,rate_amount::text,currency,amount::text,note,updated_at`

func scanEntry(row pgx.Row) (Entry, error) {
	var v Entry
	var rate, amount string
	err := row.Scan(&v.ID, &v.PeriodID, &v.PrincipalID, &v.NodeID, &v.CostUnitID, &v.Source, &v.AgentRunID, &v.StartedAt, &v.EndedAt, &v.DurationSeconds, &rate, &v.Currency, &amount, &v.Note, &v.UpdatedAt)
	v.StartedAt = utc(v.StartedAt)
	v.EndedAt = utc(v.EndedAt)
	v.UpdatedAt = utc(v.UpdatedAt)
	v.RateAmount = number(rate)
	v.Amount = number(amount)
	return v, err
}
func entriesFor(ctx context.Context, tx pgx.Tx, period, principal, node string) ([]Entry, error) {
	rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM time_entries WHERE ($1::uuid IS NULL OR period_id=$1) AND ($2::uuid IS NULL OR principal_id=$2) AND ($3::uuid IS NULL OR node_id=$3) ORDER BY id`, nullable(period), nullable(principal), nullable(node))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		v, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (m *Module) listEntries(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	period, err := filter(r, "period_id")
	if err != nil {
		return nil, err
	}
	principal, err := filter(r, "principal_id")
	if err != nil {
		return nil, err
	}
	node, err := filter(r, "node_id")
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent {
		if principal != "" && principal != p.ID {
			return nil, fail(403, "own entries only")
		}
		principal = p.ID
	}
	limit, after, since, until, err := listBounds(r)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+entryColumns+` FROM time_entries
        WHERE ($1::uuid IS NULL OR period_id=$1) AND ($2::uuid IS NULL OR principal_id=$2)
        AND ($3::uuid IS NULL OR node_id=$3) AND ($4::uuid IS NULL OR id>$4)
        AND ($5::timestamptz IS NULL OR started_at >= $5) AND ($6::timestamptz IS NULL OR started_at < $6)
        ORDER BY id LIMIT $7`, nullable(period), nullable(principal), nullable(node), nullable(after), since, until, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := listPage[Entry]{items: []Entry{}}
	for rows.Next() {
		v, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		if len(out.items) == limit {
			out.next = out.items[limit-1].ID
			break
		}
		out.items = append(out.items, v)
	}
	return out, rows.Err()
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func (m *Module) createEntry(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in EntryWrite
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	in.PeriodID = strings.ToLower(in.PeriodID)
	in.CostUnitID = strings.ToLower(in.CostUnitID)
	in.PrincipalID = strings.ToLower(in.PrincipalID)
	in.NodeID = strings.ToLower(in.NodeID)
	if !workorders.UUID(in.PeriodID) || !workorders.UUID(in.CostUnitID) || !currencyPattern.MatchString(in.Currency) || len(in.Note) > 65536 {
		return nil, fail(400, "period, cost unit, currency and bounded note required")
	}
	switch in.Source {
	case "manual":
		if p.Kind != tenant.Person {
			return nil, fail(403, "manual time requires a person")
		}
		if in.AgentRunID != nil || !workorders.UUID(in.PrincipalID) || !workorders.UUID(in.NodeID) || !validInterval(in.StartedAt, in.EndedAt) {
			return nil, fail(400, "manual principal, node and interval required; run is not allowed")
		}
		if in.PrincipalID != p.ID && !admin(r.Context(), tx, p) {
			return nil, fail(403, "only an admin person can record another person's time")
		}
		var kind string
		if err := tx.QueryRow(r.Context(), `SELECT kind FROM principals WHERE id=$1`, in.PrincipalID).Scan(&kind); err != nil {
			return nil, err
		}
		if kind != "person" {
			return nil, fail(400, "manual time must belong to a person")
		}
	case "agent_run":
		if in.AgentRunID == nil || !workorders.UUID(*in.AgentRunID) {
			return nil, fail(400, "agent run required")
		}
		if in.PrincipalID != "" || in.NodeID != "" || !in.StartedAt.IsZero() || !in.EndedAt.IsZero() {
			return nil, fail(400, "run principal, node and interval are server-derived")
		}
		runID := strings.ToLower(*in.AgentRunID)
		in.AgentRunID = &runID
		var start, end *time.Time
		var status string
		if err := tx.QueryRow(r.Context(), `SELECT agent_principal_id::text,work_order_id::text,started_at,ended_at,status FROM agent_runs WHERE id=$1 FOR SHARE`, runID).Scan(&in.PrincipalID, &in.NodeID, &start, &end, &status); err != nil {
			return nil, err
		}
		if (p.Kind == tenant.Agent && p.ID != in.PrincipalID) || (p.Kind == tenant.Person && !admin(r.Context(), tx, p)) {
			return nil, fail(403, "run owner or admin person required")
		}
		if (status != "completed" && status != "failed" && status != "cancelled") || start == nil || end == nil {
			return nil, fail(409, "run needs terminal start and end times")
		}
		in.StartedAt = *start
		in.EndedAt = *end
		existing, err := scanEntry(tx.QueryRow(r.Context(), `SELECT `+entryColumns+` FROM time_entries WHERE agent_run_id=$1`, runID))
		if err == nil {
			if existing.PeriodID == in.PeriodID && existing.CostUnitID == in.CostUnitID && existing.Currency == in.Currency && existing.Note == in.Note {
				return existing, nil
			}
			return nil, fail(409, "run already converted with different values")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	default:
		return nil, fail(400, "source must be manual or agent_run")
	}
	if !validInterval(in.StartedAt, in.EndedAt) || in.StartedAt.Nanosecond() != in.EndedAt.Nanosecond() {
		return nil, fail(409, "entry requires a positive whole-second duration with exact source timestamps")
	}
	period, err := loadPeriod(r.Context(), tx, in.PeriodID)
	if err != nil {
		return nil, err
	}
	if err = m.gate(r.Context(), tx, p, "time_entry"); err != nil {
		return nil, err
	}
	// Check again after the period lock for concurrent conversions of one run.
	if in.AgentRunID != nil {
		existing, err := scanEntry(tx.QueryRow(r.Context(), `SELECT `+entryColumns+` FROM time_entries WHERE agent_run_id=$1`, *in.AgentRunID))
		if err == nil {
			if existing.PeriodID == in.PeriodID && existing.CostUnitID == in.CostUnitID && existing.Currency == in.Currency && existing.Note == in.Note {
				return existing, nil
			}
			return nil, fail(409, "run already converted with different values")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if period.State != "open" || period.PrincipalID != in.PrincipalID || in.StartedAt.Before(period.StartsAt) || in.EndedAt.After(period.EndsAt) {
		return nil, fail(409, "entry must fit its principal's open period")
	}
	var workID string
	if err := tx.QueryRow(r.Context(), `SELECT id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, in.NodeID).Scan(&workID); err != nil {
		return nil, err
	}
	// Lock the pricing node so rate replacement and node deletion cannot race.
	var costID string
	if err := tx.QueryRow(r.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='cost_unit' FOR SHARE OF n`, in.CostUnitID).Scan(&costID); err != nil {
		return nil, err
	}
	var rate string
	var count int
	if err := tx.QueryRow(r.Context(), `SELECT count(*),coalesce(min(bill_amount)::text,'') FROM cost_unit_rates WHERE cost_unit_node_id=$1 AND unit='hour' AND currency=$2 AND effective_from<=$3::date AND (effective_until IS NULL OR effective_until>$3::date)`, in.CostUnitID, in.Currency, in.StartedAt.UTC().Format("2006-01-02")).Scan(&count, &rate); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, fail(409, "exactly one effective hourly rate is required")
	}
	seconds := in.EndedAt.Unix() - in.StartedAt.Unix()
	v, err := scanEntry(tx.QueryRow(r.Context(), `INSERT INTO time_entries(tenant_id,period_id,principal_id,node_id,cost_unit_node_id,source,agent_run_id,started_at,ended_at,duration_seconds,rate_amount,currency,amount,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::bigint,$11::numeric,$12,round($11::numeric*($10::bigint)::numeric/3600,4),$13) RETURNING `+entryColumns, p.TenantID, in.PeriodID, in.PrincipalID, in.NodeID, in.CostUnitID, in.Source, in.AgentRunID, in.StartedAt, in.EndedAt, seconds, rate, in.Currency, in.Note))
	if err != nil {
		return nil, err
	}
	if _, err = m.writer.Append(r.Context(), tx, p, events.Change{NodeID: &v.NodeID, Type: "hours.entry_created", After: v}); err != nil {
		return nil, err
	}
	before := period
	if err = tx.QueryRow(r.Context(), `UPDATE time_periods SET revision=revision+1 WHERE id=$1 RETURNING revision`, period.ID).Scan(&period.Revision); err != nil {
		return nil, err
	}
	if _, err = m.writer.Append(r.Context(), tx, p, events.Change{Type: "hours.period_updated", Before: before, After: period}); err != nil {
		return nil, err
	}
	return v, nil
}
func (m *Module) totals(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := strings.ToLower(r.PathValue("nodeId"))
	approved := r.URL.Query().Get("approved_only")
	if approved != "" && approved != "true" && approved != "false" {
		return nil, fail(400, "approved_only must be true or false")
	}
	var live bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, fail(404, "live root node not found")
	}
	principal := ""
	if p.Kind == tenant.Agent {
		principal = p.ID
	}
	rows, err := tx.Query(r.Context(), `WITH RECURSIVE subtree(id) AS (
 SELECT id FROM nodes WHERE id=$1 AND deleted_at IS NULL
 UNION SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id=s.id WHERE n.deleted_at IS NULL
 ) SELECT e.currency,sum(e.duration_seconds)::bigint,sum(e.amount)::text
 FROM subtree s JOIN time_entries e ON e.node_id=s.id JOIN time_periods p ON p.id=e.period_id AND p.tenant_id=e.tenant_id
 WHERE (NOT $2::boolean OR p.state='approved') AND ($3::uuid IS NULL OR e.principal_id=$3)
 GROUP BY e.currency ORDER BY e.currency`, id, approved == "true", nullable(principal))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Totals{NodeID: id, Amounts: []Amount{}}
	for rows.Next() {
		var a Amount
		var seconds int64
		var amount string
		if err := rows.Scan(&a.Currency, &seconds, &amount); err != nil {
			return nil, err
		}
		a.Amount = number(amount)
		out.DurationSeconds += seconds
		out.Amounts = append(out.Amounts, a)
	}
	return out, rows.Err()
}
