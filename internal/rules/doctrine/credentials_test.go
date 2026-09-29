// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func allowCredential(t *testing.T, dir, ref, tenantID, repository string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"grants": []map[string]string{{"tenant_id": tenantID, "repository": repository}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref+".allowlist.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsRequireTenantRepositoryGrant(t *testing.T) {
	const tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const tenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	dir := t.TempDir()
	const ref = "doctrine-private-read"
	if err := os.WriteFile(filepath.Join(dir, ref), []byte(fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Credentials{Dir: dir}
	denied := func(ref, tid, repo string) {
		t.Helper()
		token, err := c.Token(ref, tid, repo)
		if token != "" || !errors.Is(err, ErrCredential) || err.Error() != "credential unavailable" {
			t.Fatal("credential denial must return no token and only a generic error")
		}
	}
	// Having a token file is insufficient, even for a workspace owner.
	denied(ref, tenantA, privateRepo)
	policy := `{"grants":[{"tenant_id":"` + tenantA + `","repository":"` + privateRepo + `"},{"tenant_id":"` + tenantB + `","repository":"` + fixtureRepo + `"}]}`
	writePolicy := func(raw string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ref+".allowlist.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy(policy)
	for _, pair := range [][2]string{{tenantA, privateRepo}, {tenantB, fixtureRepo}, {tenantA, strings.ToUpper(privateRepo)}} {
		if token, err := c.Token(ref, pair[0], pair[1]); err != nil || token != fixtureToken {
			t.Fatal("explicit tenant/repository grant did not resolve the fixture token")
		}
	}
	// Grants are pairs, never the Cartesian product of tenants and repositories.
	denied(ref, tenantA, fixtureRepo)
	denied(ref, tenantB, privateRepo)
	denied(ref, "", privateRepo)
	denied(ref, tenantA, "../escape")
	denied("missing", tenantA, privateRepo)
	denied("../escape", tenantA, privateRepo)
	if token, err := (Credentials{}).Token(ref, tenantA, privateRepo); token != "" || !errors.Is(err, ErrCredential) {
		t.Fatal("missing directory must fail closed")
	}
	for name, raw := range map[string]string{
		"malformed": "{", "empty": `{}`, "null": `null`, "no grants": `{"grants":[]}`,
		"unknown field": strings.Replace(policy, `"grants"`, `"tenants"`, 1),
		"trailing data": policy + `{}`, "oversized": policy + strings.Repeat(" ", 64<<10),
		"wildcard tenant":     strings.ReplaceAll(policy, tenantA, "*"),
		"wildcard repository": strings.ReplaceAll(policy, privateRepo, "inspr-at/*"),
	} {
		t.Run(name, func(t *testing.T) {
			writePolicy(raw)
			denied(ref, tenantA, privateRepo)
		})
	}
	writePolicy(policy)
	if err := os.WriteFile(filepath.Join(dir, ref), []byte("not a token!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	denied(ref, tenantA, privateRepo)
}
