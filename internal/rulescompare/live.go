// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// LiveSchema is the one-time harness comparison. It is not the offline
// instruction-comparison document.
const LiveSchema = "aeon.rules-comparison.v1"

var liveIdentity = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,95}$`)

// LiveLimits records what this comparison does not claim.
type LiveLimits struct {
	OneTime            bool `json:"one_time"`
	WaitingWindow      bool `json:"waiting_window"`
	RolloutAuthorized  bool `json:"rollout_authorized"`
	FileContentOmitted bool `json:"file_content_omitted"`
	ModelLoadVerified  bool `json:"model_load_verified"`
}

// LiveFile is one loaded file with no text.
type LiveFile struct {
	Logical    string `json:"logical"`
	SHA256     string `json:"sha256"`
	Bytes      int    `json:"bytes"`
	Rules      int    `json:"rules"`
	Unresolved int    `json:"unresolved"`
}

// LiveLocal is the loaded side of a comparison. SetSHA256 covers file hashes,
// not file text.
type LiveLocal struct {
	Files      []LiveFile `json:"files"`
	RuleCount  int        `json:"rule_count"`
	SetSHA256  string     `json:"set_sha256"`
	Unresolved int        `json:"unresolved"`
}

// LiveMerged is the fetched bundle with no body and no rule text.
type LiveMerged struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	RuleCount int    `json:"rule_count"`
}

// LiveRule is one identity in the comparison. Hashes stand in for text.
type LiveRule struct {
	Identity         string   `json:"identity"`
	Status           string   `json:"status"`
	LocalTextSHA256  string   `json:"local_text_sha256,omitempty"`
	MergedTextSHA256 string   `json:"merged_text_sha256,omitempty"`
	Changed          []string `json:"changed,omitempty"`
}

// LiveCounts are the summary counts. Files is the number of loaded files.
type LiveCounts struct {
	Both       int `json:"both"`
	OnlyLocal  int `json:"only_local"`
	OnlyMerged int `json:"only_merged"`
	Differs    int `json:"differs"`
	Files      int `json:"files"`
}

// LiveReport is hashes, statuses and counts. It has no instruction text.
type LiveReport struct {
	Schema     string     `json:"schema"`
	Harness    string     `json:"harness"`
	Role       string     `json:"role"`
	ProjectID  string     `json:"project_id"`
	RepoName   string     `json:"repo_name,omitempty"`
	RepoSHA256 string     `json:"repo_sha256"`
	Limits     LiveLimits `json:"limits"`
	Gaps       []string   `json:"gaps"`
	Merged     LiveMerged `json:"merged"`
	Local      LiveLocal  `json:"local"`
	Rules      []LiveRule `json:"rules"`
	Counts     LiveCounts `json:"counts"`
	Summary    string     `json:"summary"`
}

// Text is the human summary: statuses and identities, no instruction prose.
func (r LiveReport) Text() string {
	var b strings.Builder
	b.WriteString(r.Summary)
	b.WriteByte('\n')
	for _, rule := range r.Rules {
		fmt.Fprintf(&b, "  %s %s\n", rule.Status, rule.Identity)
	}
	return b.String()
}

// DiffChain parses the chain and compares it with merged. merged is the
// caller's already-fetched bundle. Rule text is hashed and then dropped.
func DiffChain(chain Chain, role, projectID string, merged rules.Merged) (LiveReport, error) {
	files := make([]LiveFile, 0, len(chain.Files))
	var local []rulesimport.LoadedRule
	unresolved := 0
	h := sha256.New()
	for _, file := range chain.Files {
		parsed, err := parseChainFile(file)
		if err != nil {
			return LiveReport{}, err
		}
		files = append(files, LiveFile{Logical: file.Logical, SHA256: file.SHA256, Bytes: file.Bytes, Rules: len(parsed.Rules), Unresolved: parsed.Unresolved})
		local = append(local, parsed.Rules...)
		unresolved += parsed.Unresolved
		h.Write([]byte(file.SHA256))
		h.Write([]byte{'\n'})
	}
	seenMerged := map[string]bool{}
	for _, rule := range merged.Rules {
		if rule.Identity == "" || seenMerged[rule.Identity] {
			return LiveReport{}, fmt.Errorf("merged rules need unique identities")
		}
		seenMerged[rule.Identity] = true
	}
	rows := diffLoaded(chain.Harness, local, merged.Rules)
	counts := LiveCounts{Files: len(files)}
	for _, row := range rows {
		switch row.Status {
		case "both":
			counts.Both++
		case "only_local":
			counts.OnlyLocal++
		case "only_merged":
			counts.OnlyMerged++
		case "differs":
			counts.Differs++
		}
	}
	name := chain.RepoName
	if name == "" {
		name = "repository"
	}
	report := LiveReport{
		Schema: LiveSchema, Harness: chain.Harness, Role: role, ProjectID: projectID,
		RepoName: chain.RepoName, RepoSHA256: chain.RepoSHA256,
		Limits: LiveLimits{OneTime: true, FileContentOmitted: true},
		Gaps:   chain.Gaps,
		Merged: LiveMerged{Version: merged.Version, SHA256: merged.SHA256, RuleCount: len(merged.Rules)},
		Local: LiveLocal{
			Files: files, RuleCount: len(local), SetSHA256: hex.EncodeToString(h.Sum(nil)), Unresolved: unresolved,
		},
		Rules: rows, Counts: counts,
		Summary: fmt.Sprintf("%s · %s · both %d, only local %d, only merged %d, differs %d", chain.Harness, name, counts.Both, counts.OnlyLocal, counts.OnlyMerged, counts.Differs),
	}
	if report.Gaps == nil {
		report.Gaps = []string{}
	}
	if report.Rules == nil {
		report.Rules = []LiveRule{}
	}
	return report, nil
}

// parseChainFile reads rules from one loaded instruction file. Claude follows
// @path imports with any filename, so an unrecognized basename is still
// instruction text. The comparison keeps the imported logical name.
func parseChainFile(file ChainFile) (rulesimport.LoadedFile, error) {
	parsed, err := rulesimport.ParseLoaded(file.Logical, file.Text, file.SHA256, file.Bytes)
	if err == nil || !errors.Is(err, rulesimport.ErrUnrecognizedFile) {
		return parsed, err
	}
	parsed, err = rulesimport.ParseLoaded(claudeInstructionName(file.Logical), file.Text, file.SHA256, file.Bytes)
	if err != nil {
		return rulesimport.LoadedFile{}, err
	}
	parsed.Logical = file.Logical
	parsed.SHA256 = file.SHA256
	parsed.Bytes = file.Bytes
	return parsed, nil
}

// claudeInstructionName keeps the logical directory and selects the CLAUDE.md
// parser. The stored comparison name stays the imported filename.
func claudeInstructionName(logical string) string {
	slash := strings.LastIndex(logical, "/")
	if slash < 0 {
		return "CLAUDE.md"
	}
	return logical[:slash+1] + "CLAUDE.md"
}

type localBind struct {
	rule   rulesimport.LoadedRule
	match  string
	report string
}

func diffLoaded(harness string, local []rulesimport.LoadedRule, merged []rules.Rule) []LiveRule {
	byID := map[string]rules.Rule{}
	var mergedOrder []string
	for _, rule := range merged {
		if _, ok := byID[rule.Identity]; ok {
			continue
		}
		byID[rule.Identity] = rule
		mergedOrder = append(mergedOrder, rule.Identity)
	}
	type group struct {
		id       string
		match    string
		items    []rulesimport.LoadedRule
		conflict bool
	}
	var order []string
	groups := map[string]*group{}
	for _, rule := range local {
		bind := bindLocal(rule, byID)
		id := bind.report
		if bind.match != "" {
			id = bind.match
		}
		g := groups[id]
		if g == nil {
			g = &group{id: id, match: bind.match}
			groups[id] = g
			order = append(order, id)
		}
		// Codex lets the later file replace the earlier one. Claude keeps both
		// and reports a local conflict when they disagree.
		if harness == "codex" && len(g.items) > 0 {
			g.items = []rulesimport.LoadedRule{rule}
			g.conflict = false
			g.match = bind.match
			continue
		}
		if bind.match != "" {
			g.match = bind.match
		}
		if len(g.items) > 0 && !sameRule(g.items[0], rule) {
			g.conflict = true
		}
		g.items = append(g.items, rule)
	}
	consumed := map[string]bool{}
	rows := make([]LiveRule, 0, len(order)+len(mergedOrder))
	seen := map[string]int{}
	add := func(row LiveRule) {
		if i, ok := seen[row.Identity]; ok {
			if rows[i].LocalTextSHA256 != row.LocalTextSHA256 {
				mergedHash := rows[i].MergedTextSHA256
				if mergedHash == "" {
					mergedHash = row.MergedTextSHA256
				}
				if mergedHash == "" {
					rows[i] = LiveRule{Identity: row.Identity, Status: "only_local", Changed: []string{"local_conflict"}}
				} else {
					rows[i] = LiveRule{Identity: row.Identity, Status: "differs", Changed: []string{"local_conflict"}, MergedTextSHA256: mergedHash}
				}
			}
			return
		}
		seen[row.Identity] = len(rows)
		rows = append(rows, row)
	}
	for _, id := range order {
		g := groups[id]
		if g.conflict {
			if g.match == "" {
				add(LiveRule{Identity: g.id, Status: "only_local", Changed: []string{"local_conflict"}})
				continue
			}
			m := byID[g.match]
			add(LiveRule{Identity: g.id, Status: "differs", Changed: []string{"local_conflict"}, MergedTextSHA256: sha256Hex(m.Text)})
			consumed[g.match] = true
			continue
		}
		rule := g.items[0]
		if g.match != "" && consumed[g.match] {
			ident := bindLocal(rule, byID).report
			if ident == "" || ident == g.match {
				ident = importIdentity(rule.Identity)
			}
			add(LiveRule{Identity: ident, Status: "only_local", LocalTextSHA256: sha256Hex(rule.Text)})
			continue
		}
		if g.match != "" {
			consumed[g.match] = true
			m := byID[g.match]
			changed := changedFields(rule, m)
			row := LiveRule{Identity: g.match, Status: "both", LocalTextSHA256: sha256Hex(rule.Text), MergedTextSHA256: sha256Hex(m.Text)}
			if len(changed) > 0 {
				row.Status = "differs"
				row.Changed = changed
			}
			add(row)
			continue
		}
		add(LiveRule{Identity: g.id, Status: "only_local", LocalTextSHA256: sha256Hex(rule.Text)})
	}
	for _, id := range mergedOrder {
		if consumed[id] {
			continue
		}
		m := byID[id]
		add(LiveRule{Identity: id, Status: "only_merged", MergedTextSHA256: sha256Hex(m.Text)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Identity < rows[j].Identity })
	return rows
}

func bindLocal(rule rulesimport.LoadedRule, merged map[string]rules.Rule) localBind {
	imp := importIdentity(rule.Identity)
	if rule.ExplicitID != "" {
		if _, ok := merged[rule.ExplicitID]; ok {
			return localBind{rule: rule, match: rule.ExplicitID, report: rule.ExplicitID}
		}
	}
	if liveIdentity.MatchString(rule.Identity) {
		if _, ok := merged[rule.Identity]; ok {
			return localBind{rule: rule, match: rule.Identity, report: rule.Identity}
		}
	}
	if _, ok := merged[imp]; ok {
		return localBind{rule: rule, match: imp, report: imp}
	}
	if rule.ExplicitID != "" && liveIdentity.MatchString(rule.ExplicitID) {
		return localBind{rule: rule, report: rule.ExplicitID}
	}
	return localBind{rule: rule, report: imp}
}

func sameRule(a, b rulesimport.LoadedRule) bool {
	return a.Text == b.Text && a.Why == b.Why && a.Strength == b.Strength && a.Enabled == b.Enabled
}

func changedFields(local rulesimport.LoadedRule, merged rules.Rule) []string {
	var changed []string
	if local.Text != merged.Text {
		changed = append(changed, "text")
	}
	if local.Why != merged.Why {
		changed = append(changed, "why")
	}
	if local.Strength != merged.Strength {
		changed = append(changed, "strength")
	}
	if local.Enabled != merged.Enabled {
		changed = append(changed, "enabled")
	}
	return changed
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
