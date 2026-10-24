// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// Risk: an architecture example survives a refactor while teaching code that no
// longer exists. Check both citation bounds and the exact copied source bytes.
func TestArchitectureGuideSources(t *testing.T) {
	const citation = "[a.go:1](../src/a.go#L1)\n"
	const excerpt = "```go source=src/a.go:1\npackage a\n```\n"
	sources := fstest.MapFS{"src/a.go": {Data: []byte("package a\n")}}
	for _, tc := range []struct {
		name, doc, problem string
	}{
		{"valid", citation + excerpt, ""},
		{"missing file", strings.ReplaceAll(citation+excerpt, "src/a.go", "src/missing.go"), "read src/missing.go"},
		{"zero line", strings.Replace(citation+excerpt, "#L1", "#L0", 1), "outside source"},
		{"past end", strings.Replace(citation+excerpt, "#L1", "#L2", 1), "outside source"},
		{"invalid line", strings.Replace(citation+excerpt, "#L1", "#Loops", 1), "invalid line"},
		{"outside repository", strings.ReplaceAll(citation+excerpt, "src/a.go", "../a.go"), "invalid source path"},
		{"stale excerpt", citation + strings.Replace(excerpt, "package a", "package b", 1), "excerpt differs"},
		{"excerpt past end", citation + strings.Replace(excerpt, ":1", ":2", 1), "outside source"},
		{"unclosed excerpt", citation + strings.TrimSuffix(excerpt, "```\n"), "unclosed fence"},
		{"unattributed example", citation + strings.Replace(excerpt, " source=src/a.go:1", "", 1), "requires source="},
		{"no citations", excerpt, "no source citations"},
		{"no excerpts", citation, "no source excerpts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkArchitectureGuide(tc.doc, sources)
			if tc.problem == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("got %v, want %q", err, tc.problem)
			}
		})
	}
	root := os.DirFS("../..")
	doc, err := fs.ReadFile(root, "docs/architecture-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkArchitectureGuide(string(doc), root); err != nil {
		t.Fatal(err)
	}
}

func checkArchitectureGuide(doc string, sources fs.FS) error {
	read := func(name, number string) ([]string, int, error) {
		if !fs.ValidPath(name) {
			return nil, 0, fmt.Errorf("invalid source path %q", name)
		}
		line, err := strconv.Atoi(number)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: invalid line %q", name, number)
		}
		raw, err := fs.ReadFile(sources, name)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(raw) == 0 || line < 1 || line > len(lines) {
			return nil, 0, fmt.Errorf("%s:%d outside source (1..%d)", name, line, len(lines))
		}
		return lines, line - 1, nil
	}
	citations := regexp.MustCompile(`\]\(\.\./([^\s)#]+)#L([^\s)]+)\)`).FindAllStringSubmatch(doc, -1)
	if len(citations) == 0 {
		return fmt.Errorf("no source citations")
	}
	for _, cite := range citations {
		if _, _, err := read(cite[1], cite[2]); err != nil {
			return err
		}
	}
	excerpts := 0
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```") {
			continue
		}
		info := strings.Fields(strings.TrimPrefix(lines[i], "```"))
		start := i + 1
		for i = start; i < len(lines) && lines[i] != "```"; i++ {
		}
		if i == len(lines) {
			return fmt.Errorf("unclosed fence at guide line %d", start)
		}
		if len(info) == 0 || !strings.Contains("|go|sql|yaml|vue|ts|css|", "|"+info[0]+"|") {
			continue
		}
		if len(info) != 2 || !strings.HasPrefix(info[1], "source=") {
			return fmt.Errorf("guide line %d requires source=path:line", start)
		}
		name, number, ok := strings.Cut(strings.TrimPrefix(info[1], "source="), ":")
		if !ok {
			return fmt.Errorf("guide line %d requires source=path:line", start)
		}
		source, offset, err := read(name, number)
		if err != nil {
			return err
		}
		count := i - start
		if count == 0 || offset+count > len(source) {
			return fmt.Errorf("%s:%s excerpt outside source", name, number)
		}
		if strings.Join(lines[start:i], "\n") != strings.Join(source[offset:offset+count], "\n") {
			return fmt.Errorf("%s:%s excerpt differs; refresh its citation and copied lines", name, number)
		}
		excerpts++
	}
	if excerpts == 0 {
		return fmt.Errorf("no source excerpts")
	}
	return nil
}
