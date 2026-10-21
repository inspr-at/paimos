// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Risk: exported prose must remain private locally, never overwrite existing
// files, and never follow a server-supplied path out of the chosen directory.
func TestRulesExportWritesPrivateBundleAndRejectsPaths(t *testing.T) {
	id := "15880000-0000-4000-8000-000000000001"
	content := "{\"published\":\"Exact stored prose.\"}\n"
	sum := sha256.Sum256([]byte(content))
	bundle := rules.ExportBundle{Manifest: rules.ExportManifest{Format: "aeon.rules-export.v1", TenantID: id, Layers: []rules.ExportLayer{{ID: id}}}, Files: []rules.ExportFile{{Path: "layer-" + id + ".json", Content: content, SHA256: hex.EncodeToString(sum[:])}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/rules/export" {
			t.Errorf("unexpected export request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(bundle)
	}))
	defer server.Close()
	connect := func() (*client.Client, error) { return client.New(server.URL, "synthetic-test-token"), nil }
	var out bytes.Buffer
	run := func(path string) error {
		rt := &runtime{stdout: &out}
		cmd := rt.rulesExportCommand(connect)
		args, err := rt.parse(cmd, []string{"--out", path})
		if err != nil {
			return err
		}
		return cmd.run(args)
	}
	directory := filepath.Join(t.TempDir(), "export")
	if err := run(directory); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, bundle.Files[0].Path))
	if err != nil || string(got) != content {
		t.Fatalf("layer changed: %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(directory, "manifest.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("manifest not private: %v %v", info, err)
	}
	if err := run(directory); err == nil {
		t.Fatal("overwrote an existing bundle directory")
	}
	bundle.Files[0].Path = "../AGENTS.md"
	invalid := filepath.Join(t.TempDir(), "invalid")
	if err := run(invalid); err == nil {
		t.Fatal("accepted a path outside the export directory")
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatal("invalid bundle created a directory")
	}
}
