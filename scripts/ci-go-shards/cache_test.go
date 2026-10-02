// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImpactedCacheKeepsExternalTestsFresh(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	pure := "github.com/inspr-at/paimos/internal/runkind"
	plan := shardPlan{whole: []string{pure, "github.com/inspr-at/paimos/internal/db", "github.com/inspr-at/paimos/scripts/releaseworkflow"}}
	fresh, cached := cachePlan(root, plan, true)
	if len(cached.whole) != 1 || cached.whole[0] != pure || len(fresh.whole) != 2 {
		t.Fatalf("unsafe cache partition: fresh=%v cache=%v", fresh, cached)
	}
	fresh, cached = cachePlan(root, plan, false)
	if len(fresh.whole) != 3 || !cached.empty() {
		t.Fatal("main must execute the complete test suite fresh")
	}
}

func TestImpactedCacheActuallyReusesPureTestsAndRejectsInventoryDrift(t *testing.T) {
	source, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, path := range []string{"internal/ciproof/go-impact-policy.json", "internal/runkind/kind.go", "internal/runkind/kind_test.go"} {
		body, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, path), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/inspr-at/paimos\n\ngo 1.26.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_GO_TEST_LANE", "impacted")
	t.Setenv("GOFLAGS", "-count=1")
	plan := shardPlan{whole: []string{"github.com/inspr-at/paimos/internal/runkind"}}
	run := func() string {
		t.Helper()
		output, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		previous := os.Stdout
		os.Stdout = output
		defer func() { os.Stdout = previous; output.Close() }()
		if runPlan(root, 1, plan) != 0 {
			t.Fatal("pure test execution failed")
		}
		body, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	run()
	if !strings.Contains(run(), "(cached)") {
		t.Fatal("untouched pure package did not reuse Go's test cache")
	}
	if err = os.WriteFile(filepath.Join(root, "internal/runkind/extra_test.go"), []byte("package runkind\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run(), "(cached)") {
		t.Fatal("changed package inventory reused the audited result")
	}
	if err = os.WriteFile(filepath.Join(root, "internal/ciproof/go-impact-policy.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if len(auditedCachePackages(root)) != 0 {
		t.Fatal("modified audit policy still authorized cache reuse")
	}
}
