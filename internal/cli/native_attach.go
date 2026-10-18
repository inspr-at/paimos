// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativeattach"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

// cmdSessionAttach is the canonical command node. I1 owns appending this node
// to cmdSession().subs in compat_cmds.go after the core CLI handoff; this slice
// does not mutate that reserved file or the legacy attachment command tree.
func (rt *runtime) cmdSessionAttach() *Command {
	var project, socket, state, mode string
	var control bool
	return &Command{
		Name: "attach", Short: "View an owned native session, with optional exclusive control",
		Use: "session attach OWNED_ID --project KEY [--control]", minArgs: 1, maxArgs: 1,
		Long: "Viewing is read-only. --control requests one exclusive controller lease. Esc leaves input, then detaches; i interrupts only outside input. EOF and signals detach the viewer without stopping the session. Submit uses " + nativeattach.SubmitLabel(goruntime.GOOS) + " with a compatible terminal, or Esc then s.",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key or UUID (required)")
			fs.string(&socket, "socket", 0, "protected owner agentd socket")
			fs.string(&state, "setup-root", 0, "private pairing state root")
			fs.string(&mode, "input-mode", 0, "now (default) or after_turn")
			fs.bool(&control, "control", 0, "explicitly request the exclusive controller lease")
		},
		run: func(args []string) error {
			if len(args) != 1 || !validUUID(args[0]) {
				return usagef("OWNED_ID must be a session UUID")
			}
			if project == "" {
				return usagef("--project is required")
			}
			if rt.jsonOut {
				return usagef("structured terminal attach does not support --json")
			}
			if socket != "" && state != "" {
				return usagef("choose --socket or --setup-root")
			}
			if mode != "" && mode != "now" && mode != "after_turn" {
				return usagef("--input-mode must be now or after_turn")
			}
			input, ok := rt.stdin.(*os.File)
			if !ok {
				return usagef("session attach requires a terminal or stdin file")
			}
			terminal := nativeattach.NewFileTerminal(input, rt.stdout)
			if control && !terminal.Interactive() {
				return usagef("--control requires an interactive terminal")
			}
			ctx, cancel := signal.NotifyContext(rt.context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGTSTP)
			defer cancel()
			resizes := make(chan os.Signal, 1)
			signal.Notify(resizes, syscall.SIGWINCH)
			defer signal.Stop(resizes)
			readCtx, finish := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
			defer finish()
			projectID, err := rt.projectIdentityCtx(readCtx, project)
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err = rt.doCtx(readCtx, http.MethodGet, "/api/projects/"+projectID+"/native-sessions/"+args[0], nil, &raw); err != nil {
				return err
			}
			validator, err := nativecontract.NewValidator()
			if err != nil {
				return err
			}
			if err = validator.ValidateJSON("NativeSessionView", raw); err != nil {
				return errors.New("invalid owned session projection")
			}
			var snapshot nativecontract.SessionView
			if err = json.Unmarshal(raw, &snapshot); err != nil {
				return errors.New("invalid owned session projection")
			}
			if snapshot.OwnedSessionID != args[0] || snapshot.CurrentBinding == nil || snapshot.CurrentBinding.ProjectID != projectID {
				return errors.New("owned session projection identity mismatch")
			}
			if socket == "" {
				if state == "" {
					inst, err := rt.resolve()
					if err != nil {
						return err
					}
					state = inst.AgentdStateRoot
					if state == "" {
						home, err := os.UserHomeDir()
						if err != nil {
							return errors.New("local pairing state unavailable")
						}
						state, err = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
						if err != nil {
							return errors.New("local pairing state unavailable")
						}
					}
				}
				local, err := agentdwire.OpenClient(filepath.Join(state, "daemon"))
				if err != nil {
					return errors.New("owning local agentd unavailable")
				}
				socket = local.Socket
			}
			session, err := nativeattach.Open(ctx, socket, nativeattach.Options{Snapshot: snapshot, Control: control})
			if err != nil {
				return err
			}
			return nativeattach.Run(ctx, session, terminal, nativeattach.RunOptions{Platform: goruntime.GOOS, InputMode: mode, Resizes: resizes})
		},
	}
}
