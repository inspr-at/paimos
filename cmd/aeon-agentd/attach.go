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
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/term"
)

func pairedAttach(root string, c agentsetup.RuntimeConfig, remote *agentd.Remote) (*agentd.AttachManager, error) {
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
	registration := map[string]any{"attach_protocol": attachwatch.Protocol, "operation": "register", "computer_id": c.ComputerID, "device_proof": string(proof), "poll_key": pollKey, "local_auth_capability": agentd.CurrentLocalAuthCapability()}
	if err = pairedClient.Do(ctx, "POST", "/api/agent-pairing/attach", registration, &registered); err != nil {
		return nil, fmt.Errorf("paired instance refused attach registration: %w", err)
	}
	if registered.State != "registered" {
		return nil, errors.New("paired instance refused attach registration; update agentd and Aeon")
	}
	return agentd.NewAttachManager(agentd.AttachConfig{Origin: c.Origin, ComputerID: c.ComputerID, Host: host, Workspace: c.Workspace, Executables: paths, Identities: c.AttachIdentities,
		Exchange: func(ctx context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
			in.PollKey = pollKey
			var out attachwatch.View
			err := pairedClient.Do(ctx, "POST", "/api/agent-pairing/attach", in, &out)
			return out, err
		}})
}
func attachCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("attach", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root string
	in := agentd.AttachLocalRequest{Operation: "preview"}
	f.StringVar(&root, "setup-root", "", "paired local setup root")
	f.IntVar(&in.PID, "pid", 0, "running harness PID")
	f.StringVar(&in.Harness, "harness", "", "paired harness name")
	f.StringVar(&in.ProjectID, "project-id", "", "project UUID")
	f.StringVar(&in.TicketID, "ticket-id", "", "ticket UUID")
	f.StringVar(&in.Transcript, "transcript", "", "physical transcript file path for the default conversation watch")
	f.BoolVar(&in.StatusOnly, "status-only", false, "report status without reading or sharing conversation text")
	if f.Parse(args) != nil || len(f.Args()) != 0 || !filepath.IsAbs(root) || in.PID < 1 || in.Transcript != "" && !filepath.IsAbs(in.Transcript) {
		return errors.New("usage: aeon-agentd attach --setup-root PATH --pid PID --harness NAME --project-id UUID --ticket-id UUID [--transcript PATH] [--status-only]")
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
	verb, confirmation := "Attach", "ATTACH"
	if !in.StatusOnly {
		verb, confirmation = "Watch", "WATCH"
	}
	fmt.Fprintf(tty, "%s this running session on %s\nHost: %s · %s · PID %d · UID %d\nStarted: %s\nExecutable: %s\nFolder: %s\nProject: %s · Ticket: %s\n", verb, view.Origin, view.Snapshot.Host, view.Snapshot.Harness, p.PID, p.UID, p.Started, p.Executable, p.CWD, view.Snapshot.ProjectID, view.Snapshot.TicketID)
	if in.StatusOnly {
		fmt.Fprintln(tty, "Status only (no conversation text). No conversation text is read or shared.")
	} else {
		fmt.Fprintf(tty, "Watch the conversation.\nTranscript: %s (%s)\nOnly new turns after approval. Audience: people explicitly granted harness.watch in this project.\n", view.Snapshot.Transcript, view.Snapshot.FileID)
	}
	fmt.Fprintln(tty, "Only attach a single trust context. Same-user processes are not isolated.")
	fmt.Fprintf(tty, "Type %s for the local check, then approve in your paired browser: ", confirmation)
	answer, err := readAttachAnswer(ctx, reader)
	if err != nil {
		return err
	}
	if answer != confirmation {
		return errors.New("attach cancelled")
	}
	view, err = client.Attach(ctx, agentd.AttachLocalRequest{Operation: "confirm", ID: view.ID, Digest: view.Digest})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Open %s/agents and review attach code %s. Keep this terminal open; Ctrl-C detaches.\n", view.Origin, view.Code)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	previous := view.State
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			next, err := client.Attach(ctx, agentd.AttachLocalRequest{Operation: "poll", ID: view.ID, Digest: view.Digest})
			if err != nil {
				return errors.New("attach ended or unreachable; start a new attach to resume")
			}
			if next.State == "confirmed_exited" || next.State == "detached" || next.State == "unreachable" {
				fmt.Fprintf(out, "Attach: %s\n", next.State)
				return nil
			}
			if next.Reason != "" {
				return errors.New(next.Reason)
			}
			if next.State == "approved" && previous != "approved" && next.ConsentMode == attachwatch.ConsentLocalAuth {
				fmt.Fprintln(out, "Waiting for local confirmation on the paired Mac; watch is not active yet.")
			}
			if next.State != previous {
				fmt.Fprintf(out, "Attach: %s\n", next.State)
				previous = next.State
			}
		}
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
