// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestAttachModesIdentityAndLease(t *testing.T) {
	for _, statusOnly := range []bool{false, true} {
		for _, end := range []string{"pid reuse", "executable changed", "cwd changed", "injected join", "confirmed exit", "permission error", "offline", "lease expired", "revoked", "missing consent"} {
			t.Run(fmt.Sprintf("status-only=%t/%s", statusOnly, end), func(t *testing.T) {
				t.Setenv("AEON_URL", "https://unpaired.invalid")
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				exe, err = filepath.EvalSymlinks(exe)
				if err != nil {
					t.Fatal(err)
				}
				peer := attachObservation{Process: attachwatch.Process{PID: 30, UID: os.Getuid(), Started: "helper", Executable: exe, CWD: root}, Parent: 20, Session: 20, TTY: true}
				leader := attachObservation{Process: attachwatch.Process{PID: 20, UID: os.Getuid(), Started: "shell", Executable: exe, CWD: root}, Parent: 1, Session: 20, TTY: true}
				target := attachObservation{Process: attachwatch.Process{PID: 40, UID: os.Getuid(), Started: "agent", Executable: exe, CWD: root}, Parent: 1}
				var sent []attachwatch.DeviceRequest
				var observeErr error
				offline := false
				revoked, expired, missingConsent := false, false, false
				m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture", Workspace: root, Executables: map[string]string{"codex": exe}, Exchange: func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
					sent = append(sent, in)
					if offline {
						return attachwatch.View{}, errors.New("offline")
					}
					state := "pending"
					if in.Operation == "poll" {
						state = "active"
						if revoked {
							state = "detached"
						}
						if expired {
							state = "unreachable"
						}
					}
					if missingConsent {
						return attachwatch.View{RequestID: in.RequestID, Digest: in.Digest, Snapshot: in.Snapshot, State: state}, nil
					}
					until := time.Now().Add(attachwatch.Lease)
					return attachwatch.View{ConsentMode: attachwatch.ConsentAeon, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Snapshot.Digest(), attachwatch.ConsentAeon), RequestID: in.RequestID, Digest: in.Digest, Snapshot: in.Snapshot, State: state, LeaseUntil: &until}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close(t.Context())
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, nil }
				m.observe = func(pid int) (attachObservation, error) {
					if pid == target.PID {
						return target, observeErr
					}
					for _, p := range []attachObservation{peer, leader} {
						if p.PID == pid {
							return p, nil
						}
					}
					return attachObservation{}, errors.New("unknown PID")
				}
				req := AttachLocalRequest{Operation: "preview", PID: target.PID, Harness: "codex", ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", StatusOnly: statusOnly}
				if !statusOnly {
					req.Transcript = filepath.Join(root, "conversation.jsonl")
					if err := os.WriteFile(req.Transcript, []byte("history\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Injected commands and folders outside the exact root cannot even preview.
				peer.Parent = target.PID
				if _, err = m.handle(t.Context(), peer, req); err == nil {
					t.Fatal("injected join accepted")
				}
				peer.Parent = leader.PID
				target.CWD = root + "-other"
				if _, err = m.handle(t.Context(), peer, req); err == nil {
					t.Fatal("sibling cwd allowed")
				}
				target.CWD = root
				invalid := req
				invalid.StatusOnly, invalid.Transcript = false, ""
				if _, err := m.handle(t.Context(), peer, invalid); err == nil {
					t.Fatal("missing transcript silently chose status-only")
				}
				invalid.StatusOnly, invalid.Transcript = true, "/never/open/this/transcript"
				if _, err := m.handle(t.Context(), peer, invalid); err == nil {
					t.Fatal("status-only accepted a transcript")
				}
				v, err := m.handle(t.Context(), peer, req)
				if err != nil {
					t.Fatal(err)
				}
				if v.Origin != "https://paired.test" || (v.Snapshot.Mode == attachwatch.ModeLease) != statusOnly || (m.sessions[v.ID].tail == nil) != statusOnly || len(sent) != 0 {
					t.Fatal("preview changed chosen mode or contacted server")
				}
				_, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
				if err != nil {
					t.Fatal(err)
				}
				poll := func() (AttachLocalView, error) {
					m.sessions[v.ID].touched = time.Now().Add(-2 * time.Second)
					return m.handle(t.Context(), peer, AttachLocalRequest{Operation: "poll", ID: v.ID, Digest: v.Digest})
				}
				if _, err = poll(); err != nil {
					t.Fatal(err)
				}
				if _, err = poll(); err != nil {
					t.Fatal(err)
				}
				switch end {
				case "pid reuse":
					target.Started = "reused"
				case "executable changed":
					target.Executable = "/other/executable"
				case "cwd changed":
					target.CWD = root + "/other"
				case "injected join":
					peer.Parent = target.PID
				case "confirmed exit":
					observeErr = errAttachExited
				case "permission error":
					observeErr = os.ErrPermission
				case "offline":
					offline = true
				case "revoked":
					revoked = true
				case "missing consent":
					missingConsent = true
				case "lease expired":
					// Release 12 makes the server's clock authoritative for expiry.
					expired = true
				}
				ended, err := poll()
				if end == "confirmed exit" {
					if err != nil || ended.State != "confirmed_exited" || sent[len(sent)-1].Operation != "exited" {
						t.Fatal("kernel exit not confirmed")
					}
				} else if err == nil {
					t.Fatal("unsafe identity retained")
				}
				if len(m.sessions) != 0 {
					t.Fatal("ended attach retained local authority")
				}
				for _, r := range sent {
					if statusOnly && (r.Text != "" || r.Snapshot.Transcript != "" || r.Snapshot.FileID != "" || r.Snapshot.Mode != attachwatch.ModeLease) {
						t.Fatal("metadata attach acquired transcript")
					}
					if end != "confirmed exit" && r.Operation == "exited" {
						t.Fatal("uncertainty reported as exit")
					}
				}
				entries, err := os.ReadDir(root)
				expectedFiles := 0
				if !statusOnly {
					expectedFiles = 1
				}
				if err != nil || len(entries) != expectedFiles {
					t.Fatal("metadata attach wrote files")
				}
			})
		}
	}
}
