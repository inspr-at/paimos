// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/systemactor"
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
	// Promotion needs the TL;DR at the pin too, whichever route it took.
	landed := Render(publicRepository, promotedCommit, false, []File{{Path: "docs/AGENTS-DOMAIN-DEV.md", Content: []byte(next["docs/AGENTS-DOMAIN-DEV.md"])}})
	next[SidecarPath("docs/AGENTS-DOMAIN-DEV.md")] = "rules:\n  " + landed[0].Rules[0].Key + ": {en: A short line.}\n"
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
		if it.ID == b.ID && (it.State != "promoted" || it.PromotedCommit != promotedCommit || it.ApprovedFileSHA != hashText(next["docs/AGENTS-DOMAIN-DEV.md"])) {
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

// Promotion needs the whole proposed content at the pin: the rule and its
// TL;DR. A TL;DR-only proposal stays waiting while the doctrine is unchanged.
func TestDoctrineInboxPromotionNeedsTheTLDR(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("inbox-c")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	file, small := ruleWith(t, src, "Small commits.")
	const path = "/api/rules/doctrine/inbox"
	in := InboxInput{RequestID: "44400000-0000-4000-8000-000000000021", SourceID: src.ID, Path: file.Path, RuleKey: small.Key, Source: small.Source, TLDR: &inboxTLDR{EN: "Keep commits small."}, Why: "The rule has no TL;DR."}
	var a Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &a)
	if a.State != "pending" || a.ProposedSHA != small.SHA256 || a.ProposedTLDR == "" {
		t.Fatalf("TL;DR-only proposal %+v", a)
	}

	// The pin moves, the doctrine does not: the TL;DR has not landed.
	f.fake.commit(publicRepository, promotedCommit, fixtureFiles(), "v26.10.3")
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, SourceInput{Visibility: "public", Ref: "v26.10.3"})
	var list inboxList
	_ = json.Unmarshal(f.call(owner, "GET", path, nil, 200), &list)
	if list.Pending != 1 || len(list.Items) != 1 || list.Items[0].ID != a.ID || list.Items[0].TLDR == nil || list.Items[0].TLDR.EN != "Keep commits small." {
		t.Fatalf("a TL;DR-only proposal must keep waiting with its draft: %+v", list)
	}

	// Sent and released with the sidecar: now it is promoted.
	forge.mainSHA = promotedCommit
	f.call(owner, "POST", path+"/"+a.ID+"/pull-request", map[string]any{}, 200)
	const released = "5555555555555555555555555555555555555555"
	f.fake.commit(publicRepository, released, forge.treeFiles, "v26.10.4")
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, SourceInput{Visibility: "public", Ref: "v26.10.4"})
	var mine inboxList
	_ = json.Unmarshal(f.call(agent, "GET", path, nil, 200), &mine)
	if len(mine.Items) != 1 || mine.Items[0].State != "promoted" || mine.Items[0].PromotedCommit != released {
		t.Fatalf("rule and TL;DR at the pin promote: %+v", mine.Items)
	}
}

// A dismissal that commits between the submission's checks and its lease
// wins: no PR is created and the proposal stays dismissed.
func TestDoctrineInboxDismissalBeatsARacingSubmission(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("inbox-d")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	file, small := ruleWith(t, src, "Small commits.")
	const path = "/api/rules/doctrine/inbox"
	in := InboxInput{RequestID: "44400000-0000-4000-8000-000000000031", SourceID: src.ID, Path: file.Path, RuleKey: small.Key, Source: strings.Replace(small.Source, "Small commits.", "Small, reviewed commits.", 1), TLDR: &inboxTLDR{EN: "Small, reviewed commits."}, Why: "Reviews."}
	var a Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &a)
	m.beforeInboxLease = func(id string) {
		m.beforeInboxLease = nil
		f.call(owner, "POST", path+"/"+id+"/dismiss", map[string]string{"reason": "Not now."}, 200)
	}
	writes := forge.writes
	if got := f.call(owner, "POST", path+"/"+a.ID+"/pull-request", map[string]any{}, 409); !strings.Contains(string(got), "not_pending") {
		t.Fatalf("a racing submission must lose to the dismissal: %s", got)
	}
	if forge.writes != writes || len(forge.pulls) != 0 {
		t.Fatal("a dismissed proposal reached GitHub")
	}
	var state, reason, pr string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT data->>'state', COALESCE(data->>'dismiss_reason',''), COALESCE(data->>'pr_number','0') FROM doctrine_proposals WHERE id=$1`, a.ID).Scan(&state, &reason, &pr); err != nil || state != "dismissed" || reason != "Not now." || pr != "0" {
		t.Fatalf("the dismissal must stand: state=%s reason=%s pr=%s err=%v", state, reason, pr, err)
	}
	if n := draftCount(t, f, tid); n != 0 {
		t.Fatalf("dismissed draft left behind: %d", n)
	}
}

// Draft text is bounded: text a proposal left behind is swept on the next
// read, and a proposal nobody acted on expires after inboxDraftTTL, on read
// or by the hourly sweep, with the reason going back to the proposer.
func TestDoctrineInboxDraftRetentionIsBounded(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-e")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write", "nodes.read"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	ticket := inboxTicket(t, f, owner)
	_, small := ruleWith(t, src, "Small commits.")
	_, tests := ruleWith(t, src, "Tests are part of done.")
	const path = "/api/rules/doctrine/inbox"
	proposal := func(id string, rule RuleView, from, to string) Proposal {
		t.Helper()
		in := InboxInput{RequestID: id, SourceID: src.ID, Path: rulePath(src, rule), RuleKey: rule.Key, Source: strings.Replace(rule.Source, from, to, 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because.", Ticket: "INB-2"}
		var p Proposal
		_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &p)
		return p
	}
	a := proposal("44400000-0000-4000-8000-000000000041", small, "Small commits.", "Small, reviewed commits.")
	b := proposal("44400000-0000-4000-8000-000000000042", tests, "Tests are part of done.", "Tests are always part of done.")

	// Text left behind by a proposal that already left the inbox is swept.
	f.call(owner, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "No."}, 200)
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO doctrine_proposal_drafts(tenant_id,proposal_id,source,tldr_en,why) VALUES($1,$2,'- Left behind.','Left.','Left.')`, tid, b.ID); err != nil {
		t.Fatal(err)
	}
	f.call(agent, "GET", path+"/summary", nil, 200)
	if n := draftCount(t, f, tid); n != 1 {
		t.Fatalf("only the waiting draft may remain, %d left", n)
	}

	// A proposal waiting longer than the TTL expires on read.
	backdate := func(id string) {
		t.Helper()
		if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET created_at=now()-interval '31 days' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	backdate(a.ID)
	var summary struct {
		Pending int `json:"pending"`
	}
	_ = json.Unmarshal(f.call(owner, "GET", path+"/summary", nil, 200), &summary)
	if summary.Pending != 0 || draftCount(t, f, tid) != 0 {
		t.Fatalf("an expired proposal must leave the inbox with its text: pending=%d", summary.Pending)
	}
	var mine inboxList
	_ = json.Unmarshal(f.call(agent, "GET", path, nil, 200), &mine)
	for _, it := range mine.Items {
		if it.ID == a.ID && (it.State != "dismissed" || it.DismissReason != inboxExpired || it.Proposed != "") {
			t.Fatalf("the proposer learns it expired: %+v", it)
		}
	}
	if comments := ticketComments(t, f, owner, ticket); len(comments) != 2 || !strings.Contains(comments[1], "expired") {
		t.Fatalf("the linked ticket learns of the expiry: %v", comments)
	}

	// The hourly sweep expires one even when nobody reads the inbox.
	c := proposal("44400000-0000-4000-8000-000000000043", small, "Small commits.", "Tiny commits.")
	backdate(c.ID)
	m.sweepInboxHourly(t.Context())
	var state, reason string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT data->>'state', COALESCE(data->>'dismiss_reason','') FROM doctrine_proposals WHERE id=$1`, c.ID).Scan(&state, &reason); err != nil || state != "dismissed" || reason != inboxExpired {
		t.Fatalf("hourly expiry state=%s reason=%s err=%v", state, reason, err)
	}
	if n := draftCount(t, f, tid); n != 0 {
		t.Fatalf("expired draft left behind: %d", n)
	}
}

// Publication has one boundary: only a person reaches GitHub. An agent, a
// person's key and the System principal the outcome job runs as are refused
// by preparePublication and proposeChange before any GitHub call, for any
// rule, locked or not.
func TestPublicationIsPersonOnly(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("inbox-f")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	keyed := f.principal(tid, "person", "scripted", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	var system tenant.Principal
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		var err error
		system, err = systemactor.Ensure(t.Context(), tx, tid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var source Source
	var files []File
	err = m.tx(t.Context(), owner, "rules.read", func(tx pgx.Tx) error {
		var err error
		if source, err = getSource(t.Context(), tx, src.ID, false); err != nil {
			return err
		}
		files, err = cachedFiles(t.Context(), tx, source)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, locked := ruleWith(t, src, "NEVER")
	dev, tests := ruleWith(t, src, "Tests are part of done.")
	lockedIn := ProposalInput{RequestID: "44400000-0000-4000-8000-000000000051", SourceID: src.ID, Path: kernel.Path, RuleKey: locked.Key, RuleSHA: locked.SHA256, Source: strings.Replace(locked.Source, "run `env`", "run `env` or `printenv`", 1), Explanation: "Name the other command too."}
	lockedIn.TLDR.EN = "Never print the environment."
	plainIn := ProposalInput{RequestID: "44400000-0000-4000-8000-000000000052", SourceID: src.ID, Path: dev.Path, RuleKey: tests.Key, RuleSHA: tests.SHA256, Source: strings.Replace(tests.Source, "Tests are part of done.", "Tests are always part of done.", 1), Explanation: "Always."}
	plainIn.TLDR.EN = "Tests are part of done."
	calls, writes := forge.calls, forge.writes
	for _, actor := range []tenant.Principal{agent, keyed, system} {
		for _, in := range []ProposalInput{lockedIn, plainIn} {
			_, _, _, err := m.preparePublication(t.Context(), actor, source, files, nil, in)
			var refused *failure
			if !errors.As(err, &refused) || refused.Status != 403 || refused.Code != "forbidden" {
				t.Fatalf("preparePublication must refuse %s (%s): %v", actor.Name, in.RuleKey, err)
			}
			_, err = m.proposeChange(t.Context(), actor, in)
			if !errors.As(err, &refused) || refused.Status != 403 || refused.Code != "forbidden" {
				t.Fatalf("proposeChange must refuse %s (%s): %v", actor.Name, in.RuleKey, err)
			}
		}
	}
	for _, actor := range []tenant.Principal{agent, keyed} {
		if got := f.call(actor, "POST", "/api/rules/doctrine/proposals", plainIn, 403); !strings.Contains(string(got), "doctrine inbox") {
			t.Fatalf("the PR endpoint is person-only: %s", got)
		}
	}
	if forge.calls != calls || forge.writes != writes || len(forge.pulls) != 0 {
		t.Fatal("a refused publication reached GitHub")
	}
	// The person passes the same boundary.
	var p Proposal
	_ = json.Unmarshal(f.call(owner, "POST", "/api/rules/doctrine/proposals", plainIn, 200), &p)
	if p.PRNumber == 0 || p.ProposedBy != owner.ID {
		t.Fatalf("a person publishes: %+v", p)
	}
}

// landTests pins a commit whose Dev rule reads text, with its TL;DR.
func landTests(t *testing.T, f doctrineFixture, owner tenant.Principal, sourceID, commit, ref, text, tldr string) {
	t.Helper()
	const dev = "docs/AGENTS-DOMAIN-DEV.md"
	next := fixtureFiles()
	next[dev] = "# Dev\n\n## Tests\n\n- " + text + "\n"
	views := Render(publicRepository, commit, false, []File{{Path: dev, Content: []byte(next[dev])}})
	next[SidecarPath(dev)] = "rules:\n  " + views[0].Rules[0].Key + ": {en: " + tldr + "}\n"
	f.fake.commit(publicRepository, commit, next, ref)
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+sourceID, SourceInput{Visibility: "public", Ref: ref})
}

func proposalState(t *testing.T, f doctrineFixture, id string) string {
	t.Helper()
	var state string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'state','proposed') FROM doctrine_proposals WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// An index skips a proposal under a lease. The pin it indexed still promotes
// the proposal: when the lease ends, and in the hourly sweep after a lease
// that expired without ending (a crashed process).
func TestDoctrineInboxPromotionSurvivesALease(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-g")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	const path = "/api/rules/doctrine/inbox"
	propose := func(id string, rule RuleView, from, to string) Proposal {
		t.Helper()
		in := InboxInput{RequestID: id, SourceID: src.ID, Path: rulePath(src, rule), RuleKey: rule.Key, Source: strings.Replace(rule.Source, from, to, 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because."}
		var p Proposal
		_ = json.Unmarshal(f.call(agent, "POST", path, in, 200), &p)
		return p
	}

	// The pin lands while a lease is held: the index skips it, the end of the
	// lease promotes it.
	_, tests := ruleWith(t, src, "Tests are part of done.")
	a := propose("44400000-0000-4000-8000-000000000061", tests, "Tests are part of done.", "Tests are always part of done.")
	indexed := false
	_, err := m.runProposal(t.Context(), owner, "rules.write", a.ID, func(ctx context.Context, p *Proposal) (string, error) {
		landTests(t, f, owner, src.ID, promotedCommit, "v26.10.5", "Tests are always part of done.", "A short line.")
		indexed = proposalState(t, f, a.ID) == "pending"
		return "", nil
	})
	if err != nil || !indexed {
		t.Fatalf("the index must skip the leased proposal: skipped=%v err=%v", indexed, err)
	}
	if state := proposalState(t, f, a.ID); state != "promoted" {
		t.Fatalf("the end of the lease must promote a landed proposal: %s", state)
	}
	if n := draftCount(t, f, tid); n != 0 {
		t.Fatalf("a promoted draft left behind: %d", n)
	}

	// A lease that never ended (a crashed process) blocks the index; once it
	// expired, the hourly sweep promotes.
	src = find(f.layer(owner, "GET", "/api/rules/doctrine", nil), publicRepository)
	_, always := ruleWith(t, src, "Tests are always part of done.")
	c := propose("44400000-0000-4000-8000-000000000062", always, "Tests are always part of done.", "Tests and estimates are always part of done.")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=data||jsonb_build_object('operation_id','crashed','operation_until',to_jsonb(now()+interval '1 hour')) WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	landTests(t, f, owner, src.ID, "6666666666666666666666666666666666666666", "v26.10.6", "Tests and estimates are always part of done.", "A short line.")
	if state := proposalState(t, f, c.ID); state != "pending" {
		t.Fatalf("the index must skip the leased proposal: %s", state)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=data||jsonb_build_object('operation_until',to_jsonb(now()-interval '1 minute')) WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	m.sweepInboxHourly(t.Context())
	if state := proposalState(t, f, c.ID); state != "promoted" {
		t.Fatalf("the hourly sweep must promote a proposal the index skipped: %s", state)
	}
}

// One toast per person and proposal: the server hands the claim to exactly
// one caller, however many tabs poll at once, and never for a proposal that
// left the inbox.
func TestDoctrineInboxNoticeIsClaimedOnce(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-h")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	second := f.principal(tid, "person", "second", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	keyed := f.principal(tid, "person", "scripted", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	const path = "/api/rules/doctrine/inbox"
	_, small := ruleWith(t, src, "Small commits.")
	_, tests := ruleWith(t, src, "Tests are part of done.")
	var a, b Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, InboxInput{RequestID: "44400000-0000-4000-8000-000000000071", SourceID: src.ID, Path: rulePath(src, small), RuleKey: small.Key, Source: strings.Replace(small.Source, "Small commits.", "Small, reviewed commits.", 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because."}, 200), &a)
	_ = json.Unmarshal(f.call(agent, "POST", path, InboxInput{RequestID: "44400000-0000-4000-8000-000000000072", SourceID: src.ID, Path: rulePath(src, tests), RuleKey: tests.Key, Source: strings.Replace(tests.Source, "Tests are part of done.", "Tests are always part of done.", 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because."}, 200), &b)
	notified := func(p tenant.Principal) map[string]bool {
		t.Helper()
		var summary struct {
			Items []InboxHeadline `json:"items"`
		}
		_ = json.Unmarshal(f.call(p, "GET", path+"/summary", nil, 200), &summary)
		out := map[string]bool{}
		for _, item := range summary.Items {
			out[item.ID] = item.Notified
		}
		return out
	}
	if got := notified(owner); len(got) != 2 || got[a.ID] || got[b.ID] {
		t.Fatalf("nothing claimed yet: %v", got)
	}
	// Eight tabs claim at once: exactly one wins.
	claim := func(p tenant.Principal, id string) (int, bool) {
		req := httptest.NewRequest("POST", path+"/"+id+"/notified", nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		var out struct {
			Claimed bool `json:"claimed"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Claimed
	}
	results := make(chan bool, 8)
	for range 8 {
		go func() {
			code, claimed := claim(owner, a.ID)
			results <- code == 200 && claimed
		}()
	}
	won := 0
	for range 8 {
		if <-results {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d tabs claimed the toast", won)
	}
	if code, claimed := claim(owner, a.ID); code != 200 || claimed {
		t.Fatal("a repeated claim must not toast again")
	}
	if got := notified(owner); !got[a.ID] || got[b.ID] {
		t.Fatalf("the summary names the claimed proposal: %v", got)
	}
	// Claims are per person; agents and keys are never notified.
	if code, claimed := claim(second, a.ID); code != 200 || !claimed {
		t.Fatal("another person has their own toast")
	}
	for _, p := range []tenant.Principal{agent, keyed} {
		if code, _ := claim(p, b.ID); code != 403 {
			t.Fatalf("%s must not claim a toast: %d", p.Name, code)
		}
	}
	if code, _ := claim(owner, "not-a-uuid"); code != 400 {
		t.Fatalf("an invalid id: %d", code)
	}
	// A proposal that left the inbox claims nothing, and its claims are swept.
	f.call(owner, "POST", path+"/"+b.ID+"/dismiss", map[string]string{"reason": "No."}, 200)
	f.call(owner, "POST", path+"/"+a.ID+"/dismiss", map[string]string{"reason": "No."}, 200)
	if code, claimed := claim(second, b.ID); code != 200 || claimed {
		t.Fatal("a dismissed proposal must not toast")
	}
	notified(owner)
	var left int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_inbox_notified WHERE tenant_id=$1`, tid).Scan(&left); err != nil || left != 0 {
		t.Fatalf("claims of proposals that left the inbox must be swept: %d %v", left, err)
	}
}

// Draft cleanup is transactional: when deleting the waiting text fails, the
// decision that would retire it rolls back with it. Nothing is dismissed
// with its text left behind, and no ticket hears of a decision that did not
// happen.
func TestDoctrineInboxDraftCleanupRollsBack(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-i")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write", "nodes.read"}, owner.ID)
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	seedPrivateGuard(t, f, m, owner)
	ticket := inboxTicket(t, f, owner)
	const path = "/api/rules/doctrine/inbox"
	_, small := ruleWith(t, src, "Small commits.")
	var a Proposal
	_ = json.Unmarshal(f.call(agent, "POST", path, InboxInput{RequestID: "44400000-0000-4000-8000-000000000081", SourceID: src.ID, Path: rulePath(src, small), RuleKey: small.Key, Source: strings.Replace(small.Source, "Small commits.", "Small, reviewed commits.", 1), TLDR: &inboxTLDR{EN: "A short line."}, Why: "Because.", Ticket: "INB-2"}, 200), &a)
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION refuse_draft_delete() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'draft delete refused'; END$$;
		CREATE TRIGGER refuse_draft_delete BEFORE DELETE ON doctrine_proposal_drafts FOR EACH ROW EXECUTE FUNCTION refuse_draft_delete()`); err != nil {
		t.Fatal(err)
	}
	var events int
	countEvents := func() int {
		t.Helper()
		var n int
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type LIKE 'doctrine.inbox_%'`, tid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	events = countEvents()
	f.call(owner, "POST", path+"/"+a.ID+"/dismiss", map[string]string{"reason": "Not now."}, 500)
	// The hourly expiry takes the same path.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET created_at=now()-interval '31 days' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	m.sweepInboxHourly(t.Context())
	if state := proposalState(t, f, a.ID); state != "pending" || draftCount(t, f, tid) != 1 || countEvents() != events || len(ticketComments(t, f, owner, ticket)) != 0 {
		t.Fatalf("a failed cleanup must roll the decision back: state=%s drafts=%d", state, draftCount(t, f, tid))
	}
	if _, err := f.d.Admin.Exec(t.Context(), `DROP TRIGGER refuse_draft_delete ON doctrine_proposal_drafts`); err != nil {
		t.Fatal(err)
	}
	m.sweepInboxHourly(t.Context())
	if state := proposalState(t, f, a.ID); state != "dismissed" || draftCount(t, f, tid) != 0 {
		t.Fatalf("the next sweep retires it: state=%s", state)
	}
}
