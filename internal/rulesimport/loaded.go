// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import "path/filepath"

// LoadedRule is one rule read from a harness instruction file.
// Details and lineage are omitted so a comparison can match imported rules
// without treating the importer's annotation as a difference.
type LoadedRule struct {
	Identity   string
	ExplicitID string
	Text       string
	Why        string
	Strength   string
	Enabled    bool
}

// LoadedFile is a parsed instruction file. Unresolved counts choices the
// parser did not guess through. Their notes can quote the file, so only the
// count leaves this package.
type LoadedFile struct {
	Logical    string
	SHA256     string
	Bytes      int
	Rules      []LoadedRule
	Unresolved int
}

// ParseLoaded parses instruction text that was already read. logical is a
// display name such as user/CLAUDE.md or repo/AGENTS.md, never an absolute
// path. AGENTS.override.md and CLAUDE.local.md use the AGENTS.md and CLAUDE.md
// parsers. Trust is private so a mixed personal file still yields its rules;
// this is a comparison, not an import.
func ParseLoaded(logical, body, rawSHA string, size int) (LoadedFile, error) {
	base := filepath.Base(logical)
	parserBase := base
	switch base {
	case "AGENTS.override.md":
		parserBase = "AGENTS.md"
	case "CLAUDE.local.md":
		parserBase = "CLAUDE.md"
	}
	file, err := classify(parserBase, SectionAll, size)
	if err != nil {
		return LoadedFile{}, err
	}
	if err = classifyContent(&file, body, SectionAll, ContextPrivate); err != nil {
		return LoadedFile{}, err
	}
	file.Trust = ContextPrivate
	file.Path = logical
	file.Base = base
	file.SHA256 = rawSHA
	file.Bytes = size
	parsed, err := parseDocument(file, body, SectionAll)
	if err != nil {
		return LoadedFile{}, err
	}
	rules := make([]LoadedRule, 0, len(parsed.rules))
	for _, raw := range parsed.rules {
		rule := toRule(built{file: file, rule: raw, lines: parsed.lines})
		rules = append(rules, LoadedRule{
			Identity:   rule.Identity,
			ExplicitID: rule.ExplicitID,
			Text:       rule.Text,
			Why:        rule.Why,
			Strength:   rule.Strength,
			Enabled:    rule.Enabled,
		})
	}
	return LoadedFile{Logical: logical, SHA256: rawSHA, Bytes: size, Rules: rules, Unresolved: len(parsed.unresolved)}, nil
}
