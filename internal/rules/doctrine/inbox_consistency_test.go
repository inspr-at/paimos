// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboxReplayComparesCompleteOriginalRequest(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-replay")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "Markus", "admin", nil, "")
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	file, rule := ruleWith(t, src, "Small commits.")
	in := InboxInput{RequestID: "44400000-0000-4000-8000-000000000091", SourceID: src.ID, Path: file.Path, RuleKey: rule.Key, Source: strings.Replace(rule.Source, "Small commits.", "Small, reviewed commits.", 1), TLDR: &inboxTLDR{EN: "Review small commits."}, Why: "Reviewability."}
	const path = "/api/rules/doctrine/inbox"
	f.call(owner, "POST", path, in, 200)
	changes := map[string]func(*InboxInput){
		"source":      func(v *InboxInput) { v.Source += "\n  Why: More review." },
		"tldr":        func(v *InboxInput) { v.TLDR = &inboxTLDR{EN: "Different summary."} },
		"tldr de":     func(v *InboxInput) { v.TLDR = &inboxTLDR{EN: in.TLDR.EN, DE: "Andere Zusammenfassung."} },
		"nil tldr":    func(v *InboxInput) { v.TLDR = nil },
		"why":         func(v *InboxInput) { v.Why = "Another rationale." },
		"source id":   func(v *InboxInput) { v.SourceID = "44400000-0000-4000-8000-000000000099" },
		"repository":  func(v *InboxInput) { v.Repository = "inspr-modules" },
		"base digest": func(v *InboxInput) { v.RuleSHA = strings.Repeat("a", 64) },
		"ticket":      func(v *InboxInput) { v.Ticket = "INB-99" },
		"path":        func(v *InboxInput) { v.Path = "AGENTS.md" },
		"rule key":    func(v *InboxInput) { v.RuleKey = "another-rule" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := in
			change(&changed)
			if body := f.call(owner, "POST", path, changed, 409); !strings.Contains(string(body), "request_conflict") {
				t.Fatal("wrong replay conflict")
			}
		})
	}
	for name, change := range map[string]func(*InboxInput){"tldr": changes["tldr"], "why": changes["why"], "ticket": changes["ticket"]} {
		t.Run("new UUID "+name, func(t *testing.T) {
			changed := in
			changed.RequestID = "44400000-0000-4000-8000-000000000092"
			change(&changed)
			if body := f.call(owner, "POST", path, changed, 409); !strings.Contains(string(body), "rule_proposal_open") {
				t.Fatal("wrong pending-rule conflict")
			}
		})
	}
	// Publication edits must not change what the original UUID identifies.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET input_digest=$2 WHERE id=$1`, in.RequestID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	var replay Proposal
	if err := json.Unmarshal(f.call(owner, "POST", path, in, 200), &replay); err != nil || replay.ID != in.RequestID {
		t.Fatal("original request no longer replays")
	}
}

func TestInboxPromotionPassesOlderUnmatchedHistory(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("inbox-promotion-window")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "Markus", "admin", nil, "")
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	file, rule := ruleWith(t, src, "Tests are part of done.")
	in := InboxInput{RequestID: "44400000-0000-4000-8000-000000000093", SourceID: src.ID, Path: file.Path, RuleKey: rule.Key, Source: strings.Replace(rule.Source, "Tests are part of done.", "Tests are always part of done.", 1), TLDR: &inboxTLDR{EN: "Always test."}, Why: "Coverage."}
	f.call(owner, "POST", "/api/rules/doctrine/inbox", in, 200)
	// More than one old page; none matches the pinned bytes. Explicit times
	// make the ordering independent of wall-clock speed.
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO doctrine_proposals(tenant_id,id,source_id,repository,path,rule_key,input_digest,base_commit,proposed_by,created_at,data)
	 SELECT tenant_id,gen_random_uuid(),source_id,repository,path,rule_key,input_digest,base_commit,proposed_by,'2020-01-01'::timestamptz,
	 data || '{"state":"closed","proposed_rule_sha256":"unmatched"}'::jsonb
	 FROM doctrine_proposals CROSS JOIN generate_series(1,205) WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	landTests(t, f, owner, src.ID, promotedCommit, "v9.0.1", "Tests are always part of done.", "Always test.")
	if state := proposalState(t, f, in.RequestID); state != "promoted" {
		t.Fatalf("landed proposal starved behind old history: %s", state)
	}
}
