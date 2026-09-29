// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReadFile accepts an explicitly selected quota/stream JSONL file, never a
// credential store. It scans a bounded tail and emits only the latest reading
// per bucket. File timestamps are evidence, not the time of this scan.
func ReadFile(path, source string, now time.Time) ([]Reading, error) {
	if source != "codex" && source != "claude" {
		return nil, errors.New("capacity source must be codex or claude")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid capacity file")
	}
	for _, part := range strings.Split(strings.ToLower(abs), string(filepath.Separator)) {
		if strings.Contains(part, "credential") || strings.HasSuffix(part, ".env") || strings.HasSuffix(part, ".age") || part == "secrets" || part == ".ssh" || part == "auth.json" {
			return nil, errors.New("capacity file must be a quota stream")
		}
	}
	if filepath.Ext(abs) != ".jsonl" && filepath.Ext(abs) != ".json" {
		return nil, errors.New("capacity file must be JSON or JSONL")
	}
	f, err := openReadingFile(abs)
	if err != nil {
		return nil, errors.New("capacity file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("capacity file must be regular")
	}
	const max = 16 << 20
	start := int64(0)
	if info.Size() > max {
		start = info.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, errors.New("capacity file unavailable")
	}
	scan := bufio.NewScanner(io.LimitReader(f, max))
	scan.Buffer(make([]byte, 4096), 1<<20)
	if start > 0 {
		scan.Scan()
	} // discard incomplete first line
	parser := Parser{}
	latest := map[string]Reading{}
	inferred := map[string]bool{}
	for scan.Scan() {
		// An undated quota event preceding later output has no reliable read
		// time: file mtime also changes for non-quota events. Do not refresh it.
		if len(inferred) > 0 {
			parser = Parser{}
		}
		for k := range inferred {
			delete(latest, k)
			delete(inferred, k)
		}
		line := bytes.TrimSpace(scan.Bytes())
		if !bytes.HasSuffix(line, []byte("}")) {
			continue
		}
		at := info.ModTime().UTC()
		dated := false
		var stamp struct {
			Timestamp time.Time `json:"timestamp"`
			ReadAt    time.Time `json:"read_at"`
		}
		if json.Unmarshal(line, &stamp) == nil {
			if !stamp.Timestamp.IsZero() {
				at = stamp.Timestamp
				dated = true
			}
			if !stamp.ReadAt.IsZero() {
				at = stamp.ReadAt
				dated = true
			}
		}
		if at.After(now) {
			continue
		}
		var readings []Reading
		if source == "codex" {
			readings = parser.Codex(line, at)
		} else {
			readings = parser.Claude(line, at)
		}
		if len(readings) > 0 {
			latest = map[string]Reading{}
		}
		for _, r := range readings {
			key := r.WindowKind + "/" + r.Bucket
			if len(latest) >= 32 && latest[key].WindowKind == "" {
				continue
			}
			if !r.ReadAt.Before(latest[key].ReadAt) {
				r.Phase = "update"
				latest[key] = r
				if !dated {
					inferred[key] = true
				}
			}
		}
	}
	if scan.Err() != nil {
		return nil, errors.New("capacity stream exceeds line bound")
	}
	keys := []string{}
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []Reading{}
	for _, k := range keys {
		out = append(out, latest[k])
	}
	return out, nil
}
