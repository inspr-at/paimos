// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// The issue, knowledge and search verbs match the paimos commands INSPR
// doctrine invokes. They call the Aeon node and search APIs and print the
// classic text shapes. Commands with no Aeon resource exit 3; see
// unsupportedCompat.

var knowledgeTypes = []string{"memory", "runbook", "guideline", "external-system", "related-project"}

func (rt *runtime) cmdIssue() *Command {
	return &Command{
		Name:  "issue",
		Short: "Issues",
		Use:   "issue <command>",
		subs: []*Command{
			rt.cmdIssueList(),
			rt.cmdIssueGet(),
			rt.cmdIssueCreate(),
			rt.cmdIssueUpdate(),
			rt.cmdIssueConvert(),
			rt.cmdIssueEstimate(),
			rt.cmdIssueComment(),
			rt.cmdIssueMove(),
			rt.cmdSearch("issue search"),
		},
	}
}

func (rt *runtime) cmdIssueMove() *Command {
	var target string
	var dryRun bool
	return &Command{Name: "move", Short: "Move issues between projects", Use: "issue move <ref>... --to <project>", minArgs: 1, maxArgs: -1, addFlags: func(fs *flagSet) {
		fs.string(&target, "to", 0, "target project")
		fs.bool(&dryRun, "dry-run", 0, "preview without writing")
	}, run: func(refs []string) error {
		for _, ref := range refs {
			if _, err := normalizeIssueRef(ref); err != nil {
				return err
			}
		}
		if strings.TrimSpace(target) == "" {
			return usagef("--to <project> is required")
		}
		return rt.moveIssues(refs, target, dryRun)
	}}
}

func (rt *runtime) cmdIssueList() *Command {
	var project, status, typ, priority, assignee string
	limit := 50
	offset := 0
	return &Command{
		Name:  "list",
		Short: "List issues",
		Use:   "issue list [flags]",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 'p', "filter by project key")
			fs.string(&status, "status", 0, "filter by status")
			fs.string(&typ, "type", 0, "filter by type")
			fs.string(&priority, "priority", 0, "filter by priority")
			fs.string(&assignee, "assignee", 0, "filter by assignee id")
			fs.int(&limit, "limit", "page size")
			fs.int(&offset, "offset", "pagination offset")
		},
		run: func(args []string) error {
			if limit < 0 || offset < 0 {
				return usagef("--limit and --offset must be 0 or greater")
			}
			return rt.listIssues(project, status, typ, priority, assignee, limit, offset)
		},
	}
}

func (rt *runtime) cmdIssueGet() *Command {
	return &Command{
		Name:    "get",
		Short:   "Fetch one issue by key",
		Use:     "issue get <ref>",
		minArgs: 1,
		maxArgs: 1,
		run: func(args []string) error {
			if _, err := normalizeIssueRef(args[0]); err != nil {
				return err
			}
			return rt.getIssue(args[0])
		},
	}
}

func (rt *runtime) cmdIssueCreate() *Command {
	var project, title, typ, status, priority, parent, assignee string
	var description, descriptionFile, ac, acFile, notes, notesFile string
	var tags []string
	var dryRun bool
	var benefits benefitFlags
	var estimate string
	return &Command{
		Name:  "create",
		Short: "Create an issue",
		Use:   "issue create --project KEY --title TITLE",
		addFlags: func(fs *flagSet) {
			benefits.flags(fs)
			fs.string(&estimate, "estimate", 0, "agent hours: 2h, 90m or 1.5")
			fs.string(&project, "project", 'p', "project key (required)")
			fs.string(&title, "title", 0, "title (required)")
			fs.string(&typ, "type", 0, "epic, ticket, task, …")
			fs.string(&status, "status", 0, "initial status")
			fs.string(&priority, "priority", 0, "low, medium, or high")
			fs.string(&parent, "parent", 0, "parent issue key or id:<n>")
			fs.string(&assignee, "assignee", 0, "assignee id")
			fs.string(&description, "description", 0, "inline description")
			fs.string(&descriptionFile, "description-file", 0, "description file, or - for stdin")
			fs.string(&ac, "ac", 0, "inline acceptance criteria")
			fs.string(&acFile, "ac-file", 0, "acceptance criteria file")
			fs.string(&notes, "notes", 0, "inline notes")
			fs.string(&notesFile, "notes-file", 0, "notes file")
			fs.strings(&tags, "tags", "tag name (repeatable)")
			fs.bool(&dryRun, "dry-run", 0, "validate and print the action without writing")
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if strings.TrimSpace(title) == "" {
				return usagef("--title is required")
			}
			desc, err := rt.readText(description, descriptionFile, "description")
			if err != nil {
				return err
			}
			acText, err := rt.readText(ac, acFile, "ac")
			if err != nil {
				return err
			}
			notesText, err := rt.readText(notes, notesFile, "notes")
			if err != nil {
				return err
			}
			if strings.TrimSpace(parent) != "" {
				if _, err := normalizeIssueRef(parent); err != nil {
					return err
				}
			}
			if estimate != "" {
				if _, err := parseEstimate(estimate); err != nil {
					return err
				}
			}
			if dryRun {
				kind := strings.TrimSpace(typ)
				if kind == "" {
					kind = "ticket"
				}
				fmt.Fprintf(rt.stdout, "dry-run: would create %s in %s — %s\n", kind, strings.TrimSpace(project), strings.TrimSpace(title))
				return nil
			}
			return rt.createIssue(issueInput{
				Estimate: estimate, Benefits: benefits, Project: project, Title: title, Type: typ, Status: status, Priority: priority,
				Parent: parent, Assignee: assignee, Description: desc, AC: acText, Notes: notesText, Tags: tags,
			})
		},
	}
}

func (rt *runtime) cmdIssueUpdate() *Command {
	var title, typ, status, priority, parent, assignee, project string
	var description, descriptionFile, ac, acFile, notes, notesFile string
	var closeNote, closeNoteFile string
	var role, area string
	var addTag, removeTag []string
	var dryRun bool
	var benefits benefitFlags
	var estimate string
	return &Command{
		Name:    "update",
		Short:   "Update an issue",
		Use:     "issue update <ref> [flags]",
		minArgs: 1,
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			benefits.flags(fs)
			fs.string(&estimate, "estimate", 0, "agent hours: 2h, 90m or 1.5")
			fs.string(&title, "title", 0, "new title")
			fs.string(&role, "role", 0, "route role: scout, mechanical, build, build-hard, or review-gate")
			fs.string(&area, "area", 0, "route area: backend, frontend, full-stack, infra, design, or docs")
			fs.string(&typ, "type", 0, "refuses a different kind; use issue convert")
			fs.string(&status, "status", 0, "new status")
			fs.string(&priority, "priority", 0, "new priority")
			fs.string(&parent, "parent", 0, "new parent key or id:<n>")
			fs.string(&assignee, "assignee", 0, "new assignee id")
			fs.string(&project, "project", 0, "move to this project key")
			fs.string(&description, "description", 0, "inline description")
			fs.string(&descriptionFile, "description-file", 0, "description file, or - for stdin")
			fs.string(&ac, "ac", 0, "inline acceptance criteria")
			fs.string(&acFile, "ac-file", 0, "acceptance criteria file")
			fs.string(&notes, "notes", 0, "inline notes")
			fs.string(&notesFile, "notes-file", 0, "notes file")
			fs.string(&closeNote, "close-note", 0, "close note")
			fs.string(&closeNoteFile, "close-note-file", 0, "close note file")
			fs.strings(&addTag, "add-tag", "tag to add (repeatable)")
			fs.strings(&removeTag, "remove-tag", "tag to remove (repeatable)")
			fs.bool(&dryRun, "dry-run", 0, "validate and print the action without writing")
		},
		run: func(args []string) error {
			if _, err := normalizeIssueRef(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(parent) != "" {
				if _, err := normalizeIssueRef(parent); err != nil {
					return err
				}
			}
			desc, err := rt.readText(description, descriptionFile, "description")
			if err != nil {
				return err
			}
			acText, err := rt.readText(ac, acFile, "ac")
			if err != nil {
				return err
			}
			notesText, err := rt.readText(notes, notesFile, "notes")
			if err != nil {
				return err
			}
			closeText, err := rt.readText(closeNote, closeNoteFile, "close-note")
			if err != nil {
				return err
			}
			if err := validateRouteFlags(role, area); err != nil {
				return err
			}
			if err := rt.noteKindUpdate(args[0], typ); err != nil {
				return err
			}
			askedKind := strings.TrimSpace(typ) != ""
			changed := strings.TrimSpace(title+status+priority+parent+assignee+project+description+descriptionFile+ac+acFile+notes+notesFile+closeNote+closeNoteFile+role+area) != "" ||
				len(addTag) > 0 || len(removeTag) > 0 || benefits.changed() || estimate != ""
			if estimate != "" {
				if _, err := parseEstimate(estimate); err != nil {
					return err
				}
			}
			if !changed {
				if askedKind {
					return nil
				}
				return usagef("nothing to update")
			}
			if dryRun {
				fmt.Fprintf(rt.stdout, "dry-run: would update %s\n", args[0])
				return nil
			}
			return rt.updateIssue(issuePatch{
				Estimate: estimate, Benefits: benefits, Ref: args[0], Title: title, Status: status, Priority: priority,
				Parent: parent, Assignee: assignee, Project: project, Description: desc,
				AC: acText, Notes: notesText, CloseNote: closeText, AddTag: addTag, RemoveTag: removeTag,
				RouteRole: role, Area: area,
			})
		},
	}
}

func (rt *runtime) cmdIssueConvert() *Command {
	var to, sessionFile string
	return &Command{
		Name:    "convert",
		Short:   "Convert an issue to another kind",
		Use:     "issue convert <ref> --to <kind>",
		Long:    "Converts between issue kinds. A person session is required: pass --session-file, or call as a person. An agent key is refused and prints the ticket link.",
		minArgs: 1,
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&to, "to", 0, "issue kind to convert to")
			fs.string(&sessionFile, "session-file", 0, "person session cookie file, or - for stdin")
		},
		run: func(args []string) error {
			if _, err := normalizeIssueRef(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(to) == "" {
				return usagef("--to is required")
			}
			return rt.convertIssue(args[0], strings.TrimSpace(to), sessionFile)
		},
	}
}

func (rt *runtime) cmdIssueComment() *Command {
	var body, bodyFile string
	return &Command{
		Name:    "comment",
		Short:   "Comment on an issue",
		Use:     "issue comment <ref> (--body TEXT | --body-file PATH)",
		minArgs: 1,
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&body, "body", 0, "inline comment")
			fs.string(&bodyFile, "body-file", 0, "comment file, or - for stdin")
		},
		run: func(args []string) error {
			if _, err := normalizeIssueRef(args[0]); err != nil {
				return err
			}
			text, err := rt.readText(body, bodyFile, "body")
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return usagef("--body or --body-file is required")
			}
			return rt.commentIssue(args[0], text)
		},
	}
}

func (rt *runtime) cmdKnowledge() *Command {
	return &Command{
		Name:  "knowledge",
		Short: "Knowledge entries",
		Use:   "knowledge <command>",
		subs: []*Command{
			rt.cmdKnowledgeList(),
			rt.cmdKnowledgeGet(),
			rt.cmdKnowledgeCreate(),
			rt.cmdKnowledgeUpdate(),
		},
	}
}

func (rt *runtime) cmdKnowledgeList() *Command {
	var project, typ string
	return &Command{
		Name:  "list",
		Short: "List knowledge entries",
		Use:   "knowledge list --project KEY [--type TYPE]",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&typ, "type", 0, "memory, runbook, guideline, external-system, or related-project")
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if err := optionalKnowledgeType(typ); err != nil {
				return err
			}
			return rt.listKnowledge(project, typ)
		},
	}
}

func (rt *runtime) cmdKnowledgeGet() *Command {
	var project string
	return &Command{
		Name:    "get",
		Short:   "Fetch one knowledge entry",
		Use:     "knowledge get <type> <slug> --project KEY",
		minArgs: 2,
		maxArgs: 2,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
		},
		run: func(args []string) error {
			if err := requireKnowledgeType(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(args[1]) == "" {
				return usagef("<slug> is required")
			}
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			return rt.getKnowledge(args[0], args[1], project)
		},
	}
}

func (rt *runtime) cmdKnowledgeCreate() *Command {
	var project, typ, slug, title, body, bodyFile, status string
	return &Command{
		Name:  "create",
		Short: "Create a knowledge entry",
		Use:   "knowledge create --type TYPE --slug SLUG --project KEY --title TITLE",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&typ, "type", 0, "knowledge type (required)")
			fs.string(&slug, "slug", 0, "slug (required)")
			fs.string(&title, "title", 0, "title (required)")
			fs.string(&body, "body", 0, "inline body")
			fs.string(&bodyFile, "body-file", 0, "body file, or - for stdin")
			fs.string(&status, "status", 0, "initial status")
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if err := requireKnowledgeType(typ); err != nil {
				return err
			}
			if strings.TrimSpace(slug) == "" {
				return usagef("--slug is required")
			}
			if strings.TrimSpace(title) == "" {
				return usagef("--title is required")
			}
			text, err := rt.readText(body, bodyFile, "body")
			if err != nil {
				return err
			}
			return rt.createKnowledge(project, typ, slug, title, text, status)
		},
	}
}

func (rt *runtime) cmdKnowledgeUpdate() *Command {
	var project, title, body, bodyFile, status, newSlug, metadata, metadataFile string
	return &Command{
		Name:    "update",
		Short:   "Update a knowledge entry",
		Use:     "knowledge update <type> <slug> --project KEY [flags]",
		minArgs: 2,
		maxArgs: 2,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&title, "title", 0, "new title")
			fs.string(&body, "body", 0, "inline body")
			fs.string(&bodyFile, "body-file", 0, "body file, or - for stdin")
			fs.string(&status, "status", 0, "new status")
			fs.string(&newSlug, "slug", 0, "rename to this slug")
			fs.string(&metadata, "metadata", 0, "inline JSON metadata")
			fs.string(&metadataFile, "metadata-file", 0, "metadata JSON file")
		},
		run: func(args []string) error {
			if err := requireKnowledgeType(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(args[1]) == "" {
				return usagef("<slug> is required")
			}
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			text, err := rt.readText(body, bodyFile, "body")
			if err != nil {
				return err
			}
			meta, err := rt.readText(metadata, metadataFile, "metadata")
			if err != nil {
				return err
			}
			if strings.TrimSpace(title+body+bodyFile+status+newSlug+metadata+metadataFile) == "" {
				return usagef("nothing to update")
			}
			return rt.updateKnowledge(args[0], args[1], project, title, text, status, newSlug, meta)
		},
	}
}

func (rt *runtime) cmdSearch(use string) *Command {
	var project, typ string
	var limit int
	name := "search"
	if strings.TrimSpace(use) == "" {
		use = "search"
	}
	use += " <query>"
	return &Command{
		Name:    name,
		Short:   "Search issues by free text",
		Use:     use,
		minArgs: 1,
		maxArgs: -1,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 'p', "filter by project key")
			fs.string(&typ, "type", 0, "filter by issue type")
			fs.int(&limit, "limit", "page size")
		},
		run: func(args []string) error {
			if strings.TrimSpace(strings.Join(args, " ")) == "" {
				return usagef("search query must not be empty")
			}
			if limit < 0 {
				return usagef("--limit must be 0 or greater")
			}
			return rt.searchIssues(strings.Join(args, " "), project, typ, limit)
		},
	}
}

func (rt *runtime) cmdModel() *Command {
	return &Command{
		Name:  "model",
		Short: "Model roles",
		Use:   "model <resolve>",
		subs: []*Command{
			rt.cmdModelResolve(),
		},
	}
}

func (rt *runtime) cmdModelResolve() *Command {
	var author, harness, workspace string
	return &Command{
		Name:    "resolve",
		Short:   "Resolve a model role",
		Use:     "model resolve <role>",
		minArgs: 1,
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&author, "author-family", 0, "author model family; required for review-gate once this exists")
			fs.string(&harness, "harness", 0, "restrict to codex, claude, pi, or cursor")
			fs.string(&workspace, "workspace", 0, "workspace path")
		},
		run: func(args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return usagef("role is required")
			}
			return rt.resolveModel(args[0], author, harness)
		},
	}
}

func (rt *runtime) cmdOnboard() *Command {
	var project, agent, format, outPath string
	var check, includeLow bool
	readingListSize := 10
	return &Command{
		Name:  "onboard",
		Short: "Project briefing",
		Use:   "onboard --project KEY",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&agent, "agent", 0, "agent name")
			fs.string(&format, "format", 0, "md or html")
			fs.string(&outPath, "out", 0, "output file or directory")
			fs.bool(&check, "check", 0, "compare an existing managed briefing")
			fs.int(&readingListSize, "reading-list-size", "maximum reading list entries")
			fs.bool(&includeLow, "include-low", 0, "include low confidence memories")
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			return rt.onboard(project, agent, format, outPath, check, readingListSize, includeLow)
		},
	}
}

func normalizeIssueRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", usagef("issue reference is required")
	}
	if strings.HasPrefix(ref, "id:") {
		id := strings.TrimSpace(strings.TrimPrefix(ref, "id:"))
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return "", usagef("id:<n> must contain a positive numeric issue id")
		}
		return strconv.FormatInt(n, 10), nil
	}
	bare := true
	for _, r := range ref {
		if r < '0' || r > '9' {
			bare = false
			break
		}
	}
	if bare {
		return "", usagef("ambiguous bare issue number %q; pass the full issue key (PROJECT-%s) or explicit internal id:%s", ref, ref, ref)
	}
	return ref, nil
}

func optionalKnowledgeType(seg string) error {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return nil
	}
	return requireKnowledgeType(seg)
}

func requireKnowledgeType(seg string) error {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return usagef("--type is required (one of: %s)", strings.Join(knowledgeTypes, ", "))
	}
	for _, valid := range knowledgeTypes {
		if seg == valid {
			return nil
		}
	}
	return usagef("unknown knowledge type %q (expected one of: %s)", seg, strings.Join(knowledgeTypes, ", "))
}

func exclusive(inline, file, name string) error {
	if strings.TrimSpace(inline) != "" && strings.TrimSpace(file) != "" {
		return usagef("--%s and --%s-file are mutually exclusive", name, name)
	}
	return nil
}
