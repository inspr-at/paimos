// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/goals"
)

func (rt *runtime) cmdGoal() *Command {
	return &Command{Name: "goal", Short: "Goals and outcomes", Use: "goal <create|update>", subs: []*Command{rt.cmdGoalCreate(), rt.cmdGoalUpdate()}}
}
func (rt *runtime) goalWrite(method, path, key string, body any, dry bool) error {
	clean, err := goals.CleanIdempotencyKey(key)
	if err != nil {
		return usagef("%s", err)
	}
	if dry {
		return rt.printJSON(map[string]any{"dry_run": true, "method": method, "path": path, "headers": map[string]string{"Idempotency-Key": clean}, "body": body})
	}
	var out json.RawMessage
	if err := rt.doHeaders(method, path, body, &out, map[string]string{"Idempotency-Key": clean}); err != nil {
		return err
	}
	return rt.printJSON(out)
}
func (rt *runtime) cmdGoalCreate() *Command {
	var project, key string
	var workspace, dry bool
	return &Command{Name: "create", Short: "Create a project or workspace goal", Use: "goal create TITLE --project KEY|--workspace --idempotency-key KEY [--dry-run]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key or UUID")
			fs.bool(&workspace, "workspace", 0, "workspace goal")
			fs.string(&key, "idempotency-key", 0, "stable receipt key; reuse after a lost response")
			fs.bool(&dry, "dry-run", 0, "show request")
		},
		run: func(args []string) error {
			if workspace == (project != "") {
				return usagef("choose exactly one of --project or --workspace")
			}
			if strings.TrimSpace(args[0]) == "" || utf8.RuneCountInString(args[0]) > 300 {
				return usagef("title must contain 1–300 characters")
			}
			if _, err := goals.CleanIdempotencyKey(key); err != nil {
				return usagef("%s", err)
			}
			body := map[string]any{"title": args[0], "project_id": nil}
			if project != "" {
				p, err := rt.projectNode(project)
				if err != nil {
					return err
				}
				body["project_id"] = p.ID
			}
			return rt.goalWrite(http.MethodPost, "/api/goals", key, body, dry)
		}}
}
func (rt *runtime) cmdGoalUpdate() *Command {
	var file, key, etaText, etaAt, progress, watermark string
	var dry bool
	return &Command{Name: "update", Short: "Atomically update ETA, steps, progress and outcome refs", Use: "goal update KEY --idempotency-key KEY [--file PATH|- | --eta TEXT --eta-at RFC3339 --progress 0..100|clear] [--if-read-through N] [--dry-run]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&file, "file", 0, "bounded update JSON file or - for stdin")
			fs.string(&key, "idempotency-key", 0, "stable receipt key; reuse the same key and body on retry")
			fs.string(&etaText, "eta", 0, "authored ETA text")
			fs.string(&etaAt, "eta-at", 0, "RFC3339 ETA clock")
			fs.string(&progress, "progress", 0, "0–100 or clear")
			fs.string(&watermark, "if-read-through", 0, "latest goal event id, required for replace")
			fs.bool(&dry, "dry-run", 0, "validate and show request")
		},
		run: func(args []string) error {
			if _, err := goals.CleanIdempotencyKey(key); err != nil {
				return usagef("%s", err)
			}
			if file != "" && (etaText != "" || etaAt != "" || progress != "") {
				return usagef("--file cannot be combined with --eta, --eta-at or --progress")
			}
			var in goals.Update
			if file != "" {
				var raw []byte
				var err error
				if file == "-" {
					raw, err = readBounded(rt.stdin, goals.MaxBodyBytes)
				} else {
					raw, err = readBoundedFile(file, goals.MaxBodyBytes)
				}
				if err != nil {
					return err
				}
				in, err = goals.DecodeUpdate(bytes.NewReader(raw), time.Now())
				if err != nil {
					return usagef("%s", err)
				}
			} else {
				if etaText != "" || etaAt != "" {
					value := goals.ETAInput{Text: etaText}
					if etaAt != "" {
						at, err := time.Parse(time.RFC3339, etaAt)
						if err != nil {
							return usagef("--eta-at must be RFC3339")
						}
						value.At = &at
					}
					in.ETA = goals.Field[goals.ETAInput]{Set: true, Value: &value}
				}
				if progress != "" {
					in.ProgressOverride.Set = true
					if progress != "clear" {
						pct, err := strconv.Atoi(progress)
						if err != nil {
							return usagef("--progress must be 0–100 or clear")
						}
						in.ProgressOverride.Value = &goals.ProgressOverrideInput{Pct: &pct}
					}
				}
			}
			if watermark != "" {
				value, err := strconv.ParseInt(watermark, 10, 64)
				if err != nil || value < 0 {
					return usagef("--if-read-through must be a non-negative integer")
				}
				in.IfReadThrough = goals.Field[int64]{Set: true, Value: &value}
			}
			if err := in.Validate(time.Now()); err != nil {
				return usagef("%s", err)
			}
			return rt.goalWrite(http.MethodPatch, "/api/goals/"+url.PathEscape(args[0]), key, in, dry)
		}}
}
