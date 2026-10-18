// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

func TestDisabledAttachLocalStartupIsNotOffline(t *testing.T) {
	for _, message := range []string{"paired attach proof unavailable", "paired transport unavailable", "cannot create watch poll key", "paired attach configuration unavailable", "private partial body\x1b[31m"} {
		t.Run(message, func(t *testing.T) {
			m := DisabledAttachManager(fmt.Errorf("private startup wrapper: %w", errors.New(message)))
			_, err := m.handle(t.Context(), attachObservation{}, AttachLocalRequest{Operation: "preview"})
			var detail *AttachLocalError
			if !errors.As(err, &detail) || detail.Code != "attach_local_unavailable" {
				t.Fatal("local startup failure misclassified")
			}
			for _, action := range []string{"aeon-agentd status", "agentd log", "restart agentd"} {
				if !strings.Contains(detail.Hint, action) {
					t.Fatalf("local startup repair omitted %q", action)
				}
			}
			if strings.Contains(detail.Hint, "private") || strings.ContainsRune(detail.Hint, '\x1b') || len(m.sessions) != 0 {
				t.Fatal("startup failure leaked text or created authority")
			}
		})
	}
}

func TestDisabledAttachPairingStartupCauses(t *testing.T) {
	for _, tc := range []struct {
		cause        error
		code, action string
	}{
		{agentsetup.ErrAttachComputerDraining, "attach_draining", "pair the computer again"},
		{agentsetup.ErrAttachPairingCleaned, "attach_pairing_revoked", "pair this computer again"},
		{agentsetup.ErrAttachConfigMismatch, "attach_pairing_mismatch", "origin and workspace"},
		{fmt.Errorf("%w: private decode failure", ErrAttachExchange), "attach_offline", "Check the connection"},
	} {
		m := DisabledAttachManager(fmt.Errorf("private wrapper: %w", tc.cause))
		_, err := m.handle(t.Context(), attachObservation{}, AttachLocalRequest{Operation: "preview"})
		var detail *AttachLocalError
		if !errors.As(err, &detail) || detail.Code != tc.code || !strings.Contains(detail.Hint, tc.action) || strings.Contains(detail.Hint, "private") || len(m.sessions) != 0 {
			t.Fatalf("startup cause lost its repair: %s", tc.code)
		}
	}
}

func TestAttachDrainingNamesRealRecoveryActions(t *testing.T) {
	var detail *AttachLocalError
	if !errors.As(attachRefusal(ErrDraining), &detail) {
		t.Fatal("missing draining refusal")
	}
	for _, action := range []string{"aeon-agentd add-harness", "old enrollment may keep draining", "let owned work finish", "pair the computer again"} {
		if !strings.Contains(detail.Hint, action) {
			t.Fatalf("draining repair omitted %q", action)
		}
	}
	if strings.Contains(strings.ToLower(detail.Hint), "reconnect") {
		t.Fatal("draining repair names a nonexistent operation")
	}
}

func TestAttachRefusalHintsBuiltOnce(t *testing.T) {
	// Warm the classifier, then count allocations for a complete fixed repair.
	// Building the whole inventory per request exceeds the classifier's budget.
	cause := &client.StatusError{Status: 429, Message: "computer attach limit reached"}
	allocs := testing.AllocsPerRun(10, func() {
		err := attachRefusal(cause)
		var detail *AttachLocalError
		if !errors.As(err, &detail) || detail.Hint == "" {
			t.Fatal("missing fixed repair")
		}
	})
	// The error and errors.As targets escape; no map or formatted hint does.
	if allocs > 5 {
		t.Fatalf("fixed repair rebuilt its inventory: %.0f allocations", allocs)
	}
}

func TestAttachHintsTrackSharedLimits(t *testing.T) {
	for _, tc := range []struct{ message, value string }{
		{"computer attach limit reached", fmt.Sprintf("%d pending", attachwatch.ComputerMax)},
		{"attach attempt cap reached", fmt.Sprintf("%g minutes", attachwatch.AttemptWindow.Minutes())},
	} {
		var detail *AttachLocalError
		if !errors.As(attachRefusal(&client.StatusError{Status: 429, Message: tc.message}), &detail) || !strings.Contains(detail.Hint, tc.value) {
			t.Fatal("repair drifted from shared admission policy")
		}
	}
}
