// SPDX-License-Identifier: AGPL-3.0-only
package dsar

import (
	"bytes"
	"testing"
)

func TestParseCommands(t *testing.T) {
	base := []string{"--tenant", "fixture", "--actor-principal-id", "00000000-0000-0000-0000-000000000001", "--person", "person@example.test"}
	for _, operation := range []string{"export", "erase"} {
		args := append([]string{operation}, base...)
		if operation == "erase" {
			args = append(args, "--dry-run")
		}
		c, err := Parse(args)
		if err != nil || c.Options.Erase != (operation == "erase") || c.Output != "-" {
			t.Fatalf("parse %s: %v", operation, err)
		}
	}
	for _, args := range [][]string{nil, {"destroy"}, {"export"}, {"erase"}, {"export", "--person", "me"}, append(append([]string{"erase"}, base...), "--apply"), append([]string{"erase"}, base...), append(append([]string{"erase"}, base...), "--dry-run=false"), append(append([]string{"export"}, base...), "--dry-run"), append(append([]string{"export"}, base...), "surprise"), append(append([]string{"export"}, base...), "--tenant", ""), append(append([]string{"export"}, base...), "--actor-principal-id", "invalid")} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("unsafe command accepted: %q", args)
		}
	}
}

func TestExecuteFailureEmitsNothing(t *testing.T) {
	var out bytes.Buffer
	if err := Execute(t.Context(), nil, Command{Options: Options{Tenant: "fixture", ActorID: "00000000-0000-0000-0000-000000000001", Person: "person@example.test"}, Output: "-"}, &out); err == nil || out.Len() != 0 {
		t.Fatal("failed command emitted a packet")
	}
}
