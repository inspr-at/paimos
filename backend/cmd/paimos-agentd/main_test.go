// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndRequiredInstance(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"version"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != Version {
		t.Fatalf("version output=%q", output.String())
	}
	output.Reset()
	if err := run([]string{"--version"}, strings.NewReader(""), &output); err != nil || strings.TrimSpace(output.String()) != Version {
		t.Fatalf("--version output=%q error=%v", output.String(), err)
	}
	if err := run([]string{"status"}, strings.NewReader(""), &output); err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("status without instance error=%v", err)
	}
}

func TestDefaultSocketIsSeparatedByInstance(t *testing.T) {
	root := t.TempDir()
	_, one, err := resolvePaths(&commonFlags{instance: "ppm-one", stateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	_, two, err := resolvePaths(&commonFlags{instance: "ppm-two", stateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if one == two || filepath.Dir(one) == filepath.Dir(two) {
		t.Fatalf("instance sockets collided: %s %s", one, two)
	}
}
