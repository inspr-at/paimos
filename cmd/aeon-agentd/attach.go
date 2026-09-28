// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	host, proof, err := agentsetup.ReadAttachProof(root, c)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, a := range c.Accounts {
		paths[a.Harness] = a.Path
	}
	return agentd.NewAttachManager(agentd.AttachConfig{Origin: c.Origin, ComputerID: c.ComputerID, Host: host, Workspace: c.Workspace, Executables: paths,
		Exchange: func(ctx context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
			in.DeviceProof = string(proof)
			var out attachwatch.View
			err := remote.Client.Do(ctx, "POST", "/api/agent-pairing/attach", in, &out)
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
	f.StringVar(&in.Transcript, "transcript", "", "physical transcript file path")
	if f.Parse(args) != nil || len(f.Args()) != 0 || !filepath.IsAbs(root) || in.PID < 1 || !filepath.IsAbs(in.Transcript) {
		return errors.New("usage: aeon-agentd attach --setup-root PATH --pid PID --harness NAME --project-id UUID --ticket-id UUID --transcript PATH")
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
	fmt.Fprintf(tty, "Watch this running session on %s\nHost: %s · %s · PID %d · UID %d\nStarted: %s\nExecutable: %s\nFolder: %s\nTranscript: %s (%s)\nProject: %s · Ticket: %s\n", view.Origin, view.Snapshot.Host, view.Snapshot.Harness, p.PID, p.UID, p.Started, p.Executable, p.CWD, view.Snapshot.Transcript, view.Snapshot.FileID, view.Snapshot.ProjectID, view.Snapshot.TicketID)
	fmt.Fprintln(tty, "Only new turns after approval. Audience: people explicitly granted harness.watch in this project.")
	fmt.Fprintln(tty, "Do not attach mixed-context or confidential sessions. Redaction is best effort; same-user processes are not isolated.")
	fmt.Fprint(tty, "Type WATCH to consent locally, then approve in your paired browser: ")
	// Bound input and handle Ctrl-C without leaving a background attach running.
	answers := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(io.LimitReader(tty, 64))
		line, _ := reader.ReadString('\n')
		answers <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return nil
	case answer := <-answers:
		if answer != "WATCH" {
			return errors.New("attach cancelled")
		}
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
				return errors.New("watch ended or unreachable; start a new attach to resume")
			}
			if next.State != previous {
				fmt.Fprintf(out, "Watch: %s\n", next.State)
				previous = next.State
			}
		}
	}
}
