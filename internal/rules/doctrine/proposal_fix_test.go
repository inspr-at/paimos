// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const guardRule = "The copper observatory keeps seven violet notebooks beneath the eastern stairway for seasonal planning."
const guardTLDR = "Copper notebooks stay hidden underground."

func seedPrivateGuard(t *testing.T, f doctrineFixture, m *Module, actor tenant.Principal) SourceView {
	t.Helper()
	f.fake.commit(privateRepository, fixtureCommit, map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md":        "# Private\n\n## Planning\n<!-- aeon-rule: copper -->\n- " + guardRule + "\n  Why: The silver ledger contains nine emerald diagrams beside the northern window.\n",
		"docs/AGENTS-KERNEL-PRIVATE.tldr.yaml": "rules:\n  copper:\n    en: " + guardTLDR + "\n    de: Kupferne Notizen bleiben heute verborgen.\n  unmatched:\n    en: Reserved observatory protocol stays sealed.\n",
	}, "main")
	allowCredential(t, m.credentials.Dir, "guard-read", actor.TenantID, privateRepository)
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, "guard-read"), []byte("fixtureGuardRead319"), 0600); err != nil {
		t.Fatal("write fixture credential")
	}
	return *find(f.layer(actor, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: privateRepository, Visibility: "private", Ref: "main", CredentialRef: "guard-read"}), privateRepository)
}

func publicProposalFixture(t *testing.T) (doctrineFixture, *proposalForge, *Module, tenant.Principal, ProposalInput) {
	t.Helper()
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("proposal-fix")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	rule := src.Files[1].Rules[0]
	in := ProposalInput{RequestID: "31900000-0000-4000-8000-000000000009", SourceID: src.ID, Path: src.Files[1].Path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: rule.Source, Explanation: "Clarify transcript hygiene."}
	in.TLDR.EN = "Keep credentials out of transcripts."
	return f, forge, m, owner, in
}

func fullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '!' && r <= '~' {
			return r + 0xfee0
		}
		return r
	}, s)
}

func TestPrivateQuoteNormalization(t *testing.T) {
	if guardPrivateQuotes(quoteCorpus("Café notebooks stay hidden today."), nil, "Cafe\u200b\u0301 notebooks stay hidden today.") == nil {
		t.Fatal("format insertion defeated canonical composition")
	}
	corpus := quoteCorpus(guardRule, guardTLDR)
	for _, quote := range []string{
		guardRule, guardTLDR, "Notice: " + strings.ToUpper(guardRule),
		strings.ReplaceAll(guardRule, "o", "o\u200b"), fullwidth(guardRule),
		"Please keep " + strings.Join(strings.Fields(guardRule)[3:11], " ") + " safe.",
		strings.Replace(guardRule, "beneath", "under", 1),
	} {
		if err := guardPrivateQuotes(corpus, nil, quote); err == nil {
			t.Errorf("private quotation accepted: %q", quote)
		}
	}
	for _, public := range []string{"Run tests before merging.", "The observatory publishes public release notes.", "Keep credentials out of transcripts."} {
		if err := guardPrivateQuotes(corpus, nil, public); err != nil {
			t.Fatalf("public edit rejected: %v", err)
		}
	}
	for _, text := range []string{"hsb\u200b1", "ｈｓｂ１", "barta\u200b.cm", "h\u034fsb1", "hsb\ufe0f1", "\u04bbsb1", "barta.\u0441m", "pm.b\u0430rta", "inspr\u2011doctrine\u2011private", "inspr\u2013doctrine\u2013private", "inspr\u2212doctrine\u2212private"} {
		if guardPublic(publicRepository, text) == nil {
			t.Errorf("normalized pattern missed: %q", text)
		}
	}
	// A longer private sentence keeps the corpus non-empty. Entries under five
	// words are not stored, so they cannot refuse an unrelated public sentence.
	short := quoteCorpus(guardRule, "Stay hidden.", "Hidden copper notebooks stay.")
	if guardPrivateQuotes(short, nil, "Please stay hidden today.") != nil {
		t.Fatal("two-word private entry blocked a public sentence")
	}
	if guardPrivateQuotes(short, nil, "Hidden copper notebooks stay.") != nil {
		t.Fatal("four-word private entry blocked a public sentence")
	}
}

func TestPublicGuardOnlyChangedText(t *testing.T) {
	content := "# Kernel\n\n## Rules\n<!-- aeon-rule: edit -->\n- Run tests.\n<!-- aeon-rule: existing -->\n- Existing operator@example.test reference.\n"
	path := "docs/AGENTS-KERNEL.md"
	files := []File{{Path: path, Content: []byte(content)}, {Path: SidecarPath(path), Content: []byte("rules:\n  existing: {en: 'Existing operator@example.test note.'}\n")}}
	rule := Render(publicRepository, fixtureCommit, false, files)[0].Rules[0]
	in := ProposalInput{Path: path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: strings.Replace(rule.Source, "Run tests.", "Run tests before merging.", 1), Explanation: "Clarify validation."}
	in.TLDR.EN = "Validate before merging."
	if _, err := editRule(Source{Repository: publicRepository, Commit: fixtureCommit}, files, in); err != nil {
		t.Fatalf("unchanged content blamed: %v", err)
	}
}

func TestPublicProposalRequiresPrivateCorpusAndBlocksQuotes(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	endpoint := "/api/rules/doctrine/proposals"
	before := forge.calls
	if got := f.call(owner, "POST", endpoint, in, 422); !strings.Contains(string(got), "private_index_unavailable") || forge.calls != before {
		t.Fatal("missing private index did not fail before GitHub")
	}
	private := seedPrivateGuard(t, f, m, owner)
	for _, field := range []string{"rule", "en", "de", "explanation", "unmatched", "continuation"} {
		bad := in
		switch field {
		case "rule":
			bad.Source = "<!-- aeon-rule: no-env-dump -->\n- " + fullwidth(guardRule) + "\n"
		case "en":
			bad.TLDR.EN = guardTLDR
		case "de":
			bad.TLDR.DE = "Kupferne Notizen bleiben heute verborgen."
		case "explanation":
			bad.Explanation = strings.ReplaceAll(guardRule, "o", "o\u200b")
		case "unmatched":
			bad.Explanation = "Reserved observatory protocol stays sealed."
		case "continuation":
			bad.Explanation = "The silver ledger contains nine emerald diagrams beside the northern window."
		}
		beforeWrites := forge.writes
		got := f.call(owner, "POST", endpoint, bad, 422)
		if !strings.Contains(string(got), "private_doctrine") || strings.Contains(string(got), guardTLDR) || forge.writes != beforeWrites {
			t.Fatalf("private %s reached GitHub or was reflected", field)
		}
	}
	allowCredential(t, m.credentials.Dir, "guard-read", owner.TenantID, publicRepository)
	before = forge.calls
	f.call(owner, "POST", endpoint, in, 422)
	if forge.calls != before {
		t.Fatal("revoked private grant reached GitHub")
	}
	allowCredential(t, m.credentials.Dir, "guard-read", owner.TenantID, privateRepository)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_sources SET index_error='failed' WHERE id=$1`, private.ID); err != nil {
		t.Fatal(err)
	}
	f.call(owner, "POST", endpoint, in, 422)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_sources SET index_error='' WHERE id=$1`, private.ID); err != nil {
		t.Fatal(err)
	}
	f.call(owner, "POST", endpoint, in, 200)
	if forge.minted != forge.revocations {
		t.Fatal("successful proposal left installation token live")
	}
}

// Execute the request asynchronously without using Fatal in its goroutine.
func asyncProposal(f doctrineFixture, actor tenant.Principal, endpoint string, in any) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		raw, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", endpoint, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), actor))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		done <- w
	}()
	return done
}

func TestSlowGitHubDoesNotHoldDatabaseLocks(t *testing.T) {
	for _, operation := range []string{"propose", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			f, forge, m, owner, in := publicProposalFixture(t)
			seedPrivateGuard(t, f, m, owner)
			endpoint, payload := "/api/rules/doctrine/proposals", any(in)
			blockPath := "/git/trees"
			if operation == "refresh" {
				f.call(owner, "POST", endpoint, in, 200)
				endpoint += "/" + in.RequestID + "/refresh"
				payload, blockPath = nil, "/app"
			}
			entered, unblock := make(chan struct{}), make(chan struct{})
			defer close(unblock)
			forge.beforeRequest = func(r *http.Request) {
				if strings.HasSuffix(r.URL.Path, blockPath) {
					close(entered)
					select {
					case <-unblock:
					case <-r.Context().Done():
					}
				}
			}
			done := asyncProposal(f, owner, endpoint, payload)
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("request never reached slow GitHub")
			}
			ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), owner), 2*time.Second)
			defer cancel()
			err := db.InTenant(ctx, f.d.App, owner.TenantID, func(tx pgx.Tx) error {
				// FK check on tenants + events append must complete while GitHub waits.
				_, err := events.Append(ctx, tx, owner, events.Change{Type: "test.unrelated_write", After: map[string]any{"ok": true}})
				if err != nil {
					return err
				}
				// Stronger than the FK regression: no tenant or proposal row lock remains.
				if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, owner.TenantID); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `SELECT id FROM doctrine_proposals WHERE id=$1 FOR UPDATE NOWAIT`, in.RequestID)
				return err
			})
			if err != nil {
				t.Fatalf("slow GitHub held a database lock: %v", err)
			}
			// A second worker cannot issue external operations for this proposal.
			f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/refresh", nil, 409)
			unblock <- struct{}{}
			select {
			case w := <-done:
				if w.Code != 200 {
					t.Fatalf("request: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("request stuck after GitHub resumed")
			}
		})
	}
}

func TestProposalCASAndAuthorizationAfterIO(t *testing.T) {
	for _, change := range []string{"version", "authority"} {
		t.Run(change, func(t *testing.T) {
			f, forge, m, owner, in := publicProposalFixture(t)
			seedPrivateGuard(t, f, m, owner)
			f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
			forge.green()
			forge.beforeRequest = func(r *http.Request) {
				if r.URL.Path != "/app" {
					return
				}
				if change == "version" {
					_, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=jsonb_set(data,'{version}',to_jsonb((data->>'version')::bigint+1)) WHERE id=$1`, in.RequestID)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					_, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, owner.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			want := 409
			if change == "authority" {
				want = 403
			}
			f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/refresh", nil, want)
			var state string
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT data->>'state' FROM doctrine_proposals WHERE id=$1`, in.RequestID).Scan(&state); err != nil || state != "proposed" {
				t.Fatalf("stale result overwrote state: %s %v", state, err)
			}
		})
	}
}

func TestExternalMergeAndNoOpRefresh(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	seedPrivateGuard(t, f, m, owner)
	f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
	item := "/api/rules/doctrine/proposals/" + in.RequestID
	forge.green()
	f.call(owner, "POST", item+"/refresh", nil, 200)
	count := func() int {
		var n int
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, owner.TenantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	f.call(owner, "POST", item+"/refresh", nil, 200)
	if count() != before {
		t.Fatal("no-op refresh appended an event")
	}
	for key, pr := range forge.pulls {
		pr.Merged, pr.State, pr.MergeCommit = true, "closed", privateSHA
		forge.pulls[key] = pr
	}
	f.call(owner, "POST", item+"/approve", map[string]string{"head_sha": nextCommit}, 409)
	var p Proposal
	if err := json.Unmarshal(f.call(owner, "POST", item+"/refresh", nil, 200), &p); err != nil {
		t.Fatal(err)
	}
	if p.State != "merged" || p.ApprovedBy != "" || p.ReleaseRequested || forge.mergeCalls != 0 || forge.dispatches != 0 {
		t.Fatal("external merge attributed to observer or dispatched")
	}
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('doctrine.merged','doctrine.approved')`, owner.TenantID).Scan(&n); err != nil || n != 0 {
		t.Fatal("external merge audit falsely claims approval/merge")
	}
}

func TestTokenRevokedOnScopeErrorAndCancellation(t *testing.T) {
	f, forge, m, owner, _ := publicProposalFixture(t)
	_ = f
	forge.extraRepo = true
	if _, err := m.appClient(t.Context(), owner.TenantID, publicRepository); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if forge.minted != 1 || forge.revocations != 1 {
		t.Fatal("rejected installation token was not revoked")
	}
	forge.extraRepo = false
	ctx, cancel := context.WithCancel(t.Context())
	g, err := m.appClient(ctx, owner.TenantID, publicRepository)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	g.revoke()
	if forge.minted != forge.revocations || g.Token != "" {
		t.Fatal("cancelled operation retained token")
	}
}

func TestPrivateGuardFullTreeAndWriteObservation(t *testing.T) {
	const (
		profileRule = "The profile ledger forbids printing fleet hostnames in a public proposal body."
		proseLine   = "Operators keep the silver spare key inside the cedar drawer behind the north stair."
		commandLine = "Never paste an age identity into a chat transcript or a public pull request."
		reordered   = "Seasonal planning uses the silver ledger beside the northern window each week. The copper observatory keeps seven violet notebooks beneath the eastern stairway."
	)
	paragraph := "The copper observatory keeps seven violet notebooks beneath the eastern stairway. Seasonal planning uses the silver ledger beside the northern window each week."
	f, forge, m, owner, in := publicProposalFixture(t)
	f.fake.failBlob = "docs/AGENTS-PROFILE-MARKUS.md"
	f.fake.commit(privateRepository, fixtureCommit, map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md":        "# Private\n\n## Copy\n\n" + in.Source + "\n\n" + proseLine + "\n\n" + paragraph + "\n",
		"docs/AGENTS-PROFILE-MARKUS.md":        "# Profile\n\n- " + profileRule + "\n",
		"commands/secrets.md":                  "# Secrets\n\n- " + commandLine + "\n",
		"docs/AGENTS-KERNEL-PRIVATE.tldr.yaml": "rules:\n  copper:\n    en: " + guardTLDR + "\n",
		"assets/blank.dat":                     string([]byte{0, 1, 2, 3}),
	}, "main")
	allowCredential(t, m.credentials.Dir, "guard-read", owner.TenantID, privateRepository)
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, "guard-read"), []byte("fixtureGuardRead319"), 0o600); err != nil {
		t.Fatal(err)
	}
	narrow := SourceInput{Repository: privateRepository, Visibility: "private", Ref: "main", CredentialRef: "guard-read", Paths: []string{"docs/AGENTS-KERNEL-PRIVATE.md"}}
	created := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", narrow), privateRepository)
	if created.Error == "" || created.State == "ready" {
		t.Fatal("unreadable private tree indexed as ready")
	}
	before := forge.calls
	if got := f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 422); !strings.Contains(string(got), "private_index_unavailable") || forge.calls != before {
		t.Fatal("unreadable private tree reached GitHub")
	}
	f.fake.failBlob = ""
	ready := find(f.layer(owner, "POST", "/api/rules/doctrine/sources/"+created.ID+"/index", nil), privateRepository)
	if ready.Error != "" || ready.State != "ready" {
		t.Fatalf("reindex %+v", ready)
	}
	var published []string
	for _, file := range ready.Files {
		published = append(published, file.Path)
	}
	if strings.Join(published, ",") != "docs/AGENTS-KERNEL-PRIVATE.md" {
		t.Fatalf("narrow paths published %v", published)
	}
	body := f.call(owner, "GET", "/api/rules/doctrine", nil, 200)
	for _, secret := range []string{profileRule, commandLine, proseLine} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("private text reached the doctrine API")
		}
	}
	var corpus []byte
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT corpus FROM doctrine_private_guard WHERE source_id=$1`, created.ID).Scan(&corpus); err != nil || bytes.Contains(corpus, []byte("profile ledger")) || bytes.Contains(corpus, []byte(profileRule)) {
		t.Fatal("guard stored private plaintext")
	}
	if f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200) == nil {
		t.Fatal("unchanged public rule was refused")
	}
	attacks := []string{guardRule, guardTLDR, strings.ReplaceAll(guardRule, "o", "o\u200b"), fullwidth(guardRule), cyrillicize(guardRule), strings.ReplaceAll(guardRule, " ", "\n"), reordered, profileRule, proseLine, commandLine}
	for _, attack := range attacks {
		bad := in
		bad.Explanation = attack
		bad.RequestID = "31900000-0000-4000-8000-0000000000a1"
		before = forge.writes
		want := "private_doctrine"
		if !latinPublicText(attack) {
			want = "non_latin"
		}
		got := f.call(owner, "POST", "/api/rules/doctrine/proposals", bad, 422)
		if !strings.Contains(string(got), want) || bytes.Contains(got, []byte(attack)) || forge.writes != before {
			t.Fatalf("private attack reached GitHub or was reflected as %s", want)
		}
	}
}

func cyrillicize(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'a':
			return '\u0430'
		case 'e':
			return '\u0435'
		case 'o':
			return '\u043e'
		case 'p':
			return '\u0440'
		case 'c':
			return '\u0441'
		case 'y':
			return '\u0443'
		case 'x':
			return '\u0445'
		case 'i':
			return '\u0456'
		case 's':
			return '\u0455'
		case 'h':
			return '\u04bb'
		default:
			return r
		}
	}, s)
}

func TestWriteReauthorizedBeforeGitHubMutation(t *testing.T) {
	revoke := func(t *testing.T, f doctrineFixture, owner tenant.Principal) {
		t.Helper()
		if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("before ref", func(t *testing.T) {
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		forge.beforeRequest = func(r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/main") {
				revoke(t, f, owner)
			}
		}
		f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 403)
		if len(forge.refs) != 0 || len(forge.pulls) != 0 {
			t.Fatal("revoked authority created a ref or PR")
		}
	})
	t.Run("during ref", func(t *testing.T) {
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		forge.beforeRequest = func(r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs") {
				revoke(t, f, owner)
			}
		}
		got := f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 403)
		if bytes.Contains(got, []byte(guardRule)) || len(forge.refs) != 1 || len(forge.pulls) != 0 {
			t.Fatal("revocation during the ref write left no branch record or created a PR")
		}
		var branch, orphaned, event string
		err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'branch',''), COALESCE(data->>'orphaned',''), COALESCE((SELECT type FROM events WHERE tenant_id=$1 AND type='doctrine.branch_observed' LIMIT 1),'') FROM doctrine_proposals WHERE id=$2`, owner.TenantID, in.RequestID).Scan(&branch, &orphaned, &event)
		if err != nil || branch != "aeon/proposals/"+in.RequestID || orphaned != "true" || event != "doctrine.branch_observed" {
			t.Fatalf("orphaned branch not observed branch=%s orphaned=%s event=%s err=%v", branch, orphaned, event, err)
		}
	})
	t.Run("after pull", func(t *testing.T) {
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		forge.beforeRequest = func(r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") {
				revoke(t, f, owner)
			}
		}
		f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 403)
		var pr, event string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'pr_number',''), COALESCE((SELECT type FROM events WHERE tenant_id=$1 AND type='doctrine.proposal_observed' LIMIT 1),'') FROM doctrine_proposals WHERE id=$2`, owner.TenantID, in.RequestID).Scan(&pr, &event); err != nil || pr == "" || pr == "0" || event != "doctrine.proposal_observed" || len(forge.pulls) != 1 {
			t.Fatalf("lost pull pr=%s event=%s pulls=%d err=%v", pr, event, len(forge.pulls), err)
		}
	})
	t.Run("after merge", func(t *testing.T) {
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
		forge.green()
		forge.beforeRequest = func(r *http.Request) {
			if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge") {
				revoke(t, f, owner)
			}
		}
		f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/approve", map[string]string{"head_sha": nextCommit}, 403)
		var merge, state string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'merge_commit',''), COALESCE(data->>'state','') FROM doctrine_proposals WHERE id=$1`, in.RequestID).Scan(&merge, &state); err != nil || merge == "" || state != "merged" || forge.mergeCalls != 1 || forge.dispatches != 0 {
			t.Fatalf("lost merge %s %s calls=%d err=%v", merge, state, forge.mergeCalls, err)
		}
	})
	t.Run("after dispatch", func(t *testing.T) {
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
		forge.green()
		forge.beforeRequest = func(r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dispatches") {
				revoke(t, f, owner)
			}
		}
		f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/approve", map[string]string{"head_sha": nextCommit}, 403)
		var requested string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'release_requested','') FROM doctrine_proposals WHERE id=$1`, in.RequestID).Scan(&requested); err != nil || requested != "true" || forge.dispatches != 1 {
			t.Fatalf("lost dispatch %s calls=%d err=%v", requested, forge.dispatches, err)
		}
	})
}

const launderSHA = "4444444444444444444444444444444444444444"

func pinLaunder(t *testing.T, f doctrineFixture, owner tenant.Principal, sourceID, ref string) {
	t.Helper()
	files := fixtureFiles()
	files["docs/AGENTS-DOMAIN-DEV.md"] = files["docs/AGENTS-DOMAIN-DEV.md"] + "\n- " + guardRule + "\n"
	var refs []string
	if ref != "" {
		refs = []string{ref}
	}
	f.fake.commit(publicRepository, launderSHA, files, refs...)
	body := map[string]any{"visibility": "public", "commit": launderSHA}
	if ref != "" {
		body["ref"] = ref
	}
	updated := find(f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+sourceID, body), publicRepository)
	if updated == nil || updated.State != "ready" || updated.Commit != launderSHA {
		t.Fatal("launder pin did not index")
	}
}

func TestPublicPinCannotLaunderPrivateText(t *testing.T) {
	quote := func(t *testing.T, ref string) {
		t.Helper()
		f, forge, m, owner, in := publicProposalFixture(t)
		seedPrivateGuard(t, f, m, owner)
		pinLaunder(t, f, owner, in.SourceID, ref)
		bad := in
		bad.RequestID = "31900000-0000-4000-8000-0000000000b1"
		bad.Explanation = guardRule
		before := forge.writes
		got := f.call(owner, "POST", "/api/rules/doctrine/proposals", bad, 422)
		if !strings.Contains(string(got), "private_doctrine") || bytes.Contains(got, []byte(guardRule)) || forge.writes != before {
			t.Fatal("pinned private text was published")
		}
		ok := in
		ok.RequestID = "31900000-0000-4000-8000-0000000000b2"
		ok.TLDR.DE = "Öffentliche Regeln bleiben gültig."
		f.call(owner, "POST", "/api/rules/doctrine/proposals", ok, 200)
	}
	t.Run("branch", func(t *testing.T) { quote(t, "aeon/proposals/launder") })
	t.Run("sha", func(t *testing.T) { quote(t, "") })
}

func TestPublicProposalRejectsNonLatin(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	seedPrivateGuard(t, f, m, owner)
	cases := []struct {
		id   string
		name string
		text string
		code string
	}{
		{"31900000-0000-4000-8000-0000000000c1", "greek capital", "Clarify step \u0391.", "non_latin"},
		{"31900000-0000-4000-8000-0000000000c2", "armenian", "Clarify step \u0585.", "non_latin"},
		{"31900000-0000-4000-8000-0000000000c3", "arabic digit", "Clarify step \u0661.", "non_latin"},
		{"31900000-0000-4000-8000-0000000000c4", "fullwidth digit", "Clarify step \uff11.", "non_latin"},
		{"31900000-0000-4000-8000-0000000000c5", "dotless quote", strings.ReplaceAll(guardRule, "i", "\u0131"), "private_doctrine"},
	}
	for _, tc := range cases {
		bad := in
		bad.Explanation = tc.text
		bad.RequestID = tc.id
		beforeWrites, beforeCalls := forge.writes, forge.calls
		got := f.call(owner, "POST", "/api/rules/doctrine/proposals", bad, 422)
		if !strings.Contains(string(got), tc.code) || bytes.Contains(got, []byte(tc.text)) || forge.writes != beforeWrites {
			t.Fatalf("%s reached GitHub or was reflected", tc.name)
		}
		if tc.code == "non_latin" && forge.calls != beforeCalls {
			t.Fatalf("%s reached GitHub", tc.name)
		}
	}
	corpus := quoteCorpus(guardRule)
	for _, text := range []string{greekCapitals(guardRule), cyrillicCapitals(guardRule), strings.ReplaceAll(guardRule, "o", "\u0585"), strings.ReplaceAll(guardRule, "i", "\u0131")} {
		if latinPublicText(text) && strings.ContainsRune(text, '\u0131') {
			continue
		}
		if guardPrivateQuotes(corpus, nil, text) == nil {
			t.Fatal("lookalike quotation accepted")
		}
	}
	if guardPrivateQuotes(corpus, nil, strings.ReplaceAll(guardRule, "i", "\u0131")) == nil {
		t.Fatal("dotless i escaped the quotation guard")
	}
	if !latinPublicText("Öffentliche Regeln bleiben gültig. äöüÄÖÜß 0123456789") {
		t.Fatal("Latin letters, umlauts or ASCII digits refused")
	}
	if latinPublicText("step \u0391") || latinPublicText("step \u0585") || latinPublicText("step \u0661") || latinPublicText("step \uff11") {
		t.Fatal("non-Latin letter or digit accepted")
	}
}

func greekCapitals(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'a', 'A':
			return '\u0391'
		case 'b', 'B':
			return '\u0392'
		case 'e', 'E':
			return '\u0395'
		case 'i', 'I':
			return '\u0399'
		case 'k', 'K':
			return '\u039a'
		case 'o', 'O':
			return '\u039f'
		case 'p', 'P':
			return '\u03a1'
		case 't', 'T':
			return '\u03a4'
		case 'u', 'U':
			return '\u03a5'
		case 'x', 'X':
			return '\u03a7'
		case 'y', 'Y':
			return '\u0393'
		case 'n', 'N':
			return '\u0397'
		case 'v', 'V':
			return '\u039d'
		case 'w', 'W':
			return '\u03a9'
		default:
			return r
		}
	}, s)
}

func cyrillicCapitals(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'a', 'A':
			return '\u0410'
		case 'e', 'E':
			return '\u0415'
		case 'o', 'O':
			return '\u041e'
		case 'p', 'P':
			return '\u0420'
		case 'c', 'C':
			return '\u0421'
		case 'y', 'Y':
			return '\u0423'
		case 'x', 'X':
			return '\u0425'
		case 'i', 'I':
			return '\u0406'
		case 's', 'S':
			return '\u0405'
		case 'h', 'H':
			return '\u04ba'
		default:
			return r
		}
	}, s)
}

func TestPrivateGuardRebuildsWhenMissingOrRotated(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	private := seedPrivateGuard(t, f, m, owner)
	if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM doctrine_private_guard WHERE source_id=$1`, private.ID); err != nil {
		t.Fatal(err)
	}
	before := forge.calls
	if got := f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 422); !strings.Contains(string(got), "private_index_unavailable") || forge.calls != before || forge.writes != 0 {
		t.Fatal("missing guard reached GitHub")
	}
	f.fake.down = true
	m.EnsurePrivateGuards(t.Context())
	var indexError string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(index_error,'') FROM doctrine_sources WHERE id=$1`, private.ID).Scan(&indexError); err != nil || indexError != "" {
		t.Fatal("failed guard rebuild recorded an index error")
	}
	f.fake.down = false
	m.EnsurePrivateGuards(t.Context())
	clean := in
	clean.RequestID = "31900000-0000-4000-8000-0000000000d1"
	f.call(owner, "POST", "/api/rules/doctrine/proposals", clean, 200)

	rotated := bytes.Repeat([]byte{0x3c}, 32)
	m.guardMaster = rotated
	stale := in
	stale.RequestID = "31900000-0000-4000-8000-0000000000d2"
	before = forge.calls
	if got := f.call(owner, "POST", "/api/rules/doctrine/proposals", stale, 422); !strings.Contains(string(got), "private_index_unavailable") || forge.calls != before {
		t.Fatal("rotated guard reached GitHub")
	}
	m.EnsurePrivateGuards(t.Context())
	quoted := in
	quoted.RequestID = "31900000-0000-4000-8000-0000000000d3"
	quoted.Explanation = guardRule
	beforeWrites := forge.writes
	got := f.call(owner, "POST", "/api/rules/doctrine/proposals", quoted, 422)
	if !strings.Contains(string(got), "private_doctrine") || bytes.Contains(got, []byte(guardRule)) || forge.writes != beforeWrites {
		t.Fatal("rebuilt guard missed a private quotation")
	}
	var corpus []byte
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT corpus FROM doctrine_private_guard WHERE source_id=$1`, private.ID).Scan(&corpus); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(corpus, rotated) || bytes.Contains(corpus, m.guardKey(owner.TenantID)) || bytes.Contains(corpus, []byte("copper")) {
		t.Fatal("rebuilt corpus exposes the key or plaintext")
	}
	again := in
	again.RequestID = "31900000-0000-4000-8000-0000000000d4"
	again.TLDR.DE = "Öffentliche Regeln bleiben gültig."
	f.call(owner, "POST", "/api/rules/doctrine/proposals", again, 200)
}

func TestPrivateQuoteFailureIsGeneric(t *testing.T) {
	err := guardPrivateQuotes(quoteCorpus(guardRule), nil, guardRule)
	var f *failure
	if !errors.As(err, &f) || f.Code != "private_doctrine" || strings.Contains(err.Error(), guardRule) {
		t.Fatal("private match reflected")
	}
}
