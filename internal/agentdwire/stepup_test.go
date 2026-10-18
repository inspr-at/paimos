// SPDX-License-Identifier: AGPL-3.0-only
package agentdwire

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/stepup"
)

func TestStepUpWireChecksOriginAndSendsOnlyID(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const computer = "22222222-2222-4222-8222-222222222222"
	for _, mode := range []string{"approve", "deny", "wrong instance", "legacy pairing", "wrong reply"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "step-wire-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(root, "agentd.sock")
			if err = os.WriteFile(socket+".token", []byte("12345678901234567890123456789012"), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			posts := 0
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/step-up" || r.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" {
					t.Error("unauthenticated wire request")
					w.WriteHeader(403)
					return
				}
				if r.Method == "GET" {
					origin := "https://paired.test"
					if mode == "wrong instance" {
						origin = "https://other.test"
					}
					_ = json.NewEncoder(w).Encode(stepup.Info{Origin: origin, ComputerID: computer, Ready: mode != "legacy pairing"})
					return
				}
				posts++
				var in map[string]any
				if json.NewDecoder(r.Body).Decode(&in) != nil || len(in) != 1 || in["challenge_id"] != id {
					t.Error("agent supplied more than the ID")
				}
				if mode == "deny" {
					http.Error(w, "Touch ID denied", 409)
					return
				}
				outID := id
				if mode == "wrong reply" {
					outID = computer
				}
				_ = json.NewEncoder(w).Encode(stepup.Proof{ChallengeID: outID, Signature: "MAYCAQECAQE="})
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			defer listener.Close()
			c := Client{Socket: socket, TokenFile: socket + ".token"}
			signature, err := c.ConfirmStepUp(t.Context(), "https://paired.test", id)
			if mode == "approve" {
				if err != nil || signature == "" || posts != 1 {
					t.Fatal("approval failed", err)
				}
			} else if err == nil || signature != "" {
				t.Fatal("refusal produced proof")
			}
			if (mode == "wrong instance" || mode == "legacy pairing") && posts != 0 {
				t.Fatal("wrong pairing prompted")
			}
		})
	}
}
