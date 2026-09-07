// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodexAccountRegistryPinsOpaqueKeysAndRejectsUnsafeHomes(t *testing.T) {
	home := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"accounts": []map[string]string{{"key": "coordinator", "home": home, "email": "one@example.invalid"}},
	})
	registry, err := ParseCodexAccountRegistry(raw)
	if err != nil || !registry.HasAccount("coordinator") || registry.HasAccount("chatgpt") {
		t.Fatalf("registry=%+v err=%v", registry, err)
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	account, ok := registry.lookup("coordinator")
	if !ok || account.home != canonical || account.email != "one@example.invalid" {
		t.Fatalf("lookup=%+v ok=%t canonical=%s", account, ok, canonical)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"accounts":[{"key":"chatgpt","home":"` + home + `","email":"one@example.invalid"}]}`),
		[]byte(`{"accounts":[{"key":"/tmp/home","home":"` + home + `","email":"one@example.invalid"}]}`),
		[]byte(`{"accounts":[{"key":"ok","home":"relative","email":"one@example.invalid"}]}`),
		[]byte(`{"accounts":[{"key":"ok","home":"` + home + `","email":"not-an-email"}]}`),
		[]byte(`{"accounts":[{"key":"ok","home":"` + home + `","email":"one@example.invalid","token":"secret"}]}`),
	} {
		if _, err := ParseCodexAccountRegistry(invalid); err == nil {
			t.Fatalf("accepted invalid registry %s", invalid)
		}
	}
}

func TestApplyCodexHomeFromOperatorEnvOmitsUnknownNames(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("PAIMOS_PAI952_SECRET", "nope")
	env := applyCodexHome(nil, "/canonical/home")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "PAIMOS_PAI952_SECRET") || !strings.Contains(joined, "CODEX_HOME=/canonical/home") || !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("env=%q", env)
	}
}

func TestApplyCodexHomeReplacesInheritedHomeAndPreservesAllowlist(t *testing.T) {
	env := applyCodexHome([]string{"PATH=/bin", "CODEX_HOME=/wrong", "SECRET=nope"}, "/canonical/home")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CODEX_HOME=/canonical/home") || strings.Count(joined, "CODEX_HOME=") != 1 || !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("env=%q", env)
	}
}

func TestValidAccountKeyRejectsClassLabelsAndPaths(t *testing.T) {
	if validAccountKey("chatgpt") || validAccountKey("api_key") || validAccountKey("/tmp/codex") || validAccountKey("local_probe") || validAccountKey("pi_context") || !validAccountKey("coordinator") {
		t.Fatal("account key validation drifted")
	}
}

func writeCodexHome(t *testing.T, email string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "paimos-fixture-identity"), []byte(email+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func testCodexRegistry(t *testing.T, entries ...codexAccountRegistryEntry) CodexAccountRegistry {
	t.Helper()
	raw, err := json.Marshal(codexAccountRegistryFile{Accounts: entries})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ParseCodexAccountRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestParsePiAccountRegistryPinsOpaqueKeysAndRejectsCodexShape(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"accounts": []map[string]string{{"key": "operator-pi", "agent_dir": dir}},
	})
	registry, err := ParsePiAccountRegistry(raw)
	if err != nil || !registry.HasAccount("operator-pi") || registry.HasAccount("codex-home") {
		t.Fatalf("registry=%+v err=%v", registry, err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	account, ok := registry.lookup("operator-pi")
	if !ok || account.agentDir != canonical {
		t.Fatalf("lookup=%+v ok=%t canonical=%s", account, ok, canonical)
	}
	codexShaped, _ := json.Marshal(map[string]any{
		"accounts": []map[string]string{{"key": "coordinator", "home": dir, "email": "one@example.invalid"}},
	})
	if _, err := ParsePiAccountRegistry(codexShaped); err == nil {
		t.Fatal("pi registry accepted a Codex registry shape")
	}
	if _, err := ParsePiAccountRegistry([]byte(`{"accounts":[{"key":"pi_context","agent_dir":"` + dir + `"}]}`)); err == nil {
		t.Fatal("accepted reserved pi_context key")
	}
}
