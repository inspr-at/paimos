// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/releasehistory/codename"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestProspectiveSequenceReuse(t *testing.T) {
	h := History{Releases: []Release{
		{Version: "261001130110.0.0", ReleaseChannel: "stable", ReleaseSequence: 115, State: StatePublished},
		{Version: "261001205522.0.0", ReleaseChannel: "stable", ReleaseSequence: 116, State: StateWithdrawn},
		{Version: "261001230453.0.0", ReleaseChannel: "stable", ReleaseSequence: 117, State: StateReserved},
		{Version: "261002004358.0.0", ReleaseChannel: "stable", ReleaseSequence: 118, State: StatePublished},
		{Version: "261002010000.0.0", ReleaseChannel: "preview", ReleaseSequence: 200, State: StatePublished},
	}}
	for _, state := range []string{StateReserved, StateWithdrawn} {
		h.Releases = append(h.Releases, Release{Version: "261002020000.0.0", ReleaseChannel: "stable", ReleaseSequence: 119, State: state})
		seq, err := NextSequence(h, "stable")
		if err != nil || seq != 119 || !strings.HasPrefix(codename.Codename(seq), "S") {
			t.Fatalf("failed %s attempt consumes next name: %d %v", state, seq, err)
		}
	}
	h.Releases = append(h.Releases, Release{Version: "261002030000.0.0", ReleaseChannel: "stable", ReleaseSequence: 119, State: StatePublished})
	if seq, err := NextSequence(h, "stable"); err != nil || seq != 120 {
		t.Fatalf("successful publication must advance: %d %v", seq, err)
	}
	h.Releases = append(h.Releases, Release{Version: "261002040000.0.0", ReleaseChannel: "stable", ReleaseSequence: 119, State: StatePublished})
	if _, err := NextSequence(h, "stable"); err == nil {
		t.Fatal("duplicate published sequence accepted")
	}
}

func TestReserveFileReusesOnlyClassifiedFailure(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	first := "261002120000.0.0"
	name, err := ReserveFile(ctx, dir, first, "AEON-530")
	if err != nil || name != codename.Codename(3) {
		t.Fatalf("first reserve: %s %v", name, err)
	}
	path := filepath.Join(dir, "version.json")
	before, _ := os.ReadFile(path)
	if _, err := ReserveFile(ctx, dir, "261002120001.0.0", "AEON-530"); err == nil {
		t.Fatal("unclassified pending attempt accepted as a failure")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed reserve changed version.json")
	}
	var fields map[string]any
	json.Unmarshal(before, &fields)
	fields["withdrawn_releases"] = []Withdrawal{{Version: first, Digest: "sha256:" + strings.Repeat("a", 64), Ticket: "AEON-530", Reason: "failed index completion"}}
	fields["unrelated"] = "preserved"
	raw, _ := json.Marshal(fields)
	os.WriteFile(path, raw, 0644)
	if retry, err := ReserveFile(ctx, dir, "261002120001.0.0", "AEON-530"); err != nil || retry != name {
		t.Fatalf("withdrawn retry: %s %v", retry, err)
	}
	raw, _ = os.ReadFile(path)
	json.Unmarshal(raw, &fields)
	if fields["release_sequence"] != float64(3) || fields["unrelated"] != "preserved" || fields["withdrawn_releases"] == nil {
		t.Fatal("retry changed sequence, metadata or withdrawal ledger")
	}
	if _, err := ReserveFile(ctx, dir, first, "AEON-530"); err == nil {
		t.Fatal("failed coordinate reused")
	}
	h, err := Build(ctx, Options{Repo: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range h.Releases {
		if r.Version == first && (r.State != StateWithdrawn || r.ReleaseSequence != 0 || r.Codename != "" || HasSnapshot(r)) {
			t.Fatalf("withdrawn diagnostics own a public identity: %+v", r)
		}
	}
}

func TestWithdrawnTagKeepsEvidenceNotNotes(t *testing.T) {
	dir := repo(t)
	path := filepath.Join(dir, "version.json")
	raw, _ := os.ReadFile(path)
	var v map[string]any
	json.Unmarshal(raw, &v)
	failed := "260923143005.0.0"
	v["withdrawn_releases"] = []Withdrawal{{Version: failed, Digest: "sha256:" + strings.Repeat("b", 64), Ticket: "AEON-530", Reason: "failed before release"}}
	raw, _ = json.Marshal(v)
	os.WriteFile(path, raw, 0644)
	h, err := Build(context.Background(), Options{Repo: dir})
	if err != nil {
		t.Fatal(err)
	}
	r := h.Releases[0]
	if r.Version != failed || r.State != StateWithdrawn || r.ReleaseSequence != 0 || r.Codename != "" || HasSnapshot(r) || r.Evidence.SourceCommit == "" || r.Evidence.Image == nil {
		t.Fatalf("withdrawn tag: %+v", r)
	}
	if seq, err := NextSequence(h, "stable"); err != nil || seq != 2 {
		t.Fatalf("withdrawn slot not returned: %d %v", seq, err)
	}
}

func TestPublicReleaseHTTPKeepsReservationsAndExcludesWithdrawals(t *testing.T) {
	h := History{Schema: Schema, Product: "PAIMOS AEON", Releases: []Release{
		{Version: "261002030000.0.0", ReleaseSequence: 119, State: StatePublished},
		{Version: "261002020000.0.0", ReleaseSequence: 119, State: StateReserved},
		{Version: "261002010000.0.0", ReleaseSequence: 119, State: StateWithdrawn},
		{Version: "261002000000.0.0", ReleaseSequence: 120, State: StateReserved},
	}}
	mux := http.NewServeMux()
	NewWith(h, h.Releases[0].Version).Mount(mux)
	for _, kind := range []tenant.PrincipalKind{tenant.Person, tenant.Agent} {
		for _, path := range []string{"/api/releases", "/api/releases/261002030000.0.0", "/api/releases/261002020000.0.0", "/api/releases/261002010000.0.0", "/api/releases/261002000000.0.0"} {
			req := httptest.NewRequest("GET", path, nil)
			req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "p1", TenantID: "t1", Kind: kind}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if path == "/api/releases" {
				var body Response
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Releases) != 3 || body.Releases[0].State != StatePublished || body.Releases[1].State != StateReserved || body.Releases[2].State != StateReserved {
					t.Fatalf("public list: %d %s", w.Code, w.Body)
				}
				if body.Releases[1].Codename != "" || body.Releases[2].Codename != codename.Codename(120) {
					t.Fatalf("reservation names: %+v", body.Releases)
				}
			} else if path == "/api/releases/261002010000.0.0" {
				if w.Code != 404 {
					t.Fatalf("withdrawn detail remains public: %d", w.Code)
				}
			} else {
				var body Release
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Version != strings.TrimPrefix(path, "/api/releases/") {
					t.Fatalf("published/reserved detail: %d %s", w.Code, w.Body)
				}
			}
		}
	}
	if len(h.Releases) != 4 || h.Releases[2].State != StateWithdrawn || CodenameOf(h, h.Releases[2].Version) != "" {
		t.Fatal("public projection changed diagnostic history or named a withdrawal")
	}
}
