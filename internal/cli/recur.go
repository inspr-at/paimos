// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/recurrences"
)

// A JSON body keeps templates, RRULEs and tags lossless and avoids shell
// quoting for multiline criteria. All existing named-instance/auth flags apply.
func (rt *runtime) cmdRecur() *Command {
	return &Command{Name: "recur", Short: "Manage server-scheduled recurring tickets", Use: "recur <create|list|get|update|pause|resume|run-now|preview>", subs: []*Command{
		rt.recurWrite("create"), rt.recurList(), rt.recurWrite("update"), rt.recurAction("get"), rt.recurAction("pause"), rt.recurAction("resume"), rt.recurAction("run-now"), rt.recurAction("preview"),
	}}
}
func (rt *runtime) recurWrite(action string) *Command {
	var bodyFile, descriptionFile, criteriaFile string
	var tags []string
	c := &Command{Name: action, Short: action + " a recurrence from JSON", Use: "recur " + action + " [--body-file FILE|-] [--tag NAME|UUID] [--description-file FILE] [--criteria-file FILE]", maxArgs: 0, addFlags: func(fs *flagSet) {
		fs.string(&bodyFile, "body-file", 0, "JSON definition; - reads stdin")
		fs.strings(&tags, "tag", "existing tag name or UUID in the target project/workspace (repeatable)")
		fs.string(&descriptionFile, "description-file", 0, "template description file; overrides JSON description")
		fs.string(&criteriaFile, "criteria-file", 0, "template criteria file, one nonblank criterion per line; overrides JSON criteria")
	}}
	if action == "update" {
		c.Use = "recur update <recurrence-id> [--body-file FILE|-] [--tag NAME|UUID] [--description-file FILE] [--criteria-file FILE]"
		c.minArgs = 1
		c.maxArgs = 1
	}
	c.run = func(args []string) error {
		if action == "update" && !validUUID(args[0]) {
			return usagef("recurrence-id must be a UUID")
		}
		stdinFiles := 0
		for _, file := range []string{bodyFile, descriptionFile, criteriaFile} {
			if file == "-" {
				stdinFiles++
			}
		}
		if bodyFile == "" {
			stdinFiles++
		}
		if stdinFiles > 1 {
			return usagef("only one input file may read stdin")
		}
		if len(tags) > 50 {
			return usagef("at most 50 --tag values are allowed")
		}
		var reader io.Reader = rt.stdin
		var file *os.File
		if bodyFile != "" && bodyFile != "-" {
			var err error
			file, err = os.Open(bodyFile)
			if err != nil {
				return err
			}
			defer file.Close()
			reader = file
		}
		raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 1<<20 {
			return usagef("definition exceeds 1 MiB")
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body == nil {
			return usagef("one JSON object is required on stdin or --body-file")
		}
		if len(tags) > 0 || descriptionFile != "" || criteriaFile != "" {
			if err := rt.mergeRecurrenceTemplate(body, tags, descriptionFile, criteriaFile); err != nil {
				return err
			}
			merged, err := json.Marshal(body)
			if err != nil || len(merged) > 1<<20 {
				return usagef("merged definition exceeds 1 MiB")
			}
		}
		path := "/api/recurrences"
		method := http.MethodPost
		if action == "update" {
			path += "/" + args[0]
			method = http.MethodPut
		}
		var out recurrences.Recurrence
		if err = rt.do(method, path, body, &out); err != nil {
			return err
		}
		return rt.printRecurrence(out)
	}
	return c
}

func (rt *runtime) mergeRecurrenceTemplate(body map[string]any, tags []string, descriptionFile, criteriaFile string) error {
	template, ok := body["template"].(map[string]any)
	if !ok {
		return usagef("template must be a JSON object")
	}
	if descriptionFile != "" {
		text, err := rt.recurTextFile(descriptionFile, 65536)
		if err != nil {
			return err
		}
		template["description"] = text
	}
	if criteriaFile != "" {
		text, err := rt.recurTextFile(criteriaFile, 100*(4096+2))
		if err != nil {
			return err
		}
		criteria := []string{}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > 4096 || len(criteria) == 100 {
				return usagef("criteria file allows at most 100 lines of 4096 bytes")
			}
			criteria = append(criteria, line)
		}
		template["acceptance_criteria"] = criteria
	}
	if len(tags) == 0 {
		return nil
	}
	project, ok := body["project_id"].(string)
	if !ok || !validUUID(project) {
		return usagef("project_id must be a UUID to resolve --tag")
	}
	merged := []string{}
	if raw, exists := template["tags"]; exists && raw != nil {
		values, ok := raw.([]any)
		if !ok || len(values) > 50 {
			return usagef("template tags must contain at most 50 UUIDs")
		}
		for _, value := range values {
			tag, ok := value.(string)
			if !ok || !validUUID(tag) {
				return usagef("template tags must be UUIDs")
			}
			merged = append(merged, strings.ToLower(tag))
		}
	}
	seen := map[string]bool{}
	for _, tag := range merged {
		seen[tag] = true
	}
	for _, ref := range tags {
		id, err := rt.recurTag(project, strings.TrimSpace(ref))
		if err != nil {
			return err
		}
		if !seen[id] {
			if len(merged) == 50 {
				return usagef("template tags allow at most 50 UUIDs")
			}
			merged = append(merged, id)
			seen[id] = true
		}
	}
	template["tags"] = merged
	return nil
}

func (rt *runtime) recurTextFile(path string, limit int64) (string, error) {
	var reader io.Reader = rt.stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
	}
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > limit {
		return "", usagef("template file exceeds %d bytes", limit)
	}
	return string(raw), nil
}

// The list's project projection distinguishes same-named tags in other
// projects. Workspace tags are also valid, matching server validation. Refuse
// a truncated scan rather than resolving a potentially ambiguous name.
func (rt *runtime) recurTag(project, ref string) (string, error) {
	if ref == "" || len(ref) > 512 {
		return "", usagef("--tag requires a name or UUID")
	}
	query := url.Values{"kind": {"tag"}, "limit": {"200"}}
	if validUUID(ref) {
		query.Set("ids", ref)
	} else {
		query.Set("q", ref)
	}
	found := ""
	for page := 0; page < 50; page++ {
		var out struct {
			Items []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := rt.do(http.MethodGet, "/api/nodes?"+query.Encode(), nil, &out); err != nil {
			return "", err
		}
		for _, tag := range out.Items {
			if tag.Project != nil && !strings.EqualFold(tag.Project.ID, project) {
				continue
			}
			if strings.EqualFold(tag.Title, ref) || strings.EqualFold(tag.ID, ref) {
				if !validUUID(tag.ID) {
					return "", fmt.Errorf("tag response contains an invalid UUID")
				}
				if found != "" && !strings.EqualFold(found, tag.ID) {
					return "", usagef("tag %q is ambiguous in this project/workspace; use its UUID", ref)
				}
				found = strings.ToLower(tag.ID)
			}
		}
		if out.NextCursor == nil || *out.NextCursor == "" {
			if found == "" {
				return "", usagef("tag %q not found in this project/workspace", ref)
			}
			return found, nil
		}
		query.Set("cursor", *out.NextCursor)
	}
	return "", fmt.Errorf("tag lookup exceeds 50 pages; use a UUID")
}
func (rt *runtime) recurList() *Command {
	var project, after string
	return &Command{Name: "list", Short: "List managed recurrences", Use: "recur list [--project KEY|UUID] [--after UUID]", maxArgs: 0, addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key or UUID")
		fs.string(&after, "after", 0, "pagination cursor UUID")
	}, run: func([]string) error {
		query := url.Values{}
		if after != "" {
			if !validUUID(after) {
				return usagef("after must be a UUID")
			}
			query.Set("after", after)
		}
		if project != "" {
			n, err := rt.nodeRef(project)
			if err != nil {
				return err
			}
			query.Set("project_id", n.ID)
		}
		path := "/api/recurrences"
		if len(query) > 0 {
			path += "?" + query.Encode()
		}
		var out struct {
			Items      []recurrences.Recurrence `json:"items"`
			NextCursor *string                  `json:"next_cursor"`
		}
		if err := rt.do(http.MethodGet, path, nil, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		for _, item := range out.Items {
			if err := rt.printRecurrence(item); err != nil {
				return err
			}
		}
		if out.NextCursor != nil {
			_, err := fmt.Fprintf(rt.stdout, "More: recur list --after %s\n", *out.NextCursor)
			return err
		}
		return nil
	}}
}
func (rt *runtime) recurAction(action string) *Command {
	var revision, count int
	count = 5
	var key, after string
	return &Command{Name: action, Short: action + " a recurrence", Use: "recur " + action + " <recurrence-id>", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) {
		switch action {
		case "pause", "resume":
			fs.int(&revision, "revision", "expected revision; defaults to the current GET result")
		case "run-now":
			fs.string(&key, "idempotency-key", 0, "required retry-safe manual occurrence key")
		case "preview":
			fs.int(&count, "count", "number of future occurrences (1..100)")
			fs.string(&after, "after", 0, "RFC3339 anchor, default database clock")
		}
	}, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("recurrence-id must be a UUID")
		}
		path := "/api/recurrences/" + args[0]
		switch action {
		case "get":
			var out recurrences.Recurrence
			if err := rt.do(http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			return rt.printRecurrence(out)
		case "pause", "resume":
			if revision < 0 {
				return usagef("revision must be positive")
			}
			if revision == 0 {
				var current recurrences.Recurrence
				if err := rt.do(http.MethodGet, path, nil, &current); err != nil {
					return err
				}
				revision = int(current.Revision)
			}
			var out recurrences.Recurrence
			if err := rt.do(http.MethodPost, path+"/"+action, map[string]int{"expected_revision": revision}, &out); err != nil {
				return err
			}
			return rt.printRecurrence(out)
		case "run-now":
			if key == "" || len(key) > 128 {
				return usagef("--idempotency-key is required (1..128 bytes)")
			}
			var out recurrences.Occurrence
			if err := rt.do(http.MethodPost, path+"/run-now", map[string]string{"idempotency_key": key}, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err := fmt.Fprintf(rt.stdout, "%s #%d %s %s\n", out.RecurrenceID, out.Number, out.Outcome, out.Reason)
			return err
		case "preview":
			if count < 1 || count > 100 {
				return usagef("count must be 1..100")
			}
			query := url.Values{"count": {strconv.Itoa(count)}}
			if after != "" {
				query.Set("after", after)
			}
			var out struct {
				Times []string `json:"times"`
				Kind  string   `json:"trigger_kind"`
			}
			if err := rt.do(http.MethodGet, path+"/preview?"+query.Encode(), nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			if out.Kind == "event" {
				_, err := fmt.Fprintln(rt.stdout, "Event trigger: publication times cannot be predicted")
				return err
			}
			for _, at := range out.Times {
				if _, err := fmt.Fprintln(rt.stdout, at); err != nil {
					return err
				}
			}
			return nil
		}
		return nil
	}}
}
func (rt *runtime) printRecurrence(item recurrences.Recurrence) error {
	if rt.jsonOut {
		return rt.printJSON(item)
	}
	state := "active"
	if item.Paused {
		state = "paused"
	}
	next := "next publication"
	if item.NextAt != nil {
		next = item.NextAt.Format("2006-01-02T15:04:05Z07:00")
	}
	_, err := fmt.Fprintf(rt.stdout, "%s %s · %s · %s · revision %d\n", item.ID, item.Template.Title, state, next, item.Revision)
	return err
}
