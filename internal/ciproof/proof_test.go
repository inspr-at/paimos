// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fixture struct {
	t        *testing.T
	git, dir string
	repo     *Repository
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, git: git, dir: filepath.Join(t.TempDir(), "mirror.git")}
	f.command(nil, "init", "--bare", f.dir)
	f.repo, err = OpenRepository(context.Background(), f.dir, git)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) command(input []byte, args ...string) string {
	f.t.Helper()
	c := exec.Command(f.git, args...)
	c.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Markus Barta", "GIT_AUTHOR_EMAIL=markus@barta.com", "GIT_COMMITTER_NAME=Markus Barta", "GIT_COMMITTER_EMAIL=markus@barta.com", "GIT_AUTHOR_DATE=2026-10-02T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-02T00:00:00Z"}
	c.Stdin = bytes.NewReader(input)
	b, err := c.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture Git %s: %v: %s", args[0], err, b)
	}
	return strings.TrimSpace(string(b))
}

func (f *fixture) commit(files map[string]string, message string, parents ...string) string {
	f.t.Helper()
	var tree func(map[string]string) string
	tree = func(files map[string]string) string {
		dirs := map[string]map[string]string{}
		rows := []string{}
		for name, content := range files {
			first, rest, nested := strings.Cut(name, "/")
			if nested {
				if dirs[first] == nil {
					dirs[first] = map[string]string{}
				}
				dirs[first][rest] = content
				continue
			}
			blob := f.command([]byte(content), "--git-dir="+f.dir, "hash-object", "-w", "--stdin")
			rows = append(rows, "100644 blob "+blob+"\t"+name+"\x00")
		}
		for dir, contents := range dirs {
			rows = append(rows, "040000 tree "+tree(contents)+"\t"+dir+"\x00")
		}
		sort.Strings(rows)
		return f.command([]byte(strings.Join(rows, "")), "--git-dir="+f.dir, "mktree", "-z")
	}
	args := []string{"--git-dir=" + f.dir, "commit-tree", tree(files)}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return f.command([]byte(message+"\n"), args...)
}

func files() map[string]string {
	workflow := "name: CI\njobs:\n"
	for _, job := range []string{"go", "go-test", "go-static", "go-timing", "web", "e2e", "release-check", "migration-compat", "status-autopilot-ui", "footer-ui"} {
		workflow += "  " + job + ":\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo diagnostic\n"
	}
	out := map[string]string{".github/workflows/ci.yml": workflow, "internal/sample/sample_test.go": "package sample\nfunc TestOld() {}\n", "odd\npath\twith spaces.txt": "nul-safe Git paths", "go.mod": "module github.com/inspr-at/paimos\n"}
	for _, count := range []int{4, 7} {
		path := "scripts/ci/go-shards.txt"
		if count == 4 {
			path = "scripts/ci/go-shards-4.txt"
		}
		var rows strings.Builder
		for n := 1; n <= count; n++ {
			fmt.Fprintf(&rows, "%d 0 github.com/inspr-at/paimos/internal/p%d\n", n, n)
		}
		fmt.Fprintln(&rows, "1 0 github.com/inspr-at/paimos/internal/split TestFirst")
		fmt.Fprintln(&rows, "2 0 github.com/inspr-at/paimos/internal/split TestSecond")
		out[path] = rows.String()
	}
	return out
}

func fixturePlan(t *testing.T) (*fixture, Plan) {
	t.Helper()
	f := newFixture(t)
	content := files()
	base := f.commit(content, "base")
	content["internal/sample/sample.go"] = "package sample\n"
	head := f.commit(content, "candidate", base)
	pin, err := PolicyDigest(context.Background(), f.repo, base)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{RepositoryID: 123, Event: "push", DeliveryID: "delivery-1", Generation: 1, Base: base, Candidate: head, CheckTarget: head}
	p, err := NewPlan(context.Background(), f.repo, Pin{base, pin}, b, Hash("environment", []byte("pinned-hosted-epoch-1")))
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestTrustedInventoryCannotBeNarrowedByCandidate(t *testing.T) {
	f, p := fixturePlan(t)
	content := files()
	content[".github/workflows/ci.yml"] = "jobs: {}\n"
	content["scripts/ci/go-shards.txt"] = ""
	delete(content, "scripts/ci/go-shards-4.txt")
	delete(content, "internal/sample/sample_test.go")
	content["internal/new/new_test.go"] = "package new\nfunc TestNew() {}\n"
	content["package.json"] = `{"scripts":{"preinstall":"touch should-never-exist"}}`
	head := f.commit(content, "narrowing", p.Base.Commit)
	b := p.Binding
	b.Candidate = head
	b.CheckTarget = head
	narrow, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, u := range narrow.Obligations {
		ids[u.ID] = true
		if u.Action != "run" || u.State != "pending" {
			t.Fatal("shadow granted execution credit")
		}
	}
	for _, u := range p.Obligations {
		if !ids[u.ID] {
			t.Fatalf("candidate removed trusted obligation %s", u.ID)
		}
	}
	for _, id := range []string{"job/footer-ui", "job/status-autopilot-ui", "go-package/github.com/inspr-at/paimos/internal/new", "go-package/github.com/inspr-at/paimos/internal/sample"} {
		if !ids[id] {
			t.Fatalf("missing union inventory %s", id)
		}
	}
	if len(narrow.Reasons) != 6 {
		t.Fatalf("policy changes not diagnosed: %v", narrow.Reasons)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "should-never-exist")); !os.IsNotExist(err) {
		t.Fatal("candidate code executed")
	}
}

func TestMissingEmptyAndTamperedTrustedInventory(t *testing.T) {
	for _, kind := range []string{"empty-tree", "no-workflow", "empty-jobs", "no-required-context", "no-shards", "empty-shards", "missing-shard", "duplicate-row"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			content := files()
			switch kind {
			case "empty-tree":
				content = map[string]string{}
			case "no-workflow":
				delete(content, policyPaths[0])
			case "empty-jobs":
				content[policyPaths[0]] = "jobs: {}"
			case "no-required-context":
				content[policyPaths[0]] = "jobs:\n  go-test: {}"
			case "no-shards":
				delete(content, policyPaths[1])
			case "empty-shards":
				content[policyPaths[1]] = "# nothing\n"
			case "missing-shard":
				content[policyPaths[1]] = "1 0 github.com/inspr-at/paimos/internal/x\n"
			case "duplicate-row":
				content[policyPaths[1]] += "1 0 github.com/inspr-at/paimos/internal/p1\n"
			}
			commit := f.commit(content, kind)
			if _, err := PolicyDigest(context.Background(), f.repo, commit); err == nil {
				t.Fatal("invalid trusted inventory admitted")
			}
		})
	}
	f, p := fixturePlan(t)
	for _, pin := range []Pin{{p.Policy.Commit, Hash("wrong")}, {strings.Repeat("a", 40), p.Policy.Digest}, {"origin/main", p.Policy.Digest}} {
		if _, err := NewPlan(context.Background(), f.repo, pin, p.Binding, p.EnvironmentDigest); err == nil {
			t.Fatal("untrusted policy pin admitted")
		}
	}
}

func TestRehashedCandidateNarrowingRejectedByTrustedRebuild(t *testing.T) {
	f, p := fixturePlan(t)
	// Retain each required context and make the forged object internally
	// self-consistent. Shape validation alone must never admit this inventory.
	forged := p
	forged.Obligations = append([]Obligation(nil), p.Obligations[1:]...)
	units := []Obligation{}
	for _, u := range forged.Obligations {
		u.InputDigest = ""
		u.Fingerprint = ""
		u.Action = ""
		u.State = ""
		units = append(units, u)
	}
	forged.InventoryDigest = digest("suite-inventory", units)
	forged.ID = planID(forged)
	if err := ValidatePlan(forged); err != nil {
		t.Fatalf("fixture not internally consistent: %v", err)
	}
	if err := VerifyPlan(context.Background(), f.repo, forged); err == nil {
		t.Fatal("rehashed narrowed plan admitted")
	}
	if _, err := WithLedger(context.Background(), f.repo, filepath.Join(t.TempDir(), "ledger"), forged, nil); err == nil {
		t.Fatal("candidate narrowed ledger")
	}
	// A candidate cannot supply its own policy pin, even with a matching hash.
	candidateDigest, err := PolicyDigest(context.Background(), f.repo, p.Candidate.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(context.Background(), f.repo, Pin{p.Candidate.Commit, candidateDigest}, p.Binding, p.EnvironmentDigest); err == nil {
		t.Fatal("candidate-owned policy admitted")
	}
}

func TestImmutableGitInventoryAndModeBinding(t *testing.T) {
	f, p := fixturePlan(t)
	s, err := f.repo.Snapshot(context.Background(), p.Base.Commit)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range s.Entries {
		if e.Path == "odd\npath\twith spaces.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("NUL-safe path lost")
	}
	for _, id := range []string{p.Base.Tree, "HEAD", "--help", "$(touch should-never-exist)", strings.Repeat("0", 40)} {
		if _, err := f.repo.Snapshot(context.Background(), id); err == nil {
			t.Fatalf("invalid commit accepted %q", id)
		}
	}
	if _, err := OpenRepository(context.Background(), t.TempDir(), f.git); err == nil {
		t.Fatal("non-bare repository admitted")
	}
	// Symlink target bytes are never followed, and its mode changes the digest.
	blob := f.command([]byte("target-does-not-exist"), "--git-dir="+f.dir, "hash-object", "-w", "--stdin")
	var snapshots []Snapshot
	for _, mode := range []string{"100644", "100755", "120000"} {
		tree := f.command([]byte(mode+" blob "+blob+"\tlink\x00"), "--git-dir="+f.dir, "mktree", "-z")
		commit := f.command([]byte(mode), "--git-dir="+f.dir, "commit-tree", tree)
		s, err := f.repo.Snapshot(context.Background(), commit)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, s)
	}
	if snapshots[0].ManifestDigest == snapshots[1].ManifestDigest || snapshots[1].ManifestDigest == snapshots[2].ManifestDigest {
		t.Fatal("mode omitted from tree manifest")
	}
	if Hash("d", []byte("ab"), []byte("c")) == Hash("d", []byte("a"), []byte("bc")) || Hash("a", []byte("x")) == Hash("b", []byte("x")) {
		t.Fatal("ambiguous digest framing")
	}
}

func TestCandidateBaseGroupAndGenerationBinding(t *testing.T) {
	f, p := fixturePlan(t)
	content := files()
	source := f.commit(content, "PR source", p.Base.Commit)
	candidate := f.commit(content, "PR merge", p.Base.Commit, source)
	b := p.Binding
	b.Event = "pull_request"
	b.PR = 7
	b.SourceHead = source
	b.Candidate = candidate
	b.CheckTarget = source
	pr, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
	if err != nil {
		t.Fatal(err)
	}
	b.Event = "merge_group"
	b.PR = 0
	b.SourceHead = ""
	b.GroupID = "group-generation-1"
	b.GroupPRs = []int64{7, 9}
	b.CheckTarget = candidate
	group, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Candidate.Tree != group.Candidate.Tree || pr.ID == group.ID || pr.Obligations[0].Fingerprint == group.Obligations[0].Fingerprint {
		t.Fatal("event-dependent binding conflated")
	}
	for _, mutate := range []func(*Binding){func(b *Binding) { b.GroupPRs = []int64{} }, func(b *Binding) { b.GroupPRs = []int64{9, 7} }, func(b *Binding) { b.CheckTarget = b.Base }, func(b *Binding) { b.Generation = 0 }, func(b *Binding) { b.Event = "unknown" }, func(b *Binding) { b.Base = "refs/heads/main" }, func(b *Binding) { b.DeliveryID = "" }, func(b *Binding) { b.Candidate = b.Base }} {
		invalid := b
		mutate(&invalid)
		if _, err := NewPlan(context.Background(), f.repo, p.Policy, invalid, p.EnvironmentDigest); err == nil {
			t.Fatal("invalid/stale binding admitted")
		}
	}
	b.Generation++
	changed, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID == group.ID {
		t.Fatal("replacement generation inherited plan")
	}
	other := f.commit(content, "other merge", p.Base.Commit, source)
	b.Candidate = other
	b.CheckTarget = other
	sameTree, err := NewPlan(context.Background(), f.repo, p.Policy, b, p.EnvironmentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if sameTree.Candidate.Tree != group.Candidate.Tree || sameTree.Obligations[0].Fingerprint == group.Obligations[0].Fingerprint {
		t.Fatal("same tree erased commit/event provenance")
	}
}

func validReceipt(p Plan) Receipt {
	u := p.Obligations[0]
	return Receipt{Schema: ReceiptSchema, PlanID: p.ID, ObligationID: u.ID, Fingerprint: u.Fingerprint, EnvironmentDigest: p.EnvironmentDigest, CandidateCommit: p.Candidate.Commit, CandidateTree: p.Candidate.Tree, PolicyDigest: p.Policy.Digest, WorkflowCommit: p.Policy.Commit, ExecutorDigest: Hash("executor"), RunID: 12, Attempt: 1, JobID: "job-1", StartedAt: "2026-10-02T01:00:00Z", CompletedAt: "2026-10-02T01:01:00Z", Result: "success", Manifest: ExecutionManifest{Kind: "test", Expected: []string{"test-a", "test-b"}, Executed: []string{"test-a", "test-b"}, Skipped: []string{}}, ArtifactDigests: []string{}}
}

func TestReceiptCannotTurnAbsenceIntoSuccess(t *testing.T) {
	_, p := fixturePlan(t)
	receipt := validReceipt(p)
	if err := ValidateReceipt(p, receipt); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Receipt){
		"empty":            func(r *Receipt) { r.Manifest.Expected = []string{}; r.Manifest.Executed = []string{} },
		"partial":          func(r *Receipt) { r.Manifest.Executed = []string{"test-a"} },
		"duplicate":        func(r *Receipt) { r.Manifest.Executed = []string{"test-a", "test-a"} },
		"wrong-identity":   func(r *Receipt) { r.Manifest.Executed = []string{"test-a", "test-c"} },
		"skipped":          func(r *Receipt) { r.Manifest.Executed = []string{"test-a"}; r.Manifest.Skipped = []string{"test-b"} },
		"wrong-commit":     func(r *Receipt) { r.CandidateCommit = p.Base.Commit },
		"wrong-tree":       func(r *Receipt) { r.CandidateTree = p.Base.Tree },
		"stale-plan":       func(r *Receipt) { r.PlanID = Hash("stale") },
		"wrong-context":    func(r *Receipt) { r.EnvironmentDigest = Hash("wrong") },
		"wrong-workflow":   func(r *Receipt) { r.WorkflowCommit = p.Candidate.Commit },
		"wrong-unit":       func(r *Receipt) { r.ObligationID = "unknown" },
		"unknown-schema":   func(r *Receipt) { r.Schema = "aeon.ci.receipt.v2" },
		"missing-attempt":  func(r *Receipt) { r.Attempt = 0 },
		"missing-terminal": func(r *Receipt) { r.CompletedAt = "" },
		"backwards-time":   func(r *Receipt) { r.CompletedAt = r.StartedAt },
	} {
		t.Run(name, func(t *testing.T) {
			r := validReceipt(p)
			mutate(&r)
			if err := ValidateReceipt(p, r); err == nil {
				t.Fatal("invalid receipt admitted")
			}
		})
	}
	if p.Obligations[0].State != "pending" || p.Obligations[0].Action != "run" {
		t.Fatal("diagnostic receipt authorized reuse")
	}
}

func TestContractsRejectUnknownDuplicateAndEmptyFields(t *testing.T) {
	_, p := fixturePlan(t)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Plan
	if err := Decode("plan", raw, &decoded); err != nil {
		t.Fatal(err)
	}
	bindingJSON, err := json.Marshal(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	var binding Binding
	if err := Decode("binding", bindingJSON, &binding); err != nil {
		t.Fatalf("CLI binding contract: %v", err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"mode":"shadow"`, `"mode":"active"`, 1)),
		[]byte(strings.Replace(string(raw), `"mode":"shadow"`, `"mode":"shadow","mode":"shadow"`, 1)),
		[]byte(strings.Replace(string(raw), `"mode":"shadow"`, `"mode":"shadow","candidate_selection":[]`, 1)),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		if err := Decode("plan", bad, &decoded); err == nil {
			t.Fatal("invalid contract admitted")
		}
	}
	p.Obligations = []Obligation{}
	p.ID = planID(p)
	if err := ValidatePlan(p); err == nil {
		t.Fatal("empty plan admitted")
	}
}

func TestShadowLedgerAppendReplayAndCorruption(t *testing.T) {
	f, p := fixturePlan(t)
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	first, err := WithLedger(context.Background(), f.repo, path, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.PreviousDigest != "" {
		t.Fatal("bad first record")
	}
	if _, err := WithLedger(context.Background(), f.repo, path, p, nil); err == nil {
		t.Fatal("duplicate plan appended")
	}
	r := validReceipt(p)
	second, err := WithLedger(context.Background(), f.repo, path, p, &r)
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != 2 || second.PreviousDigest != first.Digest {
		t.Fatal("broken chain")
	}
	if _, err := WithLedger(context.Background(), f.repo, path, p, &r); err == nil {
		t.Fatal("duplicate receipt appended")
	}
	failure := r
	failure.Attempt = 2
	failure.Result = "failure"
	failure.Manifest.Executed = []string{"test-a"}
	if _, err := WithLedger(context.Background(), f.repo, path, p, &failure); err != nil {
		t.Fatal(err)
	}
	if p.Obligations[0].State != "pending" {
		t.Fatal("ledger minted authoritative green")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)-1], 0600); err != nil {
		t.Fatal(err)
	}
	failure.Attempt = 3
	if _, err := WithLedger(context.Background(), f.repo, path, p, &failure); err == nil {
		t.Fatal("truncated ledger accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), `"sequence":1`, `"sequence":4`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WithLedger(context.Background(), f.repo, path, p, &failure); err == nil {
		t.Fatal("rewritten chain accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := WithLedger(context.Background(), f.repo, link, p, nil); err == nil {
		t.Fatal("symlink ledger accepted")
	}
	orphan := filepath.Join(t.TempDir(), "orphan")
	if _, err := WithLedger(context.Background(), f.repo, orphan, p, &r); err == nil {
		t.Fatal("orphan receipt appended")
	}
}
