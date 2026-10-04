// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

func (rt *runtime) cmdKeys() *Command {
	var add, remove []string
	var sessionFile, rawURL string
	command := &Command{Name: "keys", Short: "Show and manage agent keys", Use: "keys <show|scopes|owner-workstation>",
		subs: []*Command{{Name: "scopes", Short: "Change scopes without rotating a key", Use: "keys scopes <key-id> [--add SCOPE] [--remove SCOPE] [--session-file PATH] [--url URL]", minArgs: 1, maxArgs: 1,
			Long: "Requires a person with keys.manage. Pass an existing aeon_session cookie value through --session-file (or - for stdin); it is never stored or printed. Otherwise configured agent credentials are used and the server refuses agent callers. Scopes may be comma-separated; changes are atomic and audited.",
			addFlags: func(fs *flagSet) {
				fs.strings(&add, "add", "scope to add (repeatable or comma-separated)")
				fs.strings(&remove, "remove", "scope to remove (repeatable or comma-separated)")
				fs.string(&sessionFile, "session-file", 0, "person session cookie file, or - for stdin")
				fs.string(&rawURL, "url", 0, "instance URL for --session-file (default: configured instance)")
			},
			run: func(args []string) error {
				if !validUUID(args[0]) {
					return usagef("key-id must be a UUID")
				}
				if len(add)+len(remove) == 0 {
					return usagef("provide --add or --remove")
				}
				parse := func(values []string) []string {
					out := []string{}
					for _, value := range values {
						for _, s := range strings.Split(value, ",") {
							out = append(out, strings.TrimSpace(s))
						}
					}
					return out
				}
				body := map[string]any{"add": parse(add), "remove": parse(remove)}
				// Decode only metadata, even if a faulty server adds a secret field.
				var result struct {
					ID     string   `json:"id"`
					Scopes []string `json:"scopes"`
				}
				path := "/api/agent-keys/" + args[0] + "/scopes"
				if sessionFile == "" {
					if rawURL != "" {
						return usagef("--url requires --session-file")
					}
					if err := rt.do(http.MethodPatch, path, body, &result); err != nil {
						return err
					}
				} else {
					if rawURL == "" {
						cfg, _, err := rt.loadConfig()
						if err != nil {
							return err
						}
						_, instance, err := pickInstance(cfg, rt.instance)
						if err != nil {
							return err
						}
						rawURL = instance.URL
					}
					baseURL, err := normalizeURL(rawURL)
					if err != nil {
						return err
					}
					session, err := rt.readSecret(sessionFile, "session cookie")
					if err != nil {
						return err
					}
					cookie := &http.Cookie{Name: "aeon_session", Value: session}
					if cookie.Valid() != nil {
						return usagef("invalid session cookie value")
					}
					c := client.New(baseURL, "")
					c.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					if err := c.DoWithHeaders(context.Background(), http.MethodPatch, path, body, &result, map[string]string{"Cookie": cookie.String(), "Origin": baseURL}); err != nil {
						return rt.fail(err, session)
					}
				}
				if rt.jsonOut {
					return rt.printJSON(result)
				}
				_, err := fmt.Fprintf(rt.stdout, "Updated scopes for %s: %s\n", result.ID, strings.Join(result.Scopes, ", "))
				return err
			},
		}},
	}
	command.subs = append(command.subs, rt.cmdKeyShow(), rt.cmdOwnerWorkstation())
	return command
}
