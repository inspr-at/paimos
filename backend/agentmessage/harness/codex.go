// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AdapterCodex         = "codex"
	KindCodexThread      = "codex_thread"
	defaultClientVersion = "dev"
)

type CodexPlugin struct{}

func (CodexPlugin) Name() string         { return AdapterCodex }
func (CodexPlugin) Kind() string         { return KindCodexThread }
func (CodexPlugin) MaximumLevel() string { return LevelSteer }
func (CodexPlugin) Mode() string         { return ModeLocal }

func (CodexPlugin) ValidateTarget(_ context.Context, ref string) error {
	if !utf8.ValidString(ref) || len([]byte(ref)) < 1 || len([]byte(ref)) > 256 || strings.ContainsAny(ref, "\x00\r\n") {
		return &Error{Code: CodeTargetRefInvalid, Message: "Codex thread references must be 1 to 256 safe UTF-8 bytes"}
	}
	return nil
}

func (p CodexPlugin) Deliver(ctx context.Context, req DeliverRequest) (DeliverResult, error) {
	target := strings.TrimSpace(req.TargetRef)
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_SESSION_ID"))
	}
	if target == "" {
		return DeliverResult{}, &UnavailableError{Message: "Codex delivery requires --deliver-target or CODEX_THREAD_ID"}
	}
	if err := p.ValidateTarget(ctx, target); err != nil {
		return DeliverResult{}, err
	}
	if req.Level != LevelSteer {
		return DeliverResult{EffectiveLevel: LevelSimple, Primitive: "codex queue --thread"}, deliverCodexQueue(ctx, req.Body, target, req.Stdout, req.Stderr)
	}
	steered, reason, err := DeliverCodexSteer(ctx, req.Body, target, req.Stderr, req.ClientVersion)
	if err != nil {
		return DeliverResult{}, err
	}
	if steered {
		return DeliverResult{EffectiveLevel: LevelSteer, Primitive: "codex app-server turn/steer"}, nil
	}
	if err := deliverCodexQueue(ctx, req.Body, target, req.Stdout, req.Stderr); err != nil {
		return DeliverResult{}, err
	}
	return DeliverResult{EffectiveLevel: LevelSimple, FallbackReason: reason, Primitive: "codex queue --thread"}, nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func deliverCodexQueue(ctx context.Context, body, target string, stdout, stderr io.Writer) error {
	path, err := exec.LookPath("codex")
	if err != nil {
		return &UnavailableError{Message: "Codex delivery requires the codex CLI in PATH"}
	}
	cmd := exec.CommandContext(ctx, path, "queue", "--thread", target, "--message", body) // #nosec G204 G702 -- fixed argv and operator-controlled PATH.
	cmd.Stdout, cmd.Stderr = writerOrDiscard(stdout), writerOrDiscard(stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deliver to Codex thread: %w", err)
	}
	return nil
}

type codexRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *codexRPCError) Error() string {
	return fmt.Sprintf("Codex app-server error %d: %s", e.Code, e.Message)
}

// DeliverCodexSteer uses only the documented app-server proxy JSON-RPC
// sequence. It returns idle and raced/rejected turns as simple fallbacks.
func DeliverCodexSteer(ctx context.Context, body, target string, stderr io.Writer, clientVersion string) (bool, string, error) {
	if strings.TrimSpace(target) == "" {
		return false, "", &UnavailableError{Message: "Codex steer requires a receiver-owned thread target"}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return false, "", &UnavailableError{Message: "Codex delivery requires the codex CLI in PATH"}
	}
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	daemon := exec.CommandContext(operationCtx, path, "app-server", "daemon", "start") // #nosec G204 G702 -- fixed argv and operator-controlled PATH.
	daemon.Stdout, daemon.Stderr = io.Discard, writerOrDiscard(stderr)
	if err := daemon.Run(); err != nil {
		return false, "", fmt.Errorf("start Codex app-server daemon: %w", err)
	}
	proxy := exec.CommandContext(operationCtx, path, "app-server", "proxy") // #nosec G204 G702 -- fixed argv and operator-controlled PATH.
	stdin, err := proxy.StdinPipe()
	if err != nil {
		return false, "", fmt.Errorf("open Codex proxy stdin: %w", err)
	}
	stdoutPipe, err := proxy.StdoutPipe()
	if err != nil {
		return false, "", fmt.Errorf("open Codex proxy stdout: %w", err)
	}
	proxy.Stderr = writerOrDiscard(stderr)
	if err := proxy.Start(); err != nil {
		return false, "", fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	encoder := json.NewEncoder(stdin)
	decoder := json.NewDecoder(bufio.NewReader(stdoutPipe))
	finish := func() error {
		_ = stdin.Close()
		if err := proxy.Wait(); err != nil && operationCtx.Err() == nil {
			return fmt.Errorf("Codex app-server proxy: %w", err)
		}
		return nil
	}
	if clientVersion == "" {
		clientVersion = defaultClientVersion
	}
	var initialize json.RawMessage
	if err := codexRPCCall(encoder, decoder, 1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "paimos", "version": clientVersion},
	}, &initialize); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("notify Codex app-server initialized: %w", err)
	}
	var turns struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := codexRPCCall(encoder, decoder, 2, "thread/turns/list", map[string]any{
		"threadId": target, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
	}, &turns); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("list Codex thread turns: %w", err)
	}
	if len(turns.Data) == 0 || turns.Data[0].Status != "inProgress" {
		if err := finish(); err != nil {
			return false, "", err
		}
		return false, "idle", nil
	}
	var steerResult struct {
		TurnID string `json:"turnId"`
	}
	err = codexRPCCall(encoder, decoder, 3, "turn/steer", map[string]any{
		"threadId": target, "expectedTurnId": turns.Data[0].ID,
		"input": []map[string]string{{"type": "text", "text": body}},
	}, &steerResult)
	if err != nil {
		var remote *codexRPCError
		if errors.As(err, &remote) {
			_ = finish()
			return false, "not_steerable", nil
		}
		_ = finish()
		return false, "", fmt.Errorf("steer Codex turn: %w", err)
	}
	if err := finish(); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func codexRPCCall(encoder *json.Encoder, decoder *json.Decoder, id int, method string, params, result any) error {
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *codexRPCError  `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			return err
		}
		if len(response.ID) == 0 || string(response.ID) != strconv.Itoa(id) {
			continue
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

func init() {
	if err := Register(CodexPlugin{}); err != nil {
		panic(err)
	}
}
