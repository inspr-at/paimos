// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsecurity"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/term"
)

func pairedAttach(root string, c agentsetup.RuntimeConfig, remote *agentd.Remote) (*agentd.AttachManager, error) {
	// Startup cannot grandfather a fallback whose provenance is unavailable.
	// Drop it without disabling signed images or other healthy installations.
	c.AttachIdentities = startupAttachIdentities(c)
	// Freeze the paired origin even if a caller supplied a differently configured
	// remote; never follow a redirect carrying registration or poll authority.
	if remote == nil || remote.Client == nil || remote.Client.HTTP == nil {
		return nil, errors.New("paired transport unavailable")
	}
	pairedClient := *remote.Client
	pairedClient.BaseURL = c.Origin
	pairedHTTP := *remote.Client.HTTP
	pairedHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	pairedClient.HTTP = &pairedHTTP
	host, proof, err := agentsetup.ReadAttachProof(root, c)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, a := range c.Accounts {
		paths[a.Harness] = a.Path
	}
	// Generated once per daemon start; captured only by the exchange closure.
	// Never add this key to RuntimeConfig, setup stores, local replies or logs.
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		return nil, errors.New("cannot create watch poll key")
	}
	pollKey := hex.EncodeToString(key[:])
	clear(key[:])
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var registered attachwatch.View
	registration := map[string]any{"attach_protocol": attachwatch.Protocol, "local_consent_proof_version": attachwatch.LocalConsentProofVersion, "operation": "register", "computer_id": c.ComputerID, "device_proof": string(proof), "poll_key": pollKey, "local_auth_capability": agentd.CurrentLocalAuthCapability()}
	if err = pairedClient.Do(ctx, "POST", "/api/agent-pairing/attach", registration, &registered); err != nil {
		return nil, fmt.Errorf("paired instance refused attach registration: %w", err)
	}
	if registered.State != "registered" {
		return nil, errors.New("paired instance refused attach registration; update agentd and Aeon")
	}
	if registered.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion {
		return nil, errors.New("paired instance lacks local consent proof v2; upgrade Aeon and paimos-agentd, then restart; existing pairing keys remain valid")
	}
	return agentd.NewAttachManager(agentd.AttachConfig{Origin: c.Origin, ComputerID: c.ComputerID, Host: host, Workspace: c.Workspace, Executables: paths, Identities: c.AttachIdentities,
		LocalSigner: func(ctx context.Context, consent, nonce, reason string) (string, error) {
			if c.LocalAuthKeyID == "" {
				return "", agentsecurity.ErrUnavailable
			}
			return agentsecurity.DefaultSigner().Sign(ctx, c.LocalAuthKeyID, attachwatch.LocalConsentHash(consent, nonce, reason), reason)
		},
		Exchange: func(ctx context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
			in.PollKey = pollKey
			var out attachwatch.View
			err := pairedClient.Do(ctx, "POST", "/api/agent-pairing/attach", in, &out)
			return out, err
		}})
}

func startupAttachIdentities(c agentsetup.RuntimeConfig) map[string]agentsetup.AttachIdentity {
	identities := make(map[string]agentsetup.AttachIdentity, len(c.AttachIdentities))
	for harness, identity := range c.AttachIdentities {
		for _, account := range c.Accounts {
			if account.Harness != harness {
				continue
			}
			derived := agentsetup.RecordAttachIdentity(harness, account.Path, c.Workspace)
			if derived != nil && *derived == identity {
				identities[harness] = identity
				break
			}
		}
	}
	return identities
}

func attachCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("attach", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root, language string
	var noBrowser bool
	in := agentd.AttachLocalRequest{Operation: "preview"}
	f.StringVar(&root, "setup-root", "", "paired local setup root")
	f.StringVar(&language, "language", "en", "attach wording language: en or de")
	f.BoolVar(&noBrowser, "no-browser", false, "print the approval link without opening a browser")
	f.IntVar(&in.PID, "pid", 0, "running harness PID")
	f.StringVar(&in.Harness, "harness", "", "paired harness name")
	f.StringVar(&in.ProjectID, "project-id", "", "project UUID")
	f.StringVar(&in.TicketID, "ticket-id", "", "ticket UUID")
	f.StringVar(&in.Transcript, "transcript", "", "physical transcript file path for the default conversation watch")
	f.BoolVar(&in.StatusOnly, "status-only", false, "report status without reading or sharing conversation text")
	if f.Parse(args) != nil || len(f.Args()) != 0 || !filepath.IsAbs(root) || in.PID < 1 || in.Transcript != "" && !filepath.IsAbs(in.Transcript) || language != "en" && language != "de" {
		return errors.New("usage: aeon-agentd attach --setup-root PATH --pid PID --harness NAME --project-id UUID --ticket-id UUID [--transcript PATH] [--status-only] [--language en|de] [--no-browser]")
	}
	// Never read confirmation from stdin, an agent pipe, a flag or fetched text.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("attach requires a separate interactive terminal")
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return errors.New("attach requires an interactive terminal")
	}
	client, err := agentdwire.OpenClient(filepath.Join(root, "daemon"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reader := bufio.NewReader(io.LimitReader(tty, 4096))
	in.StatusOnly, err = promptAttachMode(ctx, reader, tty, in.StatusOnly)
	if err != nil {
		return err
	}
	if in.StatusOnly {
		in.Transcript = ""
	} else if in.Transcript == "" {
		return errors.New("watch requires --transcript PATH; choose --status-only for no conversation text")
	}
	view, err := client.Attach(ctx, in)
	if err != nil {
		return err
	}
	localID := view.ID
	defer func() {
		op, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Attach(op, agentd.AttachLocalRequest{Operation: "detach", ID: localID})
	}()
	p := view.Snapshot.Process
	words := attachWording(language)
	fmt.Fprintln(tty, words.Unlinked)
	fmt.Fprintln(tty, words.PairingHelp)
	verb := "Attach"
	if !in.StatusOnly {
		verb = "Watch"
	}
	fmt.Fprintf(tty, "%s this running session on %s\nHost: %s · %s · PID %d · UID %d\nStarted: %s\nExecutable: %s\nFolder: %s\nProject: %s · Ticket: %s\n", verb, view.Origin, view.Snapshot.Host, view.Snapshot.Harness, p.PID, p.UID, p.Started, p.Executable, p.CWD, view.Snapshot.ProjectID, view.Snapshot.TicketID)
	if in.StatusOnly {
		fmt.Fprintln(tty, "Status only (no conversation text). No conversation text is read or shared.")
	} else {
		fmt.Fprintf(tty, "Watch the conversation.\nTranscript: %s (%s)\nOnly new turns after approval. Audience: people explicitly granted harness.watch in this project.\n", view.Snapshot.Transcript, view.Snapshot.FileID)
	}
	fmt.Fprintln(tty, "Only attach a single trust context. Same-user processes are not isolated.")
	fmt.Fprintln(tty, "Touch ID is the default on an upgraded Mac pairing even when this daemon reports that it cannot run. Linux and older pairings keep approval in Aeon. Save approval in Aeon to allow a headless Mac.")
	view, err = confirmAttachApproval(ctx, reader, tty, out, view, language, noBrowser || runtime.GOOS != "darwin", client.Attach, openAttachBrowser)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	previous := view.State
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "Detached. Nothing is shared.")
			return nil
		case <-ticker.C:
			next, err := client.Attach(ctx, agentd.AttachLocalRequest{Operation: "poll", ID: view.ID, Digest: view.Digest})
			if err != nil {
				return attachPollFailure(previous, view.ExpiresAt, time.Now())
			}
			if next.State == "confirmed_exited" || next.State == "detached" || next.State == "unreachable" {
				fmt.Fprintln(out, attachEndedLine(next.State, previous))
				return nil
			}
			if next.Reason != "" {
				return errors.New(next.Reason)
			}
			if next.State != previous {
				if next.State == "active" {
					fmt.Fprintln(out, words.Linked)
				}
				fmt.Fprintln(out, attachStateLine(next.State, next.ConsentMode, in.StatusOnly))
				previous = next.State
			}
		}
	}
}

// Where to approve, said once the request waits. The link carries the code in the
// URL fragment, which no server, proxy or referrer ever receives, and the page
// drops it from the address bar on arrival; opening it only fills the code in.
// Approval stays a person's click on the review. A code that is not nine digits
// gets no link, so nothing but digits is ever put into a URL.
func attachApprovalNotice(origin, code string, expires *time.Time, now time.Time) string {
	var b strings.Builder
	b.WriteString("Waiting for your approval in Aeon")
	if left := attachTimeLeft(expires, now); left != "" {
		fmt.Fprintf(&b, " (expires in %s)", left)
	}
	b.WriteString(".\n")
	link := attachApprovalURL(origin, code)
	if link == "" {
		b.WriteString("  Open Aeon → Attach session and enter the attach code.\n")
	} else {
		fmt.Fprintf(&b, "  Open %s \u2192 Attach session and enter code %s-%s-%s\n", strings.Split(link, "#")[0], code[:3], code[3:6], code[6:])
		fmt.Fprintf(&b, "  or open this link, which fills the code in (you still approve):\n  %s\n", link)
	}
	b.WriteString("Keep this terminal open; Ctrl-C detaches.\n")
	return b.String()
}

// The server's expiry, shown relative to now. A clock that disagrees with the
// server by more than the request's lifetime is not trusted: say nothing.
func attachTimeLeft(expires *time.Time, now time.Time) string {
	if expires == nil {
		return ""
	}
	left := expires.Sub(now)
	switch {
	case left <= 0 || left > 15*time.Minute:
		return ""
	case left < 90*time.Second:
		return fmt.Sprintf("%d s", int(left.Round(time.Second)/time.Second))
	default:
		return fmt.Sprintf("%d min", int(left.Round(time.Minute)/time.Minute))
	}
}

// Progress after approval. A pending request is announced once by the notice above.
func attachStateLine(state, consentMode string, statusOnly bool) string {
	switch {
	case state == "approved" && consentMode == attachwatch.ConsentLocalAuth:
		return "Approved in Aeon. Waiting for local confirmation on the paired Mac; nothing is shared yet."
	case state == "approved":
		return "Approved in Aeon. Connecting\u2026"
	case state == "active" && statusOnly:
		return "Attached. Session status is reported until you detach (Ctrl-C) or revoke it in Aeon."
	case state == "active":
		return "Attached. New turns are shared until you revoke it in Aeon or press Ctrl-C."
	default:
		return "Attach: " + state
	}
}

// A request that never got past approval names the two ways that happens.
func attachEndedLine(state, previous string) string {
	switch {
	case state == "confirmed_exited":
		return "Attach: the session exited."
	case state == "unreachable" && previous != "active":
		return "The attach request expired before it was approved. Run attach again."
	case state == "detached" && previous == "pending":
		return "The attach request was declined or cancelled in Aeon. Run attach again."
	case state == "detached" && previous == "approved":
		return "The approval was withdrawn before the attach started. Run attach again."
	default:
		return "Attach: " + state
	}
}

// Every refused poll ends the attach. Which way it ended is what the person needs
// to know: still waiting when the code's lifetime ran out, or answered in Aeon.
func attachPollFailure(previous string, expires *time.Time, now time.Time) error {
	switch previous {
	case "pending":
		if expires != nil && !now.Before(expires.Add(-5*time.Second)) {
			return errors.New("the attach request expired before it was approved; run attach again")
		}
		return errors.New("the attach request was declined, cancelled or expired in Aeon; run attach again")
	case "approved":
		return errors.New("the approval ended before the attach started; run attach again")
	default:
		return errors.New("the attach ended or lost contact; run attach again to resume")
	}
}

// Both choices are made on /dev/tty before any request leaves the daemon.
// --status-only pins the private choice; an empty menu answer keeps watch default.
func promptAttachMode(ctx context.Context, reader *bufio.Reader, out io.Writer, statusOnly bool) (bool, error) {
	if statusOnly {
		fmt.Fprintln(out, "Selected: Status only (no conversation text).")
		return true, nil
	}
	fmt.Fprintln(out, "1. Watch the conversation (default)")
	fmt.Fprintln(out, "2. Status only (no conversation text)")
	fmt.Fprint(out, "Choose mode [1]: ")
	answer, err := readAttachAnswer(ctx, reader)
	if err != nil {
		return false, err
	}
	switch answer {
	case "", "1":
		return false, nil
	case "2":
		return true, nil
	default:
		return false, errors.New("attach cancelled; choose 1 or 2")
	}
}
func readAttachAnswer(ctx context.Context, reader *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	answers := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		answers <- result{strings.TrimSpace(line), err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case answer := <-answers:
		return answer.line, answer.err
	}
}
