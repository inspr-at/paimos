//go:build darwin || linux

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/sys/unix"
)

var errAttachTranscript = errors.New("transcript identity or format unsafe; watch detached")

// A transcript is untrusted data. Pin every path component without following
// links; hold the opened regular file. Never reopen a replacement or rewind.
type attachTail struct {
	activityEnabled bool
	activity        *agentactivity.Activity
	file            *os.File
	path, id        string
	offset          int64
	partial         []byte
	discard         bool
	redactor        attachRedactor
}

func openAttachTail(path string, uid int) (*attachTail, error) {
	if !attachwatch.PhysicalPath(path) {
		return nil, errAttachTranscript
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errAttachTranscript
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if e != nil {
			return nil, errAttachTranscript
		}
		fd = next
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || int(st.Uid) != uid || st.Mode&0022 != 0 {
		unix.Close(fd)
		return nil, errAttachTranscript
	}
	f := os.NewFile(uintptr(fd), "attached-transcript")
	t := &attachTail{file: f, path: path, id: fmt.Sprintf("%d:%d", st.Dev, st.Ino)}
	if err = t.startNow(); err != nil {
		f.Close()
		return nil, err
	}
	return t, nil
}
func (t *attachTail) close() { _ = t.file.Close(); clear(t.partial); t.partial = nil }
func (t *attachTail) check() error {
	var opened, named unix.Stat_t
	if unix.Fstat(int(t.file.Fd()), &opened) != nil || unix.Lstat(t.path, &named) != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&unix.S_IFMT != unix.S_IFREG || opened.Nlink != 1 || named.Nlink != 1 || opened.Dev != named.Dev || opened.Ino != named.Ino || opened.Size < t.offset || opened.Mode&0022 != 0 {
		return errAttachTranscript
	}
	// Also refuse ancestor replacement with a symlink, even when it targets the
	// same inode. This check never opens or reads a replacement transcript.
	physical, err := filepath.EvalSymlinks(t.path)
	if err != nil || physical != t.path {
		return errAttachTranscript
	}
	return nil
}
func (t *attachTail) startNow() error {
	if err := t.check(); err != nil {
		return err
	}
	size, err := t.file.Seek(0, io.SeekEnd)
	if err != nil {
		return errAttachTranscript
	}
	t.offset = size
	t.partial = nil
	t.discard = false
	t.redactor = attachRedactor{}
	if size > 0 {
		// Inspect just the record boundary; no historical record is ever read/uploaded.
		var last [1]byte
		if _, err = t.file.ReadAt(last[:], size-1); err != nil {
			return errAttachTranscript
		}
		t.discard = last[0] != '\n'
	}
	return nil
}
func (t *attachTail) next() (string, error) {
	t.activity = nil
	if err := t.check(); err != nil {
		return "", err
	}
	// Bound work, memory and output per poll even under an adversarial writer.
	var input [32 << 10]byte
	n, err := t.file.Read(input[:])
	if err != nil && err != io.EOF {
		return "", errAttachTranscript
	}
	t.offset += int64(n)
	var out strings.Builder
	for _, b := range input[:n] {
		if b == '\n' {
			if !t.discard {
				if t.activityEnabled {
					if a := agentactivity.TranscriptTool(t.partial); a != nil {
						t.activity = a
					}
				}
				if line, ok := t.redactor.record(t.partial); ok && out.Len()+len(line)+1 <= attachwatch.MaxText {
					out.WriteString(line)
					out.WriteByte('\n')
				}
			}
			clear(t.partial)
			t.partial = t.partial[:0]
			t.discard = false
			continue
		}
		if t.discard {
			continue
		}
		if len(t.partial) >= attachwatch.MaxText {
			clear(t.partial)
			t.partial = t.partial[:0]
			t.discard = true
			continue
		}
		t.partial = append(t.partial, b)
	}
	clear(input[:])
	if err = t.check(); err != nil {
		return "", err
	}
	return out.String(), nil
}

var attachSensitive = regexp.MustCompile(`(?i)(api[ _-]?key|access[ _-]?token|refresh[ _-]?token|authorization|bearer[ :]+|password|passwd|client[ _-]?secret|private[ _-]?key|device[ _-]?proof|lifecycle[ _-]?secret|aeon_[a-z0-9_]+|sk-[a-z0-9_-]+|gh[pousr]_[a-z0-9_]+|AKIA[A-Z0-9]+)`)
var attachEnv = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])[A-Z_][A-Z0-9_]*\s*=\s*\S`)
var attachAssignment = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:secret|token)[\s"']*[:=]`)
var attachURLUserinfo = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*:)?//[^\s/?#]*@`)
var attachOpaque = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)

type attachRedactor struct{ privateKey bool }

func (r *attachRedactor) line(raw []byte) (string, bool) {
	if len(raw) > attachwatch.MaxText || !utf8.Valid(raw) {
		return "", false
	}
	s := string(bytes.TrimSuffix(raw, []byte{'\r'}))
	if strings.Contains(s, "-----BEGIN ") {
		r.privateKey = true
	}
	// Combining marks can split both keywords and opaque-token matches. Drop
	// the entire line rather than normalize and risk displaying a split secret.
	if strings.ContainsFunc(s, func(v rune) bool { return unicode.In(v, unicode.Mn) }) {
		return "", false
	}
	if r.privateKey {
		if strings.Contains(s, "-----END ") {
			r.privateKey = false
		}
		return "[redacted]", true
	}
	// Dropping the whole record prevents partially redacted JSON or split values.
	if attachSensitive.MatchString(s) || attachEnv.MatchString(s) || attachAssignment.MatchString(s) || attachURLUserinfo.MatchString(s) || attachOpaque.MatchString(s) {
		return "[redacted]", true
	}
	for _, v := range s {
		if unicode.IsControl(v) && v != '\t' || unicode.In(v, unicode.Cf) {
			return "", false
		}
	}
	return s, true
}

// Known JSONL conversation records are decoded before redaction, so escaped
// credential names cannot bypass matching. Tool payloads and unknown structured
// records are dropped; ordinary line-oriented text remains supported.
func (r *attachRedactor) record(raw []byte) (string, bool) {
	text := string(raw)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var entry map[string]json.RawMessage
		if json.Unmarshal(trimmed, &entry) != nil {
			return "", false
		}
		var kind string
		_ = json.Unmarshal(entry["type"], &kind)
		payload := entry
		if kind == "response_item" || kind == "event_msg" {
			if json.Unmarshal(entry["payload"], &payload) != nil {
				return "", false
			}
			_ = json.Unmarshal(payload["type"], &kind)
		}
		switch kind {
		case "assistant", "user":
			if json.Unmarshal(entry["message"], &payload) != nil {
				return "", false
			}
		case "agent_message", "user_message":
			if json.Unmarshal(payload["message"], &text) != nil {
				return "", false
			}
			return r.multiline(text)
		case "message":
		default:
			return "", false
		}
		if json.Unmarshal(payload["content"], &text) != nil {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(payload["content"], &content) != nil {
				return "", false
			}
			var parts []string
			for _, p := range content {
				if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
					parts = append(parts, p.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
	}
	return r.multiline(text)
}
func (r *attachRedactor) multiline(text string) (string, bool) {
	if len(text) > attachwatch.MaxText || !utf8.ValidString(text) {
		return "", false
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		clean, ok := r.line([]byte(line))
		if !ok {
			return "", false
		}
		out = append(out, clean)
	}
	return strings.Join(out, "\n"), text != ""
}
