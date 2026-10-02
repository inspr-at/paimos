// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory/codename"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestClassifyTicketsAndTagMessages(t *testing.T) {
	for subject, want := range map[string][2]string{
		"feat(AEON-74): wide lists":           {"feat", "AEON-74"},
		"fix(PAI-1057): retry a busy BEGIN":   {"fix", "PAI-1057"},
		"test: cover the walker":              {"test", ""},
		"docs(AEON-56): rule 11":              {"docs", "AEON-56"},
		"release: v260924090430.0.0":          {"release", ""},
		"B10: repair journey progression":     {"other", ""},
		"Merge branch 'main' into u10":        {"other", ""},
		"chore!: drop legacy view":            {"chore", ""},
		"feature(AEON-1): old spelling works": {"feat", "AEON-1"},
	} {
		kind, scope := Classify(subject)
		if kind != want[0] || scope != want[1] {
			t.Errorf("%q: got %s(%s) want %s(%s)", subject, kind, scope, want[0], want[1])
		}
	}
	if got := Tickets("AEON-70 Business (AEON-70), attachments backend (AEON-72)", "PAI-1054 not-a-key-0 X-1"); strings.Join(got, ",") != "AEON-70,AEON-72,PAI-1054" {
		t.Fatalf("tickets %v", got)
	}
	headline, channel, seq := ParseTagMessage("PAIMOS AEON v260924080611.0.0 · inspr-calendar-v2 · stable · release_sequence 17 · Business hours/rates (AEON-70) · attachments\n\nbody")
	if headline != "Business hours/rates (AEON-70) · attachments" || channel != "stable" || seq != 17 {
		t.Fatalf("tag message: %q %q %d", headline, channel, seq)
	}
	if headline, _, seq := ParseTagMessage("just a note"); headline != "just a note" || seq != 0 {
		t.Fatalf("plain tag message: %q %d", headline, seq)
	}
	if !ValidVersion("260924090430.0.0") || ValidVersion("260231090430.0.0") || ValidVersion("v260924090430.0.0") || ValidVersion("2609240904.0.0") {
		t.Fatal("version validation")
	}
}

// repo builds a small git history: a first release, a reserved-and-tagged
// version, a second release, a reservation that was never tagged, and a
// lightweight tag that is not a release.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), append([]string{"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	version := func(v string, seq int, ticket string, reserved ...string) string {
		list, _ := json.Marshal(reserved)
		if reserved == nil {
			list = []byte("[]")
		}
		at, _ := time.Parse("060102150405", strings.TrimSuffix(v, ".0.0"))
		return `{"product":"PAIMOS AEON","version_scheme":"inspr-calendar-v2","version":"` + v + `","release_channel":"stable","release_sequence":` + string(rune('0'+seq)) + `,"reserved_at":"` + at.UTC().Format(time.RFC3339) + `","ticket":"` + ticket + `","unpublished_reservations":` + string(list) + `}`
	}
	commit := func(date, subject string) {
		run([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "--allow-empty", "-qm", subject)
	}
	tag := func(date, v, headline string, seq int) {
		msg := "PAIMOS AEON v" + v + " · inspr-calendar-v2 · stable · release_sequence " + string(rune('0'+seq)) + " · " + headline
		run([]string{"GIT_COMMITTER_DATE=" + date}, "tag", "-a", "v"+v, "-m", msg)
	}
	run(nil, "init", "-q", "-b", "main")
	write("version.json", version("260923134337.0.0", 1, "AEON-13"))
	run(nil, "add", "version.json")
	commit("2026-09-23T13:40:00Z", "feat(AEON-13): live shell")
	tag("2026-09-23T13:43:37Z", "260923134337.0.0", "R0 live shell (AEON-13)", 1)
	write("version.json", version("260923134631.0.0", 1, "AEON-13", "260923134337.0.0"))
	run(nil, "add", "version.json")
	commit("2026-09-23T13:46:00Z", "release: v260923134631.0.0")
	tag("2026-09-23T13:46:31Z", "260923134631.0.0", "R0 live shell (AEON-13)", 1)
	commit("2026-09-23T14:00:00Z", "fix(AEON-15): header mark")
	commit("2026-09-23T14:01:00Z", "test: cover the header")
	commit("2026-09-23T14:02:00Z", "B1: tracker core AEON-15")
	write("version.json", version("260923143005.0.0", 2, "AEON-15", "260923134337.0.0", "260923140000.0.0"))
	run(nil, "add", "version.json")
	commit("2026-09-23T14:30:00Z", "release: v260923143005.0.0")
	tag("2026-09-23T14:30:05Z", "260923143005.0.0", "R1 tracker core (AEON-15)", 2)
	run(nil, "tag", "v260923150000.0.0") // lightweight: not a release
	return dir
}

func TestBuildFromGit(t *testing.T) {
	dir := repo(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	h, err := Build(context.Background(), Options{Repo: dir, Repository: "inspr-at/aeon", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if h.Schema != Schema || h.Product != "PAIMOS AEON" || h.Source != "git" || !h.GeneratedAt.Equal(now) {
		t.Fatalf("history head %+v", h)
	}
	versions := []string{}
	for _, r := range h.Releases {
		versions = append(versions, r.Version+":"+r.State)
	}
	if strings.Join(versions, " ") != "260923143005.0.0:published 260923140000.0.0:reserved 260923134631.0.0:published 260923134337.0.0:reserved" {
		t.Fatalf("releases %v", versions)
	}
	latest := h.Releases[0]
	if latest.Headline != "R1 tracker core (AEON-15)" || latest.ReleaseSequence != 2 || latest.ReleaseChannel != "stable" || strings.Join(latest.Tickets, ",") != "AEON-15" {
		t.Fatalf("latest %+v", latest)
	}
	if latest.ReservedAt == nil || latest.ReservedAt.Format(time.RFC3339) != "2026-09-23T14:30:05Z" || latest.TaggedAt == nil || latest.PublishedAt != nil {
		t.Fatalf("latest times %+v %+v %+v", latest.ReservedAt, latest.TaggedAt, latest.PublishedAt)
	}
	// Changes since the previous published release, merges and older commits left out.
	subjects := []string{}
	for _, c := range latest.Changes {
		subjects = append(subjects, c.Type+"|"+c.Subject+"|"+strings.Join(c.Tickets, ","))
	}
	if strings.Join(subjects, "\n") != "release|release: v260923143005.0.0|\nother|B1: tracker core AEON-15|AEON-15\ntest|test: cover the header|\nfix|fix(AEON-15): header mark|AEON-15" {
		t.Fatalf("changes\n%s", strings.Join(subjects, "\n"))
	}
	if latest.Evidence.SourceCommit == "" || !strings.HasSuffix(latest.Evidence.SourceURL, latest.Evidence.SourceCommit) || latest.Evidence.Image != nil || len(latest.Evidence.Unavailable) == 0 {
		t.Fatalf("evidence %+v", latest.Evidence)
	}
	// The first published release lists what came before it; the reservation keeps its own.
	if len(h.Releases[2].Changes) != 2 || len(h.Releases[3].Changes) != 1 {
		t.Fatalf("first release changes %d, reservation %d", len(h.Releases[2].Changes), len(h.Releases[3].Changes))
	}
	// A reservation that was never tagged: time from its coordinate, and it says so.
	never := h.Releases[1]
	if never.Tag != "" || never.ReservedAt == nil || never.ReservedAt.Format(time.RFC3339) != "2026-09-23T14:00:00Z" || len(never.Evidence.Unavailable) != 1 {
		t.Fatalf("untagged reservation %+v", never)
	}
	// Codenames come from the sequence (AEON-430). The published release 1 owns
	// its name; the reservation it replaced and the untagged one have none.
	names := []string{}
	for _, r := range h.Releases {
		names = append(names, r.Codename)
	}
	if strings.Join(names, "|") != "Blue Bot||Avid Axle|" {
		t.Fatalf("codenames %q", names)
	}
}

func TestBuildLightweightReleaseTags(t *testing.T) {
	const version = "260923150000.0.0"
	for _, tc := range []struct {
		name, metadata string
		reservedAtHead bool
		published      bool
	}{
		{name: "matching", metadata: `{"version":"260923150000.0.0","release_channel":"preview","release_sequence":42}`, published: true},
		{name: "mismatching", metadata: `{"version":"260923143005.0.0","release_channel":"stable","release_sequence":2}`},
		{name: "missing"},
		{name: "malformed", metadata: `{`},
		{name: "reserved at tag", metadata: `{"version":"260923150000.0.0","unpublished_reservations":["v260923150000.0.0"]}`},
		{name: "reserved at head", metadata: `{"version":"260923150000.0.0"}`, reservedAtHead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
					"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com",
					"GIT_AUTHOR_DATE=2026-09-23T14:00:00Z", "GIT_COMMITTER_DATE=2026-09-23T17:01:02+02:00")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(metadata string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(metadata), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			run("init", "-q", "-b", "main")
			if tc.metadata != "" {
				write(tc.metadata)
				run("add", "version.json")
			}
			// A commit message is not release metadata, even if it looks like an
			// annotated tag. Neither its sequence nor the author's date may win.
			run("commit", "--allow-empty", "-qm", "PAIMOS AEON v"+version+" · inspr-calendar-v2 · stable · release_sequence 99 · AEON-367")
			commit := run("rev-parse", "HEAD")
			run("tag", "v"+version)
			head := versionFile{Product: "PAIMOS AEON", Version: version, VersionScheme: SchemeCalVer2}
			if tc.reservedAtHead {
				head.UnpublishedReservations = []string{version}
			}
			raw, _ := json.Marshal(head)
			write(string(raw))
			h, err := Build(context.Background(), Options{Repo: dir})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.published {
				for _, release := range h.Releases {
					if release.State == StatePublished || release.Tag != "" {
						t.Fatalf("accepted unverified lightweight tag: %+v", release)
					}
				}
				return
			}
			if len(h.Releases) != 1 {
				t.Fatalf("releases: %+v", h.Releases)
			}
			r := h.Releases[0]
			if r.Version != version || r.State != StatePublished || r.ReleaseChannel != "preview" || r.ReleaseSequence != 42 || r.Evidence.SourceCommit != commit {
				t.Fatalf("lightweight release: %+v", r)
			}
			if r.TaggedAt == nil || r.TaggedAt.Format(time.RFC3339) != "2026-09-23T15:01:02Z" || len(r.Changes) != 1 || strings.Join(r.Changes[0].Tickets, ",") != "AEON-367" {
				t.Fatalf("lightweight release time or changes: %+v", r)
			}
		})
	}
}

func TestBuildWithGitHubEvidence(t *testing.T) {
	dir := repo(t)
	calls := 0
	var sawToken bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		sawToken = sawToken || r.Header.Get("Authorization") == "Bearer test-token"
		switch {
		case r.URL.Path == "/repos/inspr-at/aeon/releases":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v260923143005.0.0", "html_url": "https://github.com/inspr-at/paimos/releases/tag/v260923143005.0.0", "published_at": "2026-09-23T14:36:00Z", "draft": false,
					"body": "aeon-agentd …\n\nContainer: ghcr.io/inspr-at/aeon:260923143005.0.0\nDigest: sha256:" + strings.Repeat("ab", 32)},
				{"tag_name": "v260923134631.0.0", "html_url": "https://example/r2", "published_at": "2026-09-23T13:52:00Z", "draft": false, "body": "no digest here"},
			})
		case r.URL.Path == "/repos/inspr-at/aeon/actions/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{
				{"name": "Release", "html_url": "https://example/release-run", "status": "completed", "conclusion": "success", "head_branch": "v260923143005.0.0"},
				{"name": "CI", "html_url": "https://example/ci-run", "status": "completed", "conclusion": "success", "head_branch": "main"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	h, err := Build(context.Background(), Options{Repo: dir, Repository: "inspr-at/aeon", GitHub: &GitHub{API: api.URL, Token: "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if h.Source != "git+github" || !sawToken || calls == 0 {
		t.Fatalf("source %s token %v calls %d", h.Source, sawToken, calls)
	}
	latest := h.Releases[0]
	if latest.PublishedAt == nil || latest.PublishedAt.Format(time.RFC3339) != "2026-09-23T14:36:00Z" || latest.Evidence.Image == nil ||
		latest.Evidence.Image.Reference != "ghcr.io/inspr-at/aeon:260923143005.0.0" || latest.Evidence.Image.Digest != "sha256:"+strings.Repeat("ab", 32) ||
		latest.Evidence.CI == nil || latest.Evidence.CI.URL != "https://example/ci-run" || latest.Evidence.ReleaseRun == nil || len(latest.Evidence.Unavailable) != 0 {
		t.Fatalf("latest evidence %+v", latest.Evidence)
	}
	earlier := h.Releases[2]
	if earlier.Evidence.Image != nil || !strings.Contains(strings.Join(earlier.Evidence.Unavailable, " "), "does not record an image digest") {
		t.Fatalf("earlier evidence %+v", earlier.Evidence)
	}
	reserved := h.Releases[3]
	if !strings.Contains(strings.Join(reserved.Evidence.Unavailable, " "), "No GitHub release exists") {
		t.Fatalf("reserved evidence %+v", reserved.Evidence)
	}
	// GitHub down: the history is still built, and every release says why evidence is missing.
	h, err = Build(context.Background(), Options{Repo: dir, Repository: "inspr-at/aeon", GitHub: &GitHub{API: "http://127.0.0.1:1"}})
	if err != nil || h.Source != "git" || !strings.Contains(strings.Join(h.Releases[0].Evidence.Unavailable, " "), "GitHub could not be read") {
		t.Fatalf("offline GitHub: %v %s %+v", err, h.Source, h.Releases[0].Evidence.Unavailable)
	}
}

func TestHTTP(t *testing.T) {
	h := History{Schema: Schema, Product: "PAIMOS AEON", Releases: []Release{{Version: "260923143005.0.0", Tag: "v260923143005.0.0", ReleaseSequence: 2, State: StatePublished, Tickets: []string{}, Changes: []Change{}}}}
	mux := http.NewServeMux()
	NewWith(h, "260923143005.0.0").Mount(mux)
	get := func(path string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if signedIn {
			req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "p1", TenantID: "t1", Kind: tenant.Person}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := get("/api/releases", false); w.Code != 401 {
		t.Fatalf("anonymous list %d", w.Code)
	}
	w := get("/api/releases", true)
	var body Response
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Current != "260923143005.0.0" || body.LiveSince.IsZero() || len(body.Releases) != 1 || body.Schema != Schema {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	for path, code := range map[string]int{"/api/releases/v260923143005.0.0": 200, "/api/releases/260923143005.0.0": 200, "/api/releases/260923143006.0.0": 404, "/api/releases/nope": 400} {
		if w := get(path, true); w.Code != code {
			t.Fatalf("%s: %d want %d", path, w.Code, code)
		}
	}
	if w := get("/api/releases/260923143005.0.0", false); w.Code != 401 {
		t.Fatalf("anonymous one %d", w.Code)
	}
	// Every served release carries its codename, also from a manifest without one.
	var named struct {
		Releases []map[string]any `json:"releases"`
	}
	if json.Unmarshal(get("/api/releases", true).Body.Bytes(), &named) != nil || named.Releases[0]["codename"] != "Blue Bot" {
		t.Fatalf("list codename %+v", named.Releases)
	}
	var one map[string]any
	if json.Unmarshal(get("/api/releases/260923143005.0.0", true).Body.Bytes(), &one) != nil || one["codename"] != "Blue Bot" || one["release_sequence"] != float64(2) {
		t.Fatalf("one codename %+v", one)
	}
	if h.Releases[0].Codename != "" {
		t.Fatal("serving must not change the module's history in place")
	}
	// The committed empty manifest loads.
	if e, err := Embedded(); err != nil || e.Schema != Schema || e.Releases == nil {
		t.Fatalf("embedded %v %+v", err, e)
	}
}

func TestWithCodenames(t *testing.T) {
	h := History{Releases: []Release{
		{Version: "260930074921.0.0", ReleaseSequence: 111, State: StatePublished, Codename: "Stale Name"},
		{Version: "260925231350.0.0", ReleaseSequence: 54, State: StateReserved},
		{Version: "260923134631.0.0", ReleaseSequence: 1, State: StatePublished},
		{Version: "260923134337.0.0", ReleaseSequence: 1, State: StateReserved},
		{Version: "260923140000.0.0", State: StateReserved},
	}}
	got := WithCodenames(h)
	names := []string{}
	for _, r := range got.Releases {
		names = append(names, r.Codename)
	}
	// Only published releases own public names.
	if strings.Join(names, "|") != "Fresh Flyby||Avid Axle||" {
		t.Fatalf("codenames %q", names)
	}
	if h.Releases[0].Codename != "Stale Name" || h.Releases[2].Codename != "" {
		t.Fatal("WithCodenames must copy the releases")
	}
}

// CodenameOf names a version by its sequence for /api/version and the portal
// (AEON-430): the published names, nothing for an unknown version or sequence.
func TestCodenameOf(t *testing.T) {
	h := History{Releases: []Release{
		{Version: "260930074921.0.0", ReleaseSequence: 111, State: StatePublished},
		{Version: "260930094206.0.0", ReleaseSequence: 112, State: StatePublished},
		{Version: "260930115354.0.0", ReleaseSequence: 113, State: StatePublished},
		{Version: "260923140000.0.0", State: StateReserved},
	}}
	for version, want := range map[string]string{
		"260930074921.0.0": "Fresh Flyby",
		"260930094206.0.0": "Glossy Glint",
		"260930115354.0.0": "Hinged Hangar",
		"260923140000.0.0": "", // no sequence, no name
		"260101000000.0.0": "", // not in this history
		"dev":              "",
	} {
		if got := CodenameOf(h, version); got != want {
			t.Errorf("CodenameOf(%q) = %q, want %q", version, got, want)
		}
	}
	if got := NewWith(h, "260930115354.0.0").CodenameOf("260930115354.0.0"); got != "Hinged Hangar" {
		t.Errorf("module CodenameOf = %q", got)
	}
}

// Build refuses a history whose version.json recorded a codename the lists no
// longer give its sequence, so `just release-history` fails before a rename.
func TestBuildRefusesRenamedCodename(t *testing.T) {
	dir := repo(t)
	path := filepath.Join(dir, "version.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(name string) {
		t.Helper()
		out := strings.Replace(string(raw), `"release_sequence":2,`, `"release_sequence":2,"codename":"`+name+`",`, 1)
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamp(codename.Codename(2))
	if _, err := Build(context.Background(), Options{Repo: dir}); err != nil {
		t.Fatalf("recorded name: %v", err)
	}
	stamp("Renamed Release")
	if _, err := Build(context.Background(), Options{Repo: dir}); err == nil || !strings.Contains(err.Error(), "Renamed Release") {
		t.Fatalf("renamed codename: err %v", err)
	}
}

// The lists this binary names releases with keep every name already shown:
// the embedded manifest's and the reservation's in version.json (AEON-430).
func TestRepositoryCodenames(t *testing.T) {
	h, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var named []codename.Named
	for _, r := range h.Releases {
		named = append(named, codename.Named{Sequence: r.ReleaseSequence, Name: r.Codename, Reusable: r.State != StatePublished})
	}
	if raw, err := os.ReadFile("../../version.json"); err == nil {
		var v versionFile
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		named = append(named, codename.Named{Sequence: v.ReleaseSequence, Name: v.Codename})
	}
	if err := codename.Guard(named); err != nil {
		t.Fatal(err)
	}
}
