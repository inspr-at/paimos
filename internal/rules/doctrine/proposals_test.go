// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
)

// A fake forge, including installation exchange, PRs, the independent gate,
// merge and the owning repo's asynchronous release receiver. Never GitHub.
type proposalForge struct {
	t               *testing.T
	reader          *fakeGitHub
	key             *rsa.PrivateKey
	writes          int
	trees           int
	refs            map[string]string
	pulls           map[string]pull
	treeFiles       map[string]string
	reviews         []gateReview
	clean           bool
	protected       bool
	released        bool
	failDispatch    bool
	dropPR          bool
	dropMerge       bool
	extraRepo       bool
	wrongVisibility bool
	calls           int
	dispatches      int
	mergeCalls      int
	revocations     int
	minted          int
	beforeRequest   func(*http.Request)
	proposalID      string
	labels          int
	bodies          []string
	mainSHA         string
}

func (f *proposalForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	if f.beforeRequest != nil {
		f.beforeRequest(r)
	}
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/installation/token" && r.Method == "DELETE" {
		if r.Header.Get("Authorization") != "Bearer installationFixtureToken319" {
			f.t.Error("revocation used wrong credential")
		}
		if r.Context().Err() != nil {
			f.t.Error("revocation inherited cancelled context")
		}
		f.revocations++
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/app" {
		send(map[string]any{"id": 8, "slug": "fixture-doctrine"})
		return
	}
	if r.URL.Path == "/users/fixture-doctrine[bot]" {
		send(map[string]any{"id": 42, "login": "fixture-doctrine[bot]"})
		return
	}

	if r.URL.Path == "/app/installations/9/access_tokens" {
		jwt := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(jwt) != 3 {
			f.t.Error("missing App JWT")
			w.WriteHeader(401)
			return
		}
		sum := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
		sig, _ := base64.RawURLEncoding.DecodeString(jwt[2])
		if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			f.t.Error("invalid App JWT signature")
		}
		var request struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if (len(request.Repositories) != 1 || (request.Repositories[0] != "inspr-modules" && request.Repositories[0] != "inspr-doctrine-private")) || len(request.Permissions) != 2 || request.Permissions["contents"] != "write" || request.Permissions["pull_requests"] != "write" {
			f.t.Error("token was not narrowly scoped")
			w.WriteHeader(400)
			return
		}
		repo := "inspr-at/" + request.Repositories[0]
		private := repo == privateRepository
		if f.wrongVisibility {
			private = !private
		}
		repos := []map[string]any{{"full_name": repo, "private": private}}
		if f.extraRepo {
			repos = append(repos, map[string]any{"full_name": "someone/else", "private": true})
		}
		f.minted++
		send(map[string]any{"token": "installationFixtureToken319", "permissions": map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}, "repositories": repos})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	suffix := "/" + strings.Join(parts[2:], "/")
	// The read-only indexer uses its own access path, as in production.
	if strings.HasPrefix(suffix, "/commits/") || strings.HasPrefix(suffix, "/git/trees/") || strings.HasPrefix(suffix, "/git/blobs/") {
		f.reader.ServeHTTP(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer installationFixtureToken319" {
		f.t.Error("write transport did not use installation credential")
		w.WriteHeader(401)
		return
	}
	if r.Method != "GET" {
		f.writes++
	}
	switch {
	case suffix == "/branches/main":
		main := fixtureCommit
		if f.mainSHA != "" {
			main = f.mainSHA
		}
		send(map[string]any{"protected": f.protected, "commit": map[string]string{"sha": main}})
	case strings.HasPrefix(suffix, "/git/commits/"):
		send(map[string]any{"tree": map[string]string{"sha": strings.Repeat("a", 40)}})
	case suffix == "/git/trees" && r.Method == "POST":
		var in struct {
			Tree []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.treeFiles = map[string]string{}
		for k, v := range fixtureFiles() {
			f.treeFiles[k] = v
		}
		for _, e := range in.Tree {
			f.treeFiles[e.Path] = e.Content
		}
		f.trees++
		send(map[string]string{"sha": strings.Repeat("b", 40)})
	case suffix == "/git/commits" && r.Method == "POST":
		var in struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Author.Name != "fixture-doctrine[bot]" || in.Author.Email != "42+fixture-doctrine[bot]@users.noreply.github.com" || !strings.Contains(in.Message, "Signed-off-by: "+in.Author.Name+" <"+in.Author.Email+">") {
			f.t.Error("commit must use the verified App bot and its acknowledged DCO trailer")
		}
		send(map[string]string{"sha": nextCommit})
	case suffix == "/git/refs" && r.Method == "POST":
		var in struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !strings.HasPrefix(in.Ref, "refs/heads/aeon/proposals/") {
			f.t.Error("attempted write outside proposal branch")
		}
		key := repo + in.Ref
		if f.refs[key] != "" {
			w.WriteHeader(422)
			return
		}
		f.refs[key] = in.SHA
		send(map[string]any{"object": map[string]string{"sha": in.SHA}})
	case strings.HasPrefix(suffix, "/git/ref/heads/"):
		send(map[string]any{"object": map[string]string{"sha": f.refs[repo+"refs/heads/"+strings.TrimPrefix(suffix, "/git/ref/heads/")]}})
	case suffix == "/pulls" && r.Method == "GET":
		var out = []pull{}
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "inspr-at:")
		if pr, ok := f.pulls[repo+head]; ok {
			out = append(out, pr)
		}
		send(out)
	case suffix == "/pulls" && r.Method == "POST":
		var in struct {
			Draft bool   `json:"draft"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Body  string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		pr := pull{Number: len(f.pulls) + 1, State: "open", MergeableState: "blocked", Draft: in.Draft}
		f.bodies = append(f.bodies, in.Body)
		pr.Head.Ref = in.Head
		pr.Head.SHA = nextCommit
		pr.Head.Repo.FullName = repo
		pr.Base.Ref = in.Base
		pr.Base.Repo.FullName = repo
		f.pulls[repo+in.Head] = pr
		if f.dropPR {
			f.dropPR = false
			w.WriteHeader(502)
			return
		}
		send(pr)
	case strings.HasPrefix(suffix, "/issues/") && strings.HasSuffix(suffix, "/labels"):
		var in struct {
			Labels []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.Labels) != 1 || in.Labels[0] != "aeon-proposal" {
			f.t.Error("unexpected outcome label")
		}
		f.labels++
		send([]any{})
	case strings.HasSuffix(suffix, "/reviews"):
		send(f.reviews)
	case strings.HasSuffix(suffix, "/merge") && r.Method == "PUT":
		f.mergeCalls++
		var in struct {
			SHA     string `json:"sha"`
			Message string `json:"commit_message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Message != "Signed-off-by: fixture-doctrine[bot] <42+fixture-doctrine[bot]@users.noreply.github.com>" {
			f.t.Error("squash discarded its existing bot sign-off")
		}
		if in.SHA != nextCommit {
			f.t.Error("merge was not SHA-guarded")
			w.WriteHeader(409)
			return
		}
		for k, pr := range f.pulls {
			if fmt.Sprintf("/pulls/%d/merge", pr.Number) == suffix && pr.Head.Repo.FullName == repo {
				pr.Merged = true
				pr.State = "closed"
				pr.MergeCommit = privateSHA
				f.pulls[k] = pr
			}
		}
		if f.dropMerge {
			f.dropMerge = false
			w.WriteHeader(502)
			return
		}
		send(map[string]any{"merged": true, "sha": privateSHA})
	case strings.HasPrefix(suffix, "/pulls/"):
		for _, pr := range f.pulls {
			if fmt.Sprintf("/pulls/%d", pr.Number) == suffix && pr.Head.Repo.FullName == repo {
				if f.clean {
					pr.MergeableState = "clean"
				}
				send(pr)
				return
			}
		}
		http.NotFound(w, r)
	case suffix == "/dispatches":
		if f.failDispatch {
			w.WriteHeader(502)
			return
		}
		f.dispatches++
		var in struct {
			Type    string            `json:"event_type"`
			Payload map[string]string `json:"client_payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Type != "doctrine-release" || in.Payload["merge_commit"] != privateSHA || in.Payload["requested_scheme"] != "CalVer3" {
			f.t.Error("bad release dispatch")
		}
		f.proposalID = in.Payload["proposal_id"]
		w.WriteHeader(204)
	case suffix == "/releases":
		out := []map[string]any{}
		if f.released {
			e, _ := json.Marshal(map[string]string{"proposal_id": f.proposalID, "merge_commit": privateSHA, "version_scheme": "CalVer3"})
			out = append(out, map[string]any{"tag_name": "v26.9.29.1", "body": releaseMarker + string(e), "draft": false, "prerelease": false})
		}
		send(out)
	default:
		http.NotFound(w, r)
	}
}
func (f *proposalForge) green() {
	f.clean = true
	evidence, _ := json.Marshal(gateEvidence{Verdict: "ok", Head: nextCommit, Checks: true, AuthorFamily: "openai", ReviewerFamily: "anthropic"})
	review := gateReview{ID: 1, State: "APPROVED", CommitID: nextCommit, Body: gateMarker + string(evidence)}
	review.User.Login = "independent-gate[bot]"
	f.reviews = []gateReview{review}
}
func newProposalFixture(t *testing.T) (doctrineFixture, *proposalForge, *Module) {
	d := dbtest.Open(t)
	reader, _ := newFakeGitHub(t)
	reader.commit(publicRepository, fixtureCommit, fixtureFiles(), "main")
	reader.commit(privateRepository, fixtureCommit, fixtureFiles(), "main")
	reader.commit(publicRepository, privateSHA, fixtureFiles(), "refs/tags/v26.9.29.1")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate test key")
	}
	dir := t.TempDir()
	raw := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err = os.WriteFile(filepath.Join(dir, "app-key"), raw, 0600); err != nil {
		t.Fatal("write test key")
	}
	forge := &proposalForge{t: t, reader: reader, key: key, refs: map[string]string{}, pulls: map[string]pull{}, protected: true}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatal("proposal request left the fixed GitHub origin")
		}
		w := httptest.NewRecorder()
		forge.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	m := New(d.App, Options{CredentialsDir: dir, Client: client, GuardKey: testGuardMaster(), App: AppConfig{ID: "8", InstallationID: "9", KeyRef: "app-key", GateLogin: "independent-gate[bot]", DCOAcknowledged: true}})
	mux := http.NewServeMux()
	m.Mount(mux)
	return doctrineFixture{t: t, d: d, mux: mux, fake: reader}, forge, m
}

func TestAppCredentialRequiresTenantRepositoryGrant(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid, other := f.tenant("app-grant-a"), f.tenant("app-grant-b")
	m.app.TenantID = tid
	denied := func(tenantID, repo string) {
		t.Helper()
		before := forge.calls
		g, err := m.appClient(t.Context(), tenantID, repo)
		if g != nil || !errors.Is(err, ErrCredential) || err.Error() != "credential unavailable" || forge.calls != before {
			t.Fatal("App credential denial must be generic and precede every GitHub call")
		}
	}
	// A valid key and host App configuration grant no repository authority.
	denied(tid, publicRepository)
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	denied(other, publicRepository)
	denied(tid, privateRepository)
	denied(tid, "someone/else")
	// Pairs must not become a Cartesian product, even with multiple grants.
	policy := `{"grants":[{"tenant_id":"` + tid + `","repository":"` + publicRepository + `"},{"tenant_id":"` + other + `","repository":"` + privateRepository + `"}]}`
	writePolicy := func(raw string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(m.credentials.Dir, "app-key.allowlist.json"), []byte(raw), 0600); err != nil {
			t.Fatal("write fixture policy")
		}
	}
	writePolicy(policy)
	denied(tid, privateRepository)
	if _, err := m.appClient(t.Context(), tid, publicRepository); err != nil {
		t.Fatal("explicit App grant rejected")
	}
	for _, invalid := range []string{`{`, `{}`, `null`, `{"grants":[]}`, policy + `{}`, strings.Replace(policy, publicRepository, "inspr-at/*", 1), strings.Replace(policy, tid, "*", 1), strings.Replace(policy, `"grants"`, `"unknown"`, 1), policy + strings.Repeat(" ", 64<<10)} {
		writePolicy(invalid)
		denied(tid, publicRepository)
	}
	// Revocation and rotation are observed on the very next call.
	allowCredential(t, m.credentials.Dir, "app-key", tid, privateRepository)
	denied(tid, publicRepository)
	if _, err := m.appClient(t.Context(), tid, privateRepository); err != nil {
		t.Fatal("private App grant rejected")
	}
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, "app-key"), []byte("invalid fixture key"), 0600); err != nil {
		t.Fatal("replace fixture key")
	}
	denied(tid, privateRepository)
}

func TestProposalRoundTripAndSafety(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("proposal-a")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "builder", "admin", []string{"rules.read", "rules.write", "rules.publish"}, owner.ID)
	viewer := f.principal(tid, "person", "viewer", "viewer", nil, "")
	otherID := f.tenant("proposal-b")
	other := f.principal(otherID, "person", "other", "admin", nil, "")
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	if src == nil || !layer.ProposalsEnabled {
		t.Fatal("missing source or proposal capability")
	}
	rule := src.Files[1].Rules[0]
	input := ProposalInput{RequestID: "31900000-0000-4000-8000-000000000001", SourceID: src.ID, Path: src.Files[1].Path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: strings.Replace(rule.Source, "prints secrets", "can expose credentials", 1), Explanation: "Clarify the reason without changing the requirement."}
	input.TLDR.EN = "Keep credentials out of transcripts."
	path := "/api/rules/doctrine/proposals"
	var p Proposal
	decode := func(raw []byte) {
		t.Helper()
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
	}
	before := forge.writes
	m.app.DCOAcknowledged = false
	f.call(owner, "POST", path, input, 503)
	m.app.DCOAcknowledged = true
	f.call(viewer, "POST", path, input, 403)
	f.call(other, "POST", path, input, 403)
	missing := input
	missing.SourceID = "31900000-0000-4000-8000-999999999999"
	f.call(owner, "POST", path, missing, 404)
	bad := input
	bad.TLDR.EN = "Contact operator@example.test"
	raw := f.call(owner, "POST", path, bad, 422)
	if !strings.Contains(string(raw), "public_identity") || strings.Contains(string(raw), "operator@example.test") || forge.writes != before {
		t.Fatal("leak guard published or reflected private content")
	}
	bad = input
	bad.Source += "\n- Another rule.\n"
	f.call(owner, "POST", path, bad, 400)
	forge.extraRepo = true
	f.call(owner, "POST", path, input, 422)
	forge.extraRepo = false
	forge.wrongVisibility = true
	f.call(owner, "POST", path, input, 422)
	forge.wrongVisibility = false
	if forge.writes != before {
		t.Fatal("bad scope reached mutation")
	}
	// A lost create-PR response leaves a reservation and recovers the same PR.
	forge.dropPR = true
	f.call(agent, "POST", path, input, 422)
	decode(f.call(agent, "POST", path, input, 200))
	if p.PRNumber != 1 || len(forge.pulls) != 1 || p.State != "proposed" {
		t.Fatalf("proposal %+v", p)
	}
	decode(f.call(agent, "POST", path, input, 200))
	if len(forge.pulls) != 1 {
		t.Fatal("retry duplicated PR")
	}
	bad = input
	bad.Explanation = "Different"
	f.call(agent, "POST", path, bad, 409)
	if !strings.Contains(forge.treeFiles[input.Path], "can expose credentials") || !strings.Contains(forge.treeFiles[SidecarPath(input.Path)], input.TLDR.EN) {
		t.Fatal("rule and TLDR did not reach git")
	}
	item := path + "/" + p.ID
	f.call(other, "POST", item+"/refresh", nil, 403)
	approve := map[string]string{"head_sha": p.HeadSHA}
	// Existing proposals cannot refresh, merge or dispatch after revocation.
	allowCredential(t, m.credentials.Dir, "app-key", otherID, publicRepository)
	beforeCalls := forge.calls
	f.call(owner, "POST", item+"/refresh", nil, 422)
	f.call(owner, "POST", item+"/approve", approve, 422)
	if forge.calls != beforeCalls {
		t.Fatal("revoked App grant reached GitHub")
	}
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	f.call(agent, "POST", item+"/approve", approve, 403)
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.green()
	savedReview := forge.reviews[0]
	forge.reviews[0].User.Login = "untrusted-reviewer"
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.reviews[0] = savedReview
	forge.reviews[0].Body = strings.Replace(savedReview.Body, `"checks_green":true`, `"checks_green":false`, 1)
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.reviews[0] = savedReview
	forge.protected = false
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.protected = true
	forge.reviews[0].State = "CHANGES_REQUESTED"
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.reviews[0] = savedReview
	f.call(owner, "POST", item+"/approve", map[string]string{"head_sha": fixtureCommit}, 409)
	if forge.mergeCalls != 0 {
		t.Fatal("a closed gate reached merge")
	}
	stale := forge.reviews[0]
	forge.reviews[0].CommitID = fixtureCommit
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.reviews[0] = stale
	same := strings.Replace(forge.reviews[0].Body, "anthropic", "openai", 1)
	forge.reviews[0].Body = same
	f.call(owner, "POST", item+"/approve", approve, 409)
	forge.green()
	decode(f.call(agent, "POST", item+"/refresh", nil, 200))
	if !p.GateReady || p.State != "in_review" {
		t.Fatal("green gate not shown")
	}
	// Approval intent survives a merge whose response is lost.
	forge.dropMerge = true
	f.call(owner, "POST", item+"/approve", approve, 422)
	forge.failDispatch = true
	f.call(owner, "POST", item+"/approve", approve, 422)
	decode(f.call(agent, "POST", item+"/refresh", nil, 200))
	if p.State != "merged" || p.ReleaseRequested || p.ApprovedBy != owner.ID {
		t.Fatal("uncertain merge did not reconcile")
	}
	forge.failDispatch = false
	decode(f.call(owner, "POST", item+"/approve", approve, 200))
	if p.State != "merged" || !p.ReleaseRequested || forge.mergeCalls != 1 || forge.dispatches != 1 {
		t.Fatal("merge/release retry duplicated work or claimed early release")
	}
	forge.released = true
	decode(f.call(agent, "POST", item+"/refresh", nil, 200))
	if p.State != "released" || p.ReleaseCommit != privateSHA {
		t.Fatalf("release %+v", p)
	}
	pin := map[string]string{"machine_key": strings.Repeat("a", 64), "commit": p.ReleaseCommit}
	f.call(agent, "POST", item+"/pins", pin, 403)
	decode(f.call(owner, "POST", item+"/pins", pin, 200))
	if p.State != "pinned" || p.PinnedMachines != 1 {
		t.Fatal("missing pin")
	}
	decode(f.call(owner, "POST", item+"/pins", pin, 200))
	if p.PinnedMachines != 1 {
		t.Fatal("duplicate machine counted twice")
	}
	// Tenant isolation, references-only SQL and value-free audit trail.
	var count int
	var data string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*),string_agg(data::text,'') FROM doctrine_proposals WHERE tenant_id=$1`, tid).Scan(&count, &data); err != nil || count != 1 {
		t.Fatalf("proposal storage %d %v", count, err)
	}
	if strings.Contains(data, input.Explanation) || strings.Contains(data, input.TLDR.EN) || strings.Contains(data, "installationFixtureToken319") {
		t.Fatal("draft text or credential was retained")
	}
	got := f.call(other, "GET", path, nil, 200)
	if strings.Contains(string(got), p.ID) {
		t.Fatal("proposal crossed tenant")
	}
	var eventCount int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('doctrine.proposed','doctrine.approved','doctrine.merged','doctrine.merge_observed','doctrine.release_requested','doctrine.machine_pin_reported','doctrine.released')`, tid).Scan(&eventCount); err != nil || eventCount < 6 {
		t.Fatalf("audit %d %v", eventCount, err)
	}
	var leaked int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND (coalesce(after::text,'') LIKE '%'||$2||'%' OR coalesce(after::text,'') LIKE '%installationFixtureToken319%')`, tid, input.TLDR.EN).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("event retained prose/credential")
	}
	f.call(owner, "DELETE", "/api/rules/doctrine/sources/"+src.ID, nil, 200)
	if forge.revocations != forge.minted {
		t.Fatal("installation tokens left live after operations")
	}
	if got := f.call(owner, "GET", path, nil, 200); !strings.Contains(string(got), p.ID) {
		t.Fatal("unlink erased proposal history")
	}
}

func TestPublicGuardAndRuleEdit(t *testing.T) {
	for _, text := range []string{"markus@", "a.barta.cm", "~/.inspr/secrets", "hsb1", "csb9", "mbp2606", "agm1", "dsc8", "imac0", "pm.barta", "paimos.agm", "hs.barta", "operator@example.test", "inspr-doctrine-private"} {
		if guardPublic(publicRepository, text) == nil {
			t.Errorf("guard missed fixture %q", text)
		}
		if guardPublic(privateRepository, text) != nil {
			t.Error("private routing rejected identity")
		}
	}
	for _, repo := range []string{publicRepository, privateRepository} {
		for _, text := range []string{"token=abcdefghijklmnop", "-----BEGIN RSA PRIVATE KEY-----", "github_pat_syntheticFixtureOnly"} {
			if guardPublic(repo, text) == nil {
				t.Fatal("credential-shaped text accepted for publication")
			}
		}
	}
	for _, text := range []string{"PPMAPIKEY", "PAIMOS_API_KEY", "Run tests before merging.", "markus-barta"} {
		if guardPublic(publicRepository, text) != nil {
			t.Errorf("public interface rejected %q", text)
		}
	}
	files := []File{}
	for p, c := range fixtureFiles() {
		files = append(files, File{Path: p, Content: []byte(c), BlobSHA: BlobSHA([]byte(c))})
	}
	s := Source{Repository: privateRepository, Visibility: "private", Commit: fixtureCommit}
	var rule RuleView
	for _, v := range Render(s.Repository, s.Commit, true, files) {
		if v.Path == "docs/AGENTS-KERNEL.md" {
			rule = v.Rules[0]
		}
	}
	in := ProposalInput{Path: "docs/AGENTS-KERNEL.md", RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: strings.Replace(rule.Source, "Why:", "Reason:", 1), Explanation: "Private edit"}
	in.TLDR.EN = "A private operator@example.test explanation"
	result, err := editRule(s, files, in)
	if err != nil || !strings.Contains(result[SidecarPath(in.Path)], in.TLDR.EN) {
		t.Fatalf("private edit %v", err)
	}
	if !strings.Contains(result[in.Path], "\r\n") {
		t.Fatal("line endings lost")
	}
	s.Repository = publicRepository
	s.Visibility = "public"
	if _, err = editRule(s, files, in); err == nil {
		t.Fatal("public edit accepted private TLDR")
	}
}

func TestPrivateProposalRoutingAndFreshSource(t *testing.T) {
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("proposal-private")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, privateRepository)
	allowCredential(t, m.credentials.Dir, "private-read", tid, privateRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	// Runtime test credential, never a real App or repository credential.
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, "private-read"), []byte("fixturePrivateRead319"), 0600); err != nil {
		t.Fatal("fixture credential")
	}
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: privateRepository, Visibility: "private", Ref: "main", CredentialRef: "private-read"})
	src := find(layer, privateRepository)
	rule := src.Files[1].Rules[0]
	input := ProposalInput{RequestID: "31900000-0000-4000-8000-000000000002", SourceID: src.ID, Path: src.Files[1].Path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: rule.Source, Explanation: "Private operator@example.test context"}
	input.TLDR.EN = "Private contact operator@example.test"
	var p Proposal
	// Cached private source bytes still require their read-credential grant.
	allowCredential(t, m.credentials.Dir, "private-read", tid, publicRepository)
	beforeCalls := forge.calls
	f.call(owner, "POST", "/api/rules/doctrine/proposals", input, 422)
	if forge.calls != beforeCalls {
		t.Fatal("revoked cached-source grant reached GitHub")
	}
	allowCredential(t, m.credentials.Dir, "private-read", tid, privateRepository)
	forge.wrongVisibility = true
	f.call(owner, "POST", "/api/rules/doctrine/proposals", input, 422)
	forge.wrongVisibility = false
	if forge.writes != 0 {
		t.Fatal("private repository becoming public reached a write")
	}
	raw := f.call(owner, "POST", "/api/rules/doctrine/proposals", input, 200)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Repository != privateRepository || !strings.Contains(p.PRURL, "/inspr-doctrine-private/") {
		t.Fatal("private edit routed to public repository")
	}
	for key := range forge.refs {
		if strings.HasPrefix(key, publicRepository) {
			t.Fatal("private edit wrote public ref")
		}
	}
	// Advance main's touched file while Aeon's pin/cache still names the old
	// bytes; a new proposal must refuse before any write.
	updated := fixtureFiles()
	updated[input.Path] += "\n- A new rule on main.\n"
	forge.reader.commit(privateRepository, fixtureCommit, updated, "main")
	input.RequestID = "31900000-0000-4000-8000-000000000003"
	before := forge.writes
	f.call(owner, "POST", "/api/rules/doctrine/proposals", input, 409)
	if forge.writes != before {
		t.Fatal("stale pinned data overwrote main")
	}
	// Call-time rotation: the next operation signs with the replacement key.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("rotation key")
	}
	if err = os.WriteFile(filepath.Join(m.credentials.Dir, "app-key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal("rotate fixture key")
	}
	forge.key = key
	if _, err = m.appClient(t.Context(), tid, privateRepository); err != nil {
		t.Fatal("App failed after credential rotation")
	}
	m.app.KeyRef = "../outside"
	if _, err = m.appClient(t.Context(), tid, privateRepository); err == nil {
		t.Fatal("escaping credential reference accepted")
	}
}

func TestProposalTransportNeverFollowsRedirectsOrReflectsBodies(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, location := range []string{"http://127.0.0.1/private", "https://example.com/private", "http://child.api.github.com/private", "https://child.api.github.com/private", "https://api.github.com/renamed"} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
				for _, supplied := range []bool{false, true} {
					calls, overrideCalls := 0, 0
					transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if calls != 1 || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
							t.Fatal("App authorization or proposal escaped the fixed GitHub origin")
						}
						if r.Header.Get("Authorization") != "Bearer fixtureOnly319" || r.Method != method {
							t.Fatal("expected authenticated operation at GitHub")
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("fixtureOnly319 private proposal fixture")), Request: r}, nil
					})
					client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { overrideCalls++; return nil }}
					g := &GitHub{Token: "fixtureOnly319"}
					original := http.DefaultTransport
					if supplied {
						g.Client = client
					} else {
						http.DefaultTransport = transport
					}
					err := g.request(t.Context(), method, "/mutation", map[string]string{"text": "private proposal fixture"}, nil)
					http.DefaultTransport = original
					if !errors.Is(err, ErrGit) || calls != 1 || overrideCalls != 0 || strings.Contains(err.Error(), location) || strings.Contains(err.Error(), "fixtureOnly319") || strings.Contains(err.Error(), "private proposal fixture") {
						t.Fatal("redirect followed or response reflected")
					}
					_ = client.CheckRedirect(nil, nil)
					if overrideCalls != 1 {
						t.Fatal("shared redirect policy mutated")
					}
				}
			}
		}
	}
	g := &GitHub{Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("fixtureOnly319 private proposal fixture")), Request: r}, nil
	})}, Token: "fixtureOnly319"}
	if err := g.request(t.Context(), "POST", "/mutation", nil, nil); !errors.Is(err, ErrGit) || strings.Contains(err.Error(), "fixtureOnly319") || strings.Contains(err.Error(), "private proposal fixture") {
		t.Fatal("remote failure was reflected")
	}
}

func TestEditRejectsAmbiguousKeysAndMultipleSidecarDocuments(t *testing.T) {
	s := Source{Repository: publicRepository, Visibility: "public", Commit: fixtureCommit}
	path := "docs/AGENTS-KERNEL.md"
	content := "# Kernel\n\n## First\n<!-- aeon-rule: duplicate -->\n- First rule.\n\n## Second\n<!-- aeon-rule: duplicate -->\n- Second rule.\n"
	files := []File{{Path: path, Content: []byte(content)}}
	rules := Render(s.Repository, s.Commit, false, files)[0].Rules
	if len(rules) != 2 || rules[0].Key != "duplicate" || rules[1].Key != "duplicate" {
		t.Fatal("fixture must index two rules with the same explicit key")
	}
	rule := rules[0]
	in := ProposalInput{Path: path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: rule.Source, Explanation: "Clarify."}
	in.TLDR.EN = "One line."
	if _, err := editRule(s, files, in); err == nil {
		t.Fatal("an ambiguous key would overwrite another rule's TLDR")
	}
	files = []File{{Path: path, Content: []byte(fixtureKernel)}, {Path: SidecarPath(path), Content: []byte(fixtureSidecar + "\n---\nrules: {}\n")}}
	rule = Render(s.Repository, s.Commit, false, files)[0].Rules[0]
	in.RuleKey, in.RuleSHA, in.Source = rule.Key, rule.SHA256, rule.Source
	if _, err := editRule(s, files, in); err == nil {
		t.Fatal("a second YAML document would have been silently discarded")
	}
}
