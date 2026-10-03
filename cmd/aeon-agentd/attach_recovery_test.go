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
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

type attachRecoveryClock struct{ now time.Time }

func (c *attachRecoveryClock) Now() time.Time { return c.now }
func (c *attachRecoveryClock) Wait(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

func TestPairedAttachRecoveryReusesOnlyPinnedMemoryAuthority(t *testing.T) {
	for _, ack := range []string{"registered", "bad state", "bad version", "forbidden"} {
		t.Run(ack, func(t *testing.T) {
			pollKey, proof := strings.Repeat("a", 64), strings.Repeat("b", 64)
			registrations, exchanges := 0, 0
			known := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in attachwatch.DeviceRequest
				if r.Method != "POST" || r.URL.Path != "/api/agent-pairing/attach" || json.NewDecoder(r.Body).Decode(&in) != nil || in.PollKey != pollKey {
					t.Error("request lost pinned poll authority")
					w.WriteHeader(400)
					return
				}
				if in.Operation == "register" {
					registrations++
					if in.DeviceProof != proof || in.AttachProtocol != attachwatch.Protocol || in.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion || in.LocalAuthCapability != attachwatch.LocalAuthUnsupported {
						t.Error("recovery changed registration authority or capabilities")
					}
					v := attachwatch.View{State: "registered", LocalConsentProofVersion: attachwatch.LocalConsentProofVersion}
					if registrations > 1 {
						switch ack {
						case "bad state":
							v.State = "pending"
						case "bad version":
							v.LocalConsentProofVersion = 1
						case "forbidden":
							w.WriteHeader(403)
							_, _ = w.Write([]byte(`{"error":"computer proof rejected","attach_refusal":"pairing_revoked"}`))
							return
						}
					}
					known = true
					_ = json.NewEncoder(w).Encode(v)
					return
				}
				exchanges++
				if in.DeviceProof != "" {
					t.Error("ordinary exchange leaked lifecycle proof")
				}
				if !known {
					w.WriteHeader(403)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "daemon poll key rejected", "attach_refusal": attachwatch.RefusalPollKeyUnknown})
					return
				}
				_ = json.NewEncoder(w).Encode(attachwatch.View{State: "pending"})
			}))
			defer server.Close()
			c := client.New(server.URL, "fixture")
			c.HTTP = server.Client()
			transport, err := pairedAttachTransport(t.Context(), c, func(context.Context) (map[string]any, error) {
				return map[string]any{"operation": "register", "device_proof": proof, "poll_key": pollKey, "attach_protocol": attachwatch.Protocol, "local_consent_proof_version": attachwatch.LocalConsentProofVersion, "local_auth_capability": attachwatch.LocalAuthUnsupported}, nil
			}, pollKey, &attachRecoveryClock{})
			if err != nil {
				t.Fatal("startup registration failed")
			}
			known = false
			out, err := transport.Exchange(t.Context(), attachwatch.DeviceRequest{Operation: "request"})
			if registrations != 2 {
				t.Fatal("missing or repeated recovery registration")
			}
			if ack == "registered" {
				if err != nil || out.State != "pending" || exchanges != 2 {
					t.Fatal("recovery failed to replay once")
				}
			} else {
				if err == nil || exchanges != 1 {
					t.Fatal("invalid registration acknowledgement replayed request")
				}
				var local *agentd.AttachLocalError
				var status *client.StatusError
				if ack == "forbidden" {
					if !errors.As(err, &status) || status.AttachRefusal != attachwatch.RefusalPairing {
						t.Fatal("lost pairing refusal")
					}
				} else if !errors.As(err, &local) || local.Code != "attach_version_mismatch" {
					t.Fatal("lost version refusal")
				}
			}
		})
	}
}

func TestAttachExchangeFailurePreservesExistingMarker(t *testing.T) {
	original := attachExchangeFailure(errors.New("fixture exchange failure"))
	wrapped := fmt.Errorf("registration failed: %w", original)
	for _, err := range []error{original, wrapped} {
		if got := attachExchangeFailure(err); got != err || !errors.Is(got, agentd.ErrAttachExchange) {
			t.Fatal("existing exchange failure was wrapped again")
		}
	}
}

func TestAttachRecoveryGermanHintNeedsOnlyAnotherAttempt(t *testing.T) {
	err := attachLocalizedFailure(&agentd.AttachLocalError{Code: "attach_poll_key_unknown"}, "de")
	if !strings.Contains(err.Error(), "in wenigen Sekunden") || !strings.Contains(err.Error(), "automatisch") || strings.Contains(err.Error(), "Starte agentd neu") {
		t.Fatal("lost retry guidance or recommended a daemon restart")
	}
}

func TestPairedAttachRecoveryRefreshesRegistrationAuthority(t *testing.T) {
	for _, cause := range []string{"changed proof", "draining", "cleaned", "mismatched", "unreadable"} {
		t.Run(cause, func(t *testing.T) {
			proof := strings.Repeat("b", 64)
			registrations, exchanges := 0, 0
			var registeredPollKey string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in attachwatch.DeviceRequest
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Error("invalid fixture request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if in.Operation == "register" {
					registrations++
					if registrations == 1 {
						registeredPollKey = in.PollKey
					}
					if len(in.PollKey) != 64 || in.PollKey != registeredPollKey || in.ComputerID != "22222222-2222-4222-8222-222222222222" || in.AttachProtocol != attachwatch.Protocol || in.LocalConsentProofVersion != attachwatch.LocalConsentProofVersion {
						t.Error("production registration lost pinned computer, poll key or protocol")
					}
					if in.DeviceProof != proof {
						t.Error("registration reused the old lifecycle proof")
					}
					_ = json.NewEncoder(w).Encode(attachwatch.View{State: "registered", LocalConsentProofVersion: attachwatch.LocalConsentProofVersion})
					return
				}
				exchanges++
				if in.PollKey != registeredPollKey || in.DeviceProof != "" {
					t.Error("production exchange lost poll authority or sent a lifecycle proof")
				}
				if exchanges == 1 {
					w.WriteHeader(403)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "daemon poll key rejected", "attach_refusal": attachwatch.RefusalPollKeyUnknown})
					return
				}
				_ = json.NewEncoder(w).Encode(attachwatch.View{State: "pending"})
			}))
			defer server.Close()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			config := agentsetup.RuntimeConfig{Origin: server.URL, TenantID: "11111111-1111-4111-8111-111111111111", ComputerID: "22222222-2222-4222-8222-222222222222", PrincipalID: "33333333-3333-4333-8333-333333333333", Workspace: root}
			snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": "44444444-4444-4444-8444-444444444444", "computer_name": "fixture", "workspace_path": root}, "view": agentsetup.View{TenantID: config.TenantID, ComputerID: config.ComputerID, PrincipalID: config.PrincipalID}, "device_secret": proof, "runtime_secret": proof, "lifecycle_secret": proof}
			save := func(createOnly bool) {
				store, err := agentsetup.OpenStore(root, false)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				raw, err := json.Marshal(snapshot)
				if err != nil || store.Write("pairing.json", raw, createOnly) != nil {
					t.Fatal("fixture pairing not saved")
				}
			}
			save(true)
			c := client.New(server.URL, "fixture")
			c.HTTP = server.Client()
			cfg, err := pairedAttachConfig(root, config, &agentd.Remote{Client: c}, &attachRecoveryClock{})
			if err != nil {
				t.Fatal("startup registration failed")
			}
			switch cause {
			case "changed proof":
				proof = strings.Repeat("c", 64)
				snapshot["lifecycle_secret"] = proof
			case "draining":
				snapshot["disconnect_all"] = true
			case "cleaned":
				snapshot["computer_cleaned"] = true
			case "mismatched":
				snapshot["origin"] = "https://other.invalid"
			case "unreadable":
				snapshot = map[string]any{"schema": "invalid"}
			}
			save(false)
			out, err := cfg.Exchange(t.Context(), attachwatch.DeviceRequest{Operation: "request"})
			if cause == "changed proof" {
				if err != nil || out.State != "pending" || registrations != 2 || exchanges != 2 {
					t.Fatal("changed proof did not recover")
				}
				return
			}
			if err == nil || errors.Is(err, agentd.ErrAttachExchange) || registrations != 1 || exchanges != 1 {
				t.Fatal("local pairing change sent registration or replay")
			}
			want := map[string]error{"draining": agentsetup.ErrAttachComputerDraining, "cleaned": agentsetup.ErrAttachPairingCleaned, "mismatched": agentsetup.ErrAttachConfigMismatch}[cause]
			if want != nil && !errors.Is(err, want) {
				t.Fatal("local pairing refusal lost its cause")
			}
		})
	}
}
