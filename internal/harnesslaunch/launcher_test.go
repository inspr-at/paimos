// SPDX-License-Identifier: AGPL-3.0-only
package harnesslaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntrypointAndEnvironment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		header          string
		needed, refused bool
	}{
		{"#!/usr/bin/env node\n", true, false},
		{"#!/usr/bin/env -S node\n", true, false},
		{"#!/usr/bin/env python\n", false, true},
		{"#!/usr/bin/env -S node --require evil\n", false, true},
		{"#!/bin/sh\n", false, false},
		{"native fixture", false, false},
	} {
		path := filepath.Join(root, "launcher")
		if err := os.WriteFile(path, []byte(tc.header), 0700); err != nil {
			t.Fatal(err)
		}
		needed, err := NeedsNode(path)
		if needed != tc.needed || errors.Is(err, ErrStart) != tc.refused {
			t.Fatalf("header %q: %v %v", tc.header, needed, err)
		}
		if tc.needed && !errors.Is(Validate(path, ""), ErrStart) {
			t.Fatal("missing pin accepted")
		}
	}
	for _, inherited := range []string{"", ":", ".:/unsafe/bin:"} {
		result := Environment([]string{"PATH=" + inherited, "NODE_OPTIONS=synthetic", "NODE_PATH=/unsafe", "HOME=/home/test"}, "/pinned/bin/node")
		if strings.Join(result, "\n") != "HOME=/home/test\nPATH=/pinned/bin:"+ServicePath {
			t.Fatal("unsafe environment retained")
		}
	}
	if got := Environment(nil, ""); len(got) != 1 || got[0] != "PATH="+ServicePath {
		t.Fatal("empty PATH retained")
	}
}
