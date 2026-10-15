// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"golang.org/x/term"
)

func (rt *runtime) cmdAgentsAssistant() *Command {
	root := &Command{Name: "assistant", Short: "Provision, rotate or revoke a messaging-only assistant", Use: "agents assistant <create|rotate|revoke>"}
	for _, operation := range []string{"create", "rotate"} {
		var name, expiry, keyOut string
		cmd := &Command{Name: operation, Short: operation + " a messaging-only assistant", Use: "agents assistant " + operation + " [principal-id] --expires 30d --key-out PATH|-", maxArgs: 0,
			Long: "Creates only inbox.read, inbox.send and inbox.receipt authority, bounded by a person owner. Expiry is required and at most 30 days. --key-out - writes only the one-time secret to non-terminal stdout for piping; files are created exclusively with mode 0600. Rotation immediately revokes the previous keys. No secret is printed in metadata or errors.",
			addFlags: func(fs *flagSet) {
				fs.string(&name, "name", 0, "unique assistant name (create only)")
				fs.string(&expiry, "expires", 0, "required lifetime, such as 7d or 30d (maximum 30d)")
				fs.string(&keyOut, "key-out", 0, "required new 0600 output file, or - for piping")
			},
			run: func(args []string) error {
				if operation == "create" && strings.TrimSpace(name) == "" {
					return usagef("--name is required")
				}
				if operation == "rotate" && (name != "" || !validUUID(args[0])) {
					return usagef("rotate requires a principal UUID and no --name")
				}
				lifetime, err := assistantLifetime(expiry)
				if err != nil {
					return err
				}
				if keyOut == "" {
					return usagef("--key-out is required")
				}
				var output io.Writer = rt.stdout
				var file *os.File
				written := false
				if keyOut == "-" {
					if f, ok := output.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
						return usagef("--key-out - requires piped stdout")
					}
				} else {
					file, err = os.OpenFile(keyOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if err != nil {
						return errors.New("cannot create key output file (must not already exist)")
					}
					defer func() {
						_ = file.Close()
						if !written {
							_ = os.Remove(keyOut)
						}
					}()
					output = file
				}
				path := "/api/agents/assistants"
				body := map[string]any{"expires_at": time.Now().Add(lifetime).UTC()}
				if operation == "create" {
					body["name"] = strings.TrimSpace(name)
				} else {
					path += "/" + args[0] + "/rotate"
				}
				var result struct {
					ID          string    `json:"id"`
					PrincipalID string    `json:"principal_id"`
					Token       string    `json:"token"`
					Scopes      []string  `json:"scopes"`
					ExpiresAt   time.Time `json:"expires_at"`
				}
				if err := rt.do("POST", path, body, &result); err != nil {
					return assistantRequestError(err)
				}
				if result.Token == "" || strings.ContainsAny(result.Token, "\r\n") {
					return errors.New("assistant response omitted a valid one-time key")
				}
				if _, err := io.WriteString(output, result.Token+"\n"); err != nil {
					return fmt.Errorf("assistant %s created, but writing its key failed; revoke it before retrying", result.PrincipalID)
				}
				if file != nil {
					if err := file.Sync(); err != nil {
						return fmt.Errorf("assistant %s created, but saving its key failed; revoke it before retrying", result.PrincipalID)
					}
					if err := file.Close(); err != nil {
						return errors.New("closing key output failed")
					}
				}
				written = true
				metadata := struct {
					ID          string    `json:"id"`
					PrincipalID string    `json:"principal_id"`
					Scopes      []string  `json:"scopes"`
					ExpiresAt   time.Time `json:"expires_at"`
				}{result.ID, result.PrincipalID, result.Scopes, result.ExpiresAt}
				if rt.jsonOut && keyOut != "-" {
					return rt.printJSON(metadata)
				}
				_, err = fmt.Fprintf(rt.stderr, "Messaging assistant %s · key %s · expires %s\n", result.PrincipalID, result.ID, result.ExpiresAt.Format(time.RFC3339))
				return err
			},
		}
		if operation == "rotate" {
			cmd.minArgs, cmd.maxArgs = 1, 1
		}
		root.subs = append(root.subs, cmd)
	}
	root.subs = append(root.subs, &Command{Name: "revoke", Short: "Revoke an owned messaging assistant and all its keys", Use: "agents assistant revoke <principal-id>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("principal-id must be a UUID")
		}
		if err := rt.do("DELETE", "/api/agents/assistants/"+args[0], nil, nil); err != nil {
			return assistantRequestError(err)
		}
		_, err := fmt.Fprintln(rt.stdout, "Messaging assistant revoked")
		return err
	}})
	return root
}

func assistantLifetime(raw string) (time.Duration, error) {
	var lifetime time.Duration
	var err error
	if strings.HasSuffix(raw, "d") {
		days, parseErr := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if parseErr != nil || days < 1 || days > 30 {
			return 0, usagef("--expires must be positive and at most 30d")
		}
		lifetime = time.Duration(days) * 24 * time.Hour
	} else {
		lifetime, err = time.ParseDuration(raw)
	}
	if err != nil || lifetime <= 0 || lifetime > 30*24*time.Hour {
		return 0, usagef("--expires is required, positive and at most 30d")
	}
	return lifetime, nil
}

// A failed server response may contain arbitrary text. Provisioning never
// repeats it to a terminal, including malformed success payloads with a token.
func assistantRequestError(err error) error {
	var status *client.StatusError
	if errors.As(err, &status) {
		return fmt.Errorf("messaging assistant request failed (HTTP %d)", status.Status)
	}
	return errors.New("messaging assistant request failed")
}
