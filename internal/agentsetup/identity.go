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

type codexIdentityFailure struct {
	detail string
	err    error
}

func (e *codexIdentityFailure) Error() string { return e.detail }
func (e *codexIdentityFailure) Unwrap() error { return e.err }

func identityFailure(detail string, err error) error {
	return &codexIdentityFailure{detail: detail, err: err}
}

func safeCodexIdentityError(err error) error {
	switch {
	case errors.Is(err, ownedprocess.ErrCleanupUnconfirmed):
		return identityFailure("identity_cleanup_unconfirmed", err)
	case errors.Is(err, context.DeadlineExceeded):
		return identityFailure("identity_timeout", err)
	case errors.Is(err, context.Canceled):
		return identityFailure("identity_canceled", err)
	case errors.Is(err, harnesslaunch.ErrStart):
		return identityFailure("identity_interpreter_invalid", err)
	}
	var failure *codexIdentityFailure
	if errors.As(err, &failure) {
		switch failure.detail {
		case "identity_start_failed", "identity_rpc_failed", "identity_response_invalid":
			return identityFailure(failure.detail, err)
		}
	}
	return identityFailure("identity_probe_failed", err)
}

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
		return "", identityFailure("identity_start_failed", err)
	}
	defer input.Close()
	// Own this descriptor so cancellation can interrupt Scan and Wait cannot
	// close it before a final buffered account response has been consumed.
	output, childOutput, err := os.Pipe()
	if err != nil {
		return "", identityFailure("identity_start_failed", err)
	}
	defer output.Close()
	defer childOutput.Close()
	cmd.Stdout = childOutput
	cmd.Stderr = io.Discard
	proc, err := ownedprocess.Start(ctx, cmd)
	if err != nil {
		return "", identityFailure("identity_start_failed", err)
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
			email, resultErr = "", safeCodexIdentityError(errors.Join(resultErr, err))
		}
	}()
	encoder := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 16<<10)
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, safeCodexIdentityError(identityFailure("identity_rpc_failed", errors.Join(ctx.Err(), err)))
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
		return nil, safeCodexIdentityError(identityFailure("identity_rpc_failed", errors.Join(ctx.Err(), scanner.Err())))
	}
	if _, err = rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-setup", "version": "1"}}); err != nil {
		return "", err
	}
	if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}) != nil {
		return "", safeCodexIdentityError(identityFailure("identity_rpc_failed", ctx.Err()))
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
		return "", identityFailure("identity_response_invalid", nil)
	}
	return strings.TrimSpace(result.Account.Email), nil
}
