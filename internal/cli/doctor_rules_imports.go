// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/rulescompare"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// Follow literal Markdown @-imports without executing anything. The existing
// descriptor-safe reader refuses symlinks, secret paths and nonregular files.
// Unreadable, cyclic or unsupported imports leave the comparison unverified,
// rather than claiming the pinned rules have drifted or are absent.
func readHarnessImports(home, harness, path string) rulescompare.HarnessFile {
	file := rulescompare.HarnessFile{Harness: harness, Path: path}
	roots := []string{home, filepath.Dir(path)}
	remaining, count := 1<<20, 0
	active, visited := map[string]bool{}, map[string]bool{}
	var read func(string, int) (string, bool)
	read = func(path string, depth int) (string, bool) {
		if active[path] || depth > 8 {
			return "", false
		}
		if visited[path] {
			return "", true
		}
		if count >= 64 || remaining <= 0 {
			return "", false
		}
		count++
		text, _, size, err := rulesimport.ReadContained(path, roots, min(remaining, rulesimport.MaxFileBytes))
		if err != nil {
			file.Missing = depth == 0 && errors.Is(err, os.ErrNotExist)
			return "", false
		}
		if size > remaining {
			return "", false
		}
		remaining -= size
		active[path], visited[path] = true, true
		defer delete(active, path)
		var out strings.Builder
		out.WriteString(text)
		verified := true
		for _, ref := range harnessImports(text) {
			if filepath.Ext(ref) != ".md" || strings.ContainsAny(ref, "$`*?{}\\") {
				verified = false
				continue
			}
			switch {
			case strings.HasPrefix(ref, "~/"):
				ref = filepath.Join(home, ref[2:])
			case !filepath.IsAbs(ref):
				ref = filepath.Join(filepath.Dir(path), ref)
			}
			imported, ok := read(filepath.Clean(ref), depth+1)
			verified = verified && ok
			out.WriteByte('\n')
			out.WriteString(imported)
		}
		return out.String(), verified
	}
	var verified bool
	file.Text, verified = read(filepath.Clean(path), 0)
	file.Unverified = !verified && !file.Missing
	return file
}

func harnessImports(text string) []string {
	pattern := regexp.MustCompile("(?:^|[ \\t])@(?:\"([^\"]+)\"|([^ \\t<>`]+))")
	var refs []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if fence == "" {
				fence = trimmed[:3]
			} else if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		// Inline code describes imports; it does not load them.
		for i, span := range strings.Split(line, "`") {
			if i%2 != 0 {
				continue
			}
			for _, match := range pattern.FindAllStringSubmatch(span, -1) {
				ref := match[1]
				if ref == "" {
					ref = strings.TrimRight(match[2], ".,;:)")
				}
				if strings.ContainsAny(ref, "/.") {
					refs = append(refs, ref)
				}
			}
		}
	}
	return refs
}
