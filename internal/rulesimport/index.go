// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// IndexedFile is one doctrine file parsed for a read-only index (AEON-318).
// Rules keep document order; each carries exactly one source reference.
type IndexedFile struct {
	File       SourceFile
	Rules      []Rule
	Unresolved []Unresolved
}

// Classifies reports whether IndexBytes would recognise path as a doctrine
// file. Only the base name is judged; nothing is opened.
func Classifies(path string) bool {
	_, err := classify(path, SectionAll, 0)
	return err == nil
}

// IndexBytes parses one doctrine file whose bytes came from a pinned git
// commit, with the grammar Build uses, so identities, heading paths and line
// ranges match an import of the same file. path is repository-relative and
// only names the file; it is never opened. The bytes get the same checks as a
// local read (size bound, UTF-8, no NUL). The trust-context gate that guards a
// publishable plan does not apply: the index is shown inside the tenant that
// chose the repository, and nothing is proposed or published from it. private
// marks a repository whose doctrine is private as a whole.
func IndexBytes(path string, raw []byte, private bool) (IndexedFile, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return IndexedFile{}, ErrProhibitedPath
	}
	if len(raw) > MaxFileBytes {
		return IndexedFile{}, ErrByteBound
	}
	if bytes.ContainsRune(raw, 0) || !utf8.Valid(raw) {
		return IndexedFile{}, ErrNotText
	}
	file, err := classify(path, SectionAll, len(raw))
	if err != nil {
		return IndexedFile{}, err
	}
	sum := sha256.Sum256(raw)
	file.SHA256 = hex.EncodeToString(sum[:])
	if private {
		file.Trust = ContextPrivate
	}
	parsed, err := parseDocument(file, NormalizeInstructionBytes(raw), SectionAll)
	if err != nil {
		return IndexedFile{}, fmt.Errorf("%s: %w", file.Base, err)
	}
	out := IndexedFile{File: file, Unresolved: parsed.unresolved, Rules: make([]Rule, 0, len(parsed.rules))}
	for _, rule := range parsed.rules {
		out.Rules = append(out.Rules, toRule(built{file: file, rule: rule, lines: parsed.lines}))
	}
	return out, nil
}

// RuleKey is the stable per-file key of a rule: its explicit aeon-rule id, or
// the hash of its normalized text when it has none (the identity's last part).
func RuleKey(r Rule) string {
	if r.ExplicitID != "" {
		return r.ExplicitID
	}
	return r.Identity[strings.LastIndex(r.Identity, "/")+1:]
}
