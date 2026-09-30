// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"strings"
)

// doctrineMaxRule is the server's rule budget; checked here to fail early.
const doctrineMaxRule = 8000

func (rt *runtime) cmdDoctrine() *Command {
	return &Command{
		Name:  "doctrine",
		Short: "Propose doctrine changes for a person to send to git",
		Use:   "doctrine <propose|list>",
		subs:  []*Command{rt.cmdDoctrinePropose(), rt.cmdDoctrineList()},
	}
}

type doctrineInboxWire struct {
	RequestID  string            `json:"request_id"`
	Repository string            `json:"repository"`
	Path       string            `json:"path"`
	RuleKey    string            `json:"rule_key"`
	RuleSHA    string            `json:"rule_sha256,omitempty"`
	Source     string            `json:"source"`
	TLDR       map[string]string `json:"tldr,omitempty"`
	Why        string            `json:"why"`
	Ticket     string            `json:"ticket,omitempty"`
}

func (rt *runtime) cmdDoctrinePropose() *Command {
	var repo, path, key, sha, file, why, whyFile, ticket, tldr, tldrDE, requestID string
	return &Command{
		Name:  "propose",
		Short: "Propose a change to one doctrine rule",
		Long: "Records the new text of one rule against the pinned doctrine. It appears under Settings → Agent rules → Doctrine for a person, " +
			"who sends it to git as a pull request, edits it first, or dismisses it with a reason. Nothing reaches git before a person acts. " +
			"The editor's checks apply: one rule in the same section, the size budget, no credentials, no private or identity text in the public " +
			"repository. Locked rules change only through a person. Without --tldr the rule keeps its current TL;DR. " +
			"Take --rule-key and the current text from GET /api/rules/doctrine (rules[].key, rules[].source).",
		Use: "doctrine propose --repo inspr-modules --path <file> --rule-key <key> --file <new-text> --why <reason> [--ticket KEY]",
		addFlags: func(fs *flagSet) {
			fs.string(&repo, "repo", 0, "doctrine repository: inspr-modules, inspr-doctrine-private or owner/name")
			fs.string(&path, "path", 0, "doctrine file in that repository")
			fs.string(&key, "rule-key", 0, "the rule's key at the pin")
			fs.string(&sha, "rule-sha", 0, "the rule's sha256 at the pin (optional guard against a moved pin)")
			fs.string(&file, "file", 0, "file with the rule's new text, or - for stdin")
			fs.string(&why, "why", 0, "why this change; it becomes the pull request description")
			fs.string(&whyFile, "why-file", 0, "file with the reason, or - for stdin")
			fs.string(&ticket, "ticket", 0, "ticket the change came from; it hears the outcome")
			fs.string(&tldr, "tldr", 0, "new English TL;DR (default: keep the current one)")
			fs.string(&tldrDE, "tldr-de", 0, "new German TL;DR (with --tldr)")
			fs.string(&requestID, "request-id", 0, "UUID that makes a retry safe (default: new)")
		},
		run: func([]string) error {
			repo, path, key, ticket = strings.TrimSpace(repo), strings.TrimSpace(path), strings.TrimSpace(key), strings.TrimSpace(ticket)
			if repo == "" || path == "" || key == "" {
				return usagef("--repo, --path and --rule-key are required")
			}
			if strings.TrimSpace(file) == "" {
				return usagef("--file is required")
			}
			if file == "-" && whyFile == "-" {
				return usagef("only one of --file and --why-file can read stdin")
			}
			source, err := rt.readText("", file, "rule")
			if err != nil {
				return err
			}
			if strings.TrimSpace(source) == "" || len(source) > doctrineMaxRule {
				return usagef("the new rule text must be 1 to %d bytes", doctrineMaxRule)
			}
			reason, err := rt.readText(why, whyFile, "why")
			if err != nil {
				return err
			}
			if strings.TrimSpace(reason) == "" {
				return usagef("--why is required")
			}
			if tldrDE != "" && tldr == "" {
				return usagef("--tldr-de needs --tldr")
			}
			requestID = strings.ToLower(strings.TrimSpace(requestID))
			if requestID == "" {
				if requestID, err = newUUIDv4(); err != nil {
					return err
				}
			} else if !validUUID(requestID) {
				return usagef("--request-id must be a UUID")
			}
			body := doctrineInboxWire{RequestID: requestID, Repository: repo, Path: path, RuleKey: key, RuleSHA: strings.TrimSpace(sha), Source: source, Why: strings.TrimSpace(reason), Ticket: ticket}
			if tldr != "" {
				body.TLDR = map[string]string{"en": strings.TrimSpace(tldr), "de": strings.TrimSpace(tldrDE)}
			}
			var proposal map[string]any
			if err := rt.do(http.MethodPost, "/api/rules/doctrine/inbox", body, &proposal); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(proposal)
			}
			fmt.Fprintf(rt.stdout, "proposed %s %s#%s (%s); a person sends it to git\n", proposal["id"], path, key, proposal["state"])
			return nil
		},
	}
}

type doctrineInboxItem struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Repository    string `json:"repository"`
	Path          string `json:"path"`
	Label         string `json:"label"`
	Ticket        string `json:"ticket"`
	PRURL         string `json:"pr_url"`
	DismissReason string `json:"dismiss_reason"`
	Outdated      bool   `json:"outdated"`
}

func (rt *runtime) cmdDoctrineList() *Command {
	return &Command{
		Name:  "list",
		Short: "List doctrine proposals",
		Long:  "An agent sees its own proposals with their outcome, including a dismissal reason; a person sees every waiting proposal.",
		Use:   "doctrine list",
		run: func([]string) error {
			var out struct {
				Pending int                 `json:"pending"`
				Items   []doctrineInboxItem `json:"items"`
			}
			if err := rt.do(http.MethodGet, "/api/rules/doctrine/inbox", nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			if len(out.Items) == 0 {
				fmt.Fprintln(rt.stdout, "no doctrine proposals")
				return nil
			}
			for _, item := range out.Items {
				line := fmt.Sprintf("%s  %-9s %s  %s", item.ID, item.State, item.Path, item.Label)
				if item.Ticket != "" {
					line += "  " + item.Ticket
				}
				switch {
				case item.PRURL != "":
					line += "  " + item.PRURL
				case item.DismissReason != "":
					line += "  — " + item.DismissReason
				case item.Outdated:
					line += "  (rule changed at the pin)"
				}
				fmt.Fprintln(rt.stdout, line)
			}
			return nil
		},
	}
}
