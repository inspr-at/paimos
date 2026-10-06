// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

func TestEscalatedClaimRequiresFreshMeasuredRoom(t *testing.T) {
	f := setup(t)
	o := f.order(t, 100)
	v := f.run(t, o)
	ids := f.reserve(t, v)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET trace='{"escalation":{"episode_id":"fixture"}}' WHERE id=$1`, v.ID)
		return err
	})
	body := claimBody(ids)
	path := "/api/runs/" + v.ID + "/claim"
	request := func() {
		t.Helper()
		raw := `{"daemon_id":"daemon-test","daemon_generation":"generation-1","reservation_ids":["` + strings.Join(ids, `","`) + `"]}`
		w := f.request(f.agent, "POST", path, raw, f.token)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "fresh measured account room") {
			t.Fatalf("unknown/stale room: %d %s", w.Code, w.Body.String())
		}
	}
	request()
	var queued agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/"+v.ID, nil, 200, &queued)
	if queued.Status != "queued" || queued.StartedAt != nil {
		t.Fatal("refused claim changed run", queued)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_kind='5h',capacity_source='agentd',capacity_read_at=clock_timestamp()-interval '11 minutes' WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	request()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_read_at=clock_timestamp() WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", path, body, 200, &v)
	if v.Status != "starting" || v.StartedAt == nil {
		t.Fatal("fresh room did not admit claim", v)
	}
}

func TestClaimAllowsLegacyNullTrace(t *testing.T) {
	f := setup(t)
	v := f.run(t, f.order(t, 100))
	ids := f.reserve(t, v)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET trace=NULL WHERE id=$1`, v.ID)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/claim", claimBody(ids), 200, &v)
	if v.Status != "starting" {
		t.Fatal("NULL trace prevented a valid claim", v)
	}
}

func (f *fixture) escalationState(t *testing.T, ticket, status string) {
	t.Helper()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Escalation project' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, ticket, project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO work_escalations(tenant_id,ticket_node_id,project_id,state) VALUES($1,$2,$3,jsonb_build_object('episode_id',($2::uuid)::text,'status',$4::text,'attempts',0,'held_cost_micros',0))`, f.person.TenantID, ticket, project, status)
		return err
	})
}

func TestQueueEscalationCannotBypassBoundedRetry(t *testing.T) {
	for _, status := range []string{"stuck", "awaiting_decision"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			ticket := f.ticket(t, "open", "high", nil)
			f.escalationState(t, ticket, status)
			raw := `{"node_id":"` + ticket + `"}`
			w := f.request(f.person, "POST", "/api/queue", raw, "")
			if w.Code != 409 || !strings.Contains(w.Body.String(), "stuck work") {
				t.Fatalf("queue bypass: %d %s", w.Code, w.Body.String())
			}
			if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, ticket); n != 0 {
				t.Fatal("refusal created queue run", n)
			}
		})
	}
}

func TestQueuedEscalationRechecksRoutingAndClaim(t *testing.T) {
	for _, status := range []string{"stuck", "awaiting_decision"} {
		for _, stage := range []string{"routing", "routed", "targeted", "order", "uncharged_trace", "old_episode"} {
			t.Run(status+"/"+stage, func(t *testing.T) {
				f := setup(t)
				ticket := f.ticket(t, "open", "high", nil)
				var v agentruns.Run
				if stage == "order" || stage == "uncharged_trace" || stage == "old_episode" {
					o := f.order(t, 100)
					f.tx(t, f.person, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, o.NodeID, ticket)
						return err
					})
					v = f.run(t, o)
				} else {
					var target map[string]any
					if stage == "targeted" {
						f.queueAccount(t, 1000000)
						target = map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
					}
					v = f.addQueue(t, ticket, target).Run
				}
				if stage == "routed" {
					f.queueAccount(t, 1000000)
					f.call(t, f.person, "POST", "/api/queue/next", map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 200, nil)
					f.call(t, f.person, "GET", "/api/runs/"+v.ID, nil, 200, &v)
				}
				f.escalationState(t, ticket, status)
				if stage == "routing" || stage == "routed" {
					if stage == "routing" {
						f.queueAccount(t, 1000000)
					}
					var picked struct{ Entry *qEntry }
					f.call(t, f.person, "POST", "/api/queue/next", map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 200, &picked)
					if picked.Entry != nil {
						t.Fatal("stuck queue run was routed without a charged retry", picked.Entry.Run.ID)
					}
				} else {
					ids := f.reserve(t, v)
					if stage == "uncharged_trace" || stage == "old_episode" {
						f.tx(t, f.person, func(tx pgx.Tx) error {
							var previous string
							if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,started_at,ended_at) VALUES($1,$2,$3,$4,'failed',now(),now()) RETURNING id::text`, f.person.TenantID, v.OrderID, f.agent.ID, f.profile).Scan(&previous); err != nil {
								return err
							}
							episode := ticket
							if stage == "old_episode" {
								episode = uuid()
								if _, err := tx.Exec(t.Context(), `UPDATE work_escalations SET state=state||jsonb_build_object('attempts',1,'held_cost_micros',100,'used_profile_ids',jsonb_build_array($2::text)) WHERE ticket_node_id=$1`, ticket, f.profile); err != nil {
									return err
								}
							}
							if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET retry_of_run_id=$4,trace=jsonb_build_object('escalation',jsonb_build_object('episode_id',$2::text,'attempts',1,'held_cost_micros',100,'used_profile_ids',jsonb_build_array($3::text))) WHERE id=$1`, v.ID, episode, f.profile, previous); err != nil {
								return err
							}
							_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_kind='5h',capacity_source='agentd',capacity_read_at=clock_timestamp() WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, v.ID)
							return err
						})
					}
					raw := `{"daemon_id":"daemon-test","daemon_generation":"generation-1","reservation_ids":["` + strings.Join(ids, `","`) + `"]}`
					w := f.request(f.agent, "POST", "/api/runs/"+v.ID+"/claim", raw, f.token)
					if w.Code != 409 || !strings.Contains(w.Body.String(), "stuck work") {
						t.Fatalf("claim bypass: %d %s", w.Code, w.Body.String())
					}
				}
				f.call(t, f.person, "GET", "/api/runs/"+v.ID, nil, 200, &v)
				if v.Status != "queued" || v.StartedAt != nil {
					t.Fatal("refused dispatch started run", v)
				}
			})
		}
	}
}
