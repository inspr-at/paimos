// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// PinnedRule is one doctrine rule the harness channel is expected to carry.
// Identity is the AEON-318 index identity. Text is compared, never echoed
// by DeliveryReport.
type PinnedRule struct {
	Identity string
	Key      string
	Text     string
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
		} else if file.Session && !strings.Contains(file.Text, "# Aeon session rules") {
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
	text := normalizedDeliveryText(file.Text)
	for _, rel := range releases {
		var ids []string
		for _, rule := range rel.Rules {
			if rule.Text != "" && strings.Contains(text, normalizedDeliveryText(rule.Text)) {
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
	if !file.Session && !strings.Contains(file.Text, "# Aeon session rules") {
		return nil
	}
	var lines []string
	text := normalizedDeliveryText(file.Text)
	seen := map[string]bool{}
	for _, rel := range releases {
		for _, rule := range rel.Rules {
			if rule.Text == "" || seen[rule.Identity] || !strings.Contains(text, normalizedDeliveryText(rule.Text)) {
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
