// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

const (
	// maxImportDepth is Claude's four-hop include limit. The file that
	// contains the first import is hop zero.
	maxImportDepth = 4
	maxImportFiles = 32
	maxImportBytes = 256 * 1024
	maxChainFiles  = 64
)

// expandImports loads @path includes from one Claude instruction file.
// A skipped include sets the omission flag and does not fail the chain.
func (b *builder) expandImports(parentPath, text string, depth int) {
	specs := claudeImports(text)
	if len(specs) == 0 {
		return
	}
	if depth > maxImportDepth {
		b.omittedImport = true
		return
	}
	root := b.importRoot(parentPath)
	if root == "" {
		b.omittedImport = true
		return
	}
	for _, spec := range specs {
		if b.importCount >= maxImportFiles || b.importBytes >= maxImportBytes || len(b.files) >= maxChainFiles {
			b.omittedImport = true
			return
		}
		target, ok := resolveImport(spec, parentPath, root, b.home)
		if !ok || b.seen[target] {
			if !ok {
				b.omittedImport = true
			}
			continue
		}
		remain := maxImportBytes - b.importBytes
		body, sum, size, err := rulesimport.ReadContained(target, []string{root}, remain)
		if err != nil {
			b.omittedImport = true
			continue
		}
		b.seen[target] = true
		b.importCount++
		b.importBytes += size
		if !b.add(b.logicalFor(target, root), body, sum, size) {
			b.omittedImport = true
			return
		}
		b.expandImports(target, body, depth+1)
	}
}

func (b *builder) importRoot(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if b.repo != "" && withinRoot(abs, b.repo) {
		return b.repo
	}
	if b.home != "" && withinRoot(abs, b.home) {
		return b.home
	}
	return ""
}

func (b *builder) logicalFor(path, root string) string {
	if b.home != "" && root == b.home && root != b.repo {
		rel, err := filepath.Rel(b.home, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return "user/" + filepath.Base(path)
		}
		return "user/" + filepath.ToSlash(rel)
	}
	return logicalName(b.repo, filepath.Dir(path), filepath.Base(path))
}

func withinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func resolveImport(spec, parentFile, root, home string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, '#'); i >= 0 {
		spec = spec[:i]
	}
	spec = strings.ReplaceAll(spec, `\ `, " ")
	spec = strings.TrimRight(spec, ".,;:)]>")
	if spec == "" || spec == "." || strings.ContainsRune(spec, 0) || strings.Contains(spec, "://") {
		return "", false
	}
	var path string
	switch {
	case strings.HasPrefix(spec, "~/"):
		if home == "" {
			return "", false
		}
		path = filepath.Join(home, strings.TrimPrefix(spec, "~/"))
	case strings.HasPrefix(spec, "~"):
		return "", false
	case filepath.IsAbs(spec):
		path = spec
	default:
		path = filepath.Join(filepath.Dir(parentFile), spec)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if !withinRoot(abs, root) || !importExtensionOK(abs) {
		return "", false
	}
	return abs, true
}

func importExtensionOK(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case "", ".md", ".markdown", ".txt":
		return true
	default:
		return false
	}
}

// claudeImports returns @path specs outside fences and inline code spans.
func claudeImports(text string) []string {
	var out []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trim, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			fence = trim[:3]
			continue
		}
		out = append(out, importsOutsideSpans(line)...)
	}
	return out
}

func importsOutsideSpans(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		if line[i] == '`' {
			j := i + 1
			for j < len(line) && line[j] != '`' {
				j++
			}
			if j < len(line) {
				i = j + 1
				continue
			}
		}
		if line[i] == '@' && (i == 0 || importSpace(line[i-1])) {
			j := i + 1
			for j < len(line) && !importSpace(line[j]) {
				j++
			}
			spec := line[i+1 : j]
			if validImportSpec(spec) {
				out = append(out, spec)
			}
			i = j
			continue
		}
		i++
	}
	return out
}

func importSpace(b byte) bool {
	return b == ' ' || b == '\t'
}

func validImportSpec(spec string) bool {
	if spec == "" || strings.Contains(spec, "://") {
		return false
	}
	path := spec
	if i := strings.IndexByte(path, '#'); i >= 0 {
		path = path[:i]
	}
	if path == "" || strings.HasPrefix(path, "@") {
		return false
	}
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "~/") || (strings.HasPrefix(path, "/") && path != "/") {
		return true
	}
	c := path[0]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.' || c == '_' || c == '-':
		return true
	default:
		return false
	}
}
