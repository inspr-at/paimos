// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const onboardHeaderPrefix = "<!-- paimos: onboarded "

type briefing struct {
	project apiNode
	key     string
	agent   string
	kinds   kindTable
	issues  []apiNode
	entries []apiNode
}

func (rt *runtime) onboard(project, agent, format, outPath string, check bool, readingLimit int, includeLow bool) error {
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "html" {
		return usagef("--format must be md or html")
	}
	if check && strings.TrimSpace(outPath) == "" {
		return usagef("--check requires --out")
	}
	if readingLimit <= 0 {
		readingLimit = 10
	}
	proj, err := rt.projectNode(project)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	children, err := rt.walkNodes(url.Values{"parent_id": {proj.ID}, "include_descendants": {"true"}}, nil)
	if err != nil {
		return err
	}
	b := briefing{project: proj, key: projectDisplayKey(proj, project), agent: strings.TrimSpace(agent), kinds: kinds}
	for _, n := range children {
		slug := kinds.slug(n.KindID)
		switch {
		case issueKinds[slug]:
			b.issues = append(b.issues, n)
		case knowledgeSupported(slug):
			b.entries = append(b.entries, n)
		}
	}
	slices.SortFunc(b.issues, func(a, c apiNode) int { return strings.Compare(a.Key, c.Key) })
	slices.SortFunc(b.entries, func(a, c apiNode) int {
		if x := strings.Compare(b.kinds.slug(a.KindID), b.kinds.slug(c.KindID)); x != 0 {
			return x
		}
		return strings.Compare(fieldString(fieldMap(a.Fields), "slug"), fieldString(fieldMap(c.Fields), "slug"))
	})
	rendered := b.markdown(rt.program, readingLimit, includeLow)
	if format == "html" {
		rendered = "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>" + htmlEscape(b.project.Title) + "</title></head><body><pre>" + htmlEscape(rendered) + "</pre></body></html>\n"
	}
	raw, err := json.Marshal(struct {
		Project      apiNode            `json:"project"`
		Agent        string             `json:"agent"`
		Issues       []apiNode          `json:"issues"`
		Entries      []apiNode          `json:"entries"`
		Kinds        map[string]apiKind `json:"kinds"`
		Program      string             `json:"program"`
		Format       string             `json:"format"`
		ReadingLimit int                `json:"reading_limit"`
		IncludeLow   bool               `json:"include_low"`
		Content      string             `json:"content"`
	}{b.project, b.agent, b.issues, b.entries, b.kinds.byID, rt.program, format, readingLimit, includeLow, rendered})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	rev := hex.EncodeToString(sum[:])[:12]
	outPath = resolveOnboardOutPath(outPath, format)
	header := fmt.Sprintf("%s%s@%s", onboardHeaderPrefix, b.key, rev)
	if b.agent != "" {
		header += " [agent=" + b.agent + "]"
	}
	header += " at " + onboardCanonicalTime + " -->\n\n"
	body := header + rendered
	if check {
		return rt.checkOnboard(outPath, rev, body)
	}
	body = strings.Replace(body, " at "+onboardCanonicalTime+" -->", " at "+time.Now().UTC().Format(time.RFC3339)+" -->", 1)
	if strings.TrimSpace(outPath) == "" {
		_, err = fmt.Fprint(rt.stdout, body)
		return err
	}
	path := outPath
	if err := writeRendered(path, body); err != nil {
		return err
	}
	if rt.jsonOut {
		return rt.printJSON(map[string]any{"path": path, "rev": rev, "bytes": len(body)})
	}
	_, err = fmt.Fprintf(rt.stdout, "wrote %s (%d bytes, rev=%s)\n", path, len(body), rev)
	return err
}

func resolveOnboardOutPath(path, format string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if stat, err := os.Stat(clean); err == nil && stat.IsDir() {
		return filepath.Join(clean, "onboarding."+format)
	}
	return clean
}

const onboardCanonicalTime = "1970-01-01T00:00:00Z"

func (rt *runtime) checkOnboard(path, rev, expected string) error {
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
	}
	var raw []byte
	if err == nil {
		raw, err = io.ReadAll(io.LimitReader(f, 4<<20+1))
	}
	if os.IsNotExist(err) {
		return rt.fail(fmt.Errorf("%s does not exist (would be created on render)", path), "")
	}
	if err != nil {
		return rt.fail(err, "")
	}
	if len(raw) > 4<<20 {
		return usagef("%s is too large", path)
	}
	line, rest, ok := strings.Cut(string(raw), "\n")
	if !ok || !strings.HasPrefix(line, onboardHeaderPrefix) || !strings.HasSuffix(line, " -->") {
		return usagef("%s has no valid paimos-managed header", path)
	}
	at := strings.LastIndex(line, " at ")
	if at < 0 || at+4 > len(line)-4 {
		return usagef("%s has no valid generation timestamp", path)
	}
	if _, err := time.Parse(time.RFC3339, line[at+4:len(line)-4]); err != nil {
		return usagef("%s has no valid generation timestamp", path)
	}
	normalized := line[:at] + " at " + onboardCanonicalTime + " -->\n" + rest
	if normalized != expected {
		return rt.fail(fmt.Errorf("%s differs from canonical bundle (current rev=%s)", path, rev), "")
	}

	if rt.jsonOut {
		return rt.printJSON(map[string]string{"check": "identical", "path": path, "rev": rev})
	}
	_, err = fmt.Fprintf(rt.stdout, "%s: identical (rev=%s)\n", path, rev)
	return err
}

func (b briefing) markdown(program string, readingLimit int, includeLow bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Welcome to %s\n\n", b.project.Title)
	if line := firstLine(b.project.Body); line != "" {
		fmt.Fprintf(&out, "> %s\n\n", line)
	}
	fmt.Fprintln(&out, "## What this project is")
	if strings.TrimSpace(b.project.Body) != "" {
		fmt.Fprintln(&out, strings.TrimSpace(b.project.Body))
	} else {
		fmt.Fprintln(&out, "_No project description on file yet — ask the project owner to add one._")
	}
	fmt.Fprintln(&out)
	b.section(&out, "related_project", "Related projects", 5)
	b.section(&out, "external_system", "Key external systems", 10)
	b.section(&out, "guideline", "How we work", 10)
	if slices.ContainsFunc(b.issues, func(n apiNode) bool {
		return n.State == "done" || n.State == "delivered" || n.State == "cancelled"
	}) {
		fmt.Fprintln(&out, "## Recent context")
		fmt.Fprintln(&out)
		recent := append([]apiNode(nil), b.issues...)
		slices.SortFunc(recent, func(a, c apiNode) int { return c.UpdatedAt.Compare(a.UpdatedAt) })
		count := 0
		for _, n := range recent {
			if n.State != "done" && n.State != "delivered" && n.State != "cancelled" {
				continue
			}
			fmt.Fprintf(&out, "- `%s` — %s _(%s, %s)_\n", n.Key, n.Title, n.State, n.UpdatedAt.UTC().Format("2006-01-02"))
			count++
			if count == 5 {
				break
			}
		}
		fmt.Fprintln(&out)
	}
	if b.agent != "" {
		artifact := composeArtifact(b.project, b.key, b.agent, b.entries, b.kinds)
		fmt.Fprintf(&out, "## If you're playing the %s role\n\n", b.agent)
		fmt.Fprintf(&out, "%s\n\n", artifact.Agent.Description)
		if artifact.Agent.Body != "" {
			fmt.Fprintf(&out, "### Excerpt\n\n%s\n\n", clipRunes(artifact.Agent.Body, 600, "..."))
		}
		if len(artifact.Agent.Bootstrap) > 0 {
			fmt.Fprintln(&out, "### Bootstrap steps")
			fmt.Fprintln(&out)
			for i, step := range artifact.Agent.Bootstrap {
				fmt.Fprintf(&out, "%d. **%s**\n", i+1, step.Title)
				if step.Command != "" {
					fmt.Fprintf(&out, "   ```\n   %s\n   ```\n", step.Command)
				}
				if step.Rationale != "" {
					fmt.Fprintf(&out, "   _%s_\n", step.Rationale)
				}
			}
			fmt.Fprintln(&out)
		}
		if len(artifact.Agent.Rules) > 0 {
			fmt.Fprintln(&out, "### Non-negotiable rules")
			fmt.Fprintln(&out)
			for _, rule := range artifact.Agent.Rules {
				fmt.Fprintf(&out, "- **%s**", rule.Title)
				if rule.MemoryRef != "" {
					fmt.Fprintf(&out, " _(memory: `%s`)_", rule.MemoryRef)
				}
				fmt.Fprintln(&out)
				if line := firstLine(rule.Body); line != "" {
					fmt.Fprintf(&out, "  %s\n", line)
				}
			}
			fmt.Fprintln(&out)
		}
	}
	b.section(&out, "runbook", "Known runbooks", 20)
	fmt.Fprintf(&out, "## Where to look\n\n- Issues, memory, runbooks: the %s web UI for this project\n- CLI quickstart: `%s session start --project %s --agent %s`\n\n", program, program, b.key, fallbackAgent(b.agent))
	memories := b.byKind("memory")
	if len(memories) > 0 {
		fmt.Fprintln(&out, "## Reading list")
		fmt.Fprintln(&out)
		count := 0
		for _, n := range memories {
			confidence := nestedField(fieldMap(n.Fields), "confidence")
			if confidence == "low" && !includeLow {
				continue
			}
			if confidence == "" {
				confidence = "unknown"
			}
			fmt.Fprintf(&out, "- **%s** _(confidence: %s)_\n", n.Title, confidence)
			if line := firstLine(n.Body); line != "" {
				fmt.Fprintf(&out, "  %s\n", line)
			}
			count++
			if count >= readingLimit {
				break
			}
		}
		fmt.Fprintln(&out)
	}
	return out.String()
}

func fallbackAgent(agent string) string {
	if agent == "" {
		return "<agent>"
	}
	return agent
}

func (b briefing) byKind(slug string) []apiNode {
	var out []apiNode
	for _, n := range b.entries {
		if b.kinds.slug(n.KindID) == slug {
			out = append(out, n)
		}
	}
	return out
}

func (b briefing) section(out *strings.Builder, slug, heading string, limit int) {
	nodes := b.byKind(slug)
	if len(nodes) == 0 {
		return
	}
	fmt.Fprintf(out, "## %s\n\n", heading)
	for i, n := range nodes {
		if i >= limit {
			break
		}
		fmt.Fprintf(out, "- **%s**", n.Title)
		if link := nestedField(fieldMap(n.Fields), "url"); link != "" {
			fmt.Fprintf(out, " — <%s>", link)
		}
		fmt.Fprintln(out)
		if line := firstLine(n.Body); line != "" {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	fmt.Fprintln(out)
}
