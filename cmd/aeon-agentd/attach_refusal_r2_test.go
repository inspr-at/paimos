// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

func TestAttachRefusalLanguagesAndSocketHaveSameCodes(t *testing.T) {
	german := make([]string, 0, len(attachGermanRefusalHints))
	for code, hint := range attachGermanRefusalHints {
		german = append(german, code)
		if hint == "" || len(hint) > 512 || strings.ContainsAny(hint, "\x1b\r\n") {
			t.Fatalf("unsafe German repair for %s", code)
		}
	}
	slices.Sort(german)
	english := agentd.AttachRefusalCodes()
	socket := agentdwire.AttachRefusalCodes()
	if !slices.Equal(english, german) || !slices.Equal(english, socket) {
		t.Fatalf("refusal inventory mismatch: English %v, German %v, socket %v", english, german, socket)
	}
}

func TestAttachGermanDrainingNamesRealRecoveryActions(t *testing.T) {
	err := attachLocalizedFailure(&agentd.AttachLocalError{Code: "attach_draining"}, "de")
	for _, action := range []string{"aeon-agentd add-harness", "während der alten Trennung", "Abschluss laufender Arbeit", "kopple den Computer erneut"} {
		if !strings.Contains(err.Error(), action) {
			t.Fatalf("German draining repair omitted %q", action)
		}
	}
	if strings.Contains(err.Error(), "verbinde den Computer") {
		t.Fatal("German repair names a nonexistent reconnect operation")
	}
}

func TestAttachGermanHintsBuiltOnce(t *testing.T) {
	err := &agentd.AttachLocalError{Code: "attach_computer_limit"}
	allocs := testing.AllocsPerRun(10, func() {
		var detail *agentd.AttachLocalError
		if !errors.As(attachLocalizedFailure(err, "de"), &detail) || detail.Hint == "" {
			t.Fatal("missing German repair")
		}
	})
	if allocs > 3 { // errors.As targets plus the new local error
		t.Fatalf("German repair rebuilt its inventory: %.0f allocations", allocs)
	}
}

func TestAttachGermanHintsTrackSharedLimits(t *testing.T) {
	for code, value := range map[string]string{
		"attach_computer_limit": fmt.Sprintf("%d offene", attachwatch.ComputerMax),
		"attach_attempt_limit":  fmt.Sprintf("%g Minuten", attachwatch.AttemptWindow.Minutes()),
	} {
		if !strings.Contains(attachLocalizedFailure(&agentd.AttachLocalError{Code: code}, "de").Error(), value) {
			t.Fatal("German repair drifted from shared admission policy")
		}
	}
}

func TestPairedAttachMarksOnlyRemoteExchangeFailures(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("fixture path unavailable")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal("fixture store permissions unavailable")
	}
	const tenantID = "11111111-1111-4111-8111-111111111111"
	const computerID = "22222222-2222-4222-8222-222222222222"
	const principalID = "33333333-3333-4333-8333-333333333333"
	var failure atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failure.Load() {
			_, _ = w.Write([]byte(`{"private partial response":`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "registered", "local_consent_proof_version": attachwatch.LocalConsentProofVersion})
	}))
	defer server.Close()
	c := agentsetup.RuntimeConfig{Origin: server.URL, TenantID: tenantID, ComputerID: computerID, PrincipalID: principalID, Workspace: root}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal("fixture store unavailable")
	}
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": "44444444-4444-4444-8444-444444444444", "computer_name": "fixture", "workspace_path": root}, "view": agentsetup.View{TenantID: tenantID, ComputerID: computerID, PrincipalID: principalID}, "device_secret": strings.Repeat("a", 64), "runtime_secret": strings.Repeat("b", 64), "lifecycle_secret": strings.Repeat("c", 64)}
	raw, _ := json.Marshal(snapshot)
	if err = store.Write("pairing.json", raw, true); err != nil {
		t.Fatal("fixture pairing not saved")
	}
	store.Close()
	for _, stage := range []string{"register", "exchange"} {
		t.Run(stage, func(t *testing.T) {
			failure.Store(stage == "register")
			var exchangeErr error
			remote := &agentd.Remote{Client: client.New(server.URL, "fixture")}
			remote.Client.HTTP = server.Client()
			manager, startErr := pairedAttach(root, c, remote)
			if stage == "exchange" {
				if startErr != nil || manager == nil {
					t.Fatal("fixture registration failed")
				}
				failure.Store(true)
				_, exchangeErr = pairedAttachExchange(t.Context(), remote.Client, attachwatch.DeviceRequest{Operation: "request"})
			} else {
				exchangeErr = startErr
			}
			if !errors.Is(exchangeErr, agentd.ErrAttachExchange) {
				t.Fatal("remote decode failure lacked exchange marker")
			}
		})
	}
	if _, err := pairedAttach(root, c, nil); err == nil || errors.Is(err, agentd.ErrAttachExchange) {
		t.Fatal("local transport startup failure marked as a remote failure")
	}
	status := &client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalPairing}
	if got := attachExchangeFailure(status); got != status || errors.Is(got, agentd.ErrAttachExchange) {
		t.Fatal("HTTP refusal lost its specific classification")
	}
	if got := attachExchangeFailure(context.DeadlineExceeded); !errors.Is(got, agentd.ErrAttachExchange) {
		t.Fatal("remote timeout lacked exchange marker")
	}
}
