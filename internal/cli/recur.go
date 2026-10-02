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
	var bodyFile string
	c := &Command{Name: action, Short: action + " a recurrence from JSON", Use: "recur " + action + " [--body-file FILE|-]", maxArgs: 0, addFlags: func(fs *flagSet) { fs.string(&bodyFile, "body-file", 0, "JSON definition; - reads stdin") }}
	if action == "update" {
		c.Use = "recur update <recurrence-id> [--body-file FILE|-]"
		c.minArgs = 1
		c.maxArgs = 1
	}
	c.run = func(args []string) error {
		if action == "update" && !validUUID(args[0]) {
			return usagef("recurrence-id must be a UUID")
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
	var key, after, releaseKey string
	var forceOverlap bool
	return &Command{Name: action, Short: action + " a recurrence", Use: "recur " + action + " <recurrence-id>", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) {
		switch action {
		case "pause", "resume":
			fs.int(&revision, "revision", "expected revision; defaults to the current GET result")
		case "run-now":
			fs.string(&key, "idempotency-key", 0, "required retry-safe manual occurrence key")
			fs.string(&releaseKey, "release-key", 0, "trusted published release key from the recurrence releases API")
			fs.bool(&forceOverlap, "force-overlap", 0, "explicitly create despite an open previous occurrence")
			fs.int(&revision, "revision", "optional expected definition revision")
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
			if key == "" || len(key) > 128 || len(releaseKey) > 256 || revision < 0 {
				return usagef("--idempotency-key is required (1..128 bytes)")
			}
			var out recurrences.Occurrence
			body := map[string]any{"idempotency_key": key}
			if releaseKey != "" {
				body["release_key"] = releaseKey
			}
			if forceOverlap {
				body["force_overlap"] = true
			}
			if revision > 0 {
				body["expected_revision"] = revision
			}
			if err := rt.do(http.MethodPost, path+"/run-now", body, &out); err != nil {
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
