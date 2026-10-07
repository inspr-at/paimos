// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/jackc/pgx/v5"
)

const columns = `id::text,project_id::text,ticket_node_id::text,repository,pull_request,branch,head_sha,state,state_since,owner,deadline_at,held_reason,link_source,updated_at,observation`

func scan(row pgx.Row) (Item, error) {
	var i Item
	var raw []byte
	err := row.Scan(&i.ID, &i.Project, &i.Ticket, &i.Repository, &i.PR, &i.Branch, &i.Head, &i.State, &i.Since, &i.Owner, &i.Deadline, &i.HeldReason, &i.LinkSource, &i.Updated, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &i.Observation)
	}
	return i, err
}
func load(ctx context.Context, tx pgx.Tx, id string) (*Item, error) {
	i, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM delivery_items WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &i, err
}
func settingsTx(ctx context.Context, tx pgx.Tx, project *string) (Settings, error) {
	out := defaults()
	for _, scope := range []*string{nil, project} {
		if scope == nil && project == nil && out.RequiredChecks == nil {
			continue
		}
		var checks []string
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT required_checks,deadlines FROM delivery_settings WHERE project_id IS NOT DISTINCT FROM $1::uuid`, scope).Scan(&checks, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		row := Settings{}
		if checks != nil {
			row.RequiredChecks = &checks
		}
		if err = json.Unmarshal(raw, &row.Deadlines); err != nil {
			return out, err
		}
		out = effective(out, row)
		if project == nil {
			break
		}
	}
	return out, nil
}
func linkTx(ctx context.Context, tx pgx.Tx, o *Observation, title string) error {
	var ticket string
	var project *string
	err := tx.QueryRow(ctx, `SELECT v.ticket_node_id::text,n.project_id::text FROM work_order_reviews v JOIN nodes n ON n.id=v.ticket_node_id WHERE v.repository=$1 AND v.pull_request=$2 AND n.deleted_at IS NULL ORDER BY v.created_at DESC,v.work_order_id DESC LIMIT 1`, o.Repository, o.PR).Scan(&ticket, &project)
	if err == nil {
		o.Ticket = &ticket
		o.Project = project
		s := "review_row"
		o.LinkSource = &s
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	for _, hint := range []struct{ key, source string }{{keyFromTitle(title), "title_key"}, {keyFromBranch(o.Branch), "branch_key"}} {
		if hint.key == "" {
			continue
		}
		err = tx.QueryRow(ctx, `SELECT n.id::text,n.project_id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE upper(n.key)=$1 AND n.deleted_at IS NULL AND k.slug='work' AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=n.id)`, hint.key).Scan(&ticket, &project)
		if err == nil {
			o.Ticket = &ticket
			o.Project = project
			s := hint.source
			o.LinkSource = &s
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}
func platformTx(ctx context.Context, tx pgx.Tx, o *Observation) error {
	o.Built = false
	o.ReviewedHead = ""
	if o.Ticket == nil {
		return nil
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN work_orders w ON w.node_id=r.work_order_id JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=$1 AND w.kind='build' AND r.status='completed')`, o.Ticket).Scan(&o.Built); err != nil {
		return err
	}
	var order, head string
	err := tx.QueryRow(ctx, `SELECT work_order_id::text,head_sha FROM work_order_reviews WHERE ticket_node_id=$1 AND repository=$2 ORDER BY created_at DESC,work_order_id DESC LIMIT 1`, o.Ticket, o.Repository).Scan(&order, &head)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ok, err := reviewgate.VerifiedLatestTx(ctx, tx, order, *o.Ticket)
	if err != nil {
		return err
	}
	if ok {
		o.ReviewedHead = head
	}
	return nil
}
func saveTx(ctx context.Context, tx pgx.Tx, tid string, o Observation) (Item, *events.Change, error) {
	if o.PR != nil && o.Ticket != nil {
		preID := stableID(tid, o.Repository, subject(nil, o.Ticket))
		if _, err := tx.Exec(ctx, `DELETE FROM delivery_items WHERE id=$1`, preID); err != nil {
			return Item{}, nil, err
		}
	}
	before, err := load(ctx, tx, o.ID)
	if err != nil {
		return Item{}, nil, err
	}
	if before != nil && before.State == Merged {
		return *before, nil, nil
	}
	after := project(o, before)
	raw, err := json.Marshal(o)
	if err != nil {
		return after, nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,branch,head_sha,state,state_since,owner,deadline_at,held_reason,link_source,updated_at,observation)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT(tenant_id,id) DO UPDATE SET project_id=EXCLUDED.project_id,ticket_node_id=EXCLUDED.ticket_node_id,pull_request=EXCLUDED.pull_request,branch=EXCLUDED.branch,head_sha=EXCLUDED.head_sha,state=EXCLUDED.state,state_since=EXCLUDED.state_since,owner=EXCLUDED.owner,deadline_at=EXCLUDED.deadline_at,held_reason=EXCLUDED.held_reason,link_source=EXCLUDED.link_source,updated_at=EXCLUDED.updated_at,observation=EXCLUDED.observation`, tid, after.ID, after.Project, after.Ticket, after.Repository, after.PR, after.Branch, after.Head, after.State, after.Since, after.Owner, after.Deadline, after.HeldReason, after.LinkSource, after.Updated, raw)
	if err != nil {
		return after, nil, err
	}
	if reflect.DeepEqual(snapshot(before), snapshot(&after)) && reflect.DeepEqual(before.HeldReason, after.HeldReason) {
		return after, nil, nil
	}
	meta, _ := json.Marshal(map[string]any{"delivery_item_id": after.ID, "repository": after.Repository, "pull_request": after.PR})
	return after, &events.Change{Type: "delivery.state_changed", NodeID: after.Ticket, Before: snapshot(before), After: snapshot(&after), At: &o.At, Metadata: meta}, nil
}

// appendChanges runs after every projection/ledger write and lock acquisition.
func appendChanges(ctx context.Context, tx pgx.Tx, tid string, changes []events.Change) error {
	if len(changes) == 0 {
		return nil
	}
	actor, err := systemactor.Ensure(ctx, tx, tid)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if _, err = events.Append(ctx, tx, actor, c); err != nil {
			return err
		}
	}
	return nil
}

type record struct {
	ID, Event, Action, Repository, Head, Hash string
	PR                                        *int64
	At                                        time.Time
	Observations                              []Observation
	Fanout                                    bool
}

// recordTx requires the tenant fence, and writes the ledger before the event counter.
func recordTx(ctx context.Context, tx pgx.Tx, tid string, r record) (bool, error) {
	if r.Event == "reconcile" || r.Event == "platform" {
		changed := []Observation{}
		for _, o := range r.Observations {
			before, err := load(ctx, tx, o.ID)
			if err != nil {
				return false, err
			}
			if before == nil || !normalizedEqual(before.Observation, o) {
				changed = append(changed, o)
			}
		}
		r.Observations = changed
		if len(changed) == 0 {
			return false, nil
		}
	}
	raw, err := json.Marshal(r.Observations)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO delivery_github_events(tenant_id,delivery_id,event,action,repository,pull_request,head_sha,received_at,payload_sha256,observations,fanout_done,processed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$8) ON CONFLICT(tenant_id,delivery_id) DO NOTHING`, tid, r.ID, r.Event, r.Action, r.Repository, r.PR, r.Head, r.At, r.Hash, raw, !r.Fanout)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	changes := []events.Change{}
	for _, o := range r.Observations {
		_, change, err := saveTx(ctx, tx, tid, o)
		if err != nil {
			return false, err
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}
	return true, appendChanges(ctx, tx, tid, changes)
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func internalRecord(kind string, at time.Time, os []Observation) record {
	raw, _ := json.Marshal(os)
	sum := digest(raw)
	repo := ""
	if len(os) > 0 {
		repo = os[0].Repository
	}
	return record{ID: kind + "-" + sum, Event: kind, Repository: repo, Hash: sum, At: at, Observations: os}
}

// Rebuild replaces the disposable projection from normalized ledger facts,
// without duplicating public events. The tenant fence serializes live ingestion.
func (m *Module) Rebuild(ctx context.Context, tid string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ctx = db.AllProjects(ctx, "delivery projection rebuild")
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		// Clearing a derived table is reversible from the append-only ledger.
		if _, err := tx.Exec(ctx, `DELETE FROM delivery_items`); err != nil {
			return err
		}
		var after int64
		for {
			rows, err := tx.Query(ctx, `SELECT sequence,observations FROM delivery_github_events WHERE sequence>$1 AND processed_at IS NOT NULL ORDER BY sequence LIMIT 100`, after)
			if err != nil {
				return err
			}
			type entry struct {
				seq int64
				raw []byte
			}
			page := []entry{}
			for rows.Next() {
				var e entry
				if err = rows.Scan(&e.seq, &e.raw); err != nil {
					rows.Close()
					return err
				}
				page = append(page, e)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			for _, e := range page {
				var os []Observation
				if err = json.Unmarshal(e.raw, &os); err != nil {
					return err
				}
				for _, o := range os {
					if _, _, err = saveTx(ctx, tx, tid, o); err != nil {
						return err
					}
				}
				after = e.seq
			}
			if len(page) < 100 {
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = m.refreshExistingFacts(ctx, tid); err != nil {
		return err
	}
	return m.refreshPlatform(ctx, tid)
}
func subject(pr *int64, ticket *string) string {
	if pr != nil {
		return fmt.Sprintf("pr/%d", *pr)
	}
	if ticket != nil {
		return "ticket/" + *ticket
	}
	return "unlinked"
}
