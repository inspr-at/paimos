// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

const candidateVersion = "260924120000.0.0"

func candidateGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE=2026-09-24T12:01:00Z", "GIT_COMMITTER_DATE=2026-09-24T12:01:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func candidateWrite(t *testing.T, dir, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func candidateRepo(t *testing.T) (string, Options) {
	t.Helper()
	dir := repo(t)
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	head := versionFile{Product: "PAIMOS AEON", VersionScheme: SchemeCalVer2, Version: candidateVersion,
		ReleaseChannel: "stable", ReleaseSequence: 3, Codename: "Cyan Cell", ReservedAt: at.Format(time.RFC3339),
		Ticket: "AEON-426", UnpublishedReservations: []string{"260923134337.0.0", "260923140000.0.0"}}
	candidateWrite(t, dir, "version.json", head)
	bundle := EmptyProductNotes()
	bundle.Releases[candidateVersion] = PublicNotes{ReleaseChannel: head.ReleaseChannel, ReleaseSequence: head.ReleaseSequence,
		SHA256: strings.Repeat("a", 64), CapturedAt: &at, Revision: 1, Items: []TicketNote{
			{Key: "AEON-427", Group: GroupFeatures, PillEN: "Frozen feature", BenefitEN: "Captured feature benefit."},
			{Key: "AEON-428", Group: GroupFixes, PillEN: "Frozen fix", BenefitEN: "Captured fix benefit."},
		}}
	candidateWrite(t, dir, ProductNotesPath, bundle)
	candidateGit(t, dir, "add", "version.json", ProductNotesPath)
	candidateGit(t, dir, "commit", "-qm", "feat(AEON-427): candidate feature and fix AEON-428")
	return dir, Options{Repo: dir, Repository: "inspr-at/paimos", Candidate: candidateVersion, Now: func() time.Time { return at }}
}

func TestCandidateHistoryAndHTTP(t *testing.T) {
	dir, opts := candidateRepo(t)
	opts.CandidateCI = &Run{Name: "CI", URL: "https://github.com/inspr-at/paimos/actions/runs/42", Status: "in_progress"}
	// Candidate identity and text must not follow later working-tree edits.
	candidateWrite(t, dir, "version.json", versionFile{Version: "260925120000.0.0"})
	candidateWrite(t, dir, ProductNotesPath, EmptyProductNotes())
	h, err := Build(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	r := h.Releases[0]
	if r.Version != candidateVersion || r.State != StateCandidate || r.Codename != "Cyan Cell" || r.ReleaseSequence != 3 || r.ReleaseChannel != "stable" || r.Evidence.SourceCommit != candidateGit(t, dir, "rev-parse", "HEAD") || len(r.Changes) != 1 {
		t.Fatalf("candidate: %+v", r)
	}
	if r.Tag != "" || r.Headline != "" || r.TaggedAt != nil || r.PublishedAt != nil || r.Evidence.Image != nil || r.Evidence.ReleaseRun != nil || r.Evidence.ReleaseURL != "" || len(r.Evidence.Pending) != 6 || r.Evidence.CI.Conclusion != "" {
		t.Fatalf("invented pending evidence: %+v", r)
	}
	if !reflect.DeepEqual(r.Evidence.CI, opts.CandidateCI) || r.Notes.Source != ProductNotesSource || len(r.Notes.PublicItems) != 2 {
		t.Fatalf("candidate capture: %+v", r)
	}
	mux := http.NewServeMux()
	NewWith(h, candidateVersion).Mount(mux)
	for _, path := range []string{"/api/releases", "/api/releases/" + candidateVersion} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "p1", TenantID: "t1", Kind: tenant.Person}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var served Release
		if path == "/api/releases" {
			var response Response
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Current != candidateVersion {
				t.Fatalf("list: %v %s", err, w.Body)
			}
			served = response.Releases[0]
		} else if err := json.Unmarshal(w.Body.Bytes(), &served); err != nil {
			t.Fatal(err)
		}
		if served.Notes.PublicItems[0].Group != GroupFeatures || served.Notes.PublicItems[1].Group != GroupFixes || served.Notes.PublicItems[0].PillEN != "Frozen feature" || served.Notes.PublicItems[1].PillEN != "Frozen fix" {
			t.Fatalf("served features/fixes: %+v", served.Notes)
		}
	}
}

func TestCandidatePublishedHistoryGolden(t *testing.T) {
	_, opts := candidateRepo(t)
	baseline := opts
	baseline.Candidate = ""
	before, err := Build(t.Context(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/candidate-published-history.json")
	if err != nil {
		t.Fatal(err)
	}
	check := func(releases []Release) {
		t.Helper()
		raw, err := json.MarshalIndent(releases, "", "  ")
		if err != nil || !bytes.Equal(bytes.TrimSpace(golden), raw) {
			t.Fatalf("published history differs from golden: %v\n%s", err, raw)
		}
	}
	check(before.Releases)
	after, err := Build(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	check(after.Releases[1:])
}

func TestCandidateLaterTaggedBuild(t *testing.T) {
	dir, opts := candidateRepo(t)
	first, err := Build(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Releases[0].Evidence.CI != nil || !slices.Contains(first.Releases[0].Evidence.Pending, "ci") {
		t.Fatal("missing CI must remain pending")
	}
	candidateGit(t, dir, "tag", "-a", "v"+candidateVersion, "-m", "PAIMOS AEON v"+candidateVersion+" · inspr-calendar-v2 · stable · release_sequence 3 · Real tag headline")
	second, err := Build(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	baseline := opts
	baseline.Candidate = ""
	normal, err := Build(t.Context(), baseline)
	if err != nil || !reflect.DeepEqual(second, normal) {
		t.Fatalf("tagged candidate changed normal history: %v", err)
	}
	r := second.Releases[0]
	if len(second.Releases) != len(first.Releases) || r.State != StatePublished || r.Headline != "Real tag headline" || r.TaggedAt == nil || len(r.Evidence.Pending) != 0 {
		t.Fatalf("tagged completion: %+v", r)
	}
}

func TestCandidateCommittedCorrections(t *testing.T) {
	dir, opts := candidateRepo(t)
	pillEN, pillDE := "Reviewed fix", "Geprüfte Korrektur"
	benefitEN, benefitDE := "Corrected captured benefit.", "Korrigierter erfasster Nutzen."
	correction := NoteCorrection{Version: candidateVersion, Key: "AEON-427", SHA256: strings.Repeat("a", 64),
		Reason: "Reviewed repair classification and wording.", Group: GroupFixes,
		PillEN: &pillEN, PillDE: &pillDE, BenefitEN: &benefitEN, BenefitDE: &benefitDE}
	layer := map[string]any{"schema": "aeon.product-note-corrections.v1", "corrections": []NoteCorrection{correction}}
	candidateWrite(t, dir, NoteCorrectionsPath, layer)
	candidateGit(t, dir, "add", NoteCorrectionsPath)
	candidateGit(t, dir, "commit", "-qm", "review captured notes")
	first, err := Build(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	notes := first.Releases[0].Notes
	want := TicketNote{Key: correction.Key, Group: GroupFixes, PillEN: pillEN, PillDE: pillDE, BenefitEN: benefitEN, BenefitDE: benefitDE}
	if notes.Source != ProductNotesSource || len(notes.PublicItems) != 2 || !reflect.DeepEqual(notes.PublicItems[0], want) ||
		!reflect.DeepEqual(notes.Corrections, []NoteCorrection{correction}) || notes.SHA256 != correction.SHA256 ||
		notes.PublicItems[1].PillEN != "Frozen fix" {
		t.Fatalf("committed correction missing or original provenance lost: %+v", notes)
	}
	// A valid, uncommitted correction must not replace the reviewed record.
	uncommitted := correction
	uncommitted.Group = GroupFeatures
	uncommitted.PillEN = new(string)
	*uncommitted.PillEN = "Uncommitted wording"
	candidateWrite(t, dir, NoteCorrectionsPath, map[string]any{"schema": "aeon.product-note-corrections.v1", "corrections": []NoteCorrection{uncommitted}})
	dirty, err := Build(t.Context(), opts)
	if err != nil || !reflect.DeepEqual(first.Releases[0].Notes, dirty.Releases[0].Notes) {
		t.Fatalf("candidate followed uncommitted corrections: %v", err)
	}
	candidateWrite(t, dir, NoteCorrectionsPath, layer)
	candidateGit(t, dir, "tag", "-a", "v"+candidateVersion, "-m", "PAIMOS AEON v"+candidateVersion+" · inspr-calendar-v2 · stable · release_sequence 3 · Real tag headline")
	tagged, err := Build(t.Context(), opts)
	if err != nil || !reflect.DeepEqual(notes, tagged.Releases[0].Notes) {
		t.Fatalf("tagging the same commit changed corrected notes: %v", err)
	}
	baseline := opts
	baseline.Candidate = ""
	normal, err := Build(t.Context(), baseline)
	if err != nil || !reflect.DeepEqual(tagged, normal) {
		t.Fatalf("tagged candidate changed the normal manifest: %v", err)
	}
}

func TestCandidateAlreadyTaggedWithNewerHistory(t *testing.T) {
	dir := repo(t)
	// Rebuilding an older version after another tag was fetched must keep the
	// normal entry, rather than rejecting it as an obsolete new candidate.
	candidateGit(t, dir, "tag", "-a", "v260925120000.0.0", "-m", "PAIMOS AEON v260925120000.0.0 · inspr-calendar-v2 · stable · release_sequence 4 · Later release")
	baseline := Options{Repo: dir, Repository: "inspr-at/paimos", Now: func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }}
	normal, err := Build(t.Context(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	baseline.Candidate = "260923143005.0.0"
	got, err := Build(t.Context(), baseline)
	if err != nil || !reflect.DeepEqual(normal, got) {
		t.Fatalf("existing tagged version differs: %v", err)
	}
}

func TestCandidateRejectsInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*versionFile)
		version string
		notes   bool
	}{
		{name: "invalid coordinate", version: "260231120000.0.0"},
		{name: "mismatched coordinate", version: "260924130000.0.0"},
		{name: "unknown scheme", change: func(v *versionFile) { v.VersionScheme = "unknown" }},
		{name: "no channel", change: func(v *versionFile) { v.ReleaseChannel = "" }},
		{name: "reused sequence", change: func(v *versionFile) { v.ReleaseSequence = 2; v.Codename = "Blue Bot" }},
		{name: "wrong reservation instant", change: func(v *versionFile) { v.ReservedAt = "2026-09-24T13:00:00Z" }},
		{name: "unpublished reservation", change: func(v *versionFile) { v.UnpublishedReservations = append(v.UnpublishedReservations, candidateVersion) }},
		{name: "missing captured notes", notes: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, opts := candidateRepo(t)
			if test.version != "" {
				opts.Candidate = test.version
			}
			if test.change != nil {
				raw, err := os.ReadFile(filepath.Join(dir, "version.json"))
				if err != nil {
					t.Fatal(err)
				}
				var v versionFile
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				test.change(&v)
				candidateWrite(t, dir, "version.json", v)
			}
			if test.notes {
				candidateWrite(t, dir, ProductNotesPath, EmptyProductNotes())
			}
			candidateGit(t, dir, "add", "version.json", ProductNotesPath)
			candidateGit(t, dir, "commit", "--allow-empty", "-qm", "test candidate input")
			if _, err := Build(t.Context(), opts); err == nil {
				t.Fatal("invalid candidate accepted")
			}
		})
	}
}

func TestCandidateCommittedSnapshotPrecedence(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(strconv.FormatBool(malformed), func(t *testing.T) {
			dir, opts := candidateRepo(t)
			snapshot := noteFixture()
			snapshot.Version = candidateVersion
			snapshot.Tickets[0].Group = GroupFixes
			if malformed {
				snapshot.Version = notesVersion
			}
			path := "release-notes/" + candidateVersion + ".json"
			candidateWrite(t, dir, path, snapshot)
			candidateGit(t, dir, "add", path)
			candidateGit(t, dir, "commit", "-qm", "capture notes")
			h, err := Build(t.Context(), opts)
			if malformed {
				if err == nil {
					t.Fatal("mismatched committed snapshot silently fell back to public notes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			notes := h.Releases[0].Notes
			if notes.Source != "HEAD:"+path || len(notes.Items) != 1 || notes.Items[0].Group != GroupFixes || notes.Items[0].PillEN != "Clear release notes" || notes.SHA256 == strings.Repeat("a", 64) {
				t.Fatalf("committed snapshot lost precedence: %+v", notes)
			}
		})
	}
}

func TestCandidateBlobBound(t *testing.T) {
	read := false
	git := func(args ...string) (string, error) {
		if args[0] == "cat-file" {
			return "16777217", nil
		}
		read = true
		return "unexpected blob", nil
	}
	if _, err := committedFile(git, "snapshot.json", 16<<20); err == nil || read {
		t.Fatal("oversized capture must be rejected before reading its body")
	}
}
