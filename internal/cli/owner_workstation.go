// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

// Whitelist metadata: even a faulty server cannot print a token through these
// commands. Session cookies are used only for the explicitly supplied request.
type workstationKeyView struct {
	ID               string   `json:"id"`
	PrincipalID      string   `json:"principal_id"`
	Name             string   `json:"name"`
	Scopes           []string `json:"scopes"`
	OwnerWorkstation bool     `json:"owner_workstation"`
	Computer         *string  `json:"workstation_computer_id"`
}

func (rt *runtime) keyMetadataRequest(method, path, sessionFile, rawURL string, body, result any) error {
	if sessionFile == "" {
		if rawURL != "" {
			return usagef("--url requires --session-file")
		}
		return rt.do(method, path, body, result)
	}
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
	base, err := normalizeURL(rawURL)
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
	c := client.New(base, "")
	c.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err := c.DoWithHeaders(context.Background(), method, path, body, result, map[string]string{"Cookie": cookie.String(), "Origin": base}); err != nil {
		return rt.fail(err, session)
	}
	return nil
}

func (rt *runtime) printWorkstationKey(v workstationKeyView) error {
	if rt.jsonOut {
		return rt.printJSON(v)
	}
	fmt.Fprintf(rt.stdout, "key: %s (%s)\n", v.Name, v.ID)
	if v.OwnerWorkstation && v.Computer != nil {
		fmt.Fprintf(rt.stdout, "owner workstation: %s\n", *v.Computer)
	}
	return nil
}

func (rt *runtime) cmdKeyShow() *Command {
	var sessionFile, rawURL string
	return &Command{Name: "show", Short: "Show key metadata without secrets", Use: "keys show <key-id> [--session-file PATH] [--url URL]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&sessionFile, "session-file", 0, "person session cookie file, or - for stdin")
			fs.string(&rawURL, "url", 0, "instance URL for --session-file")
		},
		run: func(args []string) error {
			if !validUUID(args[0]) {
				return usagef("key-id must be a UUID")
			}
			var result struct {
				Keys []workstationKeyView `json:"keys"`
			}
			if err := rt.keyMetadataRequest("GET", "/api/agent-keys", sessionFile, rawURL, nil, &result); err != nil {
				return err
			}
			for _, key := range result.Keys {
				if key.ID == args[0] {
					return rt.printWorkstationKey(key)
				}
			}
			return fmt.Errorf("key not found")
		},
	}
}

func (rt *runtime) cmdOwnerWorkstation() *Command {
	var sessionFile, rawURL, computer string
	var clear bool
	return &Command{Name: "owner-workstation", Short: "Owner person session: mark or unmark a paired workstation key", Use: "keys owner-workstation <key-id> (--computer ID | --clear) --session-file PATH [--url URL]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&sessionFile, "session-file", 0, "Owner session cookie file, or - for stdin")
			fs.string(&rawURL, "url", 0, "instance URL for --session-file")
			fs.string(&computer, "computer", 0, "paired computer UUID")
			fs.bool(&clear, "clear", 0, "remove the workstation designation")
		},
		run: func(args []string) error {
			if !validUUID(args[0]) || sessionFile == "" || clear && computer != "" || !clear && !validUUID(computer) {
				return usagef("Owner --session-file, key UUID and either --computer UUID or --clear required")
			}
			body := map[string]any{"owner_workstation": !clear}
			if !clear {
				body["workstation_computer_id"] = computer
			}
			var result workstationKeyView
			if err := rt.keyMetadataRequest("PUT", "/api/agent-keys/"+args[0]+"/owner-workstation", sessionFile, rawURL, body, &result); err != nil {
				return err
			}
			return rt.printWorkstationKey(result)
		},
	}
}
