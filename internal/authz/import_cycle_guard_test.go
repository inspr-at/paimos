// SPDX-License-Identifier: AGPL-3.0-only

package authz_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Risk: an internal authz test that imports tenantbootstrap recreates
// authz → tenantbootstrap → events → authz, and go vet refuses the package
// (AEON-1585, b64ce2a7 person_identity_test.go).
func TestAuthzInternalTestsDoNotImportTenantBootstrap(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path unavailable")
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	const forbidden = "github.com/inspr-at/paimos/internal/tenantbootstrap"
	seen := 0
	set := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(filepath.Dir(file), name)
		parsed, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if parsed.Name == nil || parsed.Name.Name != "authz" {
			continue
		}
		seen++
		for _, spec := range parsed.Imports {
			if spec.Path == nil {
				continue
			}
			if strings.Trim(spec.Path.Value, `"`) == forbidden {
				t.Errorf("%s is package authz and imports tenantbootstrap; that cycles through events back into authz", name)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no package authz test files scanned")
	}
}
