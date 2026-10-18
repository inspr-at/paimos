// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

// sourceArchive bypasses git archive's candidate-controlled export attributes.
// It exports every immutable regular blob with verified bytes, paths and modes.
// Symlinks/submodules are unsupported and refuse this executor, retaining full CI.
func (r *Repository) sourceArchive(ctx context.Context, s Snapshot) ([]byte, error) {
	var query strings.Builder
	for _, e := range s.Entries {
		if (e.Mode != "100644" && e.Mode != "100755") || e.Path == "." || path.Clean(e.Path) != e.Path || strings.HasPrefix(e.Path, "/") || strings.HasPrefix(e.Path, "../") || strings.Contains(e.Path, "\\") || e.Path == ".git" || strings.HasPrefix(e.Path, ".git/") || strings.Contains(e.Path, "\x00") {
			return nil, fmt.Errorf("unsupported candidate export path/mode")
		}
		query.WriteString(e.Blob + "\n")
	}
	raw, err := r.read(ctx, []byte(query.String()), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	var out boundedBuffer
	w := tar.NewWriter(&out)
	for _, e := range s.Entries {
		header := []byte{}
		for {
			c, err := reader.ReadByte()
			if err != nil || len(header) > 256 {
				return nil, fmt.Errorf("invalid export blob header")
			}
			if c == '\n' {
				break
			}
			header = append(header, c)
		}
		var id, kind string
		var size int64
		if _, err := fmt.Sscanf(string(header), "%s %s %d", &id, &kind, &size); err != nil || id != e.Blob || kind != "blob" || size < 0 || size > maxGitOutput {
			return nil, fmt.Errorf("invalid export blob identity")
		}
		b := make([]byte, int(size))
		if _, err := io.ReadFull(reader, b); err != nil {
			return nil, fmt.Errorf("truncated export blob")
		}
		if c, err := reader.ReadByte(); err != nil || c != '\n' || Hash("blob", b) != e.ContentDigest {
			return nil, fmt.Errorf("export blob content mismatch")
		}
		mode := int64(0644)
		if e.Mode == "100755" {
			mode = 0755
		}
		if err := w.WriteHeader(&tar.Header{Name: e.Path, Mode: mode, Size: size, Typeflag: tar.TypeReg}); err != nil {
			return nil, fmt.Errorf("candidate archive header refused")
		}
		if _, err := w.Write(b); err != nil {
			return nil, fmt.Errorf("candidate archive limit")
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("unexpected export blob data")
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
