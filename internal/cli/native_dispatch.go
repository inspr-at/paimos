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
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativedispatch"
)

// nativeWorkerDispatch is mounted as `aeon session dispatch` by I2, the owner
// of cmdSession/compat_cmds.go. It consumes only the frozen NativeLocalStart;
// the daemon derives authenticated tenant/project from the approved ancestry.
func (rt *runtime) nativeWorkerDispatch() *Command {
	var socket string
	return &Command{Name: "dispatch", Short: "Dispatch an approved native worker through agentd", Use: "session dispatch --socket PATH < native-local-start.json", minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) { fs.string(&socket, "socket", 0, "owning agentd Unix socket") },
		run: func([]string) error {
			start, err := decodeNativeWorkerStart(rt.stdin)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(socket) || len(socket) > 4096 {
				return usagef("--socket must name the owning agentd socket")
			}
			if err = agentsetup.CheckSocket(socket); err != nil {
				return fmt.Errorf("owning agentd socket unavailable")
			}
			out, err := requestNativeWorkerDispatch(rt.context(), agentdwire.Client{Socket: socket, TokenFile: socket + ".token"}, start)
			if err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			fmt.Fprintf(rt.stdout, "%s · %s\n", out.Session.OwnedSessionID, out.Receipt.State)
			return nil
		},
	}
}

func decodeNativeWorkerStart(reader io.Reader) (nativecontract.LocalStart, error) {
	var start nativecontract.LocalStart
	raw, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil || len(raw) > 16384 {
		return start, usagef("native worker request exceeds 16 KiB or is incomplete")
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return start, err
	}
	if err = v.DecodeJSON("NativeLocalStart", raw, &start); err != nil {
		return start, err
	}
	return start, nativedispatch.ValidateWorkerStart(v, start)
}

func requestNativeWorkerDispatch(ctx context.Context, local agentdwire.Client, start nativecontract.LocalStart) (nativecontract.OperationResult, error) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nativecontract.OperationResult{}, err
	}
	if err = nativedispatch.ValidateWorkerStart(v, start); err != nil {
		return nativecontract.OperationResult{}, err
	}
	// This is the existing owner-socket authentication, not a new vendor or
	// agent credential. It stays in the CLI and is never returned to the worker.
	store, err := agentsetup.OpenStore(filepath.Dir(local.TokenFile), false)
	if err != nil {
		return nativecontract.OperationResult{}, fmt.Errorf("owning agentd authentication unavailable")
	}
	defer store.Close()
	token, err := store.Read(filepath.Base(local.TokenFile), 32)
	if err != nil || len(token) != 32 {
		return nativecontract.OperationResult{}, fmt.Errorf("owning agentd authentication unavailable")
	}
	defer clear(token)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", local.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return sendNativeWorkerStart(ctx, client, token, start)
}

func sendNativeWorkerStart(ctx context.Context, client *http.Client, token []byte, start nativecontract.LocalStart) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	v, err := nativecontract.NewValidator()
	if err != nil {
		return out, err
	}
	if err = nativedispatch.ValidateWorkerStart(v, start); err != nil {
		return out, err
	}
	params, _ := json.Marshal(start)
	envelope := nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: start.Intent.RequestID, Method: "start", Params: params}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return out, err
	}
	if err = v.ValidateJSON("NativeLocalEnvelope", raw); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agentd/v1/native-sessions/start", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aeon-Session-Protocol", nativecontract.LocalProtocol)
	response, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("native dispatch unconfirmed; reconcile this request ID on the same daemon before retrying")
	}
	defer response.Body.Close()
	if response.Header.Get("X-Aeon-Session-Protocol") != nativecontract.LocalProtocol {
		return out, fmt.Errorf("native dispatch protocol mismatch; outcome unconfirmed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return out, fmt.Errorf("native dispatch response exceeded its bound or was incomplete; outcome unconfirmed")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return out, fmt.Errorf("native worker dispatch refused (HTTP %d)", response.StatusCode)
	}
	if err = v.DecodeJSON("NativeOperationResult", body, &out); err != nil {
		return out, err
	}
	if out.Receipt.RequestID != start.Intent.RequestID || out.Receipt.Operation != "start" || out.Session == nil || out.Session.Role != "worker" || out.Session.Profile != "native" || out.Session.Harness != start.Intent.Harness || out.Session.CurrentBinding != nil && (out.Session.CurrentBinding.RunID == nil || out.Receipt.Binding == nil || out.Receipt.Binding.OwnedSessionID != out.Session.OwnedSessionID) {
		return out, fmt.Errorf("native dispatch returned a mismatched receipt; outcome unconfirmed")
	}
	if out.Receipt.State == "uncertain" || out.Receipt.State == "rejected" || out.Receipt.State == "expired" {
		return out, fmt.Errorf("native dispatch %s; retain uncertain occupancy and inspect the same request ID", out.Receipt.State)
	}
	return out, nil
}
