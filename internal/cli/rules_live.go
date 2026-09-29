// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulescompare"
)

var rulesUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cmdRules is the live harness comparison. The hyphenated rules-compare and
// rules-import commands stay separate.
func (rt *runtime) cmdRules() *Command {
	return &Command{
		Name:  "rules",
		Short: "Compare loaded harness instructions with merged rules",
		Use:   "rules compare --harness claude|codex --repo PATH --project KEY --role ROLE",
		subs:  []*Command{rt.cmdRulesCompareLive()},
	}
}

func (rt *runtime) cmdRulesCompareLive() *Command {
	var harness, repo, project, role, person, agent, task, home, outPath string
	var upload bool
	return &Command{
		Name:    "compare",
		Short:   "Compare the files one harness loads with merged rules",
		Use:     "compare --harness claude|claude-code|codex --repo PATH --project KEY --role coordinator|builder|reviewer|operator [--person UUID] [--agent UUID] [--task UUID] [--home PATH] [--upload] [--out FILE.json]",
		Long:    "One comparison, with no waiting period. Reads the CLAUDE.md or AGENTS.md chain that harness loads for --repo and diffs it against GET /api/rules/merged for this project, person, role and harness. The report is statuses, identities and hashes. --upload stores that summary only. Instruction text is not uploaded, and nothing is written over AGENTS.md or CLAUDE.md.",
		minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&harness, "harness", 0, "claude, claude-code, or codex")
			fs.string(&repo, "repo", 0, "launch directory whose instruction chain is read")
			fs.string(&project, "project", 0, "project key or UUID")
			fs.string(&role, "role", 0, "coordinator, builder, reviewer, or operator")
			fs.string(&person, "person", 0, "person UUID; defaults to the caller when the caller is a person")
			fs.string(&agent, "agent", 0, "agent UUID; defaults to the caller when the caller is an agent")
			fs.string(&task, "task", 0, "optional task UUID; requires an agent")
			fs.string(&home, "home", 0, "home directory for user instruction files; defaults to the user home")
			fs.bool(&upload, "upload", 0, "store the hash and count summary for this project")
			fs.string(&outPath, "out", 0, "write the JSON report; never overwrites")
		},
		run: func([]string) error {
			canonical, err := canonicalHarness(harness)
			if err != nil {
				return err
			}
			if strings.TrimSpace(repo) == "" || strings.TrimSpace(project) == "" {
				return usagef("rules compare requires --harness, --repo, --project and --role")
			}
			if !slices.Contains(rules.Roles, role) {
				return usagef("--role must be coordinator, builder, reviewer, or operator")
			}
			if task != "" && agent == "" {
				return usagef("--task requires --agent")
			}
			for _, pair := range []struct{ name, value string }{{"person", person}, {"agent", agent}, {"task", task}} {
				if pair.value != "" && !rulesUUID.MatchString(strings.ToLower(pair.value)) {
					return usagef("--%s must be a UUID", pair.name)
				}
			}
			if home == "" {
				home, err = os.UserHomeDir()
				if err != nil {
					return usagef("cannot resolve the home directory; pass --home")
				}
			}
			chain, err := rulescompare.LoadChain(canonical, home, repo)
			if err != nil {
				return err
			}
			projectID, personID, agentID, err := rt.rulesCompareContext(project, person, agent)
			if err != nil {
				return err
			}
			if task != "" && agentID == "" {
				return usagef("--task requires --agent")
			}
			merged, err := rt.fetchMerged(projectID, personID, agentID, role, canonical, strings.ToLower(task))
			if err != nil {
				return err
			}
			report, err := rulescompare.DiffChain(chain, role, projectID, merged)
			if err != nil {
				return err
			}
			raw, err := marshalLive(report)
			if err != nil {
				return err
			}
			if rt.jsonOut {
				if _, err = rt.stdout.Write(raw); err != nil {
					return err
				}
			} else if _, err = rt.stdout.Write([]byte(report.Text())); err != nil {
				return err
			}
			if outPath != "" {
				if err = rules.WriteFile(outPath, raw, false); err != nil {
					return err
				}
			}
			if upload {
				if err = rt.uploadComparison(comparisonUpload(report, agentID)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func canonicalHarness(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude", "claude-code":
		return "claude-code", nil
	case "codex":
		return "codex", nil
	default:
		return "", usagef("--harness must be claude, claude-code, or codex")
	}
}

func (rt *runtime) rulesCompareContext(project, person, agent string) (string, string, string, error) {
	me, err := rt.rulesMe()
	if err != nil {
		return "", "", "", err
	}
	projectID := strings.ToLower(strings.TrimSpace(project))
	if !rulesUUID.MatchString(projectID) {
		node, err := rt.projectNode(project)
		if err != nil {
			return "", "", "", err
		}
		projectID = strings.ToLower(node.ID)
		if !rulesUUID.MatchString(projectID) {
			return "", "", "", usagef("project %q has no UUID", project)
		}
	}
	personID := strings.ToLower(strings.TrimSpace(person))
	if personID == "" {
		if me.Principal.Kind != "person" {
			return "", "", "", usagef("--person is required when the caller is an agent")
		}
		personID = strings.ToLower(me.Principal.ID)
	}
	if !rulesUUID.MatchString(personID) {
		return "", "", "", usagef("--person must be a UUID")
	}
	agentID := strings.ToLower(strings.TrimSpace(agent))
	if me.Principal.Kind == "agent" {
		caller := strings.ToLower(me.Principal.ID)
		if agentID == "" {
			agentID = caller
		}
		if agentID != caller {
			return "", "", "", usagef("--agent must be the calling agent")
		}
	}
	if agentID != "" && !rulesUUID.MatchString(agentID) {
		return "", "", "", usagef("--agent must be a UUID")
	}
	return projectID, personID, agentID, nil
}

func (rt *runtime) rulesMe() (client.Me, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var me client.Me
	err := rt.doCtx(ctx, http.MethodGet, "/api/me", nil, &me)
	return me, err
}

func (rt *runtime) fetchMerged(project, person, agent, role, harness, task string) (rules.Merged, error) {
	q := "project_id=" + project + "&person_id=" + person + "&role=" + role + "&harness=" + harness
	if agent != "" {
		q += "&agent_id=" + agent
	}
	if task != "" {
		q += "&task_id=" + task
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var merged rules.Merged
	err := rt.doCtx(ctx, http.MethodGet, "/api/rules/merged?"+q, nil, &merged)
	return merged, err
}

func marshalLive(report rulescompare.LiveReport) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(report); err != nil {
		return nil, err
	}
	if buf.Len() > rules.MaxCacheBytes {
		return nil, usagef("comparison report exceeds the byte bound")
	}
	return buf.Bytes(), nil
}

type rulesComparisonUpload struct {
	Harness    string                  `json:"harness"`
	Role       string                  `json:"role"`
	ProjectID  string                  `json:"project_id"`
	AgentID    string                  `json:"agent_id,omitempty"`
	RepoSHA256 string                  `json:"repo_sha256"`
	RepoName   string                  `json:"repo_name,omitempty"`
	Merged     rulescompare.LiveMerged `json:"merged"`
	Local      rulesComparisonLocal    `json:"local"`
	Rules      []rulescompare.LiveRule `json:"rules"`
	Counts     rulescompare.LiveCounts `json:"counts"`
}

type rulesComparisonLocal struct {
	SetSHA256 string `json:"set_sha256"`
	RuleCount int    `json:"rule_count"`
}

func comparisonUpload(report rulescompare.LiveReport, agentID string) rulesComparisonUpload {
	return rulesComparisonUpload{
		Harness: report.Harness, Role: report.Role, ProjectID: report.ProjectID, AgentID: agentID,
		RepoSHA256: report.RepoSHA256, RepoName: report.RepoName,
		Merged: report.Merged,
		Local:  rulesComparisonLocal{SetSHA256: report.Local.SetSHA256, RuleCount: report.Local.RuleCount},
		Rules:  report.Rules, Counts: report.Counts,
	}
}

func (rt *runtime) uploadComparison(body rulesComparisonUpload) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return rt.doCtx(ctx, http.MethodPost, "/api/rules/comparisons", body, nil)
}
