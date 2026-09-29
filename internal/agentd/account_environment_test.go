// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountEnvironmentRequiresLocalEnrollment(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{daemonID: "here", accounts: []EnrolledAccount{{ID: "local", Harness: "codex", Key: "opaque"}}, adapters: map[string]Adapter{"codex": NewCodexAdapter("", map[string]string{"opaque": home})}}
	got, err := s.AccountEnvironment("local", "here", "codex")
	if err != nil || got.Home != home || got.Variable != "CODEX_HOME" {
		t.Fatal("local account resolution failed")
	}
	for _, q := range [][3]string{{"local", "elsewhere", "codex"}, {"foreign", "here", "codex"}, {"local", "here", "claude"}} {
		if _, err := s.AccountEnvironment(q[0], q[1], q[2]); err == nil {
			t.Fatal("ownership fence bypassed")
		}
	}
	if err = os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountEnvironment("local", "here", "codex"); err == nil {
		t.Fatal("unsafe home exported")
	}
}
