// SPDX-License-Identifier: AGPL-3.0-only
package main

import "testing"

func TestSourceInventoryUsesTestingSignatureAndBuildActivity(t *testing.T) {
	source := `package fixture
import (
  t "testing"
  other "other/package"
)
func TestMain(m *t.M) {}
func TestProof(t *t.T) {}
func TestHelper(t *other.T) {}
func TestWrong(t *t.F) {}
func Testlower(t *t.T) {}
func FuzzSeed(f *t.F) {}
func BenchmarkOther(b *t.B) {}
var quoted = "func TestQuoted(t *testing.T) {}"
`
	for _, active := range []bool{true, false} {
		rows, err := sourceTests("internal/proof/proof_darwin_test.go", source, active)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].Name != "TestProof" || rows[1].Name != "FuzzSeed" {
			t.Fatalf("unexpected tests: %#v", rows)
		}
		for _, row := range rows {
			if row.Package != "internal/proof" || row.Active != active {
				t.Fatalf("lost identity/platform: %#v", row)
			}
		}
	}
	if _, err := sourceTests("internal/proof/bad_test.go", "invalid source", true); err == nil {
		t.Fatal("invalid Go source accepted")
	}
}
