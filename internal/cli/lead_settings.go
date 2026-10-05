// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

func (rt *runtime) cmdProjectLeadSettings() *Command {
	c := &Command{Name: "lead-settings", Short: "Read or revision-check inherited lead policy", Use: "project lead-settings <get|set|reset>"}
	for _, action := range []string{"get", "set", "reset"} {
		var file, revision string
		var workspace bool
		sub := &Command{Name: action, Short: action + " lead settings (edits require a person session)", Use: "project lead-settings " + action + " [PROJECT] [--workspace]", maxArgs: 1}
		sub.addFlags = func(fs *flagSet) {
			fs.bool(&workspace, "workspace", 0, "workspace execution defaults")
			if action != "get" {
				fs.string(&revision, "revision", 0, "expected policy revision")
			}
			if action == "set" {
				fs.string(&file, "from", 0, "override JSON file or - for stdin")
			}
		}
		sub.run = func(args []string) error {
			if (len(args) == 0) != workspace {
				return usagef("supply a project or --workspace")
			}
			method := http.MethodGet
			var body any
			path := "/api/settings/lead-policy"
			var rev int64
			if action != "get" {
				var err error
				rev, err = strconv.ParseInt(revision, 10, 64)
				if err != nil || rev < 0 {
					return usagef("--revision requires a nonnegative integer")
				}
			}
			if action == "set" {
				if file == "" {
					return usagef("--from is required")
				}
				// The transport has a 16 KiB bound; reject before JSON decode locally too.
				var raw []byte
				var err error
				if file == "-" {
					raw, err = readBounded(rt.stdin, 16<<10)
				} else {
					raw, err = readBoundedFile(file, 16<<10)
				}
				if err != nil {
					return err
				}
				var overrides map[string]any
				if json.Unmarshal(raw, &overrides) != nil || overrides == nil {
					return usagef("--from requires one override JSON object")
				}
				body = map[string]any{"revision": rev, "overrides": overrides}
				method = http.MethodPut
			} else if action == "reset" {
				method = http.MethodDelete
			}
			if !workspace {
				id := args[0]
				if !validUUID(id) {
					n, err := rt.projectNode(id)
					if err != nil {
						return err
					}
					id = n.ID
				}
				path = "/api/projects/" + url.PathEscape(id) + "/lead-settings"
			}
			if action == "reset" {
				path += "?revision=" + strconv.FormatInt(rev, 10)
			}
			var out json.RawMessage
			if err := rt.do(method, path, body, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err := fmt.Fprintln(rt.stdout, string(out))
			return err
		}
		c.subs = append(c.subs, sub)
	}
	return c
}
