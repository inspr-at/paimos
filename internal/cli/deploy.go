// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

func (rt *runtime) cmdDeploy() *Command {
	var project, environment, sha, version string
	return &Command{Name: "deploy", Short: "Record verified live deployments", Use: "deploy record --project PROJECT --env ENV --sha SHA [--version VERSION]", subs: []*Command{
		{Name: "record", Short: "Record a completed live check and deliver merged tickets", Use: "deploy record --project PROJECT --env ENV --sha SHA [--version VERSION]", maxArgs: 0,
			Long: "Run after OPS has verified the deployment live. Requires releases.deploy for the project. Uses the linked delivery repository and verified commit ancestry; never deploys or approves a release. Retries for the same project, environment and full SHA return the original confirmation. Human checks and ticket states that cannot advance are reported without changing them.",
			addFlags: func(fs *flagSet) {
				fs.string(&project, "project", 0, "project key or UUID")
				fs.string(&environment, "env", 0, "deployment environment or destination")
				fs.string(&sha, "sha", 0, "full deployed commit SHA (40 hex characters)")
				fs.string(&version, "version", 0, "optional deployed version label")
			}, run: func([]string) error {
				project, environment, sha, version = strings.TrimSpace(project), strings.TrimSpace(environment), strings.ToLower(sha), strings.TrimSpace(version)
				if project == "" || environment == "" || !reviewgate.ValidSHA(sha) || len(environment) > 128 || len(version) > 128 || strings.ContainsFunc(environment+version, unicode.IsControl) {
					return usagef("--project, --env and --sha (40 hex characters) are required; environment and version are limited to 128 bytes")
				}
				id, err := rt.projectIdentityCtx(rt.context(), project)
				if err != nil {
					return err
				}
				body := map[string]string{"environment": environment, "sha": sha}
				if version != "" {
					body["version"] = version
				}
				var out struct {
					EventID     int64  `json:"event_id"`
					Project     string `json:"project_id"`
					Environment string `json:"environment"`
					Repository  string `json:"repository"`
					SHA         string `json:"sha"`
					Version     string `json:"version"`
					ConfirmedAt string `json:"confirmed_at"`
					ConfirmedBy string `json:"confirmed_by"`
					Tickets     []struct {
						NodeID string `json:"node_id"`
						Result string `json:"result"`
						Reason string `json:"reason,omitempty"`
					} `json:"tickets"`
				}
				if err := rt.do(http.MethodPost, "/api/projects/"+id+"/deploys", body, &out); err != nil {
					return err
				}
				if rt.jsonOut {
					return rt.printJSON(out)
				}
				delivered, unchanged, skipped := 0, 0, 0
				for _, ticket := range out.Tickets {
					switch ticket.Result {
					case "delivered":
						delivered++
					case "unchanged":
						unchanged++
					case "skipped":
						skipped++
					}
				}
				if _, err := fmt.Fprintf(rt.stdout, "Deploy recorded: %s / %s at %s (event %d)\nDelivered: %d; unchanged: %d; skipped: %d\nConfirmed: %s by %s\n", out.Repository, out.Environment, out.SHA, out.EventID, delivered, unchanged, skipped, out.ConfirmedAt, out.ConfirmedBy); err != nil {
					return err
				}
				for _, ticket := range out.Tickets {
					if ticket.Reason != "" {
						if _, err := fmt.Fprintf(rt.stdout, "%s: %s — %s\n", ticket.NodeID, ticket.Result, ticket.Reason); err != nil {
							return err
						}
					}
				}
				return nil
			}},
	}}
}
