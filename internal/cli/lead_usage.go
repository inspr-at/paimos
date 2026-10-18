// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"net/http"
	"net/url"
	"strconv"
)

// Measurement only: this command grants no admission, assignment or execution.
func (rt *runtime) harnessLeadUsage() *Command {
	var project, from, to string
	var generation int
	return &Command{Name: "lead-usage", Short: "Read measured project lead usage and private holds", Use: "harness lead-usage --project KEY [--generation N] [--from DATE --to DATE]", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key")
		fs.int(&generation, "generation", "original lead generation (omit for project total)")
		fs.string(&from, "from", 0, "inclusive date or RFC3339")
		fs.string(&to, "to", 0, "exclusive date or RFC3339")
	}, run: func([]string) error {
		if generation < 0 || (from == "") != (to == "") {
			return usagef("generation must be positive when supplied; --from and --to are both required")
		}
		id, err := rt.harnessProject(project)
		if err != nil {
			return err
		}
		query := url.Values{}
		if generation > 0 {
			query.Set("generation", strconv.Itoa(generation))
		}
		if from != "" {
			query.Set("from", from)
			query.Set("to", to)
		}
		path := "/api/projects/" + id + "/lead/usage"
		if len(query) > 0 {
			path += "?" + query.Encode()
		}
		var out any
		if err = rt.harnessDo(http.MethodGet, path, "", nil, &out); err != nil {
			return err
		}
		return rt.printHarness(out)
	}}
}
