// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/doctrinerepo"
	"strings"
	"testing"
)

// Risk: an add must preserve all existing rules and stay within configured roots.
func TestDoctrineAddPreservesRulesAndBoundary(t *testing.T) {
	m := New(nil, Options{})
	s := Source{Repository: privateRepository, Visibility: "private", Commit: fixtureCommit, Paths: DefaultPaths}
	files := []File{{Path: "docs/AGENTS-KERNEL.md", Content: []byte(fixtureKernel)}, {Path: "docs/AGENTS-KERNEL.tldr.yaml", Content: []byte(fixtureSidecar)}}
	in := ProposalInput{Operation: "add", Section: "hard-safety/secrets", Path: files[0].Path, RuleKey: "no-plain-secrets", Source: "- Keep secrets out of plain files.", Explanation: "Keep credentials private."}
	in.TLDR.EN = "Keep secrets private."
	changed, _, next, err := m.editRuleViews(s, files, in)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key != in.RuleKey || next.Set != in.Section {
		t.Fatal("new rule identity or section changed")
	}
	after := Render(s.Repository, s.Commit, true, []File{{Path: in.Path, Content: []byte(changed[in.Path])}})
	before := Render(s.Repository, s.Commit, true, files)
	if len(after) != 1 || len(after[0].Rules) != len(before[0].Rules)+1 || next.Set != in.Section {
		t.Fatal("addition in wrong section")
	}
	for _, old := range before[0].Rules {
		found := false
		for _, r := range after[0].Rules {
			if r.Key == old.Key && r.Source == old.Source {
				found = true
			}
		}
		if !found {
			t.Fatal("existing source changed")
		}
	}
	in.Path = "docs/AGENTS-DOMAIN-LEARNINGS.md"
	in.Section = "Learnings"
	if _, _, next, err = m.editRuleViews(s, files, in); err != nil || next.Key != in.RuleKey {
		t.Fatalf("new file: %+v %v", next, err)
	}
	for _, path := range []string{"outside/AGENTS-DOMAIN-LEARNINGS.md", "docs/../AGENTS.md", "docs/AGENTS-PROFILE-UNCONFIGURED.md"} {
		in.Path = path
		if _, _, _, err = m.editRuleViews(s, files, in); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	in.Path = "docs/AGENTS-DOMAIN-LEARNINGS.md"
	s.Repository = "unconfigured/doctrine"
	_, _, _, err = m.editRuleViews(s, files, in)
	var refusal *failure
	if !errors.As(err, &refusal) || refusal.Code != "unsupported_repository" {
		t.Fatalf("wrong repository refusal: %v", err)
	}
}

// Risk: agents may suggest additions but only a person may open the PR.
func TestDoctrineAddInboxToPersonPR(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("doctrine-add")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: doctrinerepo.DefaultPublic, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	if src == nil {
		t.Fatal("source missing")
	}
	for i, path := range []string{"docs/AGENTS-KERNEL.md", "docs/AGENTS-DOMAIN-LEARNINGS.md"} {
		section := "hard-safety/secrets"
		if i == 1 {
			section = "Learnings"
		}
		id := []string{"15880000-0000-4000-8000-000000000001", "15880000-0000-4000-8000-000000000002"}[i]
		in := InboxInput{Operation: "add", Section: section, RequestID: id, SourceID: src.ID, Path: path, RuleKey: "reviewed-addition", Source: "- Verify a change before delivery.", TLDR: &inboxTLDR{EN: "Verify the delivered change."}, Why: "Keep delivery checked."}
		writes := forge.writes
		var p Proposal
		if err := json.Unmarshal(f.call(agent, "POST", "/api/rules/doctrine/inbox", in, 200), &p); err != nil {
			t.Fatal(err)
		}
		if forge.writes != writes || p.Operation != "add" {
			t.Fatal("inbox addition wrote to git or lost operation")
		}
		f.call(agent, "POST", "/api/rules/doctrine/inbox/"+id+"/pull-request", map[string]any{}, 403)
		if err := json.Unmarshal(f.call(owner, "POST", "/api/rules/doctrine/inbox/"+id+"/pull-request", map[string]any{}, 200), &p); err != nil {
			t.Fatal(err)
		}
		if p.PRNumber == 0 || p.Repository != publicRepository || !strings.Contains(forge.treeFiles[path], "<!-- aeon-rule: reviewed-addition -->") {
			t.Fatalf("addition did not reach intended PR: %+v", p)
		}
	}
}
