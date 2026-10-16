// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type admissionFake struct {
	mu             sync.Mutex
	f              *fixture
	states         map[string]string
	posts          []string
	enqueues       []string
	dequeues       int
	post           func(context.Context, string, string) error
	refuseRemoval  bool
	mergeOnDequeue bool
	reads          int
}

func (g *admissionFake) Status(_ context.Context, sha string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads++
	return g.states[sha], nil
}
func (g *admissionFake) PostStatus(ctx context.Context, sha, state string) error {
	if g.post != nil {
		if err := g.post(ctx, sha, state); err != nil {
			return err
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.states[sha] = state
	g.posts = append(g.posts, sha+":"+state)
	return nil
}
func (g *admissionFake) Enqueue(_ context.Context, pr int64, head string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.f.gh.pulls[pr]
	if p.Head != head {
		return errors.New("expectedHeadOid mismatch")
	}
	p.Queued = true
	g.f.gh.pulls[pr] = p
	g.enqueues = append(g.enqueues, head)
	return nil
}
func (g *admissionFake) Dequeue(_ context.Context, pr int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dequeues++
	if g.mergeOnDequeue {
		p := g.f.gh.pulls[pr]
		p.Queued = false
		p.Open = false
		p.Merged = true
		g.f.gh.pulls[pr] = p
		return crossreview.ErrTransient
	}
	if !g.refuseRemoval {
		p := g.f.gh.pulls[pr]
		p.Queued = false
		g.f.gh.pulls[pr] = p
	}
	return nil
}

type admissionFixture struct {
	*fixture
	writer  *admissionFake
	approve func(Pull, string)
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	f := newReviewFixture(t)
	ctx := t.Context()
	reviewer := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.read"}, KeyCreatorID: f.person.ID}
	var authorSession, reviewerSession string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Admission reviewer') RETURNING id::text`, reviewer.TenantID).Scan(&reviewer.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','fixture',decode(repeat('00',32),'hex'),$3,$4) RETURNING id::text`, reviewer.TenantID, reviewer.ID, reviewer.Scopes, f.person.ID).Scan(&reviewer.KeyID); err != nil {
			return err
		}
		reviewer.AuthKeyID = reviewer.KeyID
		for _, s := range []struct {
			principal, harness, model string
			id                        *string
		}{{f.agent.ID, "codex", "gpt-6.1-sol", &authorSession}, {reviewer.ID, "claude", "claude-opus-4-6", &reviewerSession}} {
			digest := sha256.Sum256([]byte(s.principal))
			if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,model,reasoning_effort,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,$6,'xhigh','fixture','unmanaged','worker','ship',$7,$7) RETURNING id::text`, f.person.TenantID, f.project, s.principal, f.ticket, s.harness, s.model, digest[:]).Scan(s.id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'ci.yml','nightly.yml',now())`, f.person.TenantID, f.project, f.m.config.Repository)
		return err
	})
	dbtest.BindRole(t, f.d, reviewer.TenantID, reviewer.ID, "viewer")
	policy := AdmissionPolicy{NightlyState: "green", BlockOnNightlyRed: true, Holds: []AdmissionHold{}}
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-admission", policy, 200, nil)
	approve := func(p Pull, verdict string) {
		t.Helper()
		f.at = f.at.Add(time.Second)
		f.tx(t, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			// P1 issues a run only for the ticket's latest delivery.
			if _, err := tx.Exec(ctx, `UPDATE delivery_items SET updated_at=$3 WHERE repository=$1 AND pull_request=$2`, f.m.config.Repository, p.Number, f.at); err != nil {
				return err
			}
			run, err := reviewgate.CreateRunTx(ctx, tx, f.person, f.ticket, reviewgate.RunCreate{Repository: f.m.config.Repository, PR: p.Number, Target: p.Target, Base: p.AdmissionDiff.MergeBase, Head: p.Head, Scope: p.AdmissionDiff.MergeBase + ".." + p.Head, AuthorSession: authorSession, ReviewerSession: reviewerSession, Kind: "review"}, f.at)
			if err != nil {
				return err
			}
			credential, _, err := reviewgate.ClaimRunTx(ctx, tx, reviewer, run.ID, f.at)
			if err != nil {
				return err
			}
			_, err = reviewgate.SubmitRunTx(ctx, tx, reviewer, run.ID, credential, reviewgate.RunSubmit{Binding: run.RunBinding, Verdict: verdict, VisualImpact: "no", Reason: "Documentation only"}, f.at)
			return err
		})
	}
	p := f.pull()
	p.Target = "main"
	p.AdmissionDiff = &AdmissionDiff{MergeBase: p.Base, Paths: []string{"docs/admission.md"}, Complete: true}
	p.Checks = nil
	for _, name := range *defaults().RequiredChecks {
		p.Checks = append(p.Checks, Check{Name: name, Status: "completed", Conclusion: "success"})
	}
	f.gh.pulls[p.Number] = p
	f.webhook(t, "pull_request", "opened", "admission-open", 204)
	approve(p, "ok")
	writer := &admissionFake{f: f, states: map[string]string{}}
	f.m.WithAdmission(writer)
	return &admissionFixture{fixture: f, writer: writer, approve: approve}
}
func (f *admissionFixture) hold(t *testing.T, on bool) {
	t.Helper()
	path := "/api/delivery/" + f.item(t, 7).ID + "/admission"
	var snapshot AdmissionSnapshot
	f.call(t, f.person, "GET", path, nil, 200, &snapshot)
	control := snapshot.Control
	control.Holds = []AdmissionHold{}
	if on {
		control.Holds = append(control.Holds, AdmissionHold{Kind: "manual", Reason: "Stop active groups"})
	}
	f.call(t, f.person, "PUT", path, control, 200, &snapshot)
	if snapshot.Effective || snapshot.Enforcement != "pending" {
		t.Fatalf("intent reported effective: %+v", snapshot)
	}
}
func requireAdmissionState(t *testing.T, f *admissionFixture, sha, want string) {
	t.Helper()
	got, err := f.writer.Status(t.Context(), sha)
	if err != nil || got != want {
		t.Fatalf("state %q, error %v; want %q", got, err, want)
	}
}

func TestAdmissionPublisherSerializesStalledSuccessAndCurrentFailure(t *testing.T) {
	// Risk: a stalled old success overtakes a committed hold, or another replica
	// publishes concurrently. Barriers establish the exact interleaving.
	f := newAdmissionFixture(t)
	head := f.gh.pulls[7].Head
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.writer.post = func(_ context.Context, _ string, state string) error {
		if state == "success" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- f.m.publishAdmission(t.Context(), head) }()
	<-entered
	f.hold(t, true)
	// Enforcement has its own worker lane and completes while POST is stalled.
	if err := f.m.admitQueueEntry(t.Context(), admissionQueueEntry{PR: 7, Head: head}); err != nil {
		t.Fatal(err)
	}
	var held AdmissionSnapshot
	f.call(t, f.person, "GET", "/api/delivery/"+f.item(t, 7).ID+"/admission", nil, 200, &held)
	if !held.Effective {
		t.Fatal("stalled publisher blocked queue enforcement")
	}
	restarted := New(f.d.App, f.m.config, nil, f.gh, nil).WithAdmission(f.writer)
	if err := restarted.publishAdmission(t.Context(), head); err != nil {
		t.Fatal(err)
	}
	f.writer.mu.Lock()
	if len(f.writer.posts) != 0 {
		t.Fatal("second publisher bypassed session lock")
	}
	f.writer.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, head, "failure")
	if len(f.writer.posts) != 2 || !strings.HasSuffix(f.writer.posts[0], ":success") || !strings.HasSuffix(f.writer.posts[1], ":failure") {
		t.Fatal(f.writer.posts)
	}
	// A delayed request from an uncertain prior HTTP outcome is repaired by the
	// next reconciliation even after the durable revision was acknowledged.
	f.writer.mu.Lock()
	f.writer.states[head] = "success"
	f.writer.mu.Unlock()
	if err := restarted.publishAdmission(t.Context(), head); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, head, "failure")
	posts := len(f.writer.posts)
	if err := restarted.publishAdmission(t.Context(), head); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.posts) != posts {
		t.Fatal("confirmed reconciliation repeated an identical status POST")
	}
	var revision int64
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&revision)
	})
	observed := f.gh.pulls[7]
	observed.Checks = append(append([]Check{}, observed.Checks...), Check{Name: AdmissionContext, Status: "completed", Conclusion: "failure"})
	f.gh.pulls[7] = observed
	f.webhook(t, "status", "", "admission-own-status", 204)
	f.tx(t, func(tx pgx.Tx) error {
		var after int64
		err := tx.QueryRow(t.Context(), `SELECT revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&after)
		if err == nil && after != revision {
			t.Fatal("publisher's own webhook invalidated its acknowledged revision")
		}
		return err
	})

}

func TestAdmissionPublisherResumesUncertainPublicationAfterRestart(t *testing.T) {
	// Risk: process death after GitHub accepts a request drops the outbox item.
	f := newAdmissionFixture(t)
	head := f.gh.pulls[7].Head
	ctx, cancel := context.WithCancel(t.Context())
	f.writer.post = func(_ context.Context, sha, state string) error {
		f.writer.mu.Lock()
		f.writer.states[sha] = state
		f.writer.mu.Unlock()
		cancel()
		return crossreview.ErrTransient
	}
	if err := f.m.publishAdmission(ctx, head); err == nil {
		t.Fatal("interrupted publication acknowledged")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var pending bool
		err := tx.QueryRow(t.Context(), `SELECT uncertain AND published_revision<revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&pending)
		if err == nil && !pending {
			t.Fatal("lost durable pending intent")
		}
		return err
	})
	f.hold(t, true)
	f.writer.post = nil
	restarted := New(f.d.App, f.m.config, nil, f.gh, nil).WithAdmission(f.writer)
	if err := restarted.publishAdmission(t.Context(), head); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, head, "failure")
	f.tx(t, func(tx pgx.Tx) error {
		var complete bool
		err := tx.QueryRow(t.Context(), `SELECT NOT uncertain AND published_revision=revision AND confirmed_state='failure' FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&complete)
		if err == nil && !complete {
			t.Fatal("restart did not confirm current revision")
		}
		return err
	})
}

func TestAdmissionPublisherRepostsAfterUncertainOutcome(t *testing.T) {
	// Risk: a 5xx accepted by GitHub leaves an obsolete success as the last word.
	f := newAdmissionFixture(t)
	head := f.gh.pulls[7].Head
	calls := 0
	f.writer.post = func(_ context.Context, sha, state string) error {
		calls++
		if calls == 1 {
			f.writer.mu.Lock()
			f.writer.states[sha] = state
			f.writer.mu.Unlock()
			f.hold(t, true)
			return crossreview.ErrTransient
		}
		return nil
	}
	if err := f.m.publishAdmission(t.Context(), head); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("uncertain request did not force repost: %d", calls)
	}
	requireAdmissionState(t, f, head, "failure")
}

func TestAdmissionGroupsValidatePrecedingEntriesAndInvalidateEveryGroup(t *testing.T) {
	// Risk: the triggering (last) entry is approved but a preceding entry is not;
	// a later revocation/retarget/hold must fail every live synthetic SHA.
	for _, mutation := range []string{"hold", "revocation", "retarget", "scope"} {
		t.Run(mutation, func(t *testing.T) {
			f := newAdmissionFixture(t)
			ctx := t.Context()
			p := f.gh.pulls[7]
			p8 := p
			p8.Number = 8
			p8.Head = strings.Repeat("d", 40)
			f.gh.pulls[8] = p8
			f.tx(t, func(tx pgx.Tx) error {
				if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
					return err
				}
				o, err := f.m.observationTx(ctx, tx, p8, f.at)
				if err != nil {
					return err
				}
				_, _, err = saveTx(ctx, tx, f.person.TenantID, o)
				return err
			})
			group := strings.Repeat("c", 40)
			f.webhook(t, "merge_group", "checks_requested", "group-with-preceding", 204)
			if err := f.m.publishAdmission(ctx, group); err != nil {
				t.Fatal(err)
			}
			requireAdmissionState(t, f, group, "failure") // PR8 has no approval.
			f.approve(p8, "ok")
			p.Queued = true
			p8.Queued = true
			f.gh.pulls[7] = p
			f.gh.pulls[8] = p8
			// The failed group remains invalidated. A new group can use both approvals.
			groups := []string{strings.Repeat("e", 40), strings.Repeat("f", 40)}
			for n, sha := range groups {
				auditSend(t, f.fixture, "merge_group", "approved-group-"+string(rune('a'+n)), map[string]any{"action": "checks_requested", "merge_group": map[string]string{"head_sha": sha, "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-8-group"}}, 204)
				if err := f.m.publishAdmission(ctx, sha); err != nil {
					t.Fatal(err)
				}
				requireAdmissionState(t, f, sha, "success")
			}
			switch mutation {
			case "hold":
				f.hold(t, true)
			case "revocation":
				f.approve(p, "changes")
			case "retarget", "scope":
				if mutation == "retarget" {
					p.Target = "release"
				} else {
					copy := *p.AdmissionDiff
					copy.MergeBase = strings.Repeat("9", 40)
					p.AdmissionDiff = &copy
				}
				f.gh.pulls[7] = p
				f.webhook(t, "pull_request", "edited", "changed-scope", 204)
			}
			for _, sha := range groups {
				if err := f.m.publishAdmission(ctx, sha); err != nil {
					t.Fatal(err)
				}
				requireAdmissionState(t, f, sha, "failure")
			}
			// Queue membership, rather than posting a failure status, proves enforcement.
			f.writer.refuseRemoval = true
			if err := f.m.admitQueueEntry(ctx, admissionQueueEntry{PR: 7, Head: p.Head}); err != nil {
				t.Fatal(err)
			}
			var out AdmissionSnapshot
			path := "/api/delivery/" + f.item(t, 7).ID + "/admission"
			f.call(t, f.person, "GET", path, nil, 200, &out)
			if out.Effective || out.Enforcement != "pending" {
				t.Fatalf("unconfirmed removal claimed effective: %+v", out)
			}
			f.writer.refuseRemoval = false
			if err := f.m.admitQueueEntry(ctx, admissionQueueEntry{PR: 7, Head: p.Head}); err != nil {
				t.Fatal(err)
			}
			f.call(t, f.person, "GET", path, nil, 200, &out)
			if !out.Effective || out.Enforcement != "removed" {
				t.Fatalf("confirmed removal missing: %+v", out)
			}
		})
	}
}

func TestAdmissionQueueBoundsInfrastructureRetryAndReportsMergedBeforeHold(t *testing.T) {
	// Risk: duplicate sweeps enqueue repeatedly, an unverified ejection counts as
	// infra, or a hold response claims success after the merge already won.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	p := f.gh.pulls[7]
	entry := admissionQueueEntry{PR: 7, Head: p.Head}
	run := func() {
		t.Helper()
		if err := f.m.admitQueueEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	if len(f.writer.enqueues) != 1 || f.writer.enqueues[0] != p.Head {
		t.Fatal("enqueue not bound/idempotent", f.writer.enqueues)
	}
	f.hold(t, true)
	run()
	f.hold(t, false)
	run()
	if len(f.writer.enqueues) != 2 {
		t.Fatal("lifting policy hold did not resume approved head", f.writer.enqueues)
	}
	infra := func(id int, group string) {
		t.Helper()
		f.tx(t, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO delivery_queue_failures(tenant_id,repository,pull_request,head_sha,check_name,workflow,conclusion,kind,check_run_id,at,group_sha) VALUES($1,$2,7,$3,'go','CI','timed_out','infra',$4,$5,$6)`, f.person.TenantID, f.m.config.Repository, p.Head, id, f.at, group)
			return err
		})
		live := f.gh.pulls[7]
		live.Queued = false
		f.gh.pulls[7] = live
	}
	infra(101, strings.Repeat("1", 40))
	run()
	run()
	if len(f.writer.enqueues) != 3 {
		t.Fatal("one verified infrastructure retry missing", f.writer.enqueues)
	}
	infra(102, strings.Repeat("2", 40))
	run()
	run()
	if len(f.writer.enqueues) != 3 {
		t.Fatal("repeated infrastructure failure re-enqueued", f.writer.enqueues)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var quarantined, notice bool
		var count int
		if err := tx.QueryRow(ctx, `SELECT quarantined,notice_sent FROM delivery_admission_queue WHERE pull_request=7`).Scan(&quarantined, &notice); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM inbox_compat_messages WHERE is_action_request AND body LIKE 'Admission:%'`).Scan(&count); err != nil {
			return err
		}
		if !quarantined || !notice || count != 1 {
			t.Fatalf("quarantine/Desk notice: %v %v %d", quarantined, notice, count)
		}
		return nil
	})
	// New independent fixture proves the race without quarantine as another cause.
	merged := newAdmissionFixture(t)
	live := merged.gh.pulls[7]
	if err := merged.m.admitQueueEntry(ctx, admissionQueueEntry{PR: 7, Head: live.Head}); err != nil {
		t.Fatal(err)
	}
	merged.hold(t, true)
	merged.writer.mergeOnDequeue = true
	if err := merged.m.admitQueueEntry(ctx, admissionQueueEntry{PR: 7, Head: live.Head}); err != nil {
		t.Fatal(err)
	}
	var snapshot AdmissionSnapshot
	merged.call(t, merged.person, "GET", "/api/delivery/"+merged.item(t, 7).ID+"/admission", nil, 200, &snapshot)
	if snapshot.Effective || snapshot.Enforcement != "merged_before_hold" {
		t.Fatalf("merge race concealed: %+v", snapshot)
	}
	merged.tx(t, func(tx pgx.Tx) error {
		var notices int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM inbox_compat_messages WHERE is_action_request AND body LIKE '%merged before the hold%'`).Scan(&notices)
		if err == nil && notices != 1 {
			t.Fatal("missing explicit merge-race Desk report")
		}
		return err
	})
}

func TestAdmissionUnknownGroupMembershipFailsClosed(t *testing.T) {
	// Risk: failed membership resolution acknowledges a group with no status.
	f := newAdmissionFixture(t)
	f.gh.err = errRead
	f.webhook(t, "merge_group", "checks_requested", "unknown-group", 502)
	sha := strings.Repeat("c", 40)
	if err := f.m.publishAdmission(t.Context(), sha); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, sha, "failure")
	f.gh.err = nil
	f.webhook(t, "merge_group", "checks_requested", "unknown-group", 204)
	if err := f.m.publishAdmission(t.Context(), sha); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, sha, "success")
	// Observations collected with the authority disabled cannot bootstrap a
	// green group without a fresh, fully resolved checks_requested event.
	dormant := strings.Repeat("1", 40)
	f.m.admissionGitHub = nil
	body := map[string]any{"action": "checks_requested", "merge_group": map[string]string{"head_sha": dormant, "base_sha": f.gh.pulls[7].Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-dormant"}}
	auditSend(t, f.fixture, "merge_group", "dormant-group", body, 204)
	f.m.WithAdmission(f.writer)
	if err := f.m.publishAdmission(t.Context(), dormant); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, dormant, "failure")
	auditSend(t, f.fixture, "merge_group", "active-group", body, 204)
	if err := f.m.publishAdmission(t.Context(), dormant); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, dormant, "success")
}

func TestAdmissionBackfillRetainsPartialQueueGroupsWithoutAuthorizingThem(t *testing.T) {
	// Risk: unchanged P2 rows never seed work after upgrade, or a queue-entry
	// sighting is mistaken for complete membership and grants a synthetic SHA.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	p := f.gh.pulls[7]
	group := strings.Repeat("c", 40)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM delivery_admission_outbox`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM delivery_admission_queue`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_items SET observation=jsonb_set(jsonb_set(observation,'{queued}','true'),'{queue_head}',to_jsonb($1::text)) WHERE pull_request=7`, group)
		return err
	})
	service := db.AllProjects(ctx, "admission upgrade test")
	if err := f.m.seedAdmission(service); err != nil {
		t.Fatal(err)
	}
	if err := f.m.publishAdmission(ctx, p.Head); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, p.Head, "success")
	if err := f.m.publishAdmission(ctx, group); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, group, "failure")
	f.tx(t, func(tx pgx.Tx) error {
		var members int
		var resolved bool
		err := tx.QueryRow(ctx, `SELECT jsonb_array_length(members),resolved FROM delivery_admission_outbox WHERE sha=$1`, group).Scan(&members, &resolved)
		if err == nil && (members != 1 || resolved) {
			t.Fatal("partial queue identity lost or treated as complete")
		}
		return err
	})
	f.hold(t, true)
	f.tx(t, func(tx pgx.Tx) error {
		var invalidated bool
		err := tx.QueryRow(ctx, `SELECT invalidated FROM delivery_admission_outbox WHERE sha=$1`, group).Scan(&invalidated)
		if err == nil && !invalidated {
			t.Fatal("hold missed queue-observed group")
		}
		return err
	})
	// A commit status belongs to the SHA. Two PRs sharing a head must not
	// overwrite one another's binding and hide a hold on the preceding PR.
	shared := newAdmissionFixture(t)
	p8 := shared.gh.pulls[7]
	p8.Number = 8
	shared.gh.pulls[8] = p8
	shared.tx(t, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, shared.person.TenantID); err != nil {
			return err
		}
		o, err := shared.m.observationTx(ctx, tx, p8, shared.at)
		if err != nil {
			return err
		}
		_, _, err = saveTx(ctx, tx, shared.person.TenantID, o)
		return err
	})
	shared.approve(p8, "ok")
	if err := shared.m.publishAdmission(ctx, p8.Head); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, shared, p8.Head, "success")
	shared.hold(t, true)
	if err := shared.m.publishAdmission(ctx, p8.Head); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, shared, p8.Head, "failure")

}

type admissionRaceReader struct {
	GitHub
	after func()
}

func (g *admissionRaceReader) Pull(ctx context.Context, pr int64) (Pull, error) {
	p, err := g.GitHub.Pull(ctx, pr)
	after := g.after
	g.after = nil
	if after != nil {
		after()
	}
	return p, err
}
func TestAdmissionQueueRejectsStaleGreenReadAfterCurrentFactsChange(t *testing.T) {
	// Risk: a delayed green network read overwrites a newer red CI observation,
	// hold or head during the final mutation. The callback is the read barrier.
	for _, change := range []string{"ci", "hold", "head"} {
		t.Run(change, func(t *testing.T) {
			f := newAdmissionFixture(t)
			p := f.gh.pulls[7]
			f.m.github = &admissionRaceReader{GitHub: f.gh, after: func() {
				switch change {
				case "hold":
					f.hold(t, true)
				case "ci":
					p.Checks = append([]Check{}, p.Checks...)
					p.Checks[0].Conclusion = "failure"
					f.gh.pulls[7] = p
					f.webhook(t, "check_run", "completed", "new-red-ci", 204)
				case "head":
					p.Head = strings.Repeat("9", 40)
					f.gh.pulls[7] = p
					f.webhook(t, "pull_request", "synchronize", "new-head", 204)
				}
			}}
			if err := f.m.admitQueueEntry(t.Context(), admissionQueueEntry{PR: 7, Head: strings.Repeat("b", 40)}); err != nil {
				t.Fatal(err)
			}
			if len(f.writer.enqueues) != 0 {
				t.Fatal("stale admission response enqueued", f.writer.enqueues)
			}
			current := f.item(t, 7)
			if change == "ci" && current.Observation.Checks[0].Conclusion != "failure" {
				t.Fatal("fixture lost newer red CI")
			}
			if change == "head" && current.Head != p.Head {
				t.Fatal("fixture lost newer head")
			}
		})
	}
}

func TestAdmissionPlainMergeDoesNotReportLostHold(t *testing.T) {
	// Risk: every ordinary merge is reported as a hold that lost the race.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	p := f.gh.pulls[7]
	entry := admissionQueueEntry{PR: 7, Head: p.Head}
	if err := f.m.admitQueueEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.enqueues) != 1 {
		t.Fatal("plain merge fixture never entered the queue", f.writer.enqueues)
	}
	p.Open, p.Merged, p.Queued = false, true, false
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "closed", "plain-merge", 204)
	if err := f.m.admitQueueEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}
	var snapshot AdmissionSnapshot
	f.call(t, f.person, "GET", "/api/delivery/"+f.item(t, 7).ID+"/admission", nil, 200, &snapshot)
	if snapshot.Enforcement != "superseded" || snapshot.Effective {
		t.Fatalf("plain merge reported a lost hold: %+v", snapshot)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var notices int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM inbox_compat_messages WHERE is_action_request AND body LIKE 'Admission:%'`).Scan(&notices)
		if err == nil && (notices != 0 || len(f.writer.enqueues) != 1 || f.writer.dequeues != 0) {
			t.Fatalf("plain merge mutated the queue or notified Desk: notices=%d enqueues=%v dequeues=%d", notices, f.writer.enqueues, f.writer.dequeues)
		}
		return err
	})
}

type admissionPullCounter struct {
	GitHub
	n int
}

func (g *admissionPullCounter) Pull(ctx context.Context, n int64) (Pull, error) {
	g.n++
	return g.GitHub.Pull(ctx, n)
}

func TestAdmissionSteadyQueuedEntryBoundsReads(t *testing.T) {
	// Risk: a PR that stays queued is fully re-read on every 5s pass.
	f := newAdmissionFixture(t)
	counter := &admissionPullCounter{GitHub: f.gh}
	f.m.github = counter
	ctx := t.Context()
	pass := func() int {
		t.Helper()
		before := counter.n
		if err := f.m.reconcileAdmissionQueue(ctx); err != nil {
			t.Fatal(err)
		}
		return counter.n - before
	}
	if got := pass(); got != 2 {
		t.Fatalf("enqueue pass reads=%d, want 2", got)
	}
	if len(f.writer.enqueues) != 1 {
		t.Fatal("steady-queue fixture did not enqueue", f.writer.enqueues)
	}
	if got := pass(); got != 0 {
		t.Fatalf("steady queued entry was read again before the backoff: %d", got)
	}
	f.at = f.at.Add(admissionQueueSteadyBackoff)
	if got := pass(); got != 1 {
		t.Fatalf("due queued entry reads=%d, want 1", got)
	}
	if len(f.writer.enqueues) != 1 || f.writer.dequeues != 0 {
		t.Fatal("steady read mutated the queue", f.writer.enqueues, f.writer.dequeues)
	}
}

func TestAdmissionReconcileLeavesMergedSHAUnchanged(t *testing.T) {
	// Risk: merged heads and destroyed groups are republished as failure forever.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	p := f.gh.pulls[7]
	p.Queued = true
	f.gh.pulls[7] = p
	group := strings.Repeat("c", 40)
	if err := f.m.publishAdmission(ctx, p.Head); err != nil {
		t.Fatal(err)
	}
	f.webhook(t, "merge_group", "checks_requested", "merged-group", 204)
	if err := f.m.publishAdmission(ctx, group); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, p.Head, "success")
	requireAdmissionState(t, f, group, "success")
	posts, reads := len(f.writer.posts), f.writer.reads
	auditSend(t, f.fixture, "merge_group", "merged-destroy", map[string]any{"action": "destroyed", "merge_group": map[string]string{"head_sha": group, "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"}}, 204)
	p.Open, p.Merged, p.Queued = false, true, false
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "closed", "merged-head", 204)
	f.at = f.at.Add(time.Minute)
	for range 2 {
		if err := f.m.reconcileAdmission(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.writer.posts) != posts || f.writer.reads != reads {
		t.Fatalf("merged SHA was republished or re-read: posts %v reads %d→%d", f.writer.posts[posts:], reads, f.writer.reads)
	}
	requireAdmissionState(t, f, p.Head, "success")
	requireAdmissionState(t, f, group, "success")
	f.tx(t, func(tx pgx.Tx) error {
		var headRetired, groupRetired bool
		var headState, groupState string
		if err := tx.QueryRow(ctx, `SELECT retired IS TRUE, confirmed_state FROM delivery_admission_outbox WHERE sha=$1`, p.Head).Scan(&headRetired, &headState); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT retired IS TRUE, confirmed_state FROM delivery_admission_outbox WHERE sha=$1`, group).Scan(&groupRetired, &groupState); err != nil {
			return err
		}
		if !headRetired || !groupRetired || headState != "success" || groupState != "success" {
			t.Fatalf("merged outbox not retired in place: head %v %s group %v %s", headRetired, headState, groupRetired, groupState)
		}
		return nil
	})
}
