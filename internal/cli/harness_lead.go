// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) harnessLead() *Command {
	var project, refFile, leaseFile string
	revision := -1
	claim := &Command{Name: "claim", Short: "Prove the running session selected by its person", Use: "harness lead claim --project KEY --harness-session-file PATH --worker-lease-file PATH", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 'p', "project key or UUID")
			fs.string(&refFile, "harness-session-file", 0, "private existing harness reference file")
			fs.string(&leaseFile, "worker-lease-file", 0, "private existing worker lease file")
			fs.int(&revision, "expected-revision", "lead revision; defaults to a fresh read")
		}, run: func([]string) error {
			if refFile == "-" && leaseFile == "-" {
				return usagef("the session reference and lease cannot both read stdin")
			}
			ref, err := rt.harnessSecret(refFile, "harness-session-file")
			if err != nil {
				return usagef("private harness session file unavailable or invalid")
			}
			lease, err := rt.harnessSecret(leaseFile, "worker-lease-file")
			if err != nil {
				return usagef("private worker lease file unavailable or invalid")
			}
			if len(ref) > 4096 || len(lease) < 32 || len(lease) > 256 {
				return usagef("invalid session reference or worker lease")
			}
			id, err := rt.harnessProject(project)
			if err != nil {
				return rt.fail(err, ref, lease)
			}
			path := "/api/projects/" + id + "/lead"
			if revision < 0 {
				var current harness.Lead
				if err = rt.harnessDo(http.MethodGet, path, "", nil, &current); err != nil {
					return rt.fail(err, ref, lease)
				}
				revision = int(current.Revision)
			}
			if revision < 1 {
				return usagef("a person must first confirm Adopt a running session on the project lead card")
			}
			var out harness.Lead
			if err = rt.harnessDo(http.MethodPost, path+"/claim", lease, map[string]any{"expected_revision": revision, "harness_session_ref": ref}, &out); err != nil {
				return rt.fail(err, ref, lease)
			}
			// Even a malformed/echoing upstream cannot expose the proof in success output.
			text := out.ProjectID + out.State + out.Reason
			if out.SessionID != nil {
				text += *out.SessionID
			}
			if strings.Contains(text, ref) || strings.Contains(text, lease) {
				return rt.fail(fmt.Errorf("lead response contained private proof"), ref, lease)
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s · revision %d · generation %d\n", out.State, out.Revision, out.Generation)
			return err
		}}
	return &Command{Name: "lead", Short: "Claim the explicitly selected project lead", Use: "harness lead claim", subs: []*Command{claim}}
}
