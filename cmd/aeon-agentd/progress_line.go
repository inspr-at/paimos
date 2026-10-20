// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"golang.org/x/term"
)

// Braille frames advance once per poll. A TTY rewrites one waiting line in
// place; a pipe prints the code block once and one line per state change.
const progressSpinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
const progressClearToEnd = "\r\x1b[K"

type setupProgressParts struct {
	code  []string
	hooks []string
	stage string
	facts []string
}

// setupProgressView is one command invocation. Tests inject the terminal
// flag and the clock; a bytes.Buffer is never treated as a terminal.
type setupProgressView struct {
	tty       bool
	now       func() time.Time
	frame     int
	started   bool
	spinner   bool
	stageLine string
	codes     map[string]struct{}
	hooks     map[string]struct{}
	facts     map[string]struct{}
}

func newSetupProgressView(tty bool, now func() time.Time) *setupProgressView {
	if now == nil {
		now = time.Now
	}
	return &setupProgressView{
		tty:   tty,
		now:   now,
		codes: map[string]struct{}{},
		hooks: map[string]struct{}{},
		facts: map[string]struct{}{},
	}
}

func progressWriterTTY(out io.Writer) bool {
	file, ok := out.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func setupWaitLabel(stage string) string {
	switch stage {
	case "awaiting_approval":
		return "Waiting for approval"
	case "requesting":
		return "Waiting to send the request"
	case "provisioning":
		return "Waiting for provisioning"
	case "verification_pending":
		return "Waiting for verification"
	default:
		return ""
	}
}

func (v *setupProgressView) Print(out io.Writer, jsonOutput bool, p agentsetup.Progress) error {
	if jsonOutput {
		return writeSetupProgressSnapshot(out, true, p)
	}
	text := setupProgressPartsOf(p)
	if !v.started {
		v.started = true
		if err := writeSetupProgressText(out, text); err != nil {
			return err
		}
		v.note(text)
		if v.tty && setupWaitLabel(p.Stage) != "" {
			return v.drawSpinner(out, p.Stage)
		}
		return nil
	}
	return v.printDelta(out, p.Stage, text)
}

// Finish ends an open spinner line so the shell prompt starts on the next line.
func (v *setupProgressView) Finish(out io.Writer) error {
	if v == nil || !v.spinner {
		return nil
	}
	v.spinner = false
	_, err := fmt.Fprintln(out)
	return err
}

func (v *setupProgressView) note(text setupProgressParts) {
	remember(v.codes, text.code)
	remember(v.hooks, text.hooks)
	v.stageLine = text.stage
	remember(v.facts, text.facts)
}

func (v *setupProgressView) printDelta(out io.Writer, stage string, text setupProgressParts) error {
	freshCode := unseen(v.codes, text.code)
	freshHooks := unseen(v.hooks, text.hooks)
	freshFacts := unseen(v.facts, text.facts)
	stageNew := text.stage != v.stageLine
	if len(freshCode)+len(freshHooks)+len(freshFacts) == 0 && !stageNew {
		if v.tty && setupWaitLabel(stage) != "" {
			return v.drawSpinner(out, stage)
		}
		return nil
	}
	if v.spinner {
		if _, err := io.WriteString(out, progressClearToEnd); err != nil {
			return err
		}
		v.spinner = false
	}
	if err := writeLines(out, freshCode); err != nil {
		return err
	}
	if err := writeLines(out, freshHooks); err != nil {
		return err
	}
	if stageNew {
		if err := writeLine(out, text.stage); err != nil {
			return err
		}
		v.stageLine = text.stage
	}
	if err := writeLines(out, freshFacts); err != nil {
		return err
	}
	if v.tty && setupWaitLabel(stage) != "" {
		return v.drawSpinner(out, stage)
	}
	return nil
}

func (v *setupProgressView) drawSpinner(out io.Writer, stage string) error {
	frames := []rune(progressSpinnerFrames)
	frame := frames[v.frame%len(frames)]
	v.frame++
	line := fmt.Sprintf("%s %s (last check %s)", setupWaitLabel(stage), string(frame), v.now().Format("15:04:05"))
	if v.spinner {
		if _, err := io.WriteString(out, progressClearToEnd+line); err != nil {
			return err
		}
		return nil
	}
	if _, err := io.WriteString(out, line); err != nil {
		return err
	}
	v.spinner = true
	return nil
}

func remember(seen map[string]struct{}, lines []string) {
	for _, line := range lines {
		seen[line] = struct{}{}
	}
}

func unseen(seen map[string]struct{}, lines []string) []string {
	var fresh []string
	for _, line := range lines {
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		fresh = append(fresh, line)
	}
	return fresh
}

func writeSetupProgressSnapshot(out io.Writer, jsonOutput bool, p agentsetup.Progress) error {
	if p.Schema == "" {
		p.Schema = "aeon.agent-setup.v1"
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(p)
	}
	return writeSetupProgressText(out, setupProgressPartsOf(p))
}

func writeSetupProgressText(out io.Writer, text setupProgressParts) error {
	if err := writeLines(out, text.code); err != nil {
		return err
	}
	if err := writeLines(out, text.hooks); err != nil {
		return err
	}
	if err := writeLine(out, text.stage); err != nil {
		return err
	}
	return writeLines(out, text.facts)
}

func writeLines(out io.Writer, lines []string) error {
	for _, line := range lines {
		if err := writeLine(out, line); err != nil {
			return err
		}
	}
	return nil
}

func writeLine(out io.Writer, line string) error {
	_, err := fmt.Fprintln(out, line)
	return err
}

func setupProgressPartsOf(p agentsetup.Progress) setupProgressParts {
	var text setupProgressParts
	if p.UserCode != "" {
		text.code = []string{
			"Pairing code: " + p.UserCode,
			"Approve at: " + p.VerificationURI,
		}
	}
	text.hooks = make([]string, 0, len(p.HookCapabilities))
	for _, c := range p.HookCapabilities {
		state := "verified"
		if !c.Verified {
			state = "watch-only: " + c.Blocker
		}
		text.hooks = append(text.hooks, c.Harness+" user hook: "+state)
	}
	text.stage = p.Stage + ": " + p.Action
	if p.CheckoutNotice != "" {
		text.facts = append(text.facts, p.CheckoutNotice)
	}
	if p.TouchIDConfirmation != "" {
		confirmation := "Touch ID confirmation: " + p.TouchIDConfirmation
		if p.TouchIDUpgradeCommand != "" {
			confirmation += ": " + p.TouchIDUpgradeCommand
		}
		text.facts = append(text.facts, confirmation)
		if p.TouchIDUpgradeCommand != "" {
			text.facts = append(text.facts, "Use a new, empty state folder and approve in Aeon. Keep the old pairing until the new one is verified. Point the service at the new state root through its existing owner.")
		}
	}
	// Include healthy harnesses and each approved account, so a successful
	// probe cannot disappear behind an unrelated verification failure.
	harnesses := make([]string, 0, len(p.HarnessDetails))
	for harness := range p.HarnessDetails {
		harnesses = append(harnesses, harness)
	}
	sort.Strings(harnesses)
	for _, harness := range harnesses {
		detail := p.HarnessDetails[harness]
		line := "harness " + harness + " " + detail.State
		if detail.Reason != "" {
			line += " reason " + detail.Reason
		}
		if detail.ReasonDetail != "" {
			line += " detail " + detail.ReasonDetail
		}
		if detail.Fix.Command != "" {
			line += " fix " + detail.Fix.Command
		}
		text.facts = append(text.facts, line)
		// A rotated profile may appear under several enrollments. Show its
		// active login once, approved when it matches any connected enrollment.
		logins := map[string]bool{}
		for _, account := range p.Accounts {
			if account.Harness == harness && account.State == "connected" {
				if login, ok := p.AccountLogins[account.AccountID]; ok && login.ActiveIdentity != "" {
					logins[login.ActiveIdentity] = logins[login.ActiveIdentity] || login.Approved
				}
			}
		}
		identities := make([]string, 0, len(logins))
		for identity := range logins {
			identities = append(identities, identity)
		}
		sort.Strings(identities)
		for _, identity := range identities {
			approval := "not approved on this computer; run aeon-agentd add-harness --harness " + harness
			if logins[identity] {
				approval = "approved on this computer"
			}
			text.facts = append(text.facts, "harness "+harness+" active login "+identity+": "+approval)
		}
	}
	accounts := append([]agentsetup.Enrollment(nil), p.Accounts...)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].AccountID < accounts[j].AccountID })
	for _, account := range accounts {
		detail, ok := p.AccountStatuses[account.AccountID]
		if !ok || account.State != "connected" {
			detail = agentsetup.HarnessDetail{State: account.State}
			if account.State == "connected" {
				detail.State = "unconfirmed"
			}
		}
		line := "account " + account.AccountID + " harness " + account.Harness + " " + detail.State
		if agentsetup.ValidLoginLabel(account.Label) {
			line += " login " + account.Label
		}
		if detail.Reason != "" {
			line += " reason " + detail.Reason
		}
		if detail.ReasonDetail != "" {
			line += " detail " + detail.ReasonDetail
		}
		if detail.Fix.Command != "" {
			line += " fix " + detail.Fix.Command
		}
		if account.State == "connected" && detail.State == "ready" && account.VerificationState == "expired" && account.VerificationRunID != "" {
			line += " verification expired — run verification again; run " + account.VerificationRunID
			if account.Cleanup != "" {
				line += "; local cleanup " + account.Cleanup
			}
		}
		text.facts = append(text.facts, line)
	}
	for _, blocked := range p.BlockedAccounts {
		text.facts = append(text.facts, fmt.Sprintf("blocked account %s harness %s reason %s fix %s", blocked.AccountID, blocked.Harness, blocked.Reason, blocked.Fix.Command))
	}
	return text
}
