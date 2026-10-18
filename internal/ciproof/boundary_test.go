// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBoundedContentAddressedArtifacts(t *testing.T) {
	good := Artifact{Name: "app.js", Data: []byte("opaque candidate artifact"), Digest: RawDigest([]byte("opaque candidate artifact"))}
	if err := ValidateArtifacts([]Artifact{good}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape", "/root", ".", "..", "a/b", "a\\b", "file\n"} {
		bad := good
		bad.Name = name
		if err := ValidateArtifacts([]Artifact{bad}); err == nil {
			t.Fatalf("unsafe artifact %q", name)
		}
	}
	bad := good
	bad.Digest = RawDigest([]byte("different"))
	if err := ValidateArtifacts([]Artifact{bad}); err == nil {
		t.Fatal("artifact digest forgery accepted")
	}
	if err := ValidateArtifacts([]Artifact{good, good}); err == nil {
		t.Fatal("duplicate artifact accepted")
	}
	bad.Data = make([]byte, maxArtifact+1)
	bad.Digest = RawDigest(bad.Data)
	if err := ValidateArtifacts([]Artifact{bad}); err == nil {
		t.Fatal("unbounded artifact accepted")
	}
}

func TestReadOnlyVMFramingAndTaskPoisoning(t *testing.T) {
	var out bytes.Buffer
	if err := WriteFrame(&out, []byte("input")); err != nil {
		t.Fatal(err)
	}
	if out.Len()%512 != 0 {
		t.Fatal("raw disk frame unaligned")
	}
	raw, err := ReadFrame(bytes.NewReader(out.Bytes()), 5)
	if err != nil || string(raw) != "input" {
		t.Fatal("frame data mismatch")
	}
	if _, err := ReadFrame(bytes.NewReader(out.Bytes()), 4); err == nil {
		t.Fatal("frame limit ignored")
	}
	if _, err := ReadFrame(bytes.NewReader(out.Bytes()[:17]), 5); err == nil {
		t.Fatal("truncated frame accepted")
	}
	_, _, _, _, p := authorityFixture(t)
	prof := profile(p)
	pub, _, _ := ed25519.GenerateKey(nil)
	f := newFixture(t)
	s, err := NewSupervisor(f.repo, prof, pub)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.task(p, "job/web", RawDigest([]byte("source")))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(task)
	if _, err := DecodeVMTask(b); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"env":{"NODE_OPTIONS":"--require=/fake"}`, `"app_key":"candidate"`, `"host_mount":"/"`, `"expected":["less"]`} {
		bad := append(bytes.Clone(b[:len(b)-1]), []byte(","+field+"}")...)
		if _, err := DecodeVMTask(bad); err == nil {
			t.Fatal("candidate task controls accepted")
		}
	}
}

func TestSourceExportDoesNotHonorCandidateExportIgnore(t *testing.T) {
	f := newFixture(t)
	inputs := files()
	inputs[".gitattributes"] = "secret-to-hide export-ignore\n"
	inputs["secret-to-hide"] = "ordinary test input (not a credential)"
	commit := f.commit(inputs, "attributes")
	snapshot, err := f.repo.Snapshot(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.repo.sourceArchive(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(raw))
	names := map[string]bool{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	if !names["secret-to-hide"] || len(names) != len(snapshot.Entries) {
		t.Fatal("candidate attributes narrowed source export")
	}
	bad := snapshot
	bad.Entries = append([]Entry(nil), snapshot.Entries...)
	bad.Entries[0].ContentDigest = RawDigest([]byte("wrong"))
	if _, err := f.repo.sourceArchive(context.Background(), bad); err == nil {
		t.Fatal("unverified blob exported")
	}
	bad.Entries[0].Mode = "120000"
	if _, err := f.repo.sourceArchive(context.Background(), bad); err == nil {
		t.Fatal("symlink source export accepted")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubReaderIsBoundedReadOnlyAndRedactsErrors(t *testing.T) {
	g, err := NewGitHubReader("inspr-at/paimos", "read-fixture")
	if err != nil {
		t.Fatal(err)
	}
	g.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "api.github.com" {
			t.Fatal("authority reader tried write or wrong host")
		}
		return nil, fmt.Errorf("transport-private-marker")
	})
	if _, _, err := g.Repository(context.Background()); err == nil || strings.Contains(err.Error(), "transport-private-marker") {
		t.Fatal("transport details exposed")
	}
	g.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("private response")), Header: http.Header{"Location": []string{"https://untrusted.example"}}}, nil
	})
	if _, _, err := g.Repository(context.Background()); err == nil || strings.Contains(err.Error(), "private response") {
		t.Fatal("redirect or API body accepted")
	}
	g.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + strings.Repeat("a", 40) + `"}}`)), Header: http.Header{}}, nil
	})
	if sha, err := g.Ref(context.Background(), "refs/heads/main"); err != nil || sha != strings.Repeat("a", 40) {
		t.Fatal("ref resolution failed")
	}
	if _, err := g.Ref(context.Background(), "refs/heads/main/../../private"); err == nil {
		t.Fatal("ref injection accepted")
	}
	if _, err := g.Ref(context.Background(), "refs/heads/gh-readonly-queue/main/../private"); err == nil {
		t.Fatal("queue ref traversal accepted")
	}
}

func TestExecutorUnavailableNeverMintsObservation(t *testing.T) {
	f, _, _, _, p := authorityFixture(t)
	prof := profile(p)
	pub, _, _ := ed25519.GenerateKey(nil)
	s, err := NewSupervisor(f.repo, prof, pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []ExecutionIdentity{{RunID: 1, Attempt: 2, JobID: "partial-rerun"}, {RunID: 1, Attempt: 1, JobID: "approved"}} {
		if _, err := s.Run(context.Background(), p, "job/web", Admission{}, identity); err == nil {
			t.Fatal("missing admission minted success")
		}
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if _, err := runVM(context.Background(), prof, VMTask{}, nil); err == nil {
		t.Fatal("Actions/unsupported host used external VM executor")
	}
}
