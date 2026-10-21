// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/goals"
)

// goal note (AEON-1494): the workspace status note on Goals, or one item's live
// note. Both carry their author and time on the server; null clears them.
func (rt *runtime) cmdGoalNote() *Command {
	var item, key string
	var workspace, clear, dry bool
	return &Command{Name: "note", Short: "Set or clear the workspace status note or an item's note",
		Use:     "goal note --workspace [TEXT|--clear] | goal note GOAL --item REF (TEXT|--clear); writes need --idempotency-key KEY [--dry-run]",
		Long:    goalAgentScopeHelp,
		maxArgs: 2,
		addFlags: func(fs *flagSet) {
			fs.bool(&workspace, "workspace", 0, "the workspace status note; without TEXT or --clear it is read")
			fs.string(&item, "item", 0, "outcome ref of the item whose note to set")
			fs.bool(&clear, "clear", 0, "remove the note")
			fs.string(&key, "idempotency-key", 0, "stable receipt key; reuse the same key and text on retry")
			fs.bool(&dry, "dry-run", 0, "validate and show the request")
		},
		run: func(args []string) error {
			if workspace == (item != "") {
				return usagef("choose exactly one of --workspace or --item REF")
			}
			if workspace {
				if len(args) > 1 {
					return usagef("--workspace takes at most one TEXT")
				}
				if len(args) == 0 && !clear {
					return rt.goalRead("/api/goals/status-note")
				}
				in := goals.StatusNoteInput{Text: goals.Field[string]{Set: true}}
				if len(args) == 1 {
					if clear {
						return usagef("give TEXT or --clear, not both")
					}
					in.Text.Value = &args[0]
				}
				if err := in.Validate(); err != nil {
					return usagef("%s", err)
				}
				return rt.goalWrite(http.MethodPatch, "/api/goals/status-note", key, in, dry)
			}
			if len(args) == 0 || len(args) == 1 && !clear || len(args) == 2 && clear {
				return usagef("use goal note GOAL --item REF TEXT, or GOAL --item REF --clear")
			}
			upsert := map[string]any{"ref": item, "note": nil}
			if !clear {
				text := strings.TrimSpace(args[1])
				if text == "" || utf8.RuneCountInString(text) > 200 {
					return usagef("an item note must contain 1–200 characters")
				}
				upsert["note"] = text
			}
			body := map[string]any{"items": map[string]any{"upsert": []any{upsert}}}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			in, err := goals.DecodeRequest(strings.NewReader(string(raw)))
			if err != nil {
				return usagef("%s", err)
			}
			return rt.goalWrite(http.MethodPatch, "/api/goals/"+url.PathEscape(args[0]), key, in, dry)
		}}
}
