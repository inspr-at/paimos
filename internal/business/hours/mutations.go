// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type EntryPatch struct {
	NodeID          *string    `json:"node_id"`
	CostUnitID      *string    `json:"cost_unit_node_id"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds *int64     `json:"duration_seconds"`
	Note            *string    `json:"note"`
}

func entryTag(e Entry) string { return `"` + e.UpdatedAt.UTC().Format(time.RFC3339Nano) + `"` }

func checkPrecondition(r *http.Request, e Entry) error {
	if values, ok := r.Header["If-Match"]; ok {
		for _, tag := range strings.Split(strings.Join(values, ","), ",") {
			if tag = strings.TrimSpace(tag); tag == "*" || tag == entryTag(e) {
				return nil
			}
		}
		return fail(412, "time entry changed; reload before correcting")
	}
	if values, ok := r.Header["If-Unmodified-Since"]; ok {
		if len(values) != 1 {
			return fail(400, "invalid If-Unmodified-Since")
		}
		t, err := time.Parse(time.RFC3339Nano, values[0])
		if err != nil {
			t, err = http.ParseTime(values[0])
		}
		if err != nil {
			return fail(400, "invalid If-Unmodified-Since")
		}
		// Like node PATCH, this is the last observed revision, not a time window.
		if !t.Equal(e.UpdatedAt) {
			return fail(412, "time entry changed; reload before correcting")
		}
	}
	return nil
}

// refreshPerson checks current stored authority, including undo requests whose
// session role snapshot may predate an admin's demotion.
func refreshPerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) (tenant.Principal, error) {
	var kind tenant.PrincipalKind
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE tenant_id=$1 AND id=$2 FOR SHARE`, p.TenantID, p.ID).Scan(&kind)
	if err != nil {
		return p, err
	}
	if p.Kind != tenant.Person || kind != tenant.Person || authz.RequireTx(ctx, tx, p, "hours.write", authz.Scope{}) != nil {
		return p, fail(403, "member or admin person required")
	}
	return p, nil
}

// lockEntry follows the same period-first lock order as creation and approval.
// The second read is essential: another correction may commit while we wait.
func lockEntry(ctx context.Context, tx pgx.Tx, id string) (Entry, Period, error) {
	var periodID string
	if err := tx.QueryRow(ctx, `SELECT period_id::text FROM time_entries WHERE id=$1`, id).Scan(&periodID); err != nil {
		return Entry{}, Period{}, err
	}
	period, err := loadPeriod(ctx, tx, periodID)
	if err != nil {
		return Entry{}, period, err
	}
	entry, err := scanEntry(tx.QueryRow(ctx, `SELECT `+entryColumns+` FROM time_entries WHERE id=$1 FOR UPDATE`, id))
	return entry, period, err
}

func (m *Module) mutateEntry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		httpapi.WriteError(w, 401, "unauthorized")
		return
	}
	if p.Kind != tenant.Person {
		httpapi.WriteError(w, 403, "entry corrections require a person")
		return
	}
	p.ID, p.TenantID = strings.ToLower(p.ID), strings.ToLower(p.TenantID)
	id := strings.ToLower(r.PathValue("id"))
	if !workorders.UUID(id) {
		httpapi.WriteError(w, 400, "invalid entry id")
		return
	}
	var patch EntryPatch
	if r.Method == http.MethodPatch {
		r.Body = httpapi.RequestBodyReader(w, r, 1<<20)
		if err := workorders.Decode(r, &patch); err != nil {
			workorders.WriteError(w, err)
			return
		}
		if patch == (EntryPatch{}) {
			httpapi.WriteError(w, 400, "at least one correction required")
			return
		}
	}
	var current, out Entry
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		p, err = refreshPerson(r.Context(), tx, p)
		if err != nil {
			return err
		}
		if err = m.gate(r.Context(), tx, p, "time_entry"); err != nil {
			return err
		}
		var period Period
		current, period, err = lockEntry(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if current.PrincipalID != p.ID && !admin(r.Context(), tx, p) {
			return fail(403, "entry author or admin required")
		}
		if period.State != "open" {
			return fail(409, "approved periods are immutable; time entries cannot be changed")
		}
		if err = checkPrecondition(r, current); err != nil {
			return err
		}
		change := events.Change{NodeID: &current.NodeID, Before: current}
		if r.Method == http.MethodDelete {
			if _, err = tx.Exec(r.Context(), `DELETE FROM time_entries WHERE id=$1`, id); err != nil {
				return err
			}
			change.Type = "time_entry.deleted"
		} else {
			out, err = applyPatch(r.Context(), tx, current, period, patch)
			if err != nil {
				return err
			}
			if reflect.DeepEqual(comparable(out), comparable(current)) {
				return nil
			}
			change.Type, change.After = "time_entry.updated", out
		}
		if _, err = m.writer.Append(r.Context(), tx, p, change); err != nil {
			return err
		}
		return bumpPeriod(r.Context(), tx, period.ID)
	})
	if err != nil {
		if e, ok := err.(*workorders.Error); ok && e.Status == 412 {
			w.Header().Set("ETag", entryTag(current))
			httpapi.WriteJSON(w, 412, struct {
				Error   string `json:"error"`
				Current Entry  `json:"current"`
			}{e.Message, current})
		} else {
			workorders.WriteError(w, err)
		}
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("ETag", entryTag(out))
	httpapi.WriteJSON(w, 200, out)
}

// The entry event covers both the corrected snapshot and its period revision.
// Digests and totals are computed from rows on reads, with no aggregate cache.
func bumpPeriod(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE time_periods SET revision=revision+1 WHERE id=$1`, id)
	return err
}

func applyPatch(ctx context.Context, tx pgx.Tx, before Entry, period Period, patch EntryPatch) (Entry, error) {
	v := before
	if patch.DurationSeconds != nil && patch.EndedAt != nil {
		return v, fail(400, "supply duration_seconds or ended_at, not both")
	}
	if patch.NodeID != nil {
		v.NodeID = strings.ToLower(*patch.NodeID)
	}
	if patch.CostUnitID != nil {
		v.CostUnitID = strings.ToLower(*patch.CostUnitID)
	}
	if patch.Note != nil {
		v.Note = *patch.Note
	}
	if !workorders.UUID(v.NodeID) || !workorders.UUID(v.CostUnitID) || len(v.Note) > 65536 {
		return v, fail(400, "invalid node, cost unit or note")
	}
	if patch.StartedAt != nil {
		v.StartedAt = utc(*patch.StartedAt)
	}
	seconds := before.DurationSeconds
	if patch.DurationSeconds != nil {
		seconds = *patch.DurationSeconds
	}
	if patch.EndedAt == nil && (patch.StartedAt != nil || patch.DurationSeconds != nil) {
		// Avoid both time.Duration overflow and dates beyond the supported range.
		if seconds <= 0 || seconds > period.EndsAt.Unix()-v.StartedAt.Unix() {
			return v, fail(409, "entry must fit its open period")
		}
		v.EndedAt = time.Unix(v.StartedAt.Unix()+seconds, int64(v.StartedAt.Nanosecond())).UTC()
	}
	if patch.EndedAt != nil {
		v.EndedAt = utc(*patch.EndedAt)
	}
	if !validInterval(v.StartedAt, v.EndedAt) || v.StartedAt.Nanosecond() != v.EndedAt.Nanosecond() || v.StartedAt.Before(period.StartsAt) || v.EndedAt.After(period.EndsAt) {
		return v, fail(409, "entry needs a positive whole-second interval inside its open period")
	}
	v.DurationSeconds = v.EndedAt.Unix() - v.StartedAt.Unix()
	if v.Source == "agent_run" && (v.NodeID != before.NodeID || !v.StartedAt.Equal(before.StartedAt) || !v.EndedAt.Equal(before.EndedAt)) {
		return v, fail(409, "agent-run node and interval are telemetry-derived")
	}
	if reflect.DeepEqual(comparable(v), comparable(before)) {
		return before, nil
	}
	var live string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, v.NodeID).Scan(&live); err != nil {
		return v, err
	}
	if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='cost_unit' FOR SHARE OF n`, v.CostUnitID).Scan(&live); err != nil {
		return v, err
	}
	if v.CostUnitID != before.CostUnitID || v.StartedAt.UTC().Format("2006-01-02") != before.StartedAt.UTC().Format("2006-01-02") {
		var count int
		var rate string
		if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(min(bill_amount)::text,'') FROM cost_unit_rates WHERE cost_unit_node_id=$1 AND unit='hour' AND currency=$2 AND effective_from<=$3::date AND (effective_until IS NULL OR effective_until>$3::date)`, v.CostUnitID, v.Currency, v.StartedAt.UTC().Format("2006-01-02")).Scan(&count, &rate); err != nil {
			return v, err
		}
		if count != 1 {
			return v, fail(409, "exactly one effective hourly rate is required")
		}
		v.RateAmount = number(rate)
	}
	return updateEntry(ctx, tx, v)
}

func updateEntry(ctx context.Context, tx pgx.Tx, v Entry) (Entry, error) {
	return scanEntry(tx.QueryRow(ctx, `UPDATE time_entries SET node_id=$2,cost_unit_node_id=$3,started_at=$4,ended_at=$5,duration_seconds=$6::bigint,rate_amount=$7::numeric,amount=round($7::numeric*($6::bigint)::numeric/3600,4),note=$8,updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING `+entryColumns, v.ID, v.NodeID, v.CostUnitID, v.StartedAt, v.EndedAt, v.DurationSeconds, v.RateAmount.String(), v.Note))
}
