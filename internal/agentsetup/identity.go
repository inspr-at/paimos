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
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// CodexChatGPTLogin identifies a confirmed local ChatGPT login whose account
// response has no email. It must never satisfy a named account's identity pin.
const CodexChatGPTLogin = "ChatGPT login"

func CodexAccountMatches(expected, email string) bool {
	if expected == CodexChatGPTLogin {
		return email == ""
	}
	return strings.TrimSpace(expected) != "" && strings.EqualFold(strings.TrimSpace(expected), strings.TrimSpace(email))
}

// CodexIdentity uses only the documented account/read RPC. It starts no
// thread, turn, tool or model and never opens the vendor's auth/config files.
func CodexIdentity(ctx context.Context, path, home string) (string, error) {
	return codexIdentity(ctx, path, home, "", "")
}

func codexIdentity(ctx context.Context, path, home, nodePath, dir string) (email string, resultErr error) {
	if err := harnesslaunch.Validate(path, nodePath); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.Command(path, "app-server", "--listen", "stdio://")
	cmd.Dir = commandDir(dir, filepath.Dir(home))
	cmd.Env = harnesslaunch.Environment([]string{"HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + home}, nodePath)
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", errors.New("account identity unavailable")
	}
	defer input.Close()
	// Own this descriptor so cancellation can interrupt Scan and Wait cannot
	// close it before a final buffered account response has been consumed.
	output, childOutput, err := os.Pipe()
	if err != nil {
		return "", errors.New("account identity unavailable")
	}
	defer output.Close()
	defer childOutput.Close()
	cmd.Stdout = childOutput
	cmd.Stderr = io.Discard
	proc, err := ownedprocess.Start(ctx, cmd)
	if err != nil {
		return "", errors.New("account identity unavailable")
	}
	_ = childOutput.Close()
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		_ = input.Close()
		_ = output.Close()
		close(closed)
	})
	defer func() {
		cancel()
		if !stopClose() {
			<-closed
		}
		_ = input.Close()
		_ = output.Close()
		if err := proc.Wait(); errors.Is(err, ownedprocess.ErrCleanupUnconfirmed) {
			email, resultErr = "", errors.New("account identity cleanup unconfirmed")
		}
	}()
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
	if json.Unmarshal(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" || result.Account.Email != "" && (!safeLabel.MatchString(result.Account.Email) || strings.TrimSpace(result.Account.Email) == "") {
		return "", errors.New("Codex subscription identity unavailable")
	}
	return strings.TrimSpace(result.Account.Email), nil
}
