// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// A session's account label follows the account its harness is signed in to now
// (AEON-1285). A launcher passes --account-label once; a session whose harness later
// switched accounts (a Claude subscription swap) kept reporting the first one. The
// heartbeat asks the harness itself, for the label only: Claude Code's own
// `claude auth status --json` names a subscription login by its email. No credential,
// token or auth file is read. An API-key login, an unknown harness or a probe that
// fails keeps the launch label. AEON_ACCOUNT_PROBE=off keeps the launch label always
// (the CLI tests set it, so they never run a real harness).
const (
	accountProbeEvery   = 10 * time.Minute
	accountProbeTimeout = 5 * time.Second
)

// harnessAccountProbe answers which account the harness is signed in to. answered is
// false when the harness could not be asked; an answer may still carry no label.
type harnessAccountProbe func(ctx context.Context, harness string) (label string, answered bool)

// probeHarnessAccount is the probe Run installs. Only Claude Code names its login.
func probeHarnessAccount(ctx context.Context, harness string) (string, bool) {
	if harness != "claude" || os.Getenv("AEON_ACCOUNT_PROBE") == "off" {
		return "", false
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, accountProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "auth", "status", "--json")
	cmd.WaitDelay = 100 * time.Millisecond
	output := &versionOutput{}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	if err := cmd.Run(); err != nil || output.overflow {
		return "", false
	}
	return claudeLoginLabel(output.data)
}

// claudeLoginLabel reads `claude auth status --json`: a signed-in claude.ai login
// yields its email; any other login answers without a label.
func claudeLoginLabel(raw []byte) (string, bool) {
	var status struct {
		LoggedIn   *bool  `json:"loggedIn"`
		Email      string `json:"email"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(raw, &status) != nil || status.LoggedIn == nil {
		return "", false
	}
	if !*status.LoggedIn || status.AuthMethod != "claude.ai" {
		return "", true
	}
	label := heartbeatText(status.Email, 128)
	if !strings.Contains(label, "@") || !agentsetup.ValidLoginLabel(label) {
		return "", true
	}
	return label, true
}

// heartbeatAccountLabel is the account label this beat reports: the harness's live
// login, asked at most once per accountProbeEvery, else the launch label.
func (rt *runtime) heartbeatAccountLabel(ctx context.Context, o heartbeatOptions, disk *heartbeatDisk, now time.Time) string {
	if rt.harnessAccount != nil && (disk.AccountProbedAt.IsZero() || now.Before(disk.AccountProbedAt) || now.Sub(disk.AccountProbedAt) >= accountProbeEvery) {
		disk.AccountProbedAt = now
		if label, answered := rt.harnessAccount(ctx, o.Harness); answered {
			disk.LiveAccount = label
		}
	}
	if disk.LiveAccount != "" {
		return disk.LiveAccount
	}
	return heartbeatText(o.AccountLabel, 128)
}
