// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/rules"
)

// skill render <agent> builds the canonical agent artifact from the project
// node and its knowledge children, then writes it through a harness adapter.
// The file starts with the paimos-managed header (after any native skill
// frontmatter) so sync check can detect drift. There is no separate agent
// table: Aeon knowledge is the canonical source. No httpapi.Module is added.

const (
	headerPrefix            = "<!-- paimos: rendered from "
	defaultCanonicalVersion = "1.0.0"
	defaultHarness          = "claude-code"
)

var skillNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (rt *runtime) cmdSkill() *Command {
	return &Command{
		Name:  "skill",
		Short: "Render a canonical agent artifact for a harness",
		Use:   "skill <render>",
		subs:  []*Command{rt.cmdSkillRender()},
	}
}

func (rt *runtime) cmdSkillRender() *Command {
	var project, agentFlag, harness, outPath, workspace string
	var checkOnly bool
	return &Command{
		Name:    "render",
		Short:   "Render an agent artifact through a harness adapter",
		Use:     "skill render <agent> --project KEY",
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&agentFlag, "agent", 0, "agent name, when not passed as <agent>")
			fs.string(&harness, "harness", 0, "claude-code, codex, grok, pi, cursor, gemini, or opencode (default claude-code)")
			fs.string(&outPath, "out", 0, "output file (default: the adapter path under --workspace)")
			fs.string(&workspace, "workspace", 0, "workspace root for the adapter path (default: working directory)")
			fs.bool(&checkOnly, "check", 0, "compare the existing file; exit 1 on drift, 2 when the header is missing")
		},
		run: func(args []string) error {
			name := agentFlag
			if len(args) == 1 {
				if name != "" && name != args[0] {
					return usagef("--agent and <agent> disagree")
				}
				name = args[0]
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return usagef("<agent> is required")
			}
			if !agentNameRE.MatchString(name) {
				return usagef("agent name must match %s", agentNameRE.String())
			}
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			harness = strings.TrimSpace(harness)
			if harness == "" {
				harness = defaultHarness
			}
			raw, projectKey, err := rt.canonicalArtifact(project, name)
			if err != nil {
				return err
			}
			rendered, err := renderThroughHarness(raw, harness, projectKey, name)
			if err != nil {
				return rt.fail(err, "")
			}
			root, err := workspaceRoot(workspace)
			if err != nil {
				return err
			}
			target, err := resolveSkillPath(outPath, root, rendered.SuggestedPath)
			if err != nil {
				return err
			}
			if checkOnly {
				return rt.checkRendered(target, rendered.Body)
			}
			if err := writeRendered(target, rendered.Body); err != nil {
				return err
			}
			rel := target
			if slash, err := filepath.Rel(root, target); err == nil && filepath.IsLocal(slash) {
				rel = filepath.ToSlash(slash)
			}
			if err := upsertRenderedSkill(root, renderedSkillEntry{
				Project: projectKey,
				Agent:   name,
				Harness: harness,
				Path:    rel,
			}); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(map[string]any{
					"path":    target,
					"rev":     rendered.Rev,
					"harness": harness,
					"bytes":   len(rendered.Body),
				})
			}
			fmt.Fprintf(rt.stdout, "wrote %s (%d bytes, rev=%s)\n", target, len(rendered.Body), rendered.Rev)
			return nil
		},
	}
}

type skillRender struct {
	Body          string
	SuggestedPath string
	Rev           string
}

func renderThroughHarness(canonical []byte, harness, projectKey, agentName string) (skillRender, error) {
	version := canonicalSchemaVersion(canonical)
	if err := versionSupported(version); err != nil {
		return skillRender{}, fmt.Errorf("adapter %q cannot render canonical schema %s: %w", harness, version, err)
	}
	content, suggested, err := renderHarness(harness, canonical)
	if err != nil {
		return skillRender{}, err
	}
	key, name := artifactIdentity(canonical)
	if key == "" {
		key = projectKey
	}
	if name == "" {
		name = agentName
	}
	rev := canonicalRev(canonical)
	body := injectHeader(buildHeader(key, name, rev, harness), content)
	return skillRender{Body: body, SuggestedPath: suggested, Rev: rev}, nil
}

func renderHarness(harness string, canonical []byte) (content, suggested string, err error) {
	switch harness {
	case "claude-code":
		return renderClaude(canonical)
	case "codex":
		return renderNative(".agents", canonical)
	case "grok":
		return renderNative(".grok", canonical)
	case "pi":
		return renderNative(".pi", canonical)
	case "cursor":
		return renderNative(".cursor", canonical)
	case "gemini":
		return renderNative(".gemini", canonical)
	case "opencode":
		return renderNative(".opencode", canonical)
	default:
		return "", "", fmt.Errorf("unknown harness %q (expected claude-code, codex, grok, pi, cursor, gemini, or opencode)", harness)
	}
}

type canonicalDoc struct {
	SchemaVersion string            `json:"canonical_schema_version"`
	Project       canonicalProject  `json:"project"`
	Agent         canonicalAgent    `json:"agent"`
	Repos         []canonicalRepo   `json:"repos"`
	Environments  []canonicalEnv    `json:"environments"`
	DeployRecipes []canonicalRecipe `json:"deploy_recipes"`
}

type canonicalProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

type canonicalAgent struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Slash       string          `json:"slash_command_name"`
	LaneTags    []string        `json:"lane_tags"`
	Metadata    map[string]any  `json:"metadata"`
	Body        string          `json:"body"`
	Bootstrap   []canonicalStep `json:"bootstrap_steps"`
	Rules       []canonicalRule `json:"non_negotiable_rules"`
}

type canonicalStep struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Rationale string `json:"rationale"`
}

type canonicalRule struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	MemoryRef string `json:"memory_ref"`
}

type canonicalRepo struct {
	Label         string `json:"label"`
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

type canonicalEnv struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	HostAlias string `json:"host_alias"`
	HostIP    string `json:"host_ip"`
}

type canonicalRecipe struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Summary string `json:"summary"`
}

func (rt *runtime) canonicalArtifact(project, agent string) ([]byte, string, error) {
	proj, err := rt.projectNode(project)
	if err != nil {
		return nil, "", err
	}
	kinds, nodes, err := rt.knowledgeNodes(project, "")
	if err != nil {
		return nil, "", err
	}
	key := projectDisplayKey(proj, project)
	doc := composeArtifact(proj, key, agent, nodes, kinds)
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	return raw, key, nil
}

func projectDisplayKey(n apiNode, requested string) string {
	if stored := fieldString(fieldMap(n.Fields), "project_key"); stored != "" {
		return stored
	}
	if n.Key != "" {
		return n.Key
	}
	return strings.TrimSpace(requested)
}

func composeArtifact(proj apiNode, projectKey, agent string, nodes []apiNode, kinds kindTable) canonicalDoc {
	desc, rest := splitProjectBody(proj.Body)
	if desc == "" {
		desc = "Use when operating as the " + agent + " Paimos agent."
	}
	doc := canonicalDoc{
		SchemaVersion: defaultCanonicalVersion,
		Project:       canonicalProject{ID: proj.ID, Name: proj.Title, Key: projectKey},
		Agent: canonicalAgent{
			Name:        agent,
			Description: desc,
			Slash:       agent,
			LaneTags:    []string{},
			Metadata:    map[string]any{},
			Body:        rest,
			Bootstrap:   []canonicalStep{},
			Rules:       []canonicalRule{},
		},
		Repos:         []canonicalRepo{},
		Environments:  []canonicalEnv{},
		DeployRecipes: []canonicalRecipe{},
	}
	ordered := append([]apiNode(nil), nodes...)
	slices.SortFunc(ordered, func(a, b apiNode) int {
		aKind := kinds.slug(a.KindID)
		bKind := kinds.slug(b.KindID)
		if aKind != bKind {
			return strings.Compare(aKind, bKind)
		}
		aSlug := fieldString(fieldMap(a.Fields), "slug")
		bSlug := fieldString(fieldMap(b.Fields), "slug")
		if aSlug != bSlug {
			return strings.Compare(aSlug, bSlug)
		}
		return strings.Compare(a.Key, b.Key)
	})
	for _, n := range ordered {
		fields := fieldMap(n.Fields)
		slug := fieldString(fields, "slug")
		switch kinds.slug(n.KindID) {
		case "runbook":
			cmd := nestedField(fields, "command")
			if cmd == "" {
				cmd = strings.TrimSpace(n.Body)
			}
			doc.Agent.Bootstrap = append(doc.Agent.Bootstrap, canonicalStep{
				Title: n.Title, Command: cmd, Rationale: nestedField(fields, "rationale"),
			})
		case "guideline":
			doc.Agent.Rules = append(doc.Agent.Rules, canonicalRule{
				Title: n.Title, Body: n.Body, MemoryRef: nestedField(fields, "memory_ref"),
			})
		case "memory":
			ref := slug
			if ref == "" {
				ref = n.Key
			}
			doc.Agent.Rules = append(doc.Agent.Rules, canonicalRule{Title: n.Title, Body: n.Body, MemoryRef: ref})
		case "external_system":
			doc.Environments = append(doc.Environments, canonicalEnv{
				Name: n.Title, URL: nestedField(fields, "url"),
				HostAlias: nestedField(fields, "host_alias"), HostIP: nestedField(fields, "host_ip"),
			})
		case "related_project":
			doc.Repos = append(doc.Repos, canonicalRepo{
				Label: n.Title, URL: nestedField(fields, "url"), DefaultBranch: nestedField(fields, "default_branch"),
			})
		}
	}
	return doc
}

func nestedField(fields map[string]any, key string) string {
	if s := strings.TrimSpace(fieldString(fields, key)); s != "" {
		return s
	}
	meta, _ := fields["metadata"].(map[string]any)
	return strings.TrimSpace(fieldString(meta, key))
}

func splitProjectBody(body string) (desc, rest string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	line, after, found := strings.Cut(body, "\n")
	desc = strings.TrimSpace(line)
	if !found {
		return desc, ""
	}
	return desc, strings.TrimSpace(after)
}

func renderClaude(canonical []byte) (string, string, error) {
	var art canonicalDoc
	if err := json.Unmarshal(canonical, &art); err != nil {
		return "", "", fmt.Errorf("decode canonical artifact: %w", err)
	}
	if strings.TrimSpace(art.Agent.Name) == "" {
		return "", "", fmt.Errorf("canonical artifact missing agent.name")
	}
	return renderClaudeBody(&art), claudePath(&art), nil
}

func claudePath(art *canonicalDoc) string {
	slug := strings.TrimSpace(art.Agent.Slash)
	if slug == "" {
		slug = strings.TrimSpace(art.Agent.Name)
	}
	slug = sanitizeSlug(slug)
	return filepath.Join(".claude", "commands", slug+".md")
}

func sanitizeSlug(slug string) string {
	slug = strings.ReplaceAll(slug, string(filepath.Separator), "-")
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, "\\", "-")
	return slug
}

func renderClaudeBody(art *canonicalDoc) string {
	var b strings.Builder
	projectName := art.Project.Name
	if strings.TrimSpace(projectName) == "" {
		projectName = art.Project.Key
	}
	projectKey := art.Project.Key
	if strings.TrimSpace(projectKey) == "" {
		projectKey = art.Project.ID
	}
	fmt.Fprintf(&b, "You are operating as the **%s session** for %s (PAIMOS project **%s**).\n", art.Agent.Name, projectName, projectKey)
	if hasLane(art) {
		b.WriteString("\n## Your lane\n\n")
		writeLane(&b, art)
	}
	if len(art.Agent.Bootstrap) > 0 {
		b.WriteString("\n## Bootstrap\n\n")
		for i, s := range art.Agent.Bootstrap {
			title := strings.TrimSpace(s.Title)
			if title == "" {
				title = fmt.Sprintf("Step %d", i+1)
			}
			fmt.Fprintf(&b, "%d. **%s**\n", i+1, title)
			if cmd := strings.TrimSpace(s.Command); cmd != "" {
				fmt.Fprintf(&b, "   ```sh\n   %s\n   ```\n", cmd)
			}
			if rat := strings.TrimSpace(s.Rationale); rat != "" {
				fmt.Fprintf(&b, "   _%s_\n", rat)
			}
		}
	}
	if len(art.Agent.Rules) > 0 {
		b.WriteString("\n## Non-negotiable rules\n\n")
		for i, r := range art.Agent.Rules {
			title := strings.TrimSpace(r.Title)
			if title == "" {
				title = fmt.Sprintf("Rule %d", i+1)
			}
			fmt.Fprintf(&b, "- **%s**", title)
			if ref := strings.TrimSpace(r.MemoryRef); ref != "" {
				fmt.Fprintf(&b, " _(memory: `%s`)_", ref)
			}
			b.WriteString("\n")
			if body := strings.TrimSpace(r.Body); body != "" {
				for _, line := range strings.Split(body, "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			}
		}
	}
	if len(art.DeployRecipes) > 0 {
		b.WriteString("\n## Deploy cheat sheet\n\n")
		for _, rec := range art.DeployRecipes {
			fmt.Fprintf(&b, "### %s\n\n", rec.Name)
			if s := strings.TrimSpace(rec.Summary); s != "" {
				fmt.Fprintf(&b, "%s\n\n", s)
			}
			if c := strings.TrimSpace(rec.Command); c != "" {
				fmt.Fprintf(&b, "```sh\n%s\n```\n\n", c)
			}
		}
	}
	if body := strings.TrimSpace(art.Agent.Body); body != "" {
		b.WriteString("\n## Free body\n\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

func hasLane(art *canonicalDoc) bool {
	return strings.TrimSpace(art.Agent.Description) != "" || len(art.Repos) > 0 || len(art.Environments) > 0 || len(art.Agent.LaneTags) > 0
}

func writeLane(b *strings.Builder, art *canonicalDoc) {
	if desc := strings.TrimSpace(art.Agent.Description); desc != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}
	if len(art.Agent.LaneTags) > 0 {
		fmt.Fprintf(b, "\n**Lane tags:** %s\n", strings.Join(art.Agent.LaneTags, ", "))
	}
	if len(art.Repos) > 0 {
		b.WriteString("\n**Repos:**\n")
		for _, r := range art.Repos {
			label := r.Label
			if strings.TrimSpace(label) == "" {
				label = r.URL
			}
			if r.DefaultBranch != "" {
				fmt.Fprintf(b, "- %s — %s (`%s`)\n", label, r.URL, r.DefaultBranch)
			} else {
				fmt.Fprintf(b, "- %s — %s\n", label, r.URL)
			}
		}
	}
	if len(art.Environments) > 0 {
		b.WriteString("\n**Environments:**\n")
		for _, e := range art.Environments {
			fmt.Fprintf(b, "- **%s**", e.Name)
			if e.URL != "" {
				fmt.Fprintf(b, " — %s", e.URL)
			}
			if host := formatHost(e.HostAlias, e.HostIP); host != "" {
				fmt.Fprintf(b, " (host: %s)", host)
			}
			b.WriteString("\n")
		}
	}
}

func formatHost(alias, ip string) string {
	switch {
	case alias != "" && ip != "":
		return alias + " (" + ip + ")"
	case alias != "":
		return alias
	case ip != "":
		return ip
	default:
		return ""
	}
}

func renderNative(root string, canonical []byte) (string, string, error) {
	content, _, err := renderClaude(canonical)
	if err != nil {
		return "", "", err
	}
	var art canonicalDoc
	if err := json.Unmarshal(canonical, &art); err != nil {
		return "", "", fmt.Errorf("decode canonical artifact: %w", err)
	}
	slug := strings.TrimSpace(art.Agent.Slash)
	if slug == "" {
		slug = strings.TrimSpace(art.Agent.Name)
	}
	if len(slug) > 64 || !skillNameRE.MatchString(slug) {
		return "", "", fmt.Errorf("skill name must be 1–64 lowercase letters, digits or single hyphens")
	}
	description := strings.Join(strings.Fields(art.Agent.Description), " ")
	if description == "" {
		description = "Use when operating as the " + art.Agent.Name + " Paimos agent."
	}
	if utf8.RuneCountInString(description) > 1024 {
		description = string([]rune(description)[:1024])
	}
	nameJSON, _ := json.Marshal(slug)
	descriptionJSON, _ := json.Marshal(description)
	body := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", nameJSON, descriptionJSON, content)
	return body, filepath.Join(root, "skills", slug, "SKILL.md"), nil
}

func buildHeader(projectKey, agentName, rev, harness string) string {
	if rev == "" {
		rev = "unknown"
	}
	return fmt.Sprintf("%s%s/%s@%s harness=%s -->", headerPrefix, projectKey, agentName, rev, harness)
}

func injectHeader(header, content string) string {
	frontmatter, trimmed := splitFrontmatter(strings.TrimPrefix(content, "\uFEFF"))
	if strings.HasPrefix(trimmed, headerPrefix) {
		if i := strings.Index(trimmed, "\n"); i >= 0 {
			trimmed = trimmed[i+1:]
		} else {
			trimmed = ""
		}
		trimmed = strings.TrimLeft(trimmed, "\n")
	}
	if trimmed == "" {
		return frontmatter + header + "\n"
	}
	return frontmatter + header + "\n\n" + trimmed
}

func managedHeader(body string) string {
	_, rest := splitFrontmatter(strings.TrimLeft(strings.TrimPrefix(body, "\uFEFF"), " \t\r\n"))
	rest = strings.TrimLeft(rest, " \t\r\n")
	line, _, _ := strings.Cut(rest, "\n")
	if strings.HasPrefix(line, headerPrefix) {
		return line
	}
	return ""
}

func splitFrontmatter(content string) (string, string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
		end += 9
		return content[:end] + "\n", strings.TrimLeft(content[end:], "\n")
	}
	return "", content
}

func canonicalRev(canonical []byte) string {
	var doc any
	if err := json.Unmarshal(canonical, &doc); err != nil {
		sum := sha256.Sum256(canonical)
		return hex.EncodeToString(sum[:])[:12]
	}
	normalised, err := json.Marshal(doc)
	if err != nil {
		sum := sha256.Sum256(canonical)
		return hex.EncodeToString(sum[:])[:12]
	}
	sum := sha256.Sum256(normalised)
	return hex.EncodeToString(sum[:])[:12]
}

func canonicalSchemaVersion(canonical []byte) string {
	var probe struct {
		Version string `json:"canonical_schema_version"`
	}
	if err := json.Unmarshal(canonical, &probe); err == nil && strings.TrimSpace(probe.Version) != "" {
		return strings.TrimSpace(probe.Version)
	}
	return defaultCanonicalVersion
}

func versionSupported(v string) error {
	main, _, _ := strings.Cut(strings.TrimSpace(v), "-")
	majorText, _, _ := strings.Cut(main, ".")
	if majorText != "1" {
		return fmt.Errorf("canonical schema %s does not satisfy >=1.0.0 <2.0.0", v)
	}
	return nil
}

func artifactIdentity(canonical []byte) (projectKey, agentName string) {
	var probe struct {
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
		Agent struct {
			Name string `json:"name"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(canonical, &probe); err != nil {
		return "", ""
	}
	return strings.TrimSpace(probe.Project.Key), strings.TrimSpace(probe.Agent.Name)
}

func workspaceRoot(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return os.Getwd()
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", workspace)
	}
	return abs, nil
}

func resolveSkillPath(out, root, suggested string) (string, error) {
	out = strings.TrimSpace(out)
	if out != "" {
		if filepath.IsAbs(out) {
			return filepath.Clean(out), nil
		}
		return filepath.Clean(filepath.Join(root, out)), nil
	}
	suggested = strings.TrimSpace(suggested)
	if suggested == "" {
		return "", fmt.Errorf("adapter did not provide a suggested path; pass --out")
	}
	if filepath.IsAbs(suggested) || !filepath.IsLocal(suggested) {
		return "", fmt.Errorf("suggested path %q escapes the workspace", suggested)
	}
	return filepath.Join(root, suggested), nil
}

func writeRendered(path, body string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Refuse symlink destinations, including dangling links, rather than silently
	// replacing a link the caller may expect us to follow. Rename itself never
	// follows the destination link, even if it is swapped after this check.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("render %s: refusing symlink destination", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	// CreateTemp exclusively creates a private (0600) file in the same
	// directory, so concurrent renders do not share a name and rename is atomic.
	f, err := os.CreateTemp(dir, ".paimos-render-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

func (rt *runtime) checkRendered(path, rendered string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(rt.stderr, "%s: %s does not exist (would be created on render)\n", rt.program, path)
			return &exitError{code: 1}
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	switch compareRendered(rendered, string(existing)) {
	case checkIdentical:
		if rt.jsonOut {
			return rt.printJSON(map[string]any{"check": "identical", "path": path})
		}
		fmt.Fprintf(rt.stdout, "%s: identical\n", path)
		return nil
	case checkHeaderMissing:
		fmt.Fprintf(rt.stderr, "%s: %s has no paimos-managed header — out of management surface\n", rt.program, path)
		return &exitError{code: 2}
	default:
		fmt.Fprintf(rt.stderr, "%s: %s differs from canonical render (run without --check to update)\n", rt.program, path)
		exLines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
		reLines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		fmt.Fprintf(rt.stderr, "  current: %d lines, canonical: %d lines\n", len(exLines), len(reLines))
		return &exitError{code: 1}
	}
}

type checkResult int

const (
	checkIdentical checkResult = iota
	checkDiff
	checkHeaderMissing
)

func compareRendered(rendered, existing string) checkResult {
	if rendered == existing {
		return checkIdentical
	}
	if managedHeader(existing) == "" {
		return checkHeaderMissing
	}
	return checkDiff
}

// renderRulesThroughHarness shares the skill renderer boundary while keeping the
// server's exact bounded bytes intact. This is an always-on rules artifact, not
// a command or SKILL.md. It returns only a suggested name; installation is opt-in.
func renderRulesThroughHarness(m rules.Merged) (skillRender, error) {
	if !slices.Contains(rules.Harnesses, m.Context.Harness) || len(m.Body) > rules.MaxBytes || len(m.Body) != m.ByteSize {
		return skillRender{}, fmt.Errorf("invalid bounded rules artifact")
	}
	name := "AGENTS.md"
	if m.Context.Harness == "claude-code" {
		name = "CLAUDE.md"
	} else if m.Context.Harness == "gemini" {
		name = "GEMINI.md"
	}
	return skillRender{Body: m.Body, SuggestedPath: name, Rev: m.SHA256}, nil
}
