// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeGitHub struct {
	id     int64
	name   string
	refs   map[string]string
	pulls  map[int64]PullState
	failed bool
}

func (g *fakeGitHub) Repository(context.Context) (int64, string, error) {
	if g.failed {
		return 0, "", fmt.Errorf("API outage")
	}
	return g.id, g.name, nil
}
func (g *fakeGitHub) Ref(_ context.Context, ref string) (string, error) {
	if g.failed || g.refs[ref] == "" {
		return "", fmt.Errorf("ref unavailable")
	}
	return g.refs[ref], nil
}
func (g *fakeGitHub) Pull(_ context.Context, n int64) (PullState, error) {
	p, ok := g.pulls[n]
	if !ok || g.failed {
		return PullState{}, fmt.Errorf("PR unavailable")
	}
	return p, nil
}
func (g *fakeGitHub) Pulls(context.Context) ([]PullState, error) {
	if g.failed {
		return nil, fmt.Errorf("API outage")
	}
	out := []PullState{}
	for _, p := range g.pulls {
		out = append(out, p)
	}
	return out, nil
}

func pull(n int64, base, head, merge string) PullState {
	p := PullState{Number: n, State: "open", MergeCommit: merge}
	p.Base.Ref = "main"
	p.Base.SHA = base
	p.Base.Repo.Name = "inspr-at/paimos"
	p.Base.Repo.ID = 17
	p.Head.SHA = head
	return p
}

func authenticate(t *testing.T, name, delivery string, body any) AuthenticatedEvent {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("fixture-", 8))
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write(raw)
	e, err := AuthenticateEvent(name, delivery, "sha256="+hex.EncodeToString(m.Sum(nil)), raw, secret)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func prEvent(t *testing.T, p PullState, delivery string) AuthenticatedEvent {
	return authenticate(t, "pull_request", delivery, map[string]any{"action": "synchronize", "repository": map[string]any{"id": 17, "full_name": "inspr-at/paimos"}, "pull_request": p})
}

func authorityFixture(t *testing.T) (*fixture, *Authority, *fakeGitHub, AuthenticatedEvent, Plan) {
	t.Helper()
	f := newFixture(t)
	base := f.commit(files(), "base")
	candidateFiles := files()
	candidateFiles["internal/sample/new.go"] = "package sample\n"
	head := f.commit(candidateFiles, "head", base)
	merge := f.commit(candidateFiles, "merge", base, head)
	p := pull(9, base, head, merge)
	api := &fakeGitHub{id: 17, name: "inspr-at/paimos", refs: map[string]string{"refs/heads/main": base}, pulls: map[int64]PullState{9: p}}
	d, err := PolicyDigest(context.Background(), f.repo, base)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAuthority(AuthorityConfig{RepositoryID: 17, Repository: "inspr-at/paimos", Policy: Pin{Commit: base, Digest: d}, EnvironmentDigest: Hash("env", []byte("fixture"))}, f.repo, api)
	if err != nil {
		t.Fatal(err)
	}
	e := prEvent(t, p, "delivery-1")
	plan, err := a.Plan(context.Background(), e, 1)
	if err != nil {
		t.Fatal(err)
	}
	return f, a, api, e, plan
}

func TestWebhookAuthentication(t *testing.T) {
	secret := []byte(strings.Repeat("fixture-", 8))
	raw := []byte(`{"action":"opened"}`)
	m := hmac.New(sha256.New, secret)
	m.Write(raw)
	signature := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if _, err := AuthenticateEvent("pull_request", "delivery", signature, raw, secret); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, delivery, signature string
		raw, secret               []byte
	}{
		{"pull_request", "delivery", signature, []byte(`{"action":"synchronize"}`), secret},
		{"pull_request", "delivery", signature, raw, []byte("short")},
		{"workflow_dispatch", "delivery", signature, raw, secret},
		{"pull_request", "$(bad)", signature, raw, secret},
		{"pull_request", "delivery", "sha1=invalid", raw, secret},
	} {
		if _, err := AuthenticateEvent(tc.name, tc.delivery, tc.signature, tc.raw, tc.secret); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
	for _, bad := range []string{`{"action":"opened","action":"synchronize"}`, `{"action":"opened"} {}`} {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(bad))
		if _, err := AuthenticateEvent("pull_request", "delivery", "sha256="+hex.EncodeToString(m.Sum(nil)), []byte(bad), secret); err == nil {
			t.Fatal("malformed signed JSON accepted")
		}
	}
}

func TestAuthorityShadowAndRevalidation(t *testing.T) {
	_, a, api, e, p := authorityFixture(t)
	checks, err := a.Checks(context.Background(), e, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = true
		if c.Status != "pending" || c.AppID != 0 || c.Target != p.Binding.SourceHead || c.Total <= 0 || c.Observed != 0 {
			t.Fatalf("invalid shadow proposal: %+v", c)
		}
	}
	for _, name := range []string{"ci/trusted", "go", "web", "release-check", "e2e", "migration-compat", "status-autopilot-ui", "footer-ui"} {
		if !names[name] {
			t.Fatalf("missing context %s", name)
		}
	}
	if _, err := a.Plan(context.Background(), e, 2); err == nil {
		t.Fatal("webhook replay accepted")
	}
	if _, err := a.Plan(context.Background(), AuthenticatedEvent{}, 2); err == nil {
		t.Fatal("unauthenticated event accepted")
	}
	if _, err := a.Plan(context.Background(), prEvent(t, api.pulls[9], "delivery-2"), 1); err == nil {
		t.Fatal("old generation accepted")
	}
	newEvent := prEvent(t, api.pulls[9], "delivery-3")
	if _, err := a.Plan(context.Background(), newEvent, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Checks(context.Background(), e, p, nil, nil); err == nil {
		t.Fatal("superseded plan published")
	}
}

func TestAuthorityRefusesChangedLiveState(t *testing.T) {
	for _, change := range []string{"head", "base", "closed", "fork-base", "repo-id", "outage", "merge-parent"} {
		t.Run(change, func(t *testing.T) {
			f, a, api, e, p := authorityFixture(t)
			pr := api.pulls[9]
			switch change {
			case "head":
				pr.Head.SHA = strings.Repeat("a", 40)
			case "base":
				api.refs["refs/heads/main"] = strings.Repeat("b", 40)
			case "closed":
				pr.State = "closed"
			case "fork-base":
				pr.Base.Repo.Name = "untrusted/fork"
			case "repo-id":
				api.id = 33
			case "outage":
				api.failed = true
			case "merge-parent":
				pr.MergeCommit = f.commit(files(), "wrong merge", p.Binding.Base)
			}
			api.pulls[9] = pr
			if _, err := a.Checks(context.Background(), e, p, nil, nil); err == nil {
				t.Fatal("changed state left check eligible")
			}
		})
	}
}

func TestAuthorityQueueUsesEveryGitParentAndLiveRef(t *testing.T) {
	f, a, api, _, p := authorityFixture(t)
	base := p.Binding.Base
	files2 := files()
	files2["internal/second/new.go"] = "package second\n"
	head2 := f.commit(files2, "second", base)
	group := f.commit(files2, "group", p.Candidate.Commit, head2)
	ref := "refs/heads/gh-readonly-queue/main/group-1"
	api.refs[ref] = group
	api.pulls[20] = pull(20, base, head2, group)
	e := authenticate(t, "merge_group", "queue-1", map[string]any{"action": "checks_requested", "repository": map[string]any{"id": 17, "full_name": "inspr-at/paimos"}, "merge_group": map[string]any{"head_sha": group, "base_sha": base, "head_ref": ref, "base_ref": "refs/heads/main"}})
	plan, err := a.Plan(context.Background(), e, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Binding.CheckTarget != group || fmt.Sprint(plan.Binding.GroupPRs) != "[9 20]" {
		t.Fatal("queue inventory narrowed")
	}
	if _, err := a.Checks(context.Background(), e, plan, nil, nil); err != nil {
		t.Fatal(err)
	}
	api.refs[ref] = p.Candidate.Commit
	if _, err := a.Checks(context.Background(), e, plan, nil, nil); err == nil {
		t.Fatal("replaced group accepted")
	}
	api.refs[ref] = group
	delete(api.pulls, 20)
	if _, err := a.Checks(context.Background(), e, plan, nil, nil); err == nil {
		t.Fatal("missing constituent accepted")
	}
}

func TestAuthorityRejectsActionsAndReviewAppIdentities(t *testing.T) {
	_, a, _, _, _ := authorityFixture(t)
	for _, id := range []int64{-1, 15368, 5134402} {
		c := a.config
		c.VerifierAppID = id
		if _, err := NewAuthority(c, a.repo, a.api); err == nil {
			t.Fatal("shared authority App accepted")
		}
	}
	c := a.config
	c.VerifierAppID = 99999
	if _, err := NewAuthority(c, a.repo, a.api); err != nil {
		t.Fatal(err)
	}
}

func profile(p Plan) VMProfile {
	pin := func(name string) FilePin { return FilePin{Path: "/opt/aeon/" + name, Digest: RawDigest([]byte(name))} }
	return VMProfile{Schema: "aeon.ci.vm-profile.v1", QEMU: pin("qemu"), Kernel: pin("kernel"), Initrd: pin("initrd"), RootFS: pin("rootfs"), Firmware: pin("firmware"), HarnessDigest: RawDigest([]byte("harness")), ToolchainDigest: RawDigest([]byte("toolchain")), EnvironmentDigest: p.EnvironmentDigest, SecurityEpoch: "epoch-1", UID: 1200, GID: 1200, MemoryMiB: 512, CPUs: 1, TimeoutSeconds: 30, Recipes: []Recipe{{ObligationID: "job/web", Stage: "build", Argv: []string{"/opt/aeon/bin/build"}, Reporter: "command", Expected: []string{"command/job/web"}}}}
}

func admission(t *testing.T, p Plan, profile VMProfile, private ed25519.PrivateKey) Admission {
	t.Helper()
	now := time.Now().UTC()
	a := Admission{Schema: "aeon.ci.executable-admission.v1", PlanID: p.ID, CandidateCommit: p.Candidate.Commit, TreeManifestDigest: p.Candidate.ManifestDigest, PolicyDigest: p.Policy.Digest, ExecutorDigest: ProfileDigest(profile), HarnessDigest: profile.HarnessDigest, EnvironmentDigest: p.EnvironmentDigest, SecurityEpoch: profile.SecurityEpoch, ReviewTarget: p.Binding.CheckTarget, ReviewRecord: "independent-review-fixture", ApprovedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), WholeTreeAudited: true}
	a.Signature = ed25519.Sign(private, admissionMessage(a))
	return a
}

func TestAdmissionBindsEveryExecutableByteAndEpoch(t *testing.T) {
	_, _, _, _, p := authorityFixture(t)
	profile := profile(p)
	pub, priv, _ := ed25519.GenerateKey(nil)
	a := admission(t, p, profile, priv)
	if err := VerifyAdmission(p, profile, a, pub, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"plan", "commit", "tree", "policy", "executor", "harness", "environment", "epoch", "review", "closure", "expiry", "signature"} {
		t.Run(field, func(t *testing.T) {
			bad := a
			switch field {
			case "plan":
				bad.PlanID = RawDigest([]byte("bad"))
			case "commit":
				bad.CandidateCommit = strings.Repeat("a", 40)
			case "tree":
				bad.TreeManifestDigest = RawDigest([]byte("bad"))
			case "policy":
				bad.PolicyDigest = RawDigest([]byte("bad"))
			case "executor":
				bad.ExecutorDigest = RawDigest([]byte("bad"))
			case "harness":
				bad.HarnessDigest = RawDigest([]byte("bad"))
			case "environment":
				bad.EnvironmentDigest = RawDigest([]byte("bad"))
			case "epoch":
				bad.SecurityEpoch = "revoked"
			case "review":
				bad.ReviewTarget = strings.Repeat("b", 40)
			case "closure":
				bad.WholeTreeAudited = false
			case "expiry":
				bad.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
			case "signature":
				bad.Signature = nil
			}
			if err := VerifyAdmission(p, profile, bad, pub, time.Now()); err == nil {
				t.Fatal("unadmitted executable bytes accepted")
			}
		})
	}
	if err := VerifyAdmission(p, profile, a, pub, time.Now().Add(25*time.Hour)); err == nil {
		t.Fatal("expired admission accepted")
	}
}

func TestSupervisorRejectsForgedReportsAndMutatedObservations(t *testing.T) {
	f, a, _, e, p := authorityFixture(t)
	profile := profile(p)
	pub, priv, _ := ed25519.GenerateKey(nil)
	s, err := NewSupervisor(f.repo, profile, pub)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.repo.sourceArchive(context.Background(), p.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.task(p, "job/web", RawDigest(source))
	if err != nil {
		t.Fatal(err)
	}
	grant := admission(t, p, profile, priv)
	r := GuestResult{Schema: GuestSchema, PlanID: p.ID, ObligationID: task.ObligationID, ExecutorDigest: task.ExecutorDigest, ExitCode: 0, Manifest: ExecutionManifest{Kind: "non-test", Expected: task.Expected, Executed: task.Expected, Skipped: []string{}}, OutputDigest: RawDigest(nil), Artifacts: []Artifact{}}
	raw, _ := json.Marshal(r)
	if _, err := decodeGuest(task, raw); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte("Total: 1 test in 1 file"), []byte(`{"stats":{"expected":1,"unexpected":0,"skipped":0},"errors":[]}`), append(bytesClone(raw), []byte(" {}")...)} {
		if _, err := decodeGuest(task, raw); err == nil {
			t.Fatal("forged completion accepted")
		}
	}
	bad := r
	bad.Manifest = ExecutionManifest{Kind: "non-test", Expected: []string{"less"}, Executed: []string{"less"}}
	badRaw, _ := json.Marshal(bad)
	if _, err := decodeGuest(task, badRaw); err == nil {
		t.Fatal("candidate narrowing accepted")
	}
	start := time.Now().Add(-time.Second)
	o, err := s.observe(p, task, grant, ExecutionIdentity{RunID: 10, Attempt: 1, JobID: "approved-build"}, r, start, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(p, o); err != nil {
		t.Fatal(err)
	}
	checks, err := a.Checks(context.Background(), e, p, s, []SupervisedObservation{o})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Status != "pending" {
			t.Fatal("shadow observation became success")
		}
		if c.Name == "web" && c.Observed != 1 {
			t.Fatal("external observation not accounted")
		}
	}
	forged := o
	forged.Receipt.Result = "failure"
	if err := s.Verify(p, forged); err == nil {
		t.Fatal("mutated receipt seal accepted")
	}
	serialized, _ := json.Marshal(o)
	var candidate SupervisedObservation
	json.Unmarshal(serialized, &candidate)
	if err := s.Verify(p, candidate); err == nil {
		t.Fatal("guest/JSON receipt minted supervisor provenance")
	}
	other, _ := NewSupervisor(f.repo, profile, pub)
	if err := other.Verify(p, o); err == nil {
		t.Fatal("observation transferred across supervisors")
	}
	if _, err := a.Checks(context.Background(), e, p, s, []SupervisedObservation{o, o}); err == nil {
		t.Fatal("duplicate observation accepted")
	}
	s.RevokeAdmission(grant.ReviewRecord)
	if err := s.Verify(p, o); err == nil {
		t.Fatal("revoked admission left a valid observation")
	}
	// Profile caller mutation cannot change the pinned launcher, environment or expected set.
	profile.Recipes[0].Argv[0] = "/workspace/fake"
	profile.Recipes[0].Expected[0] = "less"
	next, err := s.task(p, "job/web", RawDigest(source))
	if err != nil || next.Argv[0] != "/opt/aeon/bin/build" || next.Expected[0] != "command/job/web" {
		t.Fatal("caller changed frozen executor recipe")
	}
}

func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }

func TestReviewTamperInventoryInvalidatesAdmission(t *testing.T) {
	raw, err := os.ReadFile("testdata/authority-tamper.json")
	if err != nil {
		t.Fatal(err)
	}
	var attacks []struct{ ID, Review, Path, Content string }
	if err := json.Unmarshal(raw, &attacks); err != nil {
		t.Fatal(err)
	}
	f, _, _, _, p := authorityFixture(t)
	profile := profile(p)
	pub, priv, _ := ed25519.GenerateKey(nil)
	grant := admission(t, p, profile, priv)
	seen := map[string]bool{}
	for _, attack := range attacks {
		t.Run(attack.ID, func(t *testing.T) {
			seen[attack.Review] = true
			candidateFiles := files()
			candidateFiles[attack.Path] = attack.Content
			head := f.commit(candidateFiles, attack.ID, p.Binding.Base)
			merge := f.commit(candidateFiles, "merge "+attack.ID, p.Binding.Base, head)
			b := p.Binding
			b.Generation++
			b.SourceHead = head
			b.CheckTarget = head
			b.Candidate = merge
			changed, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
			if err != nil {
				t.Fatal(err)
			}
			if changed.InventoryDigest != p.InventoryDigest {
				t.Fatal("candidate narrowed approved obligation inventory")
			}
			if err := VerifyAdmission(changed, profile, grant, pub, time.Now()); err == nil {
				t.Fatal("tampered source inherited executable admission")
			}
		})
	}
	for _, review := range []string{"review-421.out", "review-421r2.out", "review-421r3.out", "review-421r4.out", "review-421r5.out"} {
		if !seen[review] {
			t.Fatalf("missing review fixture %s", review)
		}
	}
}

func TestVMArgumentsAndBoundaryRejectCandidateControls(t *testing.T) {
	_, _, _, _, p := authorityFixture(t)
	profile := profile(p)
	args := strings.Join(vmArguments(profile), " ")
	for _, required := range []string{"-monitor none", "-nic none", "accel=kvm", "-sandbox on", "readonly=on", "/proc/self/fd/3"} {
		if !strings.Contains(args, required) {
			t.Fatalf("missing VM boundary %s", required)
		}
	}
	for _, forbidden := range []string{"virtfs", "virtiofs", "hostfwd", "docker.sock", "/workspace", "GITHUB_ENV", "-qmp", "-netdev"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("host/candidate control channel %s", forbidden)
		}
	}
	for _, bad := range []func(*VMProfile){func(p *VMProfile) { p.UID = 0 }, func(p *VMProfile) { p.RootFS.Path = "/tmp/image,readonly=off" }, func(p *VMProfile) { p.Recipes[0].Argv[0] = "/workspace/scripts/ci-web-tests.sh" }, func(p *VMProfile) { p.Recipes[0].Expected = nil }} {
		copy := profile
		copy.Recipes = append([]Recipe(nil), profile.Recipes...)
		bad(&copy)
		if err := validateProfile(copy); err == nil {
			t.Fatal("candidate controls accepted")
		}
	}
	s, err := NewSupervisor((&fixture{repo: nil}).repo, profile, make([]byte, 32))
	if err == nil || s != nil {
		t.Fatal("missing independent mirror accepted")
	}
}
