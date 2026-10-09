// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Risk D5: hand-outs infer the wrong instance/project or silently become
// project-less when a linked working folder is malformed or ambiguous.
func TestAccountProjectFolderLinksFailClosed(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(child, 0700))
	first, second := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	got, err := linkedAccountProject(child, map[string]string{root: first, child: second})
	must(err)
	if got != second {
		t.Fatal(got)
	}
	alias := filepath.Join(root, "alias")
	must(os.Symlink(child, alias))
	got, err = linkedAccountProject(alias, map[string]string{child: second})
	must(err)
	if got != second {
		t.Fatal("symlink lost project", got)
	}
	if _, err := linkedAccountProject(child, map[string]string{child: "invalid"}); err == nil {
		t.Fatal("invalid link became default")
	}
	if _, err := linkedAccountProject(child, map[string]string{child: first, alias: second}); err == nil {
		t.Fatal("ambiguous link became default")
	}
	got, err = linkedAccountProject(root, nil)
	must(err)
	if got != "" {
		t.Fatal(got)
	}
}
