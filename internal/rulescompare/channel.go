// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/markdownsource"
	"github.com/yuin/goldmark/ast"
)

// PinnedRule is one doctrine rule the harness channel is expected to carry.
// Identity is the AEON-318 index identity. Text is compared, never echoed
// by DeliveryReport.
type PinnedRule struct {
	Identity string
	Key      string
	Text     string
	Source   string // Complete indexed source, when supplied by the doctrine API.
}

// PinnedRelease is one doctrine pin and the rules indexed at that commit.
type PinnedRelease struct {
	Repository string
	Ref        string
	Commit     string
	State      string
	Error      string
	Rules      []PinnedRule
}

// ServedDuplicate is a published Aeon rule that copies a doctrine rule.
type ServedDuplicate struct {
	Identity string
	Doctrine string
}

// HarnessFile is the rendered instruction file for one harness. Missing means
// the session hook is installed and the file is not there.
type HarnessFile struct {
	Harness    string
	Path       string
	Session    bool
	Missing    bool
	Unverified bool
	Text       string
}

// DeliveryReport is the aeon doctor form of the rules comparison (AEON-320).
// It reports harness drift from the pinned doctrine and whether a rule is
// served twice. The ok detail is exactly "no rule served twice".
func DeliveryReport(files []HarnessFile, releases []PinnedRelease, duplicates []ServedDuplicate) (status, detail string) {
	sort.Slice(files, func(i, j int) bool { return files[i].Harness < files[j].Harness })
	var problems []string
	for _, rel := range releases {
		_, commitErr := hex.DecodeString(rel.Commit)
		if rel.State != "ready" || rel.Error != "" || rel.Repository == "" || len(rel.Commit) != 40 || commitErr != nil {
			return "fail", "doctrine index unverified: " + rel.Repository
		}
		for _, rule := range rel.Rules {
			if rule.Identity == "" || strings.TrimSpace(rule.Text) == "" {
				return "fail", "doctrine index unverified: " + rel.Repository
			}
		}
	}
	if len(files) == 0 && len(releases) > 0 {
		return "warn", "no delivered files available to verify"
	}
	for _, file := range files {
		if file.Missing {
			problems = append(problems, file.label()+": rendered harness file missing")
			continue
		}
		if file.Unverified {
			problems = append(problems, file.label()+": harness imports unverified")
			problems = append(problems, sessionDoubles(file, releases)...)
			continue
		}
		if strings.TrimSpace(file.Text) == "" {
			problems = append(problems, file.label()+": delivered file is empty")
		} else if file.Session && !hasSessionHeading(file.Text) {
			problems = append(problems, file.label()+": session file is unverified")
		}
		if !file.Session {
			problems = append(problems, driftLines(file, releases)...)
		}
		problems = append(problems, sessionDoubles(file, releases)...)
	}
	dups := append([]ServedDuplicate(nil), duplicates...)
	sort.Slice(dups, func(i, j int) bool {
		if dups[i].Identity != dups[j].Identity {
			return dups[i].Identity < dups[j].Identity
		}
		return dups[i].Doctrine < dups[j].Doctrine
	})
	for _, dup := range dups {
		problems = append(problems, fmt.Sprintf("rule served twice: %s duplicates %s. Propose a change", dup.Identity, dup.Doctrine))
	}
	if len(problems) == 0 {
		return "ok", "no rule served twice"
	}
	for _, problem := range problems {
		if !strings.HasSuffix(problem, "rendered harness file missing") && !strings.HasSuffix(problem, "harness imports unverified") {
			return "fail", strings.Join(problems, "; ")
		}
	}
	return "warn", strings.Join(problems, "; ")
}

func (f HarnessFile) label() string {
	if f.Path != "" {
		return f.Harness + " (" + f.Path + ")"
	}
	return f.Harness
}

func driftLines(file HarnessFile, releases []PinnedRelease) []string {
	var lines []string
	instructions := deliveredInstructions(file)
	for _, rel := range releases {
		var ids []string
		for _, rule := range rel.Rules {
			if matchesInstruction(instructions, rule) {
				continue
			}
			ids = append(ids, rule.Identity)
		}
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		if len(ids) > 5 {
			ids = append(ids[:5], "…")
		}
		ref := rel.Ref
		if ref == "" {
			ref = rel.Commit
		}
		lines = append(lines, fmt.Sprintf("%s: harness file drifted from %s@%s (%s)", file.label(), rel.Repository, ref, strings.Join(ids, ", ")))
	}
	return lines
}

// sessionDoubles reports doctrine text that was pasted into a session file
// the harness is also loading. The doctrine file itself has no session header.
func sessionDoubles(file HarnessFile, releases []PinnedRelease) []string {
	if !file.Session && !hasSessionHeading(file.Text) {
		return nil
	}
	var lines []string
	instructions := deliveredInstructions(file)
	seen := map[string]bool{}
	for _, rel := range releases {
		for _, rule := range rel.Rules {
			// The session renderer emits the action text only, without source
			// explanations. It must still be a complete active instruction.
			if rule.Text == "" || seen[rule.Identity] || !instructions[normalizedDeliveryText(rule.Text)] {
				continue
			}
			seen[rule.Identity] = true
			lines = append(lines, fmt.Sprintf("rule served twice: doctrine %s is in the %s session file. Propose a change", rule.Identity, file.label()))
		}
	}
	sort.Strings(lines)
	return lines
}

func normalizedDeliveryText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func matchesInstruction(instructions map[string]bool, rule PinnedRule) bool {
	if rule.Source == "" {
		return rule.Text != "" && instructions[normalizedDeliveryText(rule.Text)]
	}
	expected := deliveredInstructions(HarnessFile{Text: rule.Source})
	if len(expected) == 0 {
		return false
	}
	for text := range expected {
		if !instructions[text] {
			return false
		}
	}
	return true
}

// Compare whole instruction blocks, preserving Markdown and strength markers.
// Quoted examples, HTML/comments and code are not instructions. Only the
// session renderer's explicit [identity] prefix is stripped; arbitrary prose
// surrounding a rule never constitutes evidence that the rule was delivered.
func deliveredInstructions(file HarnessFile) map[string]bool {
	raw, doc, _ := markdownsource.Document(file.Text)
	session := file.Session || sessionHeading(raw, doc)
	out := map[string]bool{}
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindBlockquote, ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock:
			return ast.WalkSkipChildren, nil
		case ast.KindParagraph, ast.KindTextBlock:
			var body strings.Builder
			for i := 0; i < n.Lines().Len(); i++ {
				segment := n.Lines().At(i)
				body.Write(segment.Value(raw))
			}
			instruction := normalizedDeliveryText(body.String())
			if session {
				if rest, ok := strings.CutPrefix(instruction, "["); ok {
					if id, text, ok := strings.Cut(rest, "] "); ok && id != "" && !strings.ContainsAny(id, " \t[]") {
						instruction = text
					}
				}
			}
			out[instruction] = true
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out
}

func hasSessionHeading(source string) bool {
	raw, doc, _ := markdownsource.Document(source)
	return sessionHeading(raw, doc)
}

func sessionHeading(raw []byte, doc ast.Node) bool {
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && h.Level == 1 && string(h.Text(raw)) == "Aeon session rules" {
			return true
		}
	}
	return false
}
