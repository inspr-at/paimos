// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

const promotedCommit = "4444444444444444444444444444444444444444"

type inboxList struct {
	Pending int         `json:"pending"`
	Items   []InboxItem `json:"items"`
}

func TestWordDiffRestoresBothSides(t *testing.T) {
	got := wordDiff("Small commits.\n  Why: review.", "Small, reviewed commits.\n  Why: review.")
	want := []DiffPart{{"del", "Small"}, {"ins", "Small,"}, {"eq", " "}, {"ins", "reviewed "}, {"eq", "commits.\n  Why: review."}}
	raw, _ := json.Marshal(got)
	wantRaw, _ := json.Marshal(want)
	if string(raw) != string(wantRaw) {
		t.Fatalf("diff %s", raw)
	}
	for _, c := range [][2]string{{"", "new rule"}, {"old rule", ""}, {"a b c", "a b c"}, {"- 🔴 **NEVER** run `env`.", "- 🔴 **NEVER** print `env` or `set`."}} {
		var base, proposed strings.Builder
		for _, part := range wordDiff(c[0], c[1]) {
			if part.Op != "ins" {
				base.WriteString(part.Text)
			}
			if part.Op != "del" {
				proposed.WriteString(part.Text)
			}
		}
		if base.String() != c[0] || proposed.String() != c[1] {
			t.Fatalf("diff of %q -> %q does not restore both sides", c[0], c[1])
		}
	}
	// Oversized inputs fall back to one replacement instead of a huge table.
	long := strings.Repeat("word ", 3000)
	if parts := wordDiff(long, long+"x"); len(parts) != 2 || parts[0].Op != "del" || parts[1].Op != "ins" {
		t.Fatal("oversized diff must be a whole replacement")
	}
}

func inboxTicket(t *testing.T, f doctrineFixture, p tenant.Principal) string {
	t.Helper()
	var ticket string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'INB-1','Inbox fixtures' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING nodes.id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'INB-2','Estimate before work',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='ticket' RETURNING nodes.id::text`, p.TenantID, project).Scan(&ticket)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func ticketComments(t *testing.T, f doctrineFixture, p tenant.Principal, ticket string) []string {
	t.Helper()
	var out []string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT after->>'body_markdown' FROM events WHERE node_id=$1 AND type='comment.created' ORDER BY id`, ticket)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			out = append(out, body)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func draftCount(t *testing.T, f doctrineFixture, tid string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_proposal_drafts`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func ruleWith(t *testing.T, src *SourceView, text string) (FileView, RuleView) {
	t.Helper()
	for _, file := range src.Files {
		for _, r := range file.Rules {
			if strings.Contains(r.Source, text) {
				return file, r
			}
		}
	}
	t.Fatalf("no rule with %q", text)
	return FileView{}, RuleView{}
}

// The agent proposes, a person promotes: nothing reaches GitHub until a person
// acts, locked rules are person-only, the diff is against the pinned rule, a
// dismissal reason returns to the proposer, and a pin that contains the change
// closes the proposal as promoted with a ticket comment.
func TestDoctrineInboxLifecycle(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("inbox-a")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "Markus", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write", "nodes.read"}, owner.ID)
	reader := f.principal(tid, "agent", "reader", "admin", []string{"rules.read", "nodes.read"}, owner.ID)
	viewer := f.principal(tid, "person", "viewer", "viewer", nil, "")
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	ticket := inboxTicket(t, f, owner)

	file, small := ruleWith(t, src, "Small commits.")
	in := InboxInput{RequestID: "44400000-0000-4000-8000-000000000001", Repository: "inspr-modules", Path: file.Path, RuleKey: small.Key,
		Source: strings.Replace(small.Source, "Small commits.", "Small, reviewed commits.", 1), Why: "Reviews catch more in small commits.", Ticket: "INB-2"}
	const path = "/api/rules/doctrine/inbox"

	// A rule without a TL;DR needs one with the change.
	f.call(agent, "POST", path, in, 400)
	in.TLDR = &inboxTLDR{EN: "Keep commits small and reviewed."}
	f.call(viewer, "POST", path, in, 403)
	f.call(reader, "POST", path, in, 403)
	calls, writes := forge.calls, forge.writes
	var a Proposal
	if err := json.Unmarshal(f.call(agent, "POST", path, in, 200), &a); err != nil {
		t.Fatal(err)
	}
	if a.State != "pending" || !a.Inbox || a.ProposedBy != agent.ID || a.PRNumber != 0 || a.Ticket != "INB-2" || a.TicketID != ticket || a.BaseRuleSHA != small.SHA256 || a.ProposedSHA == "" {
		t.Fatalf("waiting proposal %+v", a)
	}
	if forge.calls != calls || forge.writes != writes {
		t.Fatal("an inbox proposal must not reach GitHub before a person acts")
	}
	// A replay returns it; another text for the same rule waits for a person.
	var again Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &again)
	if again.ID != a.ID {
		t.Fatal("request replay created another proposal")
	}
	other := in
	other.RequestID, other.Source = "44400000-0000-4000-8000-000000000002", strings.Replace(small.Source, "Small commits.", "Tiny commits.", 1)
	f.call(agent, "POST", path, other, 409)
	dev, tests := ruleWith(t, src, "Tests are part of done.")
	devIn := InboxInput{RequestID: "44400000-0000-4000-8000-000000000005", Repository: publicRepository, Path: dev.Path, RuleKey: tests.Key,
		Source: strings.Replace(tests.Source, "Tests are part of done.", "Tests and estimates are part of done.", 1), TLDR: &inboxTLDR{EN: "Estimate before work."}, Why: "INSPR-491.", Ticket: "INB-99"}
	f.call(agent, "POST", path, devIn, 404)
	devIn.Ticket = "INB-2"

	// Locked rules are person-only.
	kernel, locked := ruleWith(t, src, "NEVER")
	lockedIn := InboxInput{RequestID: "44400000-0000-4000-8000-000000000004", SourceID: src.ID, Path: kernel.Path, RuleKey: locked.Key,
		Source: strings.Replace(locked.Source, "run `env`", "run `env` or `printenv`", 1), Why: "Name the other command too."}
	if !strings.Contains(string(f.call(agent, "POST", path, lockedIn, 403)), "locked_rule") {
		t.Fatal("a locked rule must refuse an agent")
	}
	var lockedP Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path, lockedIn, 200), &lockedP)

	// A second agent proposal, dismissed later.
	var b Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, devIn, 200), &b)

	// The person sees every waiting proposal with its diff; the agent its own.
	var list inboxList
	_ = json.Unmarshal(f.call(owner, "GET", path, nil, 200), &list)
	if list.Pending != 3 || len(list.Items) != 3 {
		t.Fatalf("person inbox %+v", list)
	}
	var item InboxItem
	for _, it := range list.Items {
		if it.ID == a.ID {
			item = it
		}
	}
	if item.Proposer != "builder" || item.ProposerKind != "agent" || item.Label != "Keep commits small and reviewed." || item.Heading != "Git" || item.TicketHref != "/p/INB-1/INB-2" || item.Why != in.Why || item.Outdated || item.BaseSHA != small.SHA256 {
		t.Fatalf("inbox item %+v", item)
	}
	var base, proposed strings.Builder
	inserted := false
	for _, part := range item.Diff {
		if part.Op != "ins" {
			base.WriteString(part.Text)
		}
		if part.Op != "del" {
			proposed.WriteString(part.Text)
		}
		inserted = inserted || part.Op == "ins" && strings.Contains(part.Text, "reviewed")
	}
	if base.String() != sourceBody(small.Source) || proposed.String() != sourceBody(in.Source) || !inserted {
		t.Fatalf("diff against the pinned rule %+v", item.Diff)
	}
	var mine inboxList
	_ = json.Unmarshal(f.call(agent, "GET", path, nil, 200), &mine)
	if len(mine.Items) != 2 || mine.Pending != 2 {
		t.Fatalf("agent sees only its own proposals: %+v", mine)
	}
	var summary struct {
		Pending int             `json:"pending"`
		Items   []InboxHeadline `json:"items"`
	}
	raw := f.call(owner, "GET", path+"/summary", nil, 200)
	_ = json.Unmarshal(raw, &summary)
	if summary.Pending != 3 || len(summary.Items) != 3 || strings.Contains(string(raw), "Small, reviewed") {
		t.Fatalf("summary carries headlines only: %s", raw)
	}

	// Only a person sends to git or dismisses.
	f.call(agent, "POST", path+"/"+a.ID+"/pull-request", map[string]any{}, 403)
	f.call(agent, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "no"}, 403)
	f.call(viewer, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "no"}, 403)
	f.call(owner, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": " "}, 400)
	var dismissed Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "Estimates belong in the workflow doctrine."}, 200), &dismissed)
	if dismissed.State != "dismissed" || dismissed.DismissedBy != owner.ID || dismissed.DismissReason != "Estimates belong in the workflow doctrine." {
		t.Fatalf("dismissed %+v", dismissed)
	}
	f.call(owner, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "again"}, 200)
	f.call(owner, "POST", path+"/"+b.ID+"/pull-request", map[string]any{}, 409)
	comments := ticketComments(t, f, owner, ticket)
	if len(comments) != 1 || !strings.Contains(comments[0], "Estimates belong in the workflow doctrine.") {
		t.Fatalf("the reason goes back on the ticket: %v", comments)
	}
	var after inboxList
	_ = json.Unmarshal(f.call(agent, "GET", path, nil, 200), &after)
	for _, it := range after.Items {
		if it.ID == b.ID && (it.State != "dismissed" || it.DismissReason == "" || it.Proposed != "") {
			t.Fatalf("the proposer sees the reason, the text is gone: %+v", it)
		}
	}
	f.call(owner, "POST", path+"/"+lockedP.ID+"/dismiss", map[string]string{"reason": "Keep the kernel short."}, 200)
	if n := draftCount(t, f, tid); n != 1 {
		t.Fatalf("dismissed drafts must be deleted, %d left", n)
	}

	// Edit then propose: the person's TL;DR replaces the draft; the PR is the AEON-319 path.
	writes = forge.writes
	edit := map[string]any{"tldr": map[string]string{"en": "Small, reviewed commits."}, "rule_sha256": small.SHA256}
	var sent Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+a.ID+"/pull-request", edit, 200), &sent)
	if sent.PRNumber == 0 || sent.State != "proposed" || sent.ProposedBy != agent.ID || sent.SubmittedBy != owner.ID || sent.EditedBy != owner.ID || forge.writes == writes {
		t.Fatalf("submitted %+v", sent)
	}
	if !strings.Contains(forge.bodies[len(forge.bodies)-1], in.Why) || !strings.Contains(forge.treeFiles[SidecarPath(file.Path)], "Small, reviewed commits.") {
		t.Fatal("the PR must carry the why and the edited TL;DR")
	}
	if n := draftCount(t, f, tid); n != 0 {
		t.Fatal("git holds the text now; the draft must be gone")
	}
	var replayed Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+a.ID+"/pull-request", map[string]any{}, 200), &replayed)
	if replayed.PRNumber != sent.PRNumber {
		t.Fatal("repeating Propose PR must return the same PR")
	}
	var empty inboxList
	_ = json.Unmarshal(f.call(owner, "GET", path, nil, 200), &empty)
	if empty.Pending != 0 || len(empty.Items) != 0 {
		t.Fatalf("nothing waits any more: %+v", empty)
	}

	// The pin moves to a release that contains the change: promoted, with a comment.
	f.fake.commit(publicRepository, promotedCommit, forge.treeFiles, "v26.10.1")
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, SourceInput{Visibility: "public", Ref: "v26.10.1"})
	var history struct {
		Proposals []Proposal `json:"proposals"`
	}
	_ = json.Unmarshal(f.call(owner, "GET", "/api/rules/doctrine/proposals", nil, 200), &history)
	var promoted *Proposal
	for i := range history.Proposals {
		if history.Proposals[i].ID == a.ID {
			promoted = &history.Proposals[i]
		}
		if history.Proposals[i].ID == b.ID {
			t.Fatal("a dismissed inbox proposal without a PR stays out of the PR history")
		}
	}
	if promoted == nil || promoted.State != "promoted" || promoted.PromotedCommit != promotedCommit {
		t.Fatalf("promoted on pin %+v", promoted)
	}
	comments = ticketComments(t, f, owner, ticket)
	if len(comments) != 2 || !strings.Contains(comments[1], "Doctrine change promoted") || !strings.Contains(comments[1], "v26.10.1") {
		t.Fatalf("the linked ticket learns of the promotion: %v", comments)
	}
	// Promoted is final; a refresh leaves it alone.
	var refreshed Proposal
	_ = json.Unmarshal(f.call(owner, "POST", "/api/rules/doctrine/proposals/"+a.ID+"/refresh", nil, 200), &refreshed)
	if refreshed.State != "promoted" {
		t.Fatalf("refresh changed a promoted proposal: %+v", refreshed)
	}
}

// A pending proposal whose rule changed at the pin is outdated: Propose PR
// needs an edit against the current rule. A change that lands without Propose
// PR still closes the proposal as promoted.
func TestDoctrineInboxOutdatedAndLandedElsewhere(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("inbox-b")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	file, small := ruleWith(t, src, "Small commits.")
	_, tests := ruleWith(t, src, "Tests are part of done.")
	const path = "/api/rules/doctrine/inbox"
	proposal := func(id string, rule RuleView, from, to string) Proposal {
		t.Helper()
		in := InboxInput{RequestID: id, SourceID: src.ID, Path: rulePath(src, rule), RuleKey: rule.Key, Source: strings.Replace(rule.Source, from, to, 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because."}
		var p Proposal
		_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &p)
		return p
	}
	a := proposal("44400000-0000-4000-8000-000000000011", small, "Small commits.", "Small, reviewed commits.")
	b := proposal("44400000-0000-4000-8000-000000000012", tests, "Tests are part of done.", "Tests are always part of done.")

	// Main moves: the Git rule changed underneath a, and b's text landed directly.
	next := fixtureFiles()
	next[file.Path] = strings.Replace(next[file.Path], "Small commits.", "Small commits, one topic each.", 1)
	next["docs/AGENTS-DOMAIN-DEV.md"] = strings.Replace(next["docs/AGENTS-DOMAIN-DEV.md"], "Tests are part of done.", "Tests are always part of done.", 1)
	f.fake.commit(publicRepository, promotedCommit, next, "v26.10.2")
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, SourceInput{Visibility: "public", Ref: "v26.10.2"})

	var list inboxList
	_ = json.Unmarshal(f.call(owner, "GET", path, nil, 200), &list)
	if list.Pending != 1 || len(list.Items) != 1 || list.Items[0].ID != a.ID || !list.Items[0].Outdated || !strings.Contains(list.Items[0].Base, "one topic each") {
		t.Fatalf("outdated inbox %+v", list)
	}
	var mine inboxList
	_ = json.Unmarshal(f.call(agent, "GET", path, nil, 200), &mine)
	for _, it := range mine.Items {
		if it.ID == b.ID && (it.State != "promoted" || it.PromotedCommit != promotedCommit) {
			t.Fatalf("a change that landed elsewhere closes as promoted: %+v", it)
		}
	}
	writes := forge.writes
	if !strings.Contains(string(f.call(owner, "POST", path+"/"+a.ID+"/pull-request", map[string]any{}, 409)), "stale_rule") || forge.writes != writes {
		t.Fatal("an outdated proposal needs edit then propose, before any GitHub write")
	}
	forge.mainSHA = promotedCommit
	current := list.Items[0]
	edit := map[string]any{"source": strings.Replace(current.Base, "Small commits, one topic each.", "Small, reviewed commits, one topic each.", 1), "rule_sha256": current.BaseSHA}
	var sent Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+a.ID+"/pull-request", edit, 200), &sent)
	if sent.PRNumber == 0 || sent.EditedBy != owner.ID || !strings.Contains(forge.treeFiles[file.Path], "Small, reviewed commits, one topic each.") {
		t.Fatalf("edit then propose against the current rule: %+v", sent)
	}
}

func rulePath(src *SourceView, rule RuleView) string {
	for _, file := range src.Files {
		for _, r := range file.Rules {
			if r.SHA256 == rule.SHA256 {
				return file.Path
			}
		}
	}
	return ""
}
