// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// CodexIdentity uses only the documented account/read RPC. It starts no
// thread, turn, tool or model and never opens the vendor's auth/config files.
func CodexIdentity(ctx context.Context, path, home string) (string, error) {
	return codexIdentity(ctx, path, home, "")
}

func codexIdentity(ctx context.Context, path, home, nodePath string) (string, error) {
	if err := harnesslaunch.Validate(path, nodePath); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "app-server", "--listen", "stdio://")
	cmd.Env = harnesslaunch.Environment([]string{"HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + home}, nodePath)
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", errors.New("account identity unavailable")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return "", errors.New("account identity unavailable")
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return "", errors.New("account identity unavailable")
	}
	// This short-lived protocol-only child is ours; cancellation cannot touch
	// an existing vendor process or an account sign-in.
	defer func() { input.Close(); cancel(); _ = cmd.Wait() }()
	encoder := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 16<<10)
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}) != nil {
			return nil, errors.New("account identity unavailable")
		}
		for i := 0; i < 32 && scanner.Scan(); i++ {
			var f struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &f) == nil && f.ID == id {
				if len(f.Error) > 0 && string(f.Error) != "null" {
					break
				}
				return f.Result, nil
			}
		}
		return nil, errors.New("account identity unavailable")
	}
	if _, err = rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-setup", "version": "1"}}); err != nil {
		return "", err
	}
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}) != nil {
		return "", errors.New("account identity unavailable")
	}
	raw, err := rpc(2, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return "", err
	}
	var result struct {
		Account *struct {
			Type  string `json:"type"`
			Email string `json:"email"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" || !safeLabel.MatchString(result.Account.Email) {
		return "", errors.New("Codex subscription identity unavailable")
	}
	return strings.TrimSpace(result.Account.Email), nil
}
