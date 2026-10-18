// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/inspr-at/paimos/internal/nativepty"
)

// I2 mounts this additive node in cmdSession(). It never changes the existing
// session-start attribution or the aeon attach ISSUE FILE attachment commands.
func (rt *runtime) cmdSessionTake() *Command {
	var project, socket, state string
	var handBack bool
	return &Command{Name: "take", Short: "Take exclusive local control of a qualified native TUI", Use: "session take OWNED_ID --project KEY [--hand-back]", minArgs: 1, maxArgs: 1,
		Long: "A qualified TUI replaces the structured writer only after verified exit. Esc belongs to the TUI. Ctrl+] then d detaches; Ctrl+] then r hands back. EOF and signals leave the owned PTY running without a controller. --hand-back explicitly returns an already taken session.",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key or UUID (required)")
			fs.string(&socket, "socket", 0, "protected owner agentd socket")
			fs.string(&state, "setup-root", 0, "private pairing state root")
			fs.bool(&handBack, "hand-back", 0, "explicitly return a taken session after verified PTY exit")
		},
		run: func(args []string) error {
			if len(args) != 1 || !validUUID(args[0]) {
				return usagef("OWNED_ID must be a session UUID")
			}
			if project == "" {
				return usagef("--project is required")
			}
			if rt.jsonOut {
				return usagef("native takeover does not support --json")
			}
			if socket != "" && state != "" {
				return usagef("choose --socket or --setup-root")
			}
			input, ok := rt.stdin.(*os.File)
			if !ok {
				return usagef("session take requires a local stdin file")
			}
			terminal := nativeattach.NewFileTerminal(input, rt.stdout)
			if !handBack && !terminal.Interactive() {
				return usagef("session take requires an interactive terminal")
			}
			ctx, cancel := signal.NotifyContext(rt.context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGTSTP)
			defer cancel()
			readCtx, finish := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
			defer finish()
			projectID, err := rt.projectIdentityCtx(readCtx, project)
			if err != nil {
				return err
			}
			refresh := func(ctx context.Context) (nativecontract.SessionView, error) {
				ctx, done := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
				defer done()
				var raw json.RawMessage
				if err := rt.doCtx(ctx, http.MethodGet, "/api/projects/"+projectID+"/native-sessions/"+args[0], nil, &raw); err != nil {
					return nativecontract.SessionView{}, err
				}
				v, err := nativecontract.NewValidator()
				if err != nil {
					return nativecontract.SessionView{}, err
				}
				if err = v.ValidateJSON("NativeSessionView", raw); err != nil {
					return nativecontract.SessionView{}, errors.New("invalid owned session projection")
				}
				var view nativecontract.SessionView
				if json.Unmarshal(raw, &view) != nil || view.CurrentBinding == nil || view.OwnedSessionID != args[0] || view.CurrentBinding.ProjectID != projectID {
					return view, errors.New("owned session projection identity mismatch")
				}
				return view, nil
			}
			view, err := refresh(readCtx)
			if err != nil {
				return err
			}
			if handBack && view.Mode != "terminal" {
				return errors.New("session is not taken")
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
			wire, err := nativepty.NewSocketWire(socket)
			if err != nil {
				return err
			}
			client, err := nativepty.OpenClient(ctx, nativepty.ClientConfig{Snapshot: view, Refresh: refresh, Wire: wire})
			if err != nil {
				wire.Close()
				return err
			}
			if handBack {
				err = client.HandBack(ctx)
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				err = errors.Join(err, client.Detach(cleanup))
				done()
				if err == nil {
					_, err = fmt.Fprintln(rt.stdout, "Control returned to the structured runtime.")
				}
				return err
			}
			resizes := make(chan os.Signal, 1)
			signal.Notify(resizes, syscall.SIGWINCH)
			defer signal.Stop(resizes)
			return nativepty.Run(ctx, client, terminal, nativepty.RunOptions{Resizes: resizes})
		},
	}
}
