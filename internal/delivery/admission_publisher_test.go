// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func TestAdmissionHistoricalHeadsOneReadAfterCheck(t *testing.T) {
	// Risk: one check update re-arms every historical head, and the next pass
	// reads each of them. One current head may be read; its history must not.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	original := strings.Repeat("b", 40)
	for i, head := range []string{strings.Repeat("d", 40), strings.Repeat("e", 40), strings.Repeat("f", 40)} {
		p := f.gh.pulls[7]
		p.Head = head
		f.gh.pulls[7] = p
		f.webhook(t, "pull_request", "synchronize", "hist-head-"+string(rune('a'+i)), 204)
	}
	current := f.gh.pulls[7].Head
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_admission_queue SET enforcement='superseded',checked_at=$2 WHERE repository=$3 AND pull_request=7 AND head_sha=$1`, original, f.at, f.m.config.Repository)
		return err
	})
	p := f.gh.pulls[7]
	p.Checks[0].Conclusion = "failure"
	f.gh.pulls[7] = p
	f.webhook(t, "check_run", "completed", "hist-check", 204)
	var superseded, pendingHist int
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM delivery_admission_queue WHERE pull_request=7 AND head_sha=$1 AND enforcement='superseded'`, original).Scan(&superseded); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM delivery_admission_queue WHERE pull_request=7 AND head_sha<>$1 AND enforcement='pending'`, current).Scan(&pendingHist)
	})
	if superseded != 1 {
		t.Fatalf("check update re-armed the superseded historical head (%d still superseded)", superseded)
	}
	if pendingHist < 2 {
		t.Fatalf("historical pending rows=%d, want at least 2", pendingHist)
	}
	counter := &admissionPullCounter{GitHub: f.gh}
	f.m.github = counter
	if err := f.m.reconcileAdmissionQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if counter.n > 1 {
		t.Fatalf("check update pass reads=%d, want at most 1", counter.n)
	}
}

func TestAdmissionIdlePassMintsNoToken(t *testing.T) {
	// Risk: every 5s queue pass mints and revokes an installation token before
	// it knows whether any row needs a read.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE delivery_admission_queue SET enforcement='queued',checked_at=$1 WHERE pull_request=7`, f.at)
		return err
	})
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	minted, revoked, other := 0, 0, 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
			minted++
			body = map[string]any{"token": "delivery-fixture-token", "permissions": map[string]string{"pull_requests": "read", "checks": "read", "contents": "read", "metadata": "read", "statuses": "read"}, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			revoked++
			body = map[string]any{}
		default:
			other++
			body = map[string]any{}
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	cfg := f.m.config
	cfg.KeyFile = keyFile
	f.m.github = AppReader{App: &crossreview.GitHubApp{Config: cfg, Client: client}}
	if err = f.m.reconcileAdmissionQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if minted != 0 || revoked != 0 || other != 0 {
		t.Fatalf("idle pass minted %d, revoked %d, other GitHub calls %d", minted, revoked, other)
	}
}

func TestAdmissionClosedBeforeDestroyDoesNotPostOnGroupSHA(t *testing.T) {
	// Risk: pull_request.closed stored before merge_group.destroyed posts
	// aeon/admission=failure on the group SHA, which is already on the default branch.
	f := newAdmissionFixture(t)
	ctx := t.Context()
	p := f.gh.pulls[7]
	p.Queued = true
	f.gh.pulls[7] = p
	group := strings.Repeat("c", 40)
	if err := f.m.publishAdmission(ctx, p.Head); err != nil {
		t.Fatal(err)
	}
	f.webhook(t, "merge_group", "checks_requested", "closed-before-destroy-group", 204)
	if err := f.m.publishAdmission(ctx, group); err != nil {
		t.Fatal(err)
	}
	requireAdmissionState(t, f, group, "success")
	posts := len(f.writer.posts)
	p.Open, p.Merged, p.Queued = false, true, false
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "closed", "closed-before-destroy", 204)
	f.at = f.at.Add(time.Minute)
	for range 2 {
		if err := f.m.reconcileAdmission(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, post := range f.writer.posts[posts:] {
		if strings.HasPrefix(post, group+":") {
			t.Fatalf("closed-before-destroy posted on the group SHA: %s", post)
		}
	}
	if len(f.writer.posts) != posts {
		t.Fatalf("closed-before-destroy published %v", f.writer.posts[posts:])
	}
	requireAdmissionState(t, f, group, "success")
	f.tx(t, func(tx pgx.Tx) error {
		var retired bool
		var state string
		err := tx.QueryRow(ctx, `SELECT retired IS TRUE, confirmed_state FROM delivery_admission_outbox WHERE sha=$1`, group).Scan(&retired, &state)
		if err == nil && (!retired || state != "success") {
			t.Fatalf("merged group stayed active: retired=%v state=%s", retired, state)
		}
		return err
	})
}

func TestAdmissionQueueSwitchSelectsLane(t *testing.T) {
	// Risk: the default starts enqueue, or enabling the switch drops the P3 queue lane.
	m := &Module{admissionGitHub: AppAdmission{}}
	if lanes := m.admissionLanes(); len(lanes) != 1 || lanes[0].name != "status" {
		t.Fatalf("default lanes: %+v", lanes)
	}
	if m.WithAdmissionQueue(true) != m {
		t.Fatal("queue switch did not return the module")
	}
	if lanes := m.admissionLanes(); len(lanes) != 2 || lanes[0].name != "queue" || lanes[1].name != "status" {
		t.Fatalf("enabled lanes: %+v", lanes)
	}
	m.WithAdmissionQueue(false)
	if lanes := m.admissionLanes(); len(lanes) != 1 || lanes[0].name != "status" {
		t.Fatalf("cleared switch still starts the queue lane: %+v", lanes)
	}
	m.admissionGitHub = nil
	m.WithAdmissionQueue(true)
	if len(m.admissionLanes()) != 0 {
		t.Fatal("queue switch started a lane without an admission publisher")
	}
}

type admissionScopeRecord struct {
	mu     sync.Mutex
	perms  []map[string]string
	calls  []string
	states map[string]string
	posts  []string
}

func (r *admissionScopeRecord) snapshot() (posts, calls []string, perms []map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	posts = append([]string(nil), r.posts...)
	calls = append([]string(nil), r.calls...)
	for _, perm := range r.perms {
		clone := map[string]string{}
		for k, v := range perm {
			clone[k] = v
		}
		perms = append(perms, clone)
	}
	return posts, calls, perms
}

func assertStatusOnlyGitHub(t *testing.T, posts, calls []string, perms []map[string]string, group string) {
	t.Helper()
	if len(calls) != 0 {
		t.Fatalf("queue API calls: %v", calls)
	}
	wrote := false
	for _, perm := range perms {
		for name, level := range perm {
			if level == "write" && name != "statuses" {
				t.Fatalf("write scope beyond statuses: %v", perm)
			}
			if name != "statuses" && name != "metadata" {
				t.Fatalf("token scope beyond statuses/metadata: %s=%s in %v", name, level, perm)
			}
			if name == "statuses" && level == "write" {
				wrote = true
			}
		}
	}
	if !wrote {
		t.Fatal("status publisher requested no statuses:write token")
	}
	successBeforeFailure := false
	failed := false
	for _, post := range posts {
		sha, state, ok := strings.Cut(post, ":")
		if !ok || sha != group {
			continue
		}
		if state == "success" && !failed {
			successBeforeFailure = true
		}
		if state == "failure" && successBeforeFailure {
			failed = true
		}
		if state == "success" && failed {
			t.Fatalf("success posted on the group SHA after the hold: %v", posts)
		}
	}
	if !failed {
		t.Fatalf("hold did not publish failure on the group SHA: %v", posts)
	}
}

func runStatusOnlyLane(t *testing.T, m *Module) {
	t.Helper()
	lanes := m.admissionLanes()
	if len(lanes) != 1 || lanes[0].name != "status" {
		t.Fatalf("status-only worker lanes: %+v", lanes)
	}
	if err := lanes[0].run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionStatusOnlyPublishesHoldOnGroupWithoutQueue(t *testing.T) {
	// Risk: with the queue switch off, a hold during a harness-enqueued merge
	// group either calls enqueue/dequeue or never posts failure on the group SHA.
	f := newAdmissionFixture(t)
	if lanes := f.m.admissionLanes(); len(lanes) != 1 || lanes[0].name != "status" {
		t.Fatal("fixture started the queue lane")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	rec := &admissionScopeRecord{states: map[string]string{}}
	cfg := f.m.config
	cfg.KeyFile = keyFile
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
				t.Fatal(err)
			}
			clone := map[string]string{}
			for name, level := range in.Permissions {
				clone[name] = level
			}
			rec.mu.Lock()
			rec.perms = append(rec.perms, clone)
			rec.mu.Unlock()
			body = map[string]any{"token": "fixture-only-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": cfg.Repository}}}
		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			body = map[string]any{}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/"+cfg.Repository+"/statuses/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/"+cfg.Repository+"/statuses/")
			var in map[string]string
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if in["context"] != AdmissionContext || (in["state"] != "success" && in["state"] != "failure") {
				t.Fatalf("unexpected status body: %v", in)
			}
			rec.mu.Lock()
			rec.states[sha] = in["state"]
			rec.posts = append(rec.posts, sha+":"+in["state"])
			rec.mu.Unlock()
			body = map[string]any{}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+cfg.Repository+"/commits/") && strings.HasSuffix(r.URL.Path, "/statuses"):
			sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"+cfg.Repository+"/commits/"), "/statuses")
			rec.mu.Lock()
			state := rec.states[sha]
			rec.mu.Unlock()
			body = []any{}
			if state != "" {
				body = []any{map[string]string{"context": AdmissionContext, "state": state}}
			}
		default:
			rec.mu.Lock()
			rec.calls = append(rec.calls, r.Method+" "+r.URL.RequestURI())
			rec.mu.Unlock()
			body = map[string]any{}
			raw, _ := json.Marshal(body)
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	f.m.WithAdmission(AppAdmission{App: &crossreview.GitHubApp{Config: cfg, Client: client}})
	head := f.gh.pulls[7].Head
	group := strings.Repeat("c", 40)
	runStatusOnlyLane(t, f.m)
	posts, calls, _ := rec.snapshot()
	if len(calls) != 0 || !slices.Contains(posts, head+":success") {
		t.Fatalf("approved head: posts %v calls %v", posts, calls)
	}
	// The harness enqueues. This worker must only observe that fact.
	p := f.gh.pulls[7]
	p.Queued = true
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "enqueued", "status-only-harness-enqueue", 204)
	if !f.item(t, 7).Observation.Queued {
		t.Fatal("harness enqueue was not stored")
	}
	runStatusOnlyLane(t, f.m)
	f.webhook(t, "merge_group", "checks_requested", "status-only-group", 204)
	runStatusOnlyLane(t, f.m)
	posts, calls, _ = rec.snapshot()
	if len(calls) != 0 || !slices.Contains(posts, group+":success") {
		t.Fatalf("group before hold: posts %v calls %v", posts, calls)
	}
	f.hold(t, true)
	runStatusOnlyLane(t, f.m)
	posts, calls, perms := rec.snapshot()
	assertStatusOnlyGitHub(t, posts, calls, perms, group)
	before := len(posts)
	p = f.gh.pulls[7]
	p.Open, p.Merged, p.Queued = false, true, false
	f.gh.pulls[7] = p
	f.webhook(t, "pull_request", "closed", "status-only-merged", 204)
	auditSend(t, f.fixture, "merge_group", "status-only-destroyed", map[string]any{"action": "destroyed", "merge_group": map[string]string{"head_sha": group, "base_sha": p.Base, "head_ref": "refs/heads/gh-readonly-queue/main/pr-7-group"}}, 204)
	f.at = f.at.Add(time.Minute)
	runStatusOnlyLane(t, f.m)
	runStatusOnlyLane(t, f.m)
	posts, calls, perms = rec.snapshot()
	if len(posts) != before {
		t.Fatalf("merge republished admission: %v", posts[before:])
	}
	assertStatusOnlyGitHub(t, posts, calls, perms, group)
	f.tx(t, func(tx pgx.Tx) error {
		var rows, attempts, quarantined, noticesSent, acted int
		if err := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(sum(attempts),0)::int, count(*) FILTER (WHERE quarantined), count(*) FILTER (WHERE notice_sent), count(*) FILTER (WHERE enforcement<>'pending') FROM delivery_admission_queue WHERE pull_request=7`).Scan(&rows, &attempts, &quarantined, &noticesSent, &acted); err != nil {
			return err
		}
		if rows < 1 || attempts != 0 || quarantined != 0 || noticesSent != 0 || acted != 0 {
			t.Fatalf("queue lane wrote rows=%d attempts=%d quarantined=%d notices=%d acted=%d", rows, attempts, quarantined, noticesSent, acted)
		}
		var desk int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_compat_messages WHERE is_action_request AND body LIKE 'Admission:%'`).Scan(&desk); err != nil {
			return err
		}
		if desk != 0 {
			t.Fatalf("queue Desk notices: %d", desk)
		}
		var retired bool
		var state string
		if err := tx.QueryRow(t.Context(), `SELECT retired IS TRUE, confirmed_state FROM delivery_admission_outbox WHERE sha=$1`, group).Scan(&retired, &state); err != nil {
			return err
		}
		if !retired || state != "failure" {
			t.Fatalf("group SHA after merge: retired=%v state=%s", retired, state)
		}
		return nil
	})
}

func TestAdmissionPublisherWebhooksAckWhilePublicationHolds(t *testing.T) {
	// Risk: publishing aeon/admission for several heads while reconciliation
	// holds the observation lock turns status, check_run and check_suite
	// deliveries into 502s, or the publisher's own echo dirties its revision.
	f := newAdmissionFixture(t)
	head := f.gh.pulls[7].Head
	foreign := receiptBody(t, f.fixture, map[string]any{"sha": head, "state": "success", "context": "ci/foreign"})
	own := receiptBody(t, f.fixture, map[string]any{"sha": head, "state": "failure", "context": AdmissionContext})
	checkRun := receiptBody(t, f.fixture, map[string]any{"action": "completed", "check_run": map[string]any{"head_sha": head, "name": "go-test", "status": "completed", "conclusion": "success", "pull_requests": []any{map[string]any{"number": 7}}}})
	checkSuite := receiptBody(t, f.fixture, map[string]any{"action": "completed", "check_suite": map[string]any{"id": 900, "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "pull_requests": []any{map[string]any{"number": 7}}}})
	controls := []struct {
		event, id string
		raw       []byte
	}{
		{"status", "control-foreign", foreign},
		{"check_run", "control-check-run", checkRun},
		{"check_suite", "control-check-suite", checkSuite},
	}
	for _, c := range controls {
		if got := admissionWebhook(f.fixture, c.event, c.id, c.raw, 0); got != http.StatusNoContent {
			t.Fatalf("control %s: %d", c.id, got)
		}
	}
	controlPoints := map[string]map[string]webhookOutcome{}
	for _, c := range controls {
		_, _, finished, points, step, class := receiptState(t, f.fixture, c.id)
		if finished == nil || step != nil || class != nil {
			t.Fatalf("control %s did not finish: step %v class %v", c.id, step, class)
		}
		controlPoints[c.id] = points
	}
	var revision int64
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&revision)
	})
	synthetic := strings.Repeat("e", 40)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_admission_outbox(tenant_id,repository,sha,context,kind,members,active,resolved) VALUES($1,$2,$3,$4,'head','[]',true,true)`, f.person.TenantID, f.m.config.Repository, synthetic, AdmissionContext)
		return err
	})

	_, releaseObservation, err := f.m.observationLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	observationHeld := true
	releaseObservationOnce := func() {
		if observationHeld {
			releaseObservation()
			observationHeld = false
		}
	}
	t.Cleanup(releaseObservationOnce)

	releasePost := make(chan struct{})
	var closePost sync.Once
	releasePublishers := func() { closePost.Do(func() { close(releasePost) }) }
	t.Cleanup(releasePublishers)
	var arrived atomic.Int32
	entered := make(chan struct{})
	f.writer.post = func(ctx context.Context, _, _ string) error {
		if arrived.Add(1) == 2 {
			close(entered)
		}
		select {
		case <-releasePost:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	heads := []string{head, synthetic}
	pubErr := make(chan error, len(heads))
	for _, sha := range heads {
		go func(sha string) { pubErr <- f.m.publishAdmission(t.Context(), sha) }(sha)
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("publishers did not reach PostStatus together")
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	f.m.webhookFailures.logger = logger
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })

	burst := []struct {
		event, id string
		raw       []byte
	}{
		{"status", "burst-own", own},
		{"status", "burst-foreign", foreign},
		{"check_run", "burst-check-run", checkRun},
		{"check_suite", "burst-check-suite", checkSuite},
	}
	codes := make([]int, len(burst))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, job := range burst {
		wg.Add(1)
		go func(i int, job struct {
			event, id string
			raw       []byte
		}) {
			defer wg.Done()
			<-start
			codes[i] = admissionWebhook(f.fixture, job.event, job.id, job.raw, 3*time.Second)
		}(i, job)
	}
	close(start)
	finishedBurst := make(chan struct{})
	go func() { wg.Wait(); close(finishedBurst) }()
	select {
	case <-finishedBurst:
	case <-time.After(10 * time.Second):
		t.Fatal("webhooks waited on the publisher or the observation lock")
	}
	for i, job := range burst {
		if codes[i] != http.StatusNoContent {
			t.Fatalf("%s answered %d while publication and observation were in progress", job.id, codes[i])
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("webhook acceptance logged %s", logs.String())
	}
	f.writer.mu.Lock()
	posts := append([]string{}, f.writer.posts...)
	f.writer.mu.Unlock()
	if len(posts) != 0 {
		t.Fatal("webhooks waited for admission POST to finish")
	}
	f.tx(t, func(tx pgx.Tx) error {
		var after int64
		if err := tx.QueryRow(t.Context(), `SELECT revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&after); err != nil {
			return err
		}
		if after != revision {
			t.Fatalf("own admission echo changed revision %d to %d", revision, after)
		}
		return nil
	})
	_, _, ownFinished, ownPoints, _, _ := receiptState(t, f.fixture, "burst-own")
	if ownFinished == nil || ownPoints["projection"] != webhookNothingToRecord || admissionEvents(t, f, "burst-own") != 0 {
		t.Fatalf("own echo projection: finished %v points %v", ownFinished != nil, ownPoints)
	}
	for _, id := range []string{"burst-foreign", "burst-check-run", "burst-check-suite"} {
		attempts, _, finished, _, step, class := receiptState(t, f.fixture, id)
		if finished != nil || attempts != 0 || step == nil || *step != "projection" || class == nil || *class != "observation_busy" || admissionEvents(t, f, id) != 0 {
			t.Fatalf("%s while observation was busy: attempts %d finished %v step %v class %v", id, attempts, finished != nil, step, class)
		}
	}
	if err := f.m.retryWebhooks(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"burst-foreign", "burst-check-run", "burst-check-suite"} {
		attempts, _, finished, _, _, class := receiptState(t, f.fixture, id)
		if finished != nil || attempts != 0 || class == nil || *class != "observation_busy" {
			t.Fatalf("retry while observation was busy finished %s: attempts %d class %v", id, attempts, class)
		}
	}
	releaseObservationOnce()
	if err := f.m.retryWebhooks(t.Context()); err != nil {
		t.Fatal(err)
	}
	retried := map[string]string{"burst-foreign": "control-foreign", "burst-check-run": "control-check-run", "burst-check-suite": "control-check-suite"}
	for id, control := range retried {
		_, _, finished, points, step, class := receiptState(t, f.fixture, id)
		if finished == nil || step != nil || class != nil || !reflect.DeepEqual(points, controlPoints[control]) || admissionEvents(t, f, id) != 1 {
			t.Fatalf("%s after the observation lock: finished %v step %v class %v points %v", id, finished != nil, step, class, points)
		}
	}
	f.tx(t, func(tx pgx.Tx) error {
		var after int64
		if err := tx.QueryRow(t.Context(), `SELECT revision FROM delivery_admission_outbox WHERE sha=$1`, head).Scan(&after); err != nil {
			return err
		}
		if after != revision {
			t.Fatalf("retried foreign delivery changed revision %d to %d", revision, after)
		}
		return nil
	})
	releasePublishers()
	for range heads {
		if err := <-pubErr; err != nil {
			t.Fatal(err)
		}
	}
	f.writer.mu.Lock()
	defer f.writer.mu.Unlock()
	if len(f.writer.posts) != len(heads) {
		t.Fatalf("publishers posted %v", f.writer.posts)
	}
}

func admissionWebhook(f *fixture, event, id string, raw []byte, timeout time.Duration) int {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/github/webhook", bytes.NewReader(raw))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w.Code
}

func admissionEvents(t *testing.T, f *admissionFixture, id string) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_github_events WHERE delivery_id=$1`, id).Scan(&n)
	})
	return n
}
