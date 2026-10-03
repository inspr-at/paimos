// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/delivery"
)

func (rt *runtime) releaseProject(ctx context.Context, ref string) (string, error) {
	if validUUID(ref) {
		return ref, nil
	}
	if strings.TrimSpace(ref) == "" {
		return "", usagef("--project is required")
	}
	n, e := rt.projectNodeCtx(ctx, ref)
	return n.ID, e
}
func (rt *runtime) releaseID(ref string) (string, error) {
	if validUUID(ref) {
		return ref, nil
	}
	n, e := rt.nodeByKey(ref)
	return n.ID, e
}
func releasePath(project, release string) string {
	base := "/api/projects/" + url.PathEscape(project) + "/releases"
	if release != "" {
		base += "/" + url.PathEscape(release)
	}
	return base
}
func (rt *runtime) releaseOutput(method, path string, body any) error {
	var out json.RawMessage
	if e := rt.do(method, path, body, &out); e != nil {
		return e
	}
	return rt.printJSON(out)
}
func (rt *runtime) cmdRelease() *Command {
	root := &Command{Name: "release", Short: "Plan and publish project releases", Use: "release <command>"}
	for _, name := range []string{"list", "show", "items", "backlog", "plan", "rename", "rank", "freeze", "unfreeze", "cut", "publish", "close", "abandon", "place", "settings"} {
		root.subs = append(root.subs, rt.releaseCommand(name))
	}
	root.subs = append(root.subs, rt.releaseAdoptCommand(), rt.releaseAdoptionStatusCommand(), rt.releaseVerifyCommand(), &Command{Name: "build", Short: "Release build status", Use: "release build status RELEASE --project PROJECT", subs: []*Command{rt.releaseBuildStatusCommand()}})
	return root
}
func (rt *runtime) releaseCommand(name string) *Command {
	var project, title, visibility, creationKey, scheme, version, reservation, before, after, cursor, part, through, deadline, settings, file, release, due string
	var expected, releaseRevision, limit int
	var clearDeadline, defaults, completedUnplaced, completedLater, expedite, clearDue bool
	visibility = "published"
	limit = 0
	min, max := 1, 1
	if name == "list" || name == "backlog" || name == "plan" {
		min, max = 0, 0
	}
	if name == "rename" {
		min, max = 2, 2
	}
	if name == "settings" {
		min, max = 0, 1
	}
	cmd := &Command{Name: name, Short: map[string]string{"list": "List releases", "show": "Show a release", "items": "Read release items", "backlog": "Read ranked backlog or new tail", "plan": "Plan a release", "rename": "Rename a release", "rank": "Move a release by neighbour identity", "freeze": "Freeze release entry", "unfreeze": "Resume lifecycle without authorizing spending", "cut": "Reserve a release version and roll unfinished work forward", "publish": "Publish cumulative cut-window notes", "close": "Close an internal release", "abandon": "Abandon and roll items forward", "place": "Place an item with captured revisions", "settings": "Set entry deadline or build settings"}[name], Use: "release " + name + " [RELEASE] --project PROJECT", minArgs: min, maxArgs: max}
	cmd.addFlags = func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key or UUID")
		fs.int(&expected, "expected-revision", "captured revision; otherwise read this record before the write")
		switch name {
		case "list", "items", "backlog":
			fs.string(&cursor, "cursor", 0, "keyset cursor")
			fs.int(&limit, "limit", "page size (releases 50, items 200)")
			if name == "list" {
				fs.string(&visibility, "state", 0, "lifecycle filter")
				visibility = ""
			}
			if name == "items" {
				fs.string(&through, "through", 0, "include through this release UUID")
				fs.bool(&completedLater, "completed-later", 0, "completed units in later releases")
			}
			if name == "backlog" {
				fs.string(&part, "part", 0, "ranked or tail")
				fs.bool(&completedUnplaced, "completed-unplaced", 0, "completion events after adoption")
			}
		case "plan":
			fs.string(&title, "title", 0, "release title")
			fs.string(&visibility, "visibility", 0, "published or internal")
			fs.string(&creationKey, "creation-key", 0, "durable retry identity")
			fs.string(&after, "after", 0, "release UUID to place after")
			fs.string(&deadline, "entry-closes-at", 0, "RFC3339 entry deadline")
		case "rank":
			fs.string(&before, "before", 0, "release UUID before which to place")
			fs.string(&after, "after", 0, "release UUID after which to place")
		case "cut":
			fs.string(&scheme, "version-scheme", 0, "explicit version scheme")
			fs.string(&version, "version", 0, "canonical project version")
		case "publish":
			fs.string(&reservation, "reservation-ref", 0, "person attestation for a non-product project")
		case "place":
			fs.string(&release, "release", 0, "destination release UUID, or none for backlog")
			fs.int(&releaseRevision, "expected-release-revision", "captured destination release revision (required for a release)")
			fs.string(&before, "before", 0, "item UUID before which to place")
			fs.string(&after, "after", 0, "item UUID after which to place")
			fs.bool(&expedite, "expedite", 0, "person-only expedite flag")
			fs.string(&due, "due-on", 0, "person-only YYYY-MM-DD due date")
			fs.bool(&clearDue, "clear-due-on", 0, "clear the due date")
		case "settings":
			fs.string(&deadline, "entry-closes-at", 0, "RFC3339 entry deadline")
			fs.bool(&clearDeadline, "clear-entry-closes-at", 0, "explicitly remove the deadline")
			fs.string(&settings, "build-settings", 0, "closed-shape JSON (at most 2 KiB)")
			fs.string(&file, "file", 0, "build settings JSON file or - for stdin")
			fs.bool(&defaults, "defaults", 0, "set project defaults with releases.deploy")
		}
	}
	cmd.run = func(args []string) error {
		ctx := context.Background()
		projectID, e := rt.releaseProject(ctx, project)
		if e != nil {
			return e
		}
		if limit < 0 || limit > 200 || name == "list" && limit > 50 {
			return usagef("invalid page size")
		}
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprint(limit))
		}
		if name == "list" {
			if visibility != "" {
				q.Set("state", visibility)
			}
			return rt.releaseOutput(http.MethodGet, releasePath(projectID, "")+"?"+q.Encode(), nil)
		}
		if name == "backlog" {
			if part != "" {
				q.Set("part", part)
			}
			if completedUnplaced {
				q.Set("completed_unplaced", "1")
			}
			return rt.releaseOutput(http.MethodGet, "/api/projects/"+projectID+"/backlog?"+q.Encode(), nil)
		}
		if name == "plan" {
			in := map[string]any{"visibility": visibility}
			if title != "" {
				in["title"] = title
			}
			if creationKey != "" {
				in["creation_key"] = creationKey
			}
			if after != "" {
				if !validUUID(after) {
					return usagef("--after must be UUID")
				}
				in["after_release_id"] = after
			}
			if deadline != "" {
				if _, e = time.Parse(time.RFC3339, deadline); e != nil {
					return usagef("invalid entry deadline")
				}
				in["entry_closes_at"] = deadline
			}
			return rt.releaseOutput(http.MethodPost, releasePath(projectID, ""), in)
		}
		if name == "place" {
			id, e := rt.releaseID(args[0])
			if e != nil {
				return e
			}
			if expected < 0 {
				return usagef("expected revision cannot be negative")
			}
			if release == "" {
				return usagef("--release UUID|none is required")
			}
			in := map[string]any{"expected_project_id": projectID, "expected_revision": expected, "release_id": nil}
			if release != "none" {
				if !validUUID(release) || releaseRevision < 1 {
					return usagef("release UUID and captured release revision required")
				}
				in["release_id"] = release
				in["expected_release_revision"] = releaseRevision
			}
			if before != "" {
				in["before_id"] = before
			}
			if after != "" {
				in["after_id"] = after
			}
			if expedite {
				in["expedite"] = true
			}
			if due != "" && clearDue {
				return usagef("due-on and clear-due-on are mutually exclusive")
			}
			if due != "" {
				if _, e = time.Parse("2006-01-02", due); e != nil {
					return usagef("invalid due date")
				}
				in["due_on"] = due
			}
			if clearDue {
				in["due_on"] = nil
			}
			return rt.releaseOutput(http.MethodPut, "/api/nodes/"+id+"/ships-in", in)
		}
		if name == "settings" && defaults {
			return rt.releaseDefaults(projectID, settings, file, deadline, clearDeadline, expected)
		}
		if len(args) == 0 {
			return usagef("RELEASE is required")
		}
		id, e := rt.releaseID(args[0])
		if e != nil {
			return e
		}
		path := releasePath(projectID, id)
		if name == "show" {
			return rt.releaseOutput(http.MethodGet, path, nil)
		}
		if name == "items" {
			if through != "" {
				if !validUUID(through) {
					return usagef("--through must be UUID")
				}
				q.Set("through", through)
			}
			if completedLater {
				q.Set("completed_later", "1")
			}
			return rt.releaseOutput(http.MethodGet, path+"/items?"+q.Encode(), nil)
		}
		if expected < 0 {
			return usagef("expected revision must be positive")
		}
		revision := int64(expected)
		if revision == 0 {
			var current delivery.ReleaseView
			if e = rt.do(http.MethodGet, path, nil, &current); e != nil {
				return e
			}
			if current.ID != id || current.ProjectID != projectID || current.Revision < 1 {
				return usagef("release read did not match the requested record")
			}
			revision = current.Revision
		}
		in := map[string]any{"expected_revision": revision}
		method := http.MethodPost
		switch name {
		case "rename":
			in["title"] = args[1]
			method = http.MethodPatch
		case "rank":
			if before != "" {
				in["before_id"] = before
			}
			if after != "" {
				in["after_id"] = after
			}
			path += "/rank"
		case "freeze":
			in["to"] = "frozen"
			path += "/state"
		case "unfreeze":
			in["to"] = "building"
			path += "/state"
		case "abandon":
			in["to"] = "abandoned"
			path += "/state"
		case "cut":
			if !delivery.ValidProjectVersion(scheme, version) {
				return usagef("valid explicit --version-scheme and --version required")
			}
			in["version_scheme"], in["version"] = scheme, version
			path += "/cut"
		case "publish":
			if reservation != "" {
				in["reservation_ref"] = reservation
			}
			path += "/publish"
		case "close":
			path += "/close"
		case "settings":
			method = http.MethodPatch
			if deadline != "" && clearDeadline {
				return usagef("entry deadline and clear are mutually exclusive")
			}
			if deadline != "" {
				if _, e = time.Parse(time.RFC3339, deadline); e != nil {
					return usagef("invalid entry deadline")
				}
				in["entry_closes_at"] = deadline
			}
			if clearDeadline {
				in["entry_closes_at"] = nil
			}
			if file != "" {
				if settings != "" {
					return usagef("file and inline settings are mutually exclusive")
				}
				reader := rt.stdin
				if file != "-" {
					f, e := os.Open(file)
					if e != nil {
						return e
					}
					defer f.Close()
					reader = f
				}
				b, e := io.ReadAll(io.LimitReader(reader, 2049))
				if e != nil {
					return e
				}
				settings = string(b)
			}
			if settings != "" {
				if _, e = delivery.ParseBuildSettings([]byte(settings)); e != nil {
					return usagef("%s", e)
				}
				in["build_settings"] = json.RawMessage(settings)
			}

			if len(in) == 1 {
				return usagef("a settings edit is required")
			}
		}
		return rt.releaseOutput(method, path, in)
	}
	return cmd
}
func (rt *runtime) releaseAdoptionStatusCommand() *Command {
	var cursor, state string
	limit := 50
	return &Command{Name: "adoption-status", Short: "Private instance-local adoption progress", Use: "release adoption-status [--cursor CURSOR --limit 50]", addFlags: func(fs *flagSet) {
		fs.string(&cursor, "cursor", 0, "keyset cursor")
		fs.string(&state, "state", 0, "job phase")
		fs.int(&limit, "limit", "page size at most 50")
	}, run: func([]string) error {
		if limit < 1 || limit > 50 {
			return usagef("limit must be 1–50")
		}
		return rt.releaseOutput(http.MethodGet, "/api/delivery/adoptions?"+url.Values{"cursor": {cursor}, "state": {state}, "limit": {fmt.Sprint(limit)}}.Encode(), nil)
	}}
}
func (rt *runtime) releaseAdoptCommand() *Command {
	var retry, report bool
	var expected, limit int
	var cursor string
	return &Command{Name: "adopt", Short: "Preview/report or retry the automatic adoption job", Use: "release adopt [PROJECT] [--retry]", maxArgs: 1, addFlags: func(fs *flagSet) {
		fs.bool(&retry, "retry", 0, "wake the existing automatic job after repair")
		fs.bool(&report, "report", 0, "read the persisted report")
		fs.int(&expected, "expected-revision", "captured job revision")
		fs.int(&limit, "limit", "report page size at most 200")
		fs.string(&cursor, "cursor", 0, "report cursor")
	}, run: func(args []string) error {
		if len(args) == 0 {
			if retry || report {
				return usagef("PROJECT is required")
			}
			return rt.releaseOutput(http.MethodGet, "/api/delivery/adoptions", nil)
		}
		project, e := rt.releaseProject(context.Background(), args[0])
		if e != nil {
			return e
		}
		if report {
			if retry {
				return usagef("report and retry are mutually exclusive")
			}
			if limit < 0 || limit > 200 {
				return usagef("invalid report limit")
			}
			q := url.Values{"cursor": {cursor}}
			if limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			return rt.releaseOutput(http.MethodGet, "/api/projects/"+project+"/delivery/adoption-report?"+q.Encode(), nil)
		}
		revision := int64(expected)
		if expected < 0 {
			return usagef("invalid job revision")
		}
		if expected == 0 {
			var status delivery.DeliveryStatus
			if e = rt.do(http.MethodGet, "/api/projects/"+project+"/delivery", nil, &status); e != nil {
				return e
			}
			if status.ProjectID != project {
				return usagef("status identity mismatch")
			}
			if status.Adoption != nil {
				revision = status.Adoption.Revision
			}
		}
		action := "preview"
		if retry {
			action = "retry"
		}
		return rt.releaseOutput(http.MethodPost, "/api/projects/"+project+"/delivery/adopt", map[string]any{"action": action, "expected_revision": revision})
	}}
}
func (rt *runtime) releaseVerifyCommand() *Command {
	var project string
	return &Command{Name: "verify", Short: "Read-only project adoption evidence", Use: "release verify --project PROJECT", addFlags: func(fs *flagSet) { fs.string(&project, "project", 'p', "project key or UUID") }, run: func([]string) error {
		id, e := rt.releaseProject(context.Background(), project)
		if e != nil {
			return e
		}
		return rt.releaseOutput(http.MethodGet, "/api/projects/"+id+"/delivery/verify", nil)
	}}
}
func (rt *runtime) releaseBuildStatusCommand() *Command {
	var project string
	return &Command{Name: "status", Short: "Read lifecycle, authorization and current build summary", Use: "release build status RELEASE --project PROJECT", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) { fs.string(&project, "project", 'p', "project key or UUID") }, run: func(args []string) error {
		id, e := rt.releaseProject(context.Background(), project)
		if e != nil {
			return e
		}
		release, e := rt.releaseID(args[0])
		if e != nil {
			return e
		}
		return rt.releaseOutput(http.MethodGet, releasePath(id, release), nil)
	}}
}

func (rt *runtime) releaseDefaults(project, settings, file, deadline string, clear bool, expected int) error {
	if deadline != "" || clear || expected < 0 {
		return usagef("defaults requires build settings only and a nonnegative revision")
	}
	if file != "" {
		if settings != "" {
			return usagef("file and inline settings are mutually exclusive")
		}
		reader := rt.stdin
		if file != "-" {
			f, e := os.Open(file)
			if e != nil {
				return e
			}
			defer f.Close()
			reader = f
		}
		b, e := io.ReadAll(io.LimitReader(reader, 2049))
		if e != nil {
			return e
		}
		settings = string(b)
	}
	if _, e := delivery.ParseBuildSettings([]byte(settings)); e != nil {
		return usagef("%s", e)
	}
	revision := int64(expected)
	path := "/api/projects/" + project + "/delivery"
	if revision == 0 {
		var status delivery.DeliveryStatus
		if e := rt.do(http.MethodGet, path, nil, &status); e != nil {
			return e
		}
		if status.ProjectID != project || status.Revision < 1 {
			return usagef("project delivery identity missing")
		}
		revision = status.Revision
	}
	return rt.releaseOutput(http.MethodPatch, path, map[string]any{"expected_revision": revision, "build_defaults": json.RawMessage(settings)})
}
