// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
)

// Risk: publication ships the wrong episode, replays Undo, bypasses person
// checks or leaves the one-day acceptance rule with no delivery episode.
func TestGitPublicationDeliveryAndOneDayAcceptance(t *testing.T) {
	f := setup(t)
	p := &releasehistory.Publication{ProjectKey: "AUT", Repository: "inspr-at/paimos", Version: "261017115202.0.0", Name: "Aglow Apsis", Tag: "v261017115202.0.0", PreviousTag: "v261016060843.0.0", Commit: strings.Repeat("a", 40), TaggedAt: f.now, Commits: []string{strings.Repeat("b", 40), strings.Repeat("c", 40)}, Tickets: map[string]string{"AUT-10": strings.Repeat("b", 40)}}
	f.m.WithPublication(p, p.Version, f.p.TenantID, p.Repository)
	human := "Touch ID"
	shipped := f.add("AUT-2", "work", "done", 2, nil)
	prior := f.add("AUT-3", "work", "done", 2, nil)
	follow := f.add("AUT-4", "work", "done", 2, nil)
	held := f.add("AUT-5", "work", "done", 2, &human)
	other := f.add("AUT-6", "work", "qa", 2, nil)
	parent := f.add("AUT-7", "work", "done", 2, nil)
	child := f.add("AUT-8", "work", "open", 2, nil)
	lateDone := f.add("AUT-9", "work", "done", 0, nil)
	gitOnly := f.add("AUT-10", "work", "done", 2, nil)
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{project_key}','"AUT"') WHERE id=$1`, f.project); err != nil {
			return err
		}
		s := Defaults()
		s.Rules["accept"] = Rule{true, 1}
		rules, _ := json.Marshal(s.Rules)
		if _, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,true,$2)`, f.p.TenantID, rules); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, child, parent); err != nil {
			return err
		}
		// Merge completion may arrive after tagging; immutable merge ancestry still wins.
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET created_at=$2 WHERE id=$1`, lateDone, f.now.Add(time.Hour)); err != nil {
			return err
		}
		for _, fact := range []struct {
			id, sha string
			at      time.Time
		}{
			{shipped, p.Commits[0], f.now.Add(-2 * time.Hour)}, {prior, strings.Repeat("d", 40), f.now.Add(-48 * time.Hour)},
			{follow, p.Commits[0], f.now.Add(-2 * time.Hour)},
			{held, p.Commits[0], f.now.Add(-2 * time.Hour)}, {other, p.Commits[0], f.now.Add(-2 * time.Hour)},
			{parent, p.Commits[0], f.now.Add(-2 * time.Hour)}, {lateDone, p.Commits[0], f.now.Add(-2 * time.Hour)},
		} {
			meta, _ := json.Marshal(map[string]any{"repository": p.Repository, "merge_sha": fact.sha, "merged_at": fact.at.Format(time.RFC3339Nano)})
			if _, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,metadata,at,before,after) VALUES($1,$2,$3,'delivery.merge_done',$4,$5,'{"state":"done"}','{"state":"done"}')`, f.p.TenantID, f.p.ID, fact.id, meta, fact.at); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_merge_audit(tenant_id,repository,merge_sha,pull_request,ticket_node_id,project_id,merged_by,via_queue,checks_passed,review_ok,flags,at) VALUES($1,$2,$3,99,$4,$5,'test',true,true,true,'{}',$6)`, f.p.TenantID, p.Repository, strings.Repeat("e", 40), follow, f.project, f.now.Add(time.Hour))
		return err
	})
	f.run(f.now)
	for id, want := range map[string]string{shipped: "delivered", prior: "done", follow: "done", held: "done", other: "qa", parent: "done", lateDone: "delivered", gitOnly: "delivered"} {
		if got := f.state(id).State; got != want {
			t.Fatalf("%s state %s; want %s", id, got, want)
		}
	}
	changes := f.changes(shipped)
	if len(changes) != 1 || !strings.Contains(changes[0].Reason, "Aglow Apsis") || !strings.Contains(changes[0].Reason, p.Version) {
		t.Fatalf("release audit: %+v", changes)
	}
	skips := func() int {
		var count int
		f.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='status_autopilot.skipped' AND metadata->>'reason' LIKE '%Touch ID%'`, held).Scan(&count)
		})
		return count
	}
	if skips() != 1 {
		t.Fatal("human-check skip was not audited")
	}
	f.run(f.now)
	if len(f.changes(shipped)) != 1 || skips() != 1 {
		t.Fatal("re-publish repeated changes/comments")
	}
	// Inject the clock at the threshold; no sleeps or elapsed-time assertions.
	var deliveredAt time.Time
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT at FROM events WHERE node_id=$1 AND type=$2 ORDER BY id DESC LIMIT 1`, shipped, Changed).Scan(&deliveredAt)
	})
	s := Defaults()
	s.Rules["accept"] = Rule{true, 1}
	c := candidate{Node: f.state(shipped), Since: deliveredAt}
	if evaluate(c, s, deliveredAt.Add(24*time.Hour-time.Nanosecond)) != nil {
		t.Fatal("accepted before one day")
	}
	if d := evaluate(c, s, deliveredAt.Add(24*time.Hour)); d == nil || d.To != "accepted" {
		t.Fatalf("one-day acceptance: %+v", d)
	}
	f.run(deliveredAt.Add(48 * time.Hour)) // next daily pass after the full day elapsed
	if f.state(shipped).State != "accepted" || f.state(held).State != "done" || f.state(follow).State != "done" {
		t.Fatal("daily acceptance selected wrong tickets")
	}
	if got := f.changes(shipped); len(got) != 2 || got[0].Rule != "accept" && got[1].Rule != "accept" {
		t.Fatalf("acceptance audit: %+v", got)
	}
	// Receipts also protect work reopened to Done, as after Undo.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='done' WHERE id=$1`, gitOnly)
		return err
	})
	f.run(deliveredAt.Add(49 * time.Hour))
	if f.state(gitOnly).State != "done" {
		t.Fatal("publication receipt did not protect reopened work")
	}
	// An explicitly disabled project must not be overridden by publication.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_projects(tenant_id,project_id,mode) VALUES($1,$2,'off')`, f.p.TenantID, f.project)
		return err
	})
	p.Tickets["AUT-11"] = p.Commits[1]
	off := f.add("AUT-11", "work", "done", 2, nil)
	f.run(deliveredAt.Add(72 * time.Hour))
	if f.state(off).State != "done" {
		t.Fatalf("disabled project delivered %s", off)
	}

	// Removing current write authority cannot be bypassed by the System actor.
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE status_autopilot_projects SET mode='inherit' WHERE project_id=$1`, f.project); err != nil {
			return err
		}
		// Retain an active owner binding, as the database requires, but on an
		// agent. It cannot substitute for the person's revoked write authority.
		var retained string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Retained fixture owner') RETURNING id::text`, f.p.TenantID).Scan(&retained); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='owner'`, f.p.TenantID, retained); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID)
		return err
	})
	if err := f.m.RunTenant(t.Context(), f.p.TenantID, deliveredAt.Add(73*time.Hour)); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revoked writer error: %v", err)
	}
	if f.state(off).State != "done" {
		t.Fatal("publication ignored revoked write authority")
	}
}
