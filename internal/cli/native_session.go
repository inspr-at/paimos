// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

type nativeSessionOptions struct {
	Profile, Role, Harness, Cwd, Resume, Socket, StateRoot string
	AfterExit                                              int
}

func (o *nativeSessionOptions) flags(fs *flagSet) {
	fs.string(&o.Profile, "profile", 0, "native: start a qualified coordinator owned by agentd")
	fs.string(&o.Role, "role", 0, "coordinator (native profile only)")
	fs.string(&o.Harness, "harness", 0, "claude (native profile only)")
	fs.string(&o.Cwd, "cwd", 0, "approved coordinator workspace")
	fs.string(&o.Resume, "resume", 0, "exact Claude conversation UUID")
	fs.int(&o.AfterExit, "after-exit", "wait for this pinned Claude process to exit before resume")
	fs.string(&o.Socket, "socket", 0, "owning agentd Unix socket")
	fs.string(&o.StateRoot, "setup-root", 0, "private pairing state root")
}

func (o nativeSessionOptions) used() bool {
	return o.Role != "" || o.Harness != "" || o.Cwd != "" || o.Resume != "" || o.Socket != "" || o.StateRoot != "" || o.AfterExit != 0
}

func (rt *runtime) startNativeSession(project string, o nativeSessionOptions) error {
	if strings.TrimSpace(project) == "" || o.Role != "coordinator" || o.Harness != "claude" || o.Cwd == "" {
		return usagef("native start requires --project, --role coordinator, --harness claude and --cwd")
	}
	if o.Socket != "" && o.StateRoot != "" {
		return usagef("choose --socket or --setup-root")
	}
	cwd, err := filepath.Abs(o.Cwd)
	if err != nil {
		return usagef("invalid --cwd")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return usagef("--cwd is unavailable")
	}
	id, err := newUUIDv4()
	if err != nil {
		return err
	}
	request := agentd.NativeSessionStartRequest{RequestID: id, ProjectID: nativeValidationProjectID, Profile: o.Profile, Role: o.Role, Harness: o.Harness, Cwd: cwd, Resume: o.Resume, AfterExit: o.AfterExit}
	// Validate flags before any network request; actual project authority is
	// resolved by PAIMOS, then independently checked by the daemon's start port.
	if request.Validate() != nil {
		return usagef("invalid native start; --after-exit requires --resume with a Claude conversation UUID")
	}
	projectID, err := rt.projectIdentityCtx(rt.context(), project)
	if err != nil {
		return err
	}
	request.ProjectID = projectID
	var local agentdwire.Client
	if o.Socket != "" {
		if !filepath.IsAbs(o.Socket) {
			return usagef("--socket must be absolute")
		}
		if err := agentsetup.CheckSocket(o.Socket); err != nil {
			return fmt.Errorf("owning agentd socket unavailable")
		}
		local = agentdwire.Client{Socket: o.Socket, TokenFile: o.Socket + ".token"}
	} else {
		root := o.StateRoot
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("local pairing state unavailable")
			}
			root, err = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
			if err != nil {
				return fmt.Errorf("local pairing state unavailable")
			}
		}
		local, err = agentdwire.OpenClient(filepath.Join(root, "daemon"))
		if err != nil {
			return fmt.Errorf("owning agentd unavailable")
		}
	}
	result, err := requestNativeStart(context.Background(), local, request)
	if err != nil {
		return err
	}
	if rt.jsonOut {
		return rt.printJSON(result)
	}
	fmt.Fprintf(rt.stdout, "%s · %s\n", result.SessionID, result.State)
	return nil
}

const nativeValidationProjectID = "00000000-0000-4000-8000-000000000000"

// This small local client remains in the reserved CLI slice. C0/P2a maps the
// pilot DTO to its frozen protocol without widening agentdwire's legacy API.
func requestNativeStart(ctx context.Context, local agentdwire.Client, request agentd.NativeSessionStartRequest) (agentd.NativeSessionStartResult, error) {
	store, err := agentsetup.OpenStore(filepath.Dir(local.TokenFile), false)
	if err != nil {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("owning agentd authentication unavailable")
	}
	defer store.Close()
	token, err := store.Read(filepath.Base(local.TokenFile), 32)
	if err != nil || len(token) != 32 {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("owning agentd authentication unavailable")
	}
	envelope := agentd.NativeStartEnvelope{Protocol: agentd.NativeSessionProtocol, RequestID: request.RequestID, Method: "start", ExpectedBinding: json.RawMessage("null"), Params: request}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return agentd.NativeSessionStartResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agentd/v1/native-sessions/start", bytes.NewReader(raw))
	if err != nil {
		return agentd.NativeSessionStartResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(agentd.NativeSessionProtocolHeader, agentd.NativeSessionProtocol)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", local.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native start unconfirmed; inspect the same daemon before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native coordinator start refused (HTTP %d); server intent and native qualification are required", response.StatusCode)
	}
	if response.Header.Get(agentd.NativeSessionProtocolHeader) != agentd.NativeSessionProtocol {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native protocol mismatch")
	}
	var result agentd.NativeSessionStartResult
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(responseBody) > 4096 {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native start response exceeded its bound or was incomplete")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.RequestID != request.RequestID || !validUUID(result.SessionID) || !slices.Contains([]string{"running", "waiting_for_exit", "starting", "exit_unconfirmed", "exited", "abandoned"}, result.State) {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native start returned an invalid receipt")
	}
	if result.State == "exit_unconfirmed" || result.State == "abandoned" {
		return agentd.NativeSessionStartResult{}, fmt.Errorf("native coordinator %s: %s", result.SessionID, result.State)
	}
	return result, nil
}
