// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

type policyDump struct {
	Routes                 map[string]string    `json:"routes"`
	ProjectFiltered        map[string]bool      `json:"project_filtered"`
	ProjectDecided         map[string]bool      `json:"project_decided"`
	PublicProduct          map[string]bool      `json:"public_product"`
	Registry               []Permission         `json:"registry"`
	BuiltinRoles           map[string][]string  `json:"builtin_roles"`
	Words                  permissionVocabulary `json:"words"`
	BuiltinAgentExclusions []string             `json:"builtin_agent_exclusions"`
	ProjectSelfPermissions []string             `json:"project_self_permissions"`
}

func emptyPolicyDump() policyDump {
	return policyDump{
		Routes: map[string]string{}, ProjectFiltered: map[string]bool{},
		ProjectDecided: map[string]bool{}, PublicProduct: map[string]bool{},
		BuiltinRoles: map[string][]string{},
		Words:        permissionVocabulary{Resources: map[string]string{}, Actions: map[string]string{}, Special: map[string]string{}},
	}
}

// The oracle is the live dump captured before this mechanical conversion at
// 79348b98ca1e10e861784fcfd8e3360e1585f8df. Its fixtures are independent per
// domain: a policy edit never rewrites a committed aggregate or fragment index.
// Maps and registry metadata compare byte-for-byte; membership lists are sets.
func TestDomainPolicyComposition(t *testing.T) {
	// Risk: moving declarations can omit a domain, silently overwrite a key,
	// widen a grant/scope, or make a ServeMux fallback public.
	t.Run("pre-conversion dump", func(t *testing.T) {
		paths, err := filepath.Glob("testdata/domains/*.json")
		if err != nil || len(paths) == 0 {
			t.Fatalf("missing pre-conversion domain fixtures: %v", err)
		}
		want := emptyPolicyDump()
		seenPermissions := map[string]bool{}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var part struct {
				License string `json:"_license"`
				policyDump
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&part); err != nil || part.License != "SPDX-License-Identifier: AGPL-3.0-only" {
				t.Fatalf("invalid baseline %s: %v", path, err)
			}
			if decoder.Decode(&struct{}{}) != io.EOF {
				t.Fatalf("trailing data in baseline %s", path)
			}
			mergeBaseline(t, path, want.Routes, part.Routes)
			mergeBaseline(t, path, want.ProjectFiltered, part.ProjectFiltered)
			mergeBaseline(t, path, want.ProjectDecided, part.ProjectDecided)
			mergeBaseline(t, path, want.PublicProduct, part.PublicProduct)
			mergeBaseline(t, path, want.Words.Resources, part.Words.Resources)
			mergeBaseline(t, path, want.Words.Actions, part.Words.Actions)
			mergeBaseline(t, path, want.Words.Special, part.Words.Special)
			for _, permission := range part.Registry {
				if seenPermissions[permission.Key] {
					t.Fatalf("duplicate baseline permission %s in %s", permission.Key, path)
				}
				seenPermissions[permission.Key] = true
				want.Registry = append(want.Registry, permission)
			}
			for role, keys := range part.BuiltinRoles {
				want.BuiltinRoles[role] = append(want.BuiltinRoles[role], keys...)
			}
			want.BuiltinAgentExclusions = append(want.BuiltinAgentExclusions, part.BuiltinAgentExclusions...)
			want.ProjectSelfPermissions = append(want.ProjectSelfPermissions, part.ProjectSelfPermissions...)
		}
		got := policyDump{
			Routes: RoutePermissions, ProjectFiltered: ProjectFilteredRoutes,
			ProjectDecided: ProjectDecidedRoutes, PublicProduct: publicProductRoutes,
			Registry: slices.Clone(Registry), BuiltinRoles: map[string][]string{}, Words: permissionWords,
			BuiltinAgentExclusions: slices.Clone(builtinAgentExclusions),
			ProjectSelfPermissions: slices.Clone(projectSelfPermissions),
		}
		for _, role := range builtinKeys {
			got.BuiltinRoles[role] = builtinPermissions(role)
		}
		for _, dump := range []*policyDump{&want, &got} {
			sort.Slice(dump.Registry, func(i, j int) bool { return dump.Registry[i].Key < dump.Registry[j].Key })
			for role, keys := range dump.BuiltinRoles {
				slices.Sort(keys)
				if len(slices.Compact(slices.Clone(keys))) != len(keys) {
					t.Fatalf("duplicate membership in %s", role)
				}
			}
			slices.Sort(dump.BuiltinAgentExclusions)
			slices.Sort(dump.ProjectSelfPermissions)
		}
		wantJSON, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		gotJSON, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("assembled policy differs from the pre-conversion dump\nwant:\n%s\ngot:\n%s", wantJSON, gotJSON)
		}
		t.Logf("byte-identical policy dump: %d routes, %d permissions, %d filtered and %d decided routes, %d built-in roles", len(got.Routes), len(got.Registry), len(got.ProjectFiltered), len(got.ProjectDecided), len(got.BuiltinRoles))
	})
	t.Run("duplicate declarations panic", func(t *testing.T) {
		assertPolicyPanic(t, "duplicate route GET /api/health", func() {
			registerRoutes("duplicate", map[string]string{"GET /api/health": PublicRoute})
		})
		assertPolicyPanic(t, "duplicate permission nodes.read", func() {
			permission, ok := Lookup("nodes.read")
			if !ok {
				t.Fatal("missing permission fixture")
			}
			registerPermissions("duplicate", []Permission{permission})
		})
		for _, kind := range []string{"built-in agent exclusion", "project self permission"} {
			t.Run(kind, func(t *testing.T) {
				assertPolicyPanic(t, "duplicate "+kind+" key", func() {
					registerPermissionSet("duplicate", kind, map[string]bool{}, []string{"key", "key"})
				})
			})
		}
		for _, kind := range []string{"project_filtered", "project_decided", "public_product", "label"} {
			t.Run(kind, func(t *testing.T) {
				assertPolicyPanic(t, "duplicate "+kind+" key", func() {
					registerDeclarations("duplicate", kind, map[string]bool{"key": true}, map[string]bool{"key": false})
				})
			})
		}
	})
	t.Run("fallbacks stay undeclared and denied", func(t *testing.T) {
		for _, path := range []string{"/api/", "/api", "/"} {
			for _, method := range []string{"", "GET ", "POST "} {
				pattern := method + path
				if _, ok := PermissionForPattern(pattern); ok || PatternIsPublic(pattern) {
					t.Fatalf("fallback %q has a permission", pattern)
				}
				if err := RequirePattern(context.Background(), pattern, Scope{}); !errors.Is(err, ErrForbidden) {
					t.Fatalf("fallback %q was not denied: %v", pattern, err)
				}
				assertPolicyPanic(t, "fallback route "+pattern, func() {
					registerRoutes("fallback", map[string]string{pattern: PublicRoute})
				})
			}
		}
	})
}

func mergeBaseline[V any](t *testing.T, path string, target, part map[string]V) {
	t.Helper()
	for key, value := range part {
		if _, exists := target[key]; exists {
			t.Fatalf("duplicate baseline key %q in %s", key, path)
		}
		target[key] = value
	}
}

func assertPolicyPanic(t *testing.T, message string, call func()) {
	t.Helper()
	defer func() {
		if value := recover(); value == nil || !strings.Contains(fmt.Sprint(value), message) {
			t.Errorf("panic = %v; want %q", value, message)
		}
	}()
	call()
}
