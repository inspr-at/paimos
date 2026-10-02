// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheContextIntegrityExpiryAndFloor(t *testing.T) {
	now := time.Now().UTC()
	c := testContext()
	exp := now.Add(time.Minute)
	r := testRule("temporary", "Temporary instruction")
	r.ExpiresAt = &exp
	m, err := Merge(c, []Snapshot{floorSnapshot(), testSnapshot("project", Scope{Layer: "project", ProjectID: c.ProjectID}, r)}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCache("https://fixture.invalid", m, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCache(raw, "https://fixture.invalid", c, now)
	if err != nil || got.SHA256 != m.SHA256 {
		t.Fatal("valid cache", err)
	}
	contexts := []Context{}
	for i := 0; i < 7; i++ {
		wrong := c
		switch i {
		case 0:
			wrong.TenantID = testProject
		case 1:
			wrong.ProjectID = testTenant
		case 2:
			wrong.PersonID = testAgent
		case 3:
			wrong.AgentID = testPerson
		case 4:
			wrong.Role = "reviewer"
		case 5:
			wrong.Harness = "pi"
		case 6:
			wrong.TaskID = testTenant
		}
		contexts = append(contexts, wrong)
	}
	for _, wrong := range contexts {
		if _, err = DecodeCache(raw, "https://fixture.invalid", wrong, now); err == nil {
			t.Fatal("wrong context accepted", wrong)
		}
	}
	if _, err = DecodeCache(raw, "https://other.invalid", c, now); err == nil {
		t.Fatal("other instance accepted")
	}
	var tampered Cache
	json.Unmarshal(raw, &tampered)
	tampered.Bundle.Version = "260928123456.0.0"
	b, _ := json.Marshal(tampered)
	if _, err = DecodeCache(b, "https://fixture.invalid", c, now); err == nil {
		t.Fatal("version tamper accepted")
	}
	for _, bad := range [][]byte{nil, []byte("broken"), append(raw, []byte("garbage")...), b} {
		floor, e := Offline(bad, "https://fixture.invalid", c, m.Floor, now)
		if e == nil || floor.Version != "floor-only" || !strings.Contains(floor.Body, "Preserve safety") || strings.Contains(floor.Body, "Temporary instruction") {
			t.Fatal("floor lost or bad cache reused", e, floor.Body)
		}
	}
	floor, err := Offline(raw, "https://fixture.invalid", c, m.Floor, exp)
	if err == nil || floor.Version != "floor-only" || strings.Contains(floor.Body, "Temporary instruction") {
		t.Fatal("expired cached exception resurrected")
	}
	if _, err = VerifyFloor([]byte(m.Floor), digest([]byte(m.Floor))); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyFloor([]byte(m.Floor), strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong floor pin accepted")
	}
}
func TestMarkStaleKeepsBundleAndRejectsTamper(t *testing.T) {
	now := time.Now().UTC()
	c := testContext()
	m, err := Merge(c, []Snapshot{floorSnapshot()}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCache("https://fixture.invalid", m, now)
	if err != nil || bytes.Contains(raw, []byte(`"stale"`)) {
		t.Fatal("fresh cache", err, string(raw))
	}
	marked, err := MarkStale(raw, "https://fixture.invalid", c, now)
	if err != nil || !bytes.Contains(marked, []byte(`"stale":true`)) {
		t.Fatal(err, string(marked))
	}
	got, err := DecodeCache(marked, "https://fixture.invalid", c, now)
	if err != nil || got.Body != m.Body || got.SHA256 != m.SHA256 || got.Floor != m.Floor {
		t.Fatal("marked cache changed the bundle", err)
	}
	again, err := MarkStale(marked, "https://fixture.invalid", c, now)
	if err != nil || !bytes.Equal(again, marked) {
		t.Fatal("second mark rewrote the cache", err)
	}
	var tampered map[string]any
	if json.Unmarshal(marked, &tampered) != nil {
		t.Fatal("marked cache")
	}
	tampered["stale"] = false
	bad, _ := json.Marshal(tampered)
	if _, err = DecodeCache(bad, "https://fixture.invalid", c, now); err == nil {
		t.Fatal("stale flag cleared without a new digest")
	}
	if _, err = MarkStale(bad, "https://fixture.invalid", c, now); err == nil {
		t.Fatal("tampered cache marked")
	}
	if _, err = MarkStale([]byte("corrupt"), "https://fixture.invalid", c, now); err == nil {
		t.Fatal("corrupt cache marked")
	}
}

func TestRuleSafeFilesRejectLinksBoundsAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	raw := []byte(`{"fixture":true}`)
	if err := WriteFile(path, raw, true); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, raw, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, len(raw)-1); err == nil {
		t.Fatal("oversize read accepted")
	}
	if b, err := ReadFile(path, len(raw)); err != nil || string(b) != string(raw) {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "preview.txt")
	if err := WriteFile(out, raw, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(out, raw, false); err == nil {
		t.Fatal("preview overwrite accepted")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link, 1024); err == nil {
		t.Fatal("leaf symlink read")
	}
	if err := WriteFile(link, raw, true); err == nil {
		t.Fatal("leaf symlink write")
	}
	linkedDir := filepath.Join(dir, "linked")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(filepath.Join(linkedDir, "cache.json"), 1024); err == nil {
		t.Fatal("parent symlink read")
	}
	if err := WriteFile(filepath.Join(linkedDir, "new.json"), raw, true); err == nil {
		t.Fatal("parent symlink write")
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "private.env", "private.key"} {
		if err := WriteFile(filepath.Join(dir, name), raw, false); err == nil {
			t.Fatalf("active or private output %s", name)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 1024); err == nil {
		t.Fatal("public cache accepted")
	}
}

func TestRuleFilesRefuseAdditionalVendorStores(t *testing.T) {
	for _, vendor := range []string{".gemini", ".opencode"} {
		root := filepath.Join(t.TempDir(), vendor)
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "settings.json")
		body := []byte(`{"fixture":"preserve vendor store"}`)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(path, 1024); err == nil {
			t.Fatal("vendor store read accepted")
		}
		if err := WriteFile(path, []byte(`{}`), true); err == nil {
			t.Fatal("vendor store overwritten")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("vendor store changed")
		}
	}
}
