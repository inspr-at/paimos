// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Risk: incremental discovery must retain exactly the uncached collector's
// signatures, including aliases, fuzz seeds and inactive platform sources.
func TestPackageInventoryMatchesUncachedCollector(t *testing.T) {
	helper, err := filepath.Abs("../../test-tier-go/main.go")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.MkdirAll("internal/fixture", 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.26.0\n",
		"internal/fixture/a_test.go": `package fixture
import tt "testing"
type object struct{}
func TestAlias(t *tt.T) {}
func FuzzSeed(f *tt.F) {}
func TestMain(m *tt.M) {}
func Testlower(t *tt.T) {}
func TestWrong(f *tt.F) {}
func (object) TestMethod(t *tt.T) {}
`,
		"internal/fixture/z_inactive_test.go": `//go:build aeon_inventory_fixture_inactive
package fixture
import "testing"
func TestPlatform(t *testing.T) {}
`,
	}
	for file, body := range files {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, output)
		}
	}
	command := exec.Command("go", "run", "-p=2", helper)
	command.Env = append(os.Environ(), "GOMAXPROCS=2")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("uncached collector: %v", err)
	}
	var golden struct {
		Tests []test `json:"tests"`
	}
	if err := json.Unmarshal(output, &golden); err != nil {
		t.Fatal(err)
	}
	actual, err := collect(request{Package: "internal/fixture", Files: []source{
		{File: "internal/fixture/a_test.go", Active: true},
		{File: "internal/fixture/z_inactive_test.go", Active: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].File+actual[i].Name < actual[j].File+actual[j].Name })
	if len(actual) != 3 || !reflect.DeepEqual(actual, golden.Tests) {
		t.Fatalf("incremental inventory differs: got %#v, uncached %#v", actual, golden.Tests)
	}
	if _, err := collect(request{Package: "internal/fixture", Files: []source{{File: "../outside_test.go"}}}); err == nil || err.Error() != "source outside requested package" {
		t.Fatalf("expected the package-boundary rejection, got %v", err)
	}
}
