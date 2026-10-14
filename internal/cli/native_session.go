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
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type nativeSessionOptions struct {
	Profile, Role, Harness, Cwd, Resume, Socket, StateRoot, Intent string
	AfterExit                                                      int
}

func (o *nativeSessionOptions) flags(fs *flagSet) {
	fs.string(&o.Profile, "profile", 0, "native: start a qualified coordinator owned by agentd")
	fs.string(&o.Role, "role", 0, "coordinator (native profile only)")
	fs.string(&o.Harness, "harness", 0, "claude (native profile only)")
	fs.string(&o.Cwd, "cwd", 0, "approved coordinator workspace")
	fs.string(&o.Intent, "intent", 0, "protected C0 local start JSON for an existing person-issued server intent (required)")
	fs.string(&o.Resume, "resume", 0, "exact Claude conversation UUID")
	fs.int(&o.AfterExit, "after-exit", "wait for this pinned Claude process to exit before resume")
	fs.string(&o.Socket, "socket", 0, "owning agentd Unix socket")
	fs.string(&o.StateRoot, "setup-root", 0, "private pairing state root")
}

func (o nativeSessionOptions) used() bool {
	return o.Role != "" || o.Harness != "" || o.Cwd != "" || o.Resume != "" || o.Socket != "" || o.StateRoot != "" || o.Intent != "" || o.AfterExit != 0
}

func (rt *runtime) startNativeSession(project string, o nativeSessionOptions) error {
	if strings.TrimSpace(project) == "" || o.Role != "coordinator" || o.Harness != "claude" || o.Cwd == "" || o.Intent == "" {
		return usagef("native start requires --project, --role coordinator, --harness claude, --cwd and --intent")
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
	v, err := nativecontract.NewValidator()
	if err != nil {
		return err
	}
	raw, err := agentsetup.ReadPrivateFile(o.Intent, nativecontract.RequestBodyBytes)
	if err != nil {
		return usagef("--intent must be bounded, owner-only C0 local start JSON")
	}
	var request nativecontract.LocalStart
	if err = v.DecodeJSON("NativeLocalStart", raw, &request); err != nil {
		return usagef("invalid C0 local start intent")
	}
	// The request ID selects a persisted PERSON-issued intent. Neither a local
	// file nor the socket proof grants authority; the daemon must re-read the
	// original actor/current grant and compare every selector before claiming.
	if request.Intent.Profile != o.Profile || request.Intent.Role != o.Role || request.Intent.Harness != o.Harness || request.Cwd != cwd ||
		(o.Resume == "" && request.NativeResumeRef != nil) || (o.Resume != "" && (request.NativeResumeRef == nil || *request.NativeResumeRef != o.Resume)) ||
		(o.AfterExit == 0 && request.AfterExit != nil) || (o.AfterExit != 0 && (request.AfterExit == nil || request.AfterExit.PID != int64(o.AfterExit))) {
		return usagef("native flags must match the exact person-issued C0 intent, resume and exit barrier")
	}
	projectID, err := rt.projectIdentityCtx(rt.context(), project)
	if err != nil {
		return err
	}
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
	result, err := requestNativeStart(rt.context(), local, projectID, request)
	if err != nil {
		return err
	}
	if rt.jsonOut {
		return rt.printJSON(result)
	}
	fmt.Fprintf(rt.stdout, "%s · %s\n", result.Session.OwnedSessionID, result.Session.State)
	return nil
}

// This client speaks only C0. Old attribution and attachment command nodes
// retain their separate contracts; an unqualified native start never falls back.
func requestNativeStart(ctx context.Context, local agentdwire.Client, project string, request nativecontract.LocalStart) (nativecontract.OperationResult, error) {
	var empty nativecontract.OperationResult
	store, err := agentsetup.OpenStore(filepath.Dir(local.TokenFile), false)
	if err != nil {
		return empty, fmt.Errorf("owning agentd authentication unavailable")
	}
	defer store.Close()
	token, err := store.Read(filepath.Base(local.TokenFile), 32)
	defer clear(token)
	if err != nil || len(token) != 32 {
		return empty, fmt.Errorf("owning agentd authentication unavailable")
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return empty, err
	}
	params, err := json.Marshal(request)
	if err != nil {
		return empty, err
	}
	envelope := nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: request.Intent.RequestID, Method: "start", Params: params}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return empty, err
	}
	if _, err = v.DecodeLocal(raw, nativecontract.LocalProtocol, "start", nativecontract.ValidationContext{PrincipalKind: "person"}); err != nil {
		return empty, fmt.Errorf("invalid C0 native start")
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agentd/v1/native-sessions/start", bytes.NewReader(raw))
	if err != nil {
		return empty, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	transport := &http.Transport{ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if agentsetup.CheckSocket(local.Socket) != nil {
			return nil, fmt.Errorf("owning agentd socket unavailable")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", local.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return empty, fmt.Errorf("native start unconfirmed; inspect the same request ID before retrying")
	}
	defer response.Body.Close()
	if response.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		return empty, fmt.Errorf("native protocol mismatch")
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, nativecontract.RequestBodyBytes+1))
	if err != nil || len(responseBody) > nativecontract.RequestBodyBytes {
		return empty, fmt.Errorf("native start response exceeded its bound or was incomplete")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var refusal nativecontract.Error
		if v.DecodeJSON("NativeError", responseBody, &refusal) == nil {
			return empty, &nativecontract.ValidationError{Reason: refusal.Reason, Path: "$"}
		}
		return empty, fmt.Errorf("native coordinator start refused (HTTP %d)", response.StatusCode)
	}
	var result nativecontract.OperationResult
	if v.DecodeJSON("NativeOperationResult", responseBody, &result) != nil || result.Receipt.RequestID != request.Intent.RequestID || result.Receipt.Operation != "start" ||
		result.Session == nil || !validUUID(result.Session.OwnedSessionID) || result.Session.Role != "coordinator" || result.Session.Profile != "native" || result.Session.Harness != request.Intent.Harness ||
		(result.Receipt.Binding != nil && (result.Session.CurrentBinding == nil || !result.Receipt.Binding.Equal(*result.Session.CurrentBinding) || result.Receipt.Binding.ProjectID != project)) {
		return empty, fmt.Errorf("native start returned an invalid receipt")
	}
	if result.Receipt.State == "rejected" || result.Receipt.State == "expired" || result.Receipt.State == "uncertain" {
		return empty, fmt.Errorf("native coordinator start %s; outcome not confirmed", result.Receipt.State)
	}
	return result, nil
}
