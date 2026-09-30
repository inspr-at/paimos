// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkingFolder(t *testing.T) {
	if !errors.Is(validateWorkingFolder("", errors.New("getcwd")), errDeletedWorkingFolder) {
		t.Fatal("getcwd failure")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if !errors.Is(validateWorkingFolder(missing, nil), errDeletedWorkingFolder) {
		t.Fatal("missing folder")
	}
	if err := validateWorkingFolder(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPairAndSetupRejectDeletedWorkingFolder(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	gone := filepath.Join(home, "gone")
	if err = os.Mkdir(gone, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err = os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"pair", "setup"} {
		var out bytes.Buffer
		err = setupCommandInput(command, []string{"--url", "https://example.test", "--workspace", home, "--harness", "claude"}, strings.NewReader("yes\n"), &out)
		if !errors.Is(err, errDeletedWorkingFolder) || err.Error() != "Your current folder no longer exists. cd to another folder and run the command again." || out.Len() != 0 {
			t.Fatalf("%s: %v %q", command, err, out.String())
		}
	}
	var out bytes.Buffer
	err = setupCommandInput("repin", nil, strings.NewReader(""), &out)
	if err == nil || errors.Is(err, errDeletedWorkingFolder) || !strings.Contains(err.Error(), "--harness claude") {
		t.Fatal(err)
	}
	entries, readErr := os.ReadDir(home)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("deleted folder wrote state", entries, readErr)
	}
}
