// SPDX-License-Identifier: AGPL-3.0-only

package ciexecutor

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/ciproof"
)

func TestGuestEnvironmentExcludesActionsAndLoaderPoisoning(t *testing.T) {
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "LD_PRELOAD", "GITHUB_ENV", "GITHUB_PATH", "AEON_CI_READ_TOKEN"} {
		t.Setenv(name, "poison-fixture")
	}
	env := strings.Join(GuestEnvironment(), "\n")
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "LD_PRELOAD", "GITHUB_", "AEON_CI_", "poison-fixture"} {
		if strings.Contains(env, name) {
			t.Fatalf("candidate/authority env leaked: %s", name)
		}
	}
	for _, setting := range []string{"GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOFLAGS=", "npm_config_offline=true", "PATH=/opt/aeon/bin:/usr/bin:/bin"} {
		if !strings.Contains(env, setting) {
			t.Fatalf("missing controlled input %s", setting)
		}
	}
}

func archive(t *testing.T, h *tar.Header, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGuestExtractionRefusesPathsLinksAndPrivileges(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "../escape", Mode: 0644, Typeflag: tar.TypeReg},
		{Name: "/absolute", Mode: 0644, Typeflag: tar.TypeReg},
		{Name: ".git/config", Mode: 0644, Typeflag: tar.TypeReg},
		{Name: "symlink", Mode: 0644, Typeflag: tar.TypeSymlink, Linkname: "/opt/aeon/bin/runner"},
		{Name: "hardlink", Mode: 0644, Typeflag: tar.TypeLink, Linkname: "/opt/aeon/bin/runner"},
		{Name: "setuid", Mode: 04755, Typeflag: tar.TypeReg},
		{Name: "device", Mode: 0644, Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
	} {
		t.Run(header.Name, func(t *testing.T) {
			if err := ExtractSource(archive(t, header, ""), t.TempDir()); err == nil {
				t.Fatal("unsafe guest archive accepted")
			}
		})
	}
	root := t.TempDir()
	body := "candidate data"
	raw := archive(t, &tar.Header{Name: "a\nfile.txt", Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(body))}, body)
	if err := ExtractSource(raw, root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "a\nfile.txt"))
	if err != nil || string(got) != body {
		t.Fatal("NUL-safe Git filename lost")
	}
	if err := ExtractSource(raw, root); err == nil {
		t.Fatal("existing guest source overwritten")
	}
}

func TestManifestCompletionRequiresEveryExpectedIdentity(t *testing.T) {
	task := ciproof.VMTask{Reporter: "go-json", Expected: []string{"package/example/p", "test/example/p/TestA"}}
	events := []map[string]string{{"Action": "start", "Package": "example/p"}, {"Action": "run", "Package": "example/p", "Test": "TestA"}, {"Action": "pass", "Package": "example/p", "Test": "TestA"}, {"Action": "pass", "Package": "example/p"}}
	stream := func(rows []map[string]string) []byte {
		var b bytes.Buffer
		for _, e := range rows {
			json.NewEncoder(&b).Encode(e)
		}
		return b.Bytes()
	}
	if _, err := Completion(task, stream(events), 0); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("Total: 1 test in 1 file"), []byte(`{"stats":{"expected":1},"errors":[]}`), stream(events[:3]), stream(append(events, events[3])), []byte(`{"Action":"skip","Package":"example/p"}`), []byte(`{"Action":"pass","Package":"example/p"}`)} {
		if _, err := Completion(task, bad, 0); err == nil {
			t.Fatal("incomplete/forged Go output accepted")
		}
	}
	if _, err := Completion(task, stream(events), 1); err == nil {
		t.Fatal("failed process accepted")
	}
	task.Expected = []string{"package/example/p"}
	if _, err := Completion(task, stream(events), 0); err == nil {
		t.Fatal("unexpected tests ignored")
	}
	task.Reporter = "harness-json"
	if _, err := Completion(task, []byte(`{"stats":{"expected":1},"errors":[]}`), 0); err == nil {
		t.Fatal("unprovisioned browser boundary accepted candidate JSON")
	}
}

func TestGuestInitCannotRunOnTheHost(t *testing.T) {
	if err := Init(); err == nil {
		t.Fatal("host process accepted as guest init")
	}
}
