// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/client"
	"net/http"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

func (rt *runtime) cmdReview() *Command {
	return &Command{Name: "review", Short: "Authenticated reviewer runs", Use: "review run <create|submit>", subs: []*Command{
		{Name: "run", Short: "Create a bound run or submit as its assigned reviewer", Use: "review run <create|submit>", subs: []*Command{rt.reviewRunCommand(false), rt.reviewRunCommand(true)}},
	}}
}

func (rt *runtime) reviewRunCommand(submit bool) *Command {
	var ticket, id, credentialFile string
	in := reviewgate.RunCreate{Kind: "review", Target: "main"}
	verdict := reviewgate.RunSubmit{}
	pr := 0
	name := "create"
	if submit {
		name = "submit"
	}
	return &Command{Name: name, Short: map[bool]string{false: "Dispatcher creates an exact-range reviewer run", true: "Assigned reviewer claims and consumes its private run credential"}[submit], Use: "review run " + name + " --ticket KEY --repo OWNER/REPO --pr N --base SHA --head SHA", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&ticket, "ticket", 0, "ticket key or UUID")
			fs.string(&in.Repository, "repo", 0, "repository OWNER/REPO")
			fs.int(&pr, "pr", "pull request number")
			fs.string(&in.Target, "target", 0, "target branch")
			fs.string(&in.Base, "base", 0, "reviewed merge-base SHA")
			fs.string(&in.Head, "head", 0, "exact reviewed head SHA")
			fs.string(&in.Scope, "scope", 0, "exact base..head range (defaults to --base..--head)")
			if submit {
				fs.string(&id, "run", 0, "assigned reviewer run UUID")
				fs.string(&credentialFile, "credential-file", 0, "already claimed run credential file, or - for stdin; otherwise claim as this reviewer")
				fs.string(&verdict.Verdict, "verdict", 0, "ok or changes")
				fs.string(&verdict.VisualImpact, "visual-impact", 0, "yes, no or uncertain")
				fs.string(&verdict.Reason, "reason", 0, "bounded verdict and visual impact reason")
			} else {
				fs.string(&in.AuthorSession, "author-session", 0, "recorded author session UUID")
				fs.string(&in.ReviewerSession, "reviewer-session", 0, "independent reviewer's session UUID")
				fs.string(&in.Kind, "kind", 0, "review or visual (Tick grant required for visual)")
			}
		}, run: func([]string) error {
			if in.Scope == "" {
				in.Scope = in.Base + ".." + in.Head
			}
			in.PR = int64(pr)
			binding := reviewgate.RunBinding{Repository: in.Repository, PR: in.PR, Target: in.Target, Base: in.Base, Head: in.Head, Scope: in.Scope}
			if ticket == "" || !binding.Valid() {
				return usagef("--ticket, --repo, --pr, --target, --base, --head and an exact --scope are required")
			}
			if submit {
				verdict.Binding = binding
				if !validUUID(id) || !verdict.Valid() {
					return usagef("--run, --verdict (ok or changes), --visual-impact (yes, no or uncertain) and --reason are required")
				}
			} else if !validUUID(in.AuthorSession) || !validUUID(in.ReviewerSession) || in.Kind != "review" && in.Kind != "visual" {
				return usagef("--author-session, --reviewer-session and --kind (review or visual) are required")
			}
			n := apiNode{ID: ticket}
			var err error
			if !validUUID(ticket) {
				n, err = rt.nodeRef(ticket)
				if err != nil {
					return err
				}
			}
			var out reviewgate.Run
			if !submit {
				err = rt.do(http.MethodPost, "/api/nodes/"+n.ID+"/review-runs", in, &out)
			} else {
				var token string
				if credentialFile != "" {
					token, err = rt.harnessSecret(credentialFile, "credential-file")
				} else {
					var claim struct {
						Credential string `json:"credential"`
					}
					err = rt.harnessDo(http.MethodPost, "/api/review-runs/"+id+"/credential", "", nil, &claim)
					token = claim.Credential
				}
				if err != nil {
					return err
				}
				if reviewgate.CredentialTenant(token) == "" {
					return fmt.Errorf("invalid run credential")
				}
				verdict.Binding.Ticket = n.ID
				baseURL, e := rt.reviewRunURL()
				if e != nil {
					return e
				}
				// A capability never travels with the dispatcher's ordinary key,
				// follows redirects, or enters CLI output or persistent config.
				private := client.New(baseURL, "")
				private.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				err = private.DoWithHeaders(rt.context(), http.MethodPost, "/api/review-runs/"+id+"/verdict", verdict, &out, map[string]string{reviewgate.CredentialHeader: token})
				if err != nil {
					return rt.fail(err, token)
				}
			}
			if err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			fmt.Fprintf(rt.stdout, "Review run %s: %s %s (%s..%s)\n", out.ID, out.Kind, out.Verdict, out.Base, out.Head)
			return nil
		}}
}

// Resolve only instance URLs for a claimed capability; do not require, load or
// migrate an ordinary agent key that this request must never send.
func (rt *runtime) reviewRunURL() (string, error) {
	var base string
	if urlVar, _, _, ok := rt.envNames(); ok {
		if rt.instance != "" {
			return "", usagef("--instance conflicts with %s", urlVar)
		}
		base = os.Getenv(urlVar)
	} else {
		path, err := rt.configFile()
		if err != nil {
			return "", err
		}
		cfg, err := readPersonConfig(path)
		if err != nil {
			return "", err
		}
		_, inst, err := pickInstance(cfg, rt.instance)
		if err != nil {
			return "", err
		}
		base = inst.URL
	}
	return normalizeURL(base)
}
