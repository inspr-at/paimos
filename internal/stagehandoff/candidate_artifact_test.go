// SPDX-License-Identifier: AGPL-3.0-only

package stagehandoff

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func nativeCandidateWorld(t *testing.T) *launchWorld {
	t.Helper()
	w := newLaunchWorld(t)
	w.p.Scopes = []string{"stage_handoffs.write", "stage_handoffs.read", "stage.deploy"}
	candidateSQL(t, w, `UPDATE journey_releases SET version_scheme=NULL,version=NULL WHERE release_node_id=$1::uuid`, w.release)
	return w
}
func candidateSQL(t *testing.T, w *launchWorld, q string, args ...any) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), w.m.pool, w.p.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}
func candidateInput(w *launchWorld) candidateArtifactWrite {
	return candidateArtifactWrite{HandoffID: w.h.ID, ExpectedAttempt: w.h.Attempt, ExpectedAuthorityEpoch: w.h.AuthorityEpoch, ExpectedJourneyRevision: w.h.JourneyRevision, IdempotencyKey: "native-artifact-1", Artifact: w.art, QADigest: emptyDigest}
}
func candidatePath(w *launchWorld) string {
	return "/api/projects/" + w.project + "/releases/" + w.release + "/candidate-artifact"
}
func candidateCall(t *testing.T, w *launchWorld, p tenant.Principal, method, path string, in any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), p))
	out := httptest.NewRecorder()
	(&httpapi.Server{Modules: []httpapi.Module{w.m}}).Handler().ServeHTTP(out, r)
	return out
}
func candidateOK(t *testing.T, w *launchWorld, in candidateArtifactWrite) candidateArtifactView {
	t.Helper()
	res := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), in)
	if res.Code != 200 {
		t.Fatalf("candidate PUT: %d %s", res.Code, res.Body.String())
	}
	var out candidateArtifactView
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func nextCandidateAttempt(t *testing.T, w *launchWorld, key string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), w.m.pool, w.p.TenantID, func(tx pgx.Tx) error {
		h, err := w.m.create(t.Context(), tx, w.p, RequestWrite{ProjectNodeID: w.project, ReleaseNodeID: w.release, Stage: "deploy", Operation: "deploy", ExpectedJourneyRevision: 1, IdempotencyKey: key}, "pharos", []string{"deployment", "launch_readiness"}, "deploy")
		if err == nil {
			w.h = h
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeCandidatePinsVersionAndEnablesExistingLaunch(t *testing.T) {
	w := nativeCandidateWorld(t)
	path := candidatePath(w)
	before := candidateCall(t, w, w.p, http.MethodGet, path, nil)
	if before.Code != 200 {
		t.Fatalf("GET: %d %s", before.Code, before.Body.String())
	}
	var empty candidateArtifactView
	if err := json.Unmarshal(before.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Version != nil || empty.VersionScheme != nil || empty.Registration != nil || empty.ReleaseSequence != 1 {
		t.Fatalf("before: %+v", empty)
	}
	in := candidateInput(w)
	first := candidateOK(t, w, in)
	if first.Version == nil || *first.Version != w.art.Version || first.Registration == nil || first.Registration.Artifact != w.art {
		t.Fatalf("record: %+v", first)
	}
	if got := candidateOK(t, w, in); got.Registration.RecordedAt != first.Registration.RecordedAt {
		t.Fatal("replay changed record")
	}
	after := candidateCall(t, w, w.p, http.MethodGet, path, nil)
	var read candidateArtifactView
	if after.Code != 200 || json.Unmarshal(after.Body.Bytes(), &read) != nil || read.Registration == nil || read.Registration.Artifact != w.art {
		t.Fatalf("read: %d %s", after.Code, after.Body.String())
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.m.pool, w.p.TenantID, func(tx pgx.Tx) error {
		var jr, rr int64
		var records, audits, launches int
		if err := tx.QueryRow(t.Context(), `SELECT j.revision,r.revision FROM journey_projects j JOIN journey_releases r ON r.release_node_id=j.current_release_node_id WHERE j.project_node_id=$1::uuid`, w.project).Scan(&jr, &rr); err != nil {
			return err
		}
		if jr != 1 || rr != 1 {
			t.Fatalf("registration changed revisions: %d/%d", jr, rr)
		}
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM stage_handoff_build_evidence),(SELECT count(*) FROM events WHERE type='stage_handoff.candidate_artifact_registered'),(SELECT count(*) FROM stage_launch_admissions)`).Scan(&records, &audits, &launches); err != nil {
			return err
		}
		if records != 1 || audits != 1 || launches != 0 {
			t.Fatalf("effects: %d records %d audits %d launches", records, audits, launches)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w.postReadinessHTTP(t, w.plan, true, true, true, time.Now(), w.h.AuthorityEpoch)
	admission, err := w.m.AdmitLaunch(t.Context(), w.p, w.bearer, w.h.ID, w.art)
	if err != nil {
		t.Fatal(err)
	}
	if admission.ArtifactDigestSHA256 != w.art.DigestSHA256 {
		t.Fatal("launch changed artifact")
	}
}

func TestNativeCandidateRejectsInvalidArtifactAndIdentity(t *testing.T) {
	w := nativeCandidateWorld(t)
	for _, tc := range []struct {
		name string
		edit func(*candidateArtifactWrite)
		code int
	}{
		{"wrong attempt", func(in *candidateArtifactWrite) { in.ExpectedAttempt++ }, 409},
		{"wrong epoch", func(in *candidateArtifactWrite) { in.ExpectedAuthorityEpoch++ }, 409},
		{"wrong revision", func(in *candidateArtifactWrite) { in.ExpectedJourneyRevision++ }, 409},
		{"wrong sequence", func(in *candidateArtifactWrite) { in.Artifact.ReleaseSequence++ }, 409},
		{"zero sequence", func(in *candidateArtifactWrite) { in.Artifact.ReleaseSequence = 0 }, 400},
		{"unknown scheme", func(in *candidateArtifactWrite) { in.Artifact.VersionScheme = "inferred" }, 400},
		{"invalid calendar", func(in *candidateArtifactWrite) { in.Artifact.Version = "260229000000.0.0" }, 400},
		{"calendar suffix", func(in *candidateArtifactWrite) { in.Artifact.Version += "-rc1" }, 400},
		{"short commit", func(in *candidateArtifactWrite) { in.Artifact.CommitDigest = "abc" }, 400},
		{"uppercase digest", func(in *candidateArtifactWrite) { in.Artifact.DigestSHA256 = strings.ToUpper(emptyDigest) }, 400},
		{"short manifest", func(in *candidateArtifactWrite) { in.Artifact.ManifestDigestSHA256 = "abcd" }, 400},
		{"coordinate credentials", func(in *candidateArtifactWrite) {
			in.Artifact.ManifestCoordinate = "https://user:placeholder@example.test/path"
		}, 400},
		{"short QA", func(in *candidateArtifactWrite) { in.QADigest = "abcd" }, 400},
		{"missing key", func(in *candidateArtifactWrite) { in.IdempotencyKey = "" }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := candidateInput(w)
			tc.edit(&in)
			r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), in)
			if r.Code != tc.code {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	candidateOK(t, w, candidateInput(w)) // failed attempts left no pin or evidence
}

func TestNativeCandidateAuthorization(t *testing.T) {
	w := nativeCandidateWorld(t)
	in := candidateInput(w)
	for _, tc := range []struct {
		name   string
		edit   func(*tenant.Principal)
		method string
	}{
		{"writer lacks scope", func(p *tenant.Principal) { p.Scopes = []string{"stage_handoffs.read"} }, http.MethodPut},
		{"reader lacks scope", func(p *tenant.Principal) { p.Scopes = []string{"stage_handoffs.write"} }, http.MethodGet},
		{"foreign tenant", func(p *tenant.Principal) { p.TenantID = "11111111-1111-4111-8111-111111111111" }, http.MethodPut},
		{"wrong principal kind", func(p *tenant.Principal) { p.Kind = tenant.Person }, http.MethodPut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := w.p
			tc.edit(&p)
			r := candidateCall(t, w, p, tc.method, candidatePath(w), in)
			if r.Code != 403 {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	candidateSQL(t, w, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, w.p.ID)
	if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), in); r.Code != 403 {
		t.Fatalf("inactive: %d %s", r.Code, r.Body.String())
	}
	candidateSQL(t, w, `UPDATE principals SET status='active' WHERE id=$1::uuid`, w.p.ID)
	other, _ := scopedForeignAgent(t, w.m, w.p, w.release, "stage.deploy")
	other.Scopes = []string{"stage_handoffs.write", "stage_handoffs.read"}
	candidateSQL(t, w, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='member' ON CONFLICT DO NOTHING`, w.p.TenantID, other.ID)
	if r := candidateCall(t, w, other, http.MethodPut, candidatePath(w), in); r.Code != 403 || !strings.Contains(r.Body.String(), "owner") {
		t.Fatalf("nonowner: %d %s", r.Code, r.Body.String())
	}
	candidateSQL(t, w, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1::uuid`, w.p.ID)
	if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), in); r.Code != 403 {
		t.Fatalf("viewer: %d %s", r.Code, r.Body.String())
	}
	person := fixturePerson(t, w.m, w.p.TenantID)
	if r := candidateCall(t, w, person, http.MethodPut, candidatePath(w), in); r.Code != 200 {
		t.Fatalf("admin: %d %s", r.Code, r.Body.String())
	}
}

func TestNativeCandidateRejectsClosedAuthority(t *testing.T) {
	for _, tc := range []struct{ name, q string }{
		{"expired", `UPDATE stage_handoffs SET created_at=now()-interval '31 minutes',expires_at=now()-interval '1 second' WHERE id=$1::uuid`},
		{"revoked", `UPDATE stage_handoffs SET state='revoked' WHERE id=$1::uuid`},
		{"terminal", `UPDATE stage_handoffs SET state='succeeded' WHERE id=$1::uuid`},
		{"revision changed", `UPDATE journey_projects SET revision=revision+1 WHERE project_node_id=(SELECT project_node_id FROM stage_handoffs WHERE id=$1::uuid)`},
		{"release closed", `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=(SELECT release_node_id FROM stage_handoffs WHERE id=$1::uuid)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := nativeCandidateWorld(t)
			candidateSQL(t, w, tc.q, w.h.ID)
			r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), candidateInput(w))
			if r.Code != 409 {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	for _, gate := range []string{"candidate", "deploy"} {
		t.Run("expired "+gate+" gate", func(t *testing.T) {
			w := nativeCandidateWorld(t)
			candidateSQL(t, w, `UPDATE agent_permission_grants SET valid_until=now()-interval '1 second' WHERE approval_request_id IN (SELECT approval_request_id FROM journey_gates WHERE gate=$1)`, gate)
			r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), candidateInput(w))
			if r.Code != 403 {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestNativeCandidateReplayAndCrossAttemptIdentity(t *testing.T) {
	w := nativeCandidateWorld(t)
	in := candidateInput(w)
	candidateOK(t, w, in)
	for _, edit := range []func(*candidateArtifactWrite){
		func(v *candidateArtifactWrite) { v.IdempotencyKey = "different-key" }, func(v *candidateArtifactWrite) { v.QADigest = strings.Repeat("a", 64) }, func(v *candidateArtifactWrite) { v.Artifact.DigestSHA256 = strings.Repeat("b", 64) },
	} {
		bad := in
		edit(&bad)
		if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), bad); r.Code != 409 {
			t.Fatalf("divergent: %d %s", r.Code, r.Body.String())
		}
	}
	nextCandidateAttempt(t, w, "second-native-attempt")
	candidateOK(t, w, in) // exact historical replay remains a read of the old receipt
	for _, edit := range []func(*Artifact){
		func(a *Artifact) { a.DigestSHA256 = strings.Repeat("b", 64) }, func(a *Artifact) { a.ManifestDigestSHA256 = strings.Repeat("b", 64) }, func(a *Artifact) { a.CommitDigest = strings.Repeat("b", 40) }, func(a *Artifact) { a.ManifestCoordinate = "lab:other" }, func(a *Artifact) { a.ReleaseChannel = "other" }, func(a *Artifact) { a.Version = "260927000000.0.0" }, func(a *Artifact) { a.VersionScheme = "legacy" },
	} {
		bad := candidateInput(w)
		edit(&bad.Artifact)
		if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), bad); r.Code != 409 {
			t.Fatalf("changed identity: %d %s", r.Code, r.Body.String())
		}
	}
	candidateOK(t, w, candidateInput(w))
}

func TestNativeCandidateSupersededNewWriteAndForeignRelease(t *testing.T) {
	w := nativeCandidateWorld(t)
	old := candidateInput(w)
	nextCandidateAttempt(t, w, "supersede-before-artifact")
	if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), old); r.Code != 409 {
		t.Fatalf("superseded: %d %s", r.Code, r.Body.String())
	}
	path := strings.Replace(candidatePath(w), w.release, w.project, 1)
	if r := candidateCall(t, w, w.p, http.MethodPut, path, candidateInput(w)); r.Code != 404 {
		t.Fatalf("foreign release: %d %s", r.Code, r.Body.String())
	}
	if r := candidateCall(t, w, w.p, http.MethodGet, path, nil); r.Code != 404 {
		t.Fatalf("read foreign release: %d %s", r.Code, r.Body.String())
	}
}

func TestNativeCandidateConcurrentDivergentRegistration(t *testing.T) {
	w := nativeCandidateWorld(t)
	a := candidateInput(w)
	b := a
	b.Artifact.DigestSHA256 = strings.Repeat("b", 64)
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, in := range []candidateArtifactWrite{a, b} {
		wg.Add(1)
		go func(in candidateArtifactWrite) {
			defer wg.Done()
			results <- candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), in).Code
		}(in)
	}
	wg.Wait()
	close(results)
	codes := map[int]int{}
	for code := range results {
		codes[code]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent results: %v", codes)
	}
}

func TestCandidateVersionValidation(t *testing.T) {
	for _, tc := range []struct {
		scheme, version string
		valid           bool
	}{
		{"legacy", "1.0.0", true}, {"inspr-calendar-v2", "280229123456.0.0", true}, {"inspr-calendar-v2", "260229123456.0.0", false}, {"inspr-calendar-v1", "26.09.27", true}, {"inspr-calendar-v1", "26.09.27.12.00.00", true}, {"inspr-calendar-v1", "26.02.29", false}, {"inspr-calendar-v1", "26.09.27.25.00.00", false},
	} {
		if got := validCandidateVersion(tc.scheme, tc.version); got != tc.valid {
			t.Errorf("%s %s: %v", tc.scheme, tc.version, got)
		}
	}
}

func TestNativeCandidateAndCompatibilityShareImmutableIdentity(t *testing.T) {
	for _, nativeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "native first", false: "compatibility first"}[nativeFirst], func(t *testing.T) {
			w := nativeCandidateWorld(t)
			a := w.art
			receipt := classicBuiltReceipt{IdempotencyKey: "classic-artifact-1", ExpectedAttemptID: int64(w.h.Attempt), ExpectedPlanRevision: w.h.JourneyRevision, Commit: a.CommitDigest, OCIConfigDigest: a.DigestSHA256, ReleaseManifestDigest: a.ManifestDigestSHA256, ReleaseManifestCoordinate: a.ManifestCoordinate, VersionScheme: a.VersionScheme, ReleaseChannel: a.ReleaseChannel, ReleaseSequence: a.ReleaseSequence, Version: a.Version, QADigest: emptyDigest}
			alias := func() {
				t.Helper()
				r := candidateCall(t, w, w.p, http.MethodPost, "/api/stage-handoffs/"+w.h.ID+"/classic-batch-alias", map[string]any{"classic_batch_id": 42, "classic_project_id": 17, "classic_batch": map[string]int{"id": 42, "project_id": 17}})
				if r.Code != 201 {
					t.Fatalf("alias %d %s", r.Code, r.Body.String())
				}
			}
			path := "/api/projects/" + w.project + "/baseline-batches/batches/42/built-receipt"
			if nativeFirst {
				candidateOK(t, w, candidateInput(w))
				nextCandidateAttempt(t, w, "compatibility-after-native")
				alias()
				receipt.ExpectedAttemptID = int64(w.h.Attempt)
				bad := receipt
				bad.OCIConfigDigest = strings.Repeat("b", 64)
				if r := candidateCall(t, w, w.p, http.MethodPost, path, bad); r.Code != 409 {
					t.Fatalf("classic divergence %d %s", r.Code, r.Body.String())
				}
				if r := candidateCall(t, w, w.p, http.MethodPost, path, receipt); r.Code != 200 {
					t.Fatalf("classic match %d %s", r.Code, r.Body.String())
				}
			} else {
				alias()
				if r := candidateCall(t, w, w.p, http.MethodPost, path, receipt); r.Code != 200 {
					t.Fatalf("classic first %d %s", r.Code, r.Body.String())
				}
				nextCandidateAttempt(t, w, "native-after-compatibility")
				bad := candidateInput(w)
				bad.Artifact.CommitDigest = strings.Repeat("b", 40)
				if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), bad); r.Code != 409 {
					t.Fatalf("native divergence %d %s", r.Code, r.Body.String())
				}
				candidateOK(t, w, candidateInput(w))
			}
		})
	}
}

func TestNativeCandidateCannotReplaceExistingVersionPin(t *testing.T) {
	w := nativeCandidateWorld(t)
	candidateSQL(t, w, `UPDATE journey_releases SET version_scheme='legacy',version='1.0.0' WHERE release_node_id=$1::uuid`, w.release)
	if r := candidateCall(t, w, w.p, http.MethodPut, candidatePath(w), candidateInput(w)); r.Code != 409 {
		t.Fatalf("existing pin: %d %s", r.Code, r.Body.String())
	}
}

func TestNativeCandidateProjectRoleBoundary(t *testing.T) {
	w := nativeCandidateWorld(t)
	candidateSQL(t, w, `UPDATE role_bindings SET scope_type='project',scope_id=$2::uuid WHERE principal_id=$1::uuid`, w.p.ID, w.project)
	foreignPath := strings.Replace(candidatePath(w), w.project, "11111111-1111-4111-8111-111111111111", 1)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if r := candidateCall(t, w, w.p, method, foreignPath, candidateInput(w)); r.Code != 403 {
			t.Fatalf("foreign project %s: %d %s", method, r.Code, r.Body.String())
		}
	}
	candidateOK(t, w, candidateInput(w))
}

func TestNativeCandidateTerminalReplayPreservesReceipt(t *testing.T) {
	w := nativeCandidateWorld(t)
	in := candidateInput(w)
	first := candidateOK(t, w, in)
	candidateSQL(t, w, `UPDATE stage_handoffs SET state='succeeded' WHERE id=$1::uuid`, w.h.ID)
	candidateSQL(t, w, `UPDATE agent_permission_grants SET valid_until=now()-interval '1 second'`)
	if got := candidateOK(t, w, in); got.Registration.RecordedAt != first.Registration.RecordedAt || got.Registration.Artifact != first.Registration.Artifact {
		t.Fatal("terminal replay changed immutable receipt")
	}
}
