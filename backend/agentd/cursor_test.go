//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const cursorHelperEnvironment = "PAIMOS_CURSOR_ACP_HELPER"

type cursorProtocolTestAdapter struct{ *CursorAdapter }

func (cursorProtocolTestAdapter) AccountLabel(context.Context) string { return "unknown" }

func newTestCursorAdapter(t *testing.T, mode string) (*CursorAdapter, *[]string) {
	t.Helper()
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return cursorSupportedCLIVersion, nil }
	adapter.SetAccounts(cursorTestRegistry(t))
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return cursorAuthenticatedStatusJSON(), nil
	}
	var argv []string
	adapter.command = func(_ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestCursorACPHelperProcess$")
		cmd.Env = append(os.Environ(), cursorHelperEnvironment+"="+mode)
		return cmd
	}
	return adapter, &argv
}

func cursorTestRegistry(t *testing.T) CursorAccountRegistry {
	t.Helper()
	return cursorTestRegistryWithUserID(t, "")
}

func cursorTestRegistryWithUserID(t *testing.T, userID string) CursorAccountRegistry {
	t.Helper()
	raw, err := json.Marshal(cursorAccountRegistryFile{Accounts: []cursorAccountRegistryEntry{
		{Key: "operator-cursor", Email: "cursor-operator@example.invalid", UserID: userID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ParseCursorAccountRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func cursorAuthenticatedStatusJSON() []byte {
	return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":9007199254740993}}`)
}

func cursorOwnedStart(t *testing.T, profile dispatchprofile.Profile) StartRequest {
	t.Helper()
	return StartRequest{
		KeepAlive: true, Workspace: t.TempDir(), Prompt: "secret-not-persisted",
		Identity: "cursor:test", Adapter: AdapterCursor, ProjectID: 963,
		AccountKey: "operator-cursor", ExpectedAccountLabel: AccountCursorContext,
		ResolvedProfile: &profile,
	}
}

func cursorHelperSessionNew(currentModelID string) map[string]any {
	return map[string]any{
		"sessionId": "sess-owned",
		"modes": map[string]any{
			"currentModeId": "agent",
			"availableModes": []map[string]string{
				{"id": "agent", "name": "Agent", "description": "Full agent capabilities with tool access"},
				{"id": "plan", "name": "Plan", "description": "Read-only mode for planning"},
				{"id": "ask", "name": "Ask", "description": "Q&A mode"},
			},
		},
		"models": map[string]any{
			"currentModelId": currentModelID,
			"availableModels": []map[string]string{
				{"modelId": "default[]", "name": "Auto"},
				{"modelId": cursorGrokACPModel, "name": "grok-4.6"},
				{"modelId": cursorComposerACPModel, "name": "composer-2.5"},
				{"modelId": "claude-opus-5[thinking=true,context=300k,effort=high,fast=false]", "name": "claude-opus-5"},
			},
		},
		"configOptions": []map[string]any{
			{"id": "mode", "currentValue": "agent"},
			{"id": "model", "currentValue": currentModelID},
		},
	}
}

func cursorTestProfile(t *testing.T) dispatchprofile.Profile {
	t.Helper()
	profile, err := dispatchprofile.Resolve("cursor-composer", dispatchprofile.CatalogVersion, AdapterCursor)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestCursorProcessOwnsExactACPSessionForControl(t *testing.T) {
	adapter, argv := newTestCursorAdapter(t, "serve")
	profile := cursorTestProfile(t)
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	key, label := process.(accountSelection).AccountSelection()
	if !slices.Equal(*argv, []string{"--model", cursorComposerArgv, "acp"}) || process.PID() <= 0 || key != "operator-cursor" || label != AccountCursorContext {
		t.Fatalf("argv=%q pid=%d key=%q label=%q", *argv, process.PID(), key, label)
	}
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "steer-unsupported", Text: "same-turn"}); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("steer err=%v", err)
	}
	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "control-live"})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.Primitive != cursorCancelPrimitive || interrupt.VendorMessageID != "sess-owned" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-live"}); err != nil {
		t.Fatal(err)
	}
	seenSession, seenTurn, seenControl := false, false, false
	deadline := time.After(2 * time.Second)
	for !seenSession || !seenTurn || !seenControl {
		select {
		case event := <-events:
			seenSession = seenSession || event.Kind == EventSessionStarted && event.HarnessSessionID == "sess-owned"
			seenTurn = seenTurn || event.Kind == EventTurnStarted
			seenControl = seenControl || event.Kind == EventControlApplied && event.CorrelationID != ""
		case <-deadline:
			t.Fatalf("events session=%t turn=%t control=%t", seenSession, seenTurn, seenControl)
		}
	}
}

func TestCursorInboxDeliversNextTurnAfterIdle(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "idle")
	profile := cursorTestProfile(t)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	inboxProc, ok := process.(interface {
		Inbox(context.Context, ControlRequest) (ControlEffect, error)
		InboxReady() bool
	})
	if !ok {
		t.Fatal("cursor process does not implement inbox")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !inboxProc.InboxReady() {
		if time.Now().After(deadline) {
			t.Fatal("initial prompt did not become idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	inbox, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "delivery-next", Text: "next-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Primitive != cursorPromptPrimitive || inbox.CorrelationID != "delivery-next" || inbox.VendorMessageID != "sess-owned" {
		t.Fatalf("inbox=%+v", inbox)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "idle-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRefusesUnapprovedAndAutoModels(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	for _, model := range []string{"auto", "Auto", "composer", "grok", "gpt-5", "sonnet-4-thinking"} {
		profile := dispatchprofile.Profile{
			ID: "cursor-unapproved", Version: "1", Harness: AdapterCursor, Model: model, Effort: "high",
			MachineSource: dispatchprofile.MachineAuthenticatedReporter, AccountSource: dispatchprofile.AccountLocalProbe, WorkspaceMode: "exclusive",
		}
		req := cursorOwnedStart(t, profile)
		_, err := adapter.Start(context.Background(), req, nil)
		if err == nil {
			t.Fatalf("model %q was accepted", model)
		}
	}
}

func TestCursorComposerRefusesInventedHighEffort(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := dispatchprofile.Profile{
		ID: "cursor-composer", Version: "1", Harness: AdapterCursor, Model: cursorComposerArgv, Effort: "high",
		MachineSource: dispatchprofile.MachineAuthenticatedReporter, AccountSource: dispatchprofile.AccountLocalProbe, WorkspaceMode: "exclusive",
	}
	_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "does not advertise a reasoning effort") {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorStartRequiresDispatchProfile(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	req := cursorOwnedStart(t, dispatchprofile.Profile{})
	req.ResolvedProfile = nil
	_, err := adapter.Start(context.Background(), req, nil)
	if !errors.Is(err, ErrDispatchProfile) {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorStartRequiresNamedAccount(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	req := cursorOwnedStart(t, profile)
	req.AccountKey = ""
	_, err := adapter.Start(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "managed account selection is unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorStartRefusesCodexExpectedClass(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	req := cursorOwnedStart(t, profile)
	req.ExpectedAccountLabel = "chatgpt"
	_, err := adapter.Start(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "managed account selection is unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorStartCancellationReapsInFlightACP(t *testing.T) {
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return cursorSupportedCLIVersion, nil }
	adapter.SetAccounts(cursorTestRegistry(t))
	adapter.statusJSON = func(context.Context, string) ([]byte, error) { return cursorAuthenticatedStatusJSON(), nil }
	var child *exec.Cmd
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestCursorACPHelperProcess$")
		child.Env = append(os.Environ(), cursorHelperEnvironment+"=block")
		return child
	}
	profile := cursorTestProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := adapter.Start(ctx, cursorOwnedStart(t, profile), nil)
	if !errors.Is(err, context.DeadlineExceeded) || child == nil || child.Process == nil || child.ProcessState == nil {
		t.Fatalf("err=%v child=%v", err, child)
	}
}

func TestCursorRejectsOversizedAndMalformedFrames(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "malformed")
	profile := cursorTestProfile(t)
	_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil {
		t.Fatal("malformed ACP initialize was accepted")
	}

	oversized, _ := newTestCursorAdapter(t, "oversized")
	events := make(chan AdapterEvent, 8)
	_, err = oversized.Start(context.Background(), cursorOwnedStart(t, profile), func(event AdapterEvent) { events <- event })
	if err == nil {
		t.Fatal("oversized ACP frame was accepted")
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.ErrorCode == ErrorEventStreamBound {
				return
			}
		case <-deadline:
			t.Fatal("oversized ACP frame did not fail the event-stream bound")
		}
	}
}

func TestCursorEOFMidPromptFailsClosed(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "eof-prompt")
	profile := cursorTestProfile(t)
	events := make(chan AdapterEvent, 8)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.ErrorCode == ErrorAppServerProtocol {
				ready, ok := process.(interface{ InboxReady() bool })
				if ok && ready.InboxReady() {
					t.Fatal("inbox stayed ready after EOF mid-prompt")
				}
				return
			}
		case <-deadline:
			t.Fatal("EOF mid-prompt did not fail closed")
		}
	}
}

func TestCursorIDBearingNotificationMethodsFailClosed(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "id-notifications")
	profile := cursorTestProfile(t)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	ready, ok := process.(interface{ InboxReady() bool })
	if !ok {
		t.Fatal("cursor process does not implement inbox ready")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !ready.InboxReady() {
		if time.Now().After(deadline) {
			t.Fatal("id-bearing notification methods hung the owned turn")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "id-note-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPermissionAndExtensionRequestsFailClosed(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "permission")
	profile := cursorTestProfile(t)
	events := make(chan AdapterEvent, 8)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	ready, ok := process.(interface{ InboxReady() bool })
	if !ok {
		t.Fatal("cursor process does not implement inbox ready")
	}
	for !ready.InboxReady() {
		if time.Now().After(deadline) {
			t.Fatal("permission helper did not complete the refused turn")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "perm-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPinnedVersionMismatchRefusesStart(t *testing.T) {
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return "2026.01.01-deadbeef", nil }
	adapter.SetAccounts(cursorTestRegistry(t))
	adapter.statusJSON = func(context.Context, string) ([]byte, error) { return cursorAuthenticatedStatusJSON(), nil }
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "pinned Cursor CLI version") {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorAccountProbeUsesOfficialStatusJSONAndStaysUnknownWithoutNamedContext(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "official-argv")
	probe := writeProbe(t, `#!/bin/sh
[ "$1:$2:$3" = "status:--format:json" ] || exit 9
touch '`+marker+`'
printf '%s\n' '{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"must-not-be-recorded@example.invalid"}}'
`)
	if got := NewCursorAdapter(probe, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("closed label=%q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("official status argv was not probed")
	}
	loggedInShaped := writeProbe(t, `#!/bin/sh
printf '%s\n' '{"loggedIn":true,"email":"must-not-be-recorded@example.invalid"}'
`)
	if got := NewCursorAdapter(loggedInShaped, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("loggedIn-shaped label=%q", got)
	}
	unauthenticated := writeProbe(t, `#!/bin/sh
printf '%s\n' '{"status":"unauthenticated","isAuthenticated":false}'
`)
	if got := NewCursorAdapter(unauthenticated, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("unauthenticated label=%q", got)
	}
}

func TestCursorStartRefusesLoggedInShapedAndMismatchedIdentity(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return []byte(`{"loggedIn":true,"email":"cursor-operator@example.invalid"}`), nil
	}
	_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "managed account identity could not be verified") {
		t.Fatalf("loggedIn-shaped err=%v", err)
	}
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"other@example.invalid"}}`), nil
	}
	_, err = adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "managed account identity could not be verified") {
		t.Fatalf("mismatch err=%v", err)
	}
}

func TestParseCursorStatusIdentityAcceptsNativeIntegerUserID(t *testing.T) {
	native := cursorAuthenticatedStatusJSON()
	email, userID, ok := parseCursorStatusIdentity(native)
	if !ok || email != "cursor-operator@example.invalid" || userID != "9007199254740993" {
		t.Fatalf("native identity email=%q userID=%q ok=%t", email, userID, ok)
	}
	if err := verifyCursorAccountIdentity(native, cursorAccount{email: "cursor-operator@example.invalid"}); err != nil {
		t.Fatalf("email-only registry refused native integer userId: %v", err)
	}
	largerThanInt64 := []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":9223372036854775808}}`)
	_, userID, ok = parseCursorStatusIdentity(largerThanInt64)
	if !ok || userID != "9223372036854775808" {
		t.Fatalf("int64 overflow identity userID=%q ok=%t", userID, ok)
	}
	stringID := []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":"usr_fixture"}}`)
	_, userID, ok = parseCursorStatusIdentity(stringID)
	if !ok || userID != "usr_fixture" {
		t.Fatalf("string identity userID=%q ok=%t", userID, ok)
	}
	missing := []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid"}}`)
	_, userID, ok = parseCursorStatusIdentity(missing)
	if !ok || userID != "" {
		t.Fatalf("missing optional identity userID=%q ok=%t", userID, ok)
	}
}

func TestParseCursorStatusIdentityRejectsAmbiguousUserIDs(t *testing.T) {
	prefix := `{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":`
	for _, raw := range []string{`{}`, `[]`, `true`, `1.5`, `1.0`, `1e2`, `-3`, `+3`, `01`, `0`, `" "`} {
		output := []byte(prefix + raw + `}}`)
		if _, _, ok := parseCursorStatusIdentity(output); ok {
			t.Fatalf("accepted ambiguous userId %s", raw)
		}
		if err := verifyCursorAccountIdentity(output, cursorAccount{email: "cursor-operator@example.invalid"}); err == nil {
			t.Fatalf("email-only registry skipped invalid userId %s", raw)
		}
	}
}

func TestVerifyCursorAccountIdentityConfiguredUserID(t *testing.T) {
	native := cursorAuthenticatedStatusJSON()
	expected := cursorAccount{email: "cursor-operator@example.invalid", userID: "9007199254740993"}
	if err := verifyCursorAccountIdentity(native, expected); err != nil {
		t.Fatalf("matching configured id: %v", err)
	}
	expected.userID = "1"
	if err := verifyCursorAccountIdentity(native, expected); err == nil {
		t.Fatal("accepted mismatched configured id")
	}
	missing := []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid"}}`)
	expected.userID = "9007199254740993"
	if err := verifyCursorAccountIdentity(missing, expected); err == nil {
		t.Fatal("skipped missing configured id")
	}
	stringNative := []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":"usr_fixture"}}`)
	if err := verifyCursorAccountIdentity(stringNative, cursorAccount{email: "cursor-operator@example.invalid", userID: "usr_fixture"}); err != nil {
		t.Fatalf("matching string id: %v", err)
	}
}

func TestCursorStartAcceptsNativeNumericUserIDWithEmailOnlyRegistry(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	profile := cursorTestProfile(t)
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "numeric-id-stop"}); err != nil {
		t.Fatal(err)
	}
	adapter.SetAccounts(cursorTestRegistryWithUserID(t, "9007199254740993"))
	process, err = adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "configured-id-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorStartRefusesInvalidAndMismatchedUserID(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	adapter.SetAccounts(cursorTestRegistryWithUserID(t, "9007199254740993"))
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid"}}`), nil
	}
	_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "managed account identity could not be verified") {
		t.Fatalf("missing configured id err=%v", err)
	}
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":1}}`), nil
	}
	_, err = adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "managed account identity could not be verified") {
		t.Fatalf("mismatched configured id err=%v", err)
	}
	adapter.SetAccounts(cursorTestRegistry(t))
	adapter.statusJSON = func(context.Context, string) ([]byte, error) {
		return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"email":"cursor-operator@example.invalid","userId":1.5}}`), nil
	}
	_, err = adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err == nil || !strings.Contains(err.Error(), "managed account identity could not be verified") {
		t.Fatalf("fractional userId err=%v", err)
	}
}

func TestCursorSessionNewMustAcknowledgeModelAndConfigBeforePrompt(t *testing.T) {
	profile := cursorTestProfile(t)
	for _, mode := range []string{"session-id-only", "auto-model", "wrong-model", "disagree-config"} {
		adapter, _ := newTestCursorAdapter(t, mode)
		_, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
		if err == nil {
			t.Fatalf("mode %q started without model acknowledgement", mode)
		}
	}
}

func TestCursorGrokLaunchUsesExactIncludedModel(t *testing.T) {
	adapter, argv := newTestCursorAdapter(t, "grok")
	profile, err := dispatchprofile.Resolve("cursor-grok", dispatchprofile.CatalogVersion, AdapterCursor)
	if err != nil {
		t.Fatal(err)
	}
	process, err := adapter.Start(context.Background(), cursorOwnedStart(t, profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*argv, []string{"--model", cursorGrokArgv, "acp"}) {
		t.Fatalf("argv=%q", *argv)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "grok-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorSupervisorReplayAndLostOwnership(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "idle")
	profile := cursorTestProfile(t)
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-cursor", Adapters: []Adapter{cursorProtocolTestAdapter{adapter}}})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCursor, Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:worker", ProjectID: 963,
		AccountKey: "operator-cursor", ExpectedAccountLabel: AccountCursorContext, ResolvedProfile: &profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.AccountKey != "operator-cursor" || session.AccountLabel != AccountCursorContext {
		t.Fatalf("session account=%q %q", session.AccountKey, session.AccountLabel)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !supervisor.InboxReady(session.ID) {
		if time.Now().After(deadline) {
			t.Fatal("owned cursor session never became inbox-ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	first, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "delivery-one", Text: "next",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "delivery-one", Text: "next",
	})
	if err != nil || first != second {
		t.Fatalf("replay first=%+v second=%+v err=%v", first, second, err)
	}
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "steer-one", Text: "mid-turn",
	}); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("steer err=%v", err)
	}
	if _, err := supervisor.Stop(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "stop-one",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorACPHelperProcess(t *testing.T) {
	mode := os.Getenv(cursorHelperEnvironment)
	if mode == "" {
		t.Skip("cursor ACP helper")
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	var mu sync.Mutex
	respond := func(id any, result any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	request := func(id any, method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}
	notify := func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	if mode == "malformed" {
		_, _ = os.Stdout.Write([]byte("{not-json\n"))
		os.Exit(0)
	}
	if mode == "oversized" {
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", maxCursorFrameBytes+1) + "\n"))
		os.Exit(0)
	}
	if mode == "block" {
		scanner.Scan()
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	promptIDs := map[any]bool{}
	pendingUnsupported := map[string]bool{}
	var promptWaiting any
	completePrompt := func(id any) {
		promptIDs[id] = true
		notify("session/update", map[string]any{
			"sessionId": "sess-owned",
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "ok"}},
		})
		respond(id, map[string]any{"stopReason": "end_turn"})
	}
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		method, _ := message["method"].(string)
		if method == "" {
			if mode == "id-notifications" {
				id, _ := message["id"].(string)
				errObj, _ := message["error"].(map[string]any)
				code, _ := errObj["code"].(float64)
				if code == -32601 {
					delete(pendingUnsupported, id)
				}
				if promptWaiting != nil && len(pendingUnsupported) == 0 {
					completePrompt(promptWaiting)
					promptWaiting = nil
				}
			}
			continue
		}
		id := message["id"]
		switch method {
		case "initialize":
			respond(id, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":        true,
					"promptCapabilities": map[string]any{"image": true, "audio": false, "embeddedContext": false},
				},
				"agentInfo": map[string]string{"name": "cursor-agent", "version": cursorSupportedCLIVersion},
			})
		case "session/new":
			current := cursorComposerACPModel
			switch mode {
			case "grok":
				current = cursorGrokACPModel
			case "auto-model":
				current = "default[]"
			case "wrong-model":
				current = "claude-opus-5[thinking=true,context=300k,effort=high,fast=false]"
			case "session-id-only":
				respond(id, map[string]any{"sessionId": "sess-owned"})
				continue
			case "disagree-config":
				result := cursorHelperSessionNew(cursorComposerACPModel)
				result["configOptions"] = []map[string]any{
					{"id": "mode", "currentValue": "agent"},
					{"id": "model", "currentValue": "default[]"},
				}
				respond(id, result)
				continue
			}
			respond(id, cursorHelperSessionNew(current))
			if mode == "permission" {
				request("perm-1", "session/request_permission", map[string]any{
					"sessionId": "sess-owned",
					"options": []map[string]string{
						{"optionId": "allow-always", "kind": "allow_always"},
						{"optionId": "reject-once", "kind": "reject_once"},
					},
				})
				request("q-1", "cursor/ask_question", map[string]any{"toolCallId": "call-q", "questions": []any{}})
				request("plan-1", "cursor/create_plan", map[string]any{"toolCallId": "call-p", "plan": "secret-plan"})
				request("unknown-1", "cursor/unknown_extension", map[string]any{"toolCallId": "call-x"})
			}
			if mode == "id-notifications" {
				pendingUnsupported["todo-1"] = true
				pendingUnsupported["task-1"] = true
				pendingUnsupported["img-1"] = true
				request("todo-1", "cursor/update_todos", map[string]any{"sessionId": "sess-owned", "todos": []any{}})
				request("task-1", "cursor/task", map[string]any{"sessionId": "sess-owned"})
				request("img-1", "cursor/generate_image", map[string]any{"sessionId": "sess-owned"})
				notify("cursor/update_todos", map[string]any{"sessionId": "sess-owned", "todos": []any{}})
				notify("cursor/task", map[string]any{"sessionId": "sess-owned"})
				notify("cursor/generate_image", map[string]any{"sessionId": "sess-owned"})
			}
		case "session/prompt":
			if mode == "session-id-only" || mode == "auto-model" || mode == "wrong-model" || mode == "disagree-config" {
				os.Exit(3)
			}
			if mode == "eof-prompt" {
				os.Exit(0)
			}
			promptIDs[id] = true
			notify("session/update", map[string]any{
				"sessionId": "sess-owned",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "ok"}},
			})
			if mode == "id-notifications" {
				if len(pendingUnsupported) > 0 {
					promptWaiting = id
					continue
				}
				respond(id, map[string]any{"stopReason": "end_turn"})
				continue
			}
			if mode == "idle" || mode == "permission" {
				respond(id, map[string]any{"stopReason": "end_turn"})
			}
		case "session/cancel":
			for promptID := range promptIDs {
				respond(promptID, map[string]any{"stopReason": "cancelled"})
				delete(promptIDs, promptID)
			}
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}
