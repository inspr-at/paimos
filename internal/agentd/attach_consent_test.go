//go:build darwin || linux

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/workorders"
)

type fakeLocalAuth struct {
	called chan string
	answer chan error
}

func (f *fakeLocalAuth) Confirm(ctx context.Context, reason string) error {
	f.called <- reason
	select {
	case err := <-f.answer:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestAttachStrictConsentActivation(t *testing.T) {
	for _, outcome := range []string{"confirm", "deny", "unavailable", "revoke", "changed identity", "changed mode", "premature activation", "policy changed while pending"} {
		t.Run(outcome, func(t *testing.T) {
			path := attachFixtureFile(t, "history\n")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			exe, err = filepath.EvalSymlinks(exe)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(path)
			peer := attachObservation{Process: attachwatch.Process{PID: 30, UID: os.Getuid(), Started: "helper", Executable: exe, CWD: root}, Parent: 20, Session: 20, TTY: true}
			leader := attachObservation{Process: attachwatch.Process{PID: 20, UID: os.Getuid()}, Parent: 1, Session: 20, TTY: true}
			target := attachObservation{Process: attachwatch.Process{PID: 40, UID: os.Getuid(), Started: "target", Executable: exe, CWD: root}, Parent: 1}
			auth := &fakeLocalAuth{called: make(chan string, 1), answer: make(chan error, 1)}
			var sent []attachwatch.DeviceRequest
			mode := attachwatch.ConsentLocalAuth
			m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture Mac", Workspace: root, Executables: map[string]string{"codex": exe}, LocalAuth: auth, Exchange: func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
				sent = append(sent, in)
				state := "pending"
				responseMode := mode
				if outcome == "policy changed while pending" && in.Operation == "request" {
					responseMode = attachwatch.ConsentAeon
				}
				if in.Operation == "poll" && len(sent) == 2 && in.ConsentDigest != "" {
					t.Error("pending poll pinned a pre-approval policy")
				}
				if in.Operation == "poll" {
					state = "approved"
					if in.LocalConfirmed || outcome == "premature activation" {
						state = "active"
					}
				}
				return attachwatch.View{RequestID: in.RequestID, Digest: in.Digest, Snapshot: in.Snapshot, State: state, ConsentMode: responseMode, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Digest, responseMode)}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close(t.Context())
			m.observe = func(pid int) (attachObservation, error) {
				for _, v := range []attachObservation{peer, leader, target} {
					if v.PID == pid {
						return v, nil
					}
				}
				return attachObservation{}, errors.New("unknown PID")
			}
			v, err := m.handle(t.Context(), peer, AttachLocalRequest{Operation: "preview", PID: target.PID, Harness: "codex", ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", Transcript: path})
			if err != nil {
				t.Fatal(err)
			}
			v, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
			if err != nil {
				t.Fatal(err)
			}
			poll := func() (AttachLocalView, error) {
				m.sessions[v.ID].touched = time.Now().Add(-2 * time.Second)
				return m.handle(t.Context(), peer, AttachLocalRequest{Operation: "poll", ID: v.ID, Digest: v.Digest})
			}
			first, err := poll()
			if outcome == "premature activation" {
				if err == nil {
					t.Fatal("server bypass activated strict watch")
				}
				return
			}
			if err != nil || first.State != "approved" {
				t.Fatal("strict approval did not wait", err)
			}
			select {
			case reason := <-auth.called:
				if !strings.Contains(reason, "PID 40") || !strings.Contains(reason, "fixture Mac") {
					t.Fatal("reason omitted session/host")
				}
			case <-time.After(time.Second):
				t.Fatal("no local confirmation")
			}
			for i := 0; i < 3; i++ {
				pending, err := poll()
				if err != nil || pending.State != "approved" || sent[len(sent)-1].LocalConfirmed {
					t.Fatal("activated without local confirmation")
				}
			}
			if outcome == "revoke" {
				m.end(t.Context(), v.ID, m.sessions[v.ID])
				if len(m.sessions) != 0 {
					t.Fatal("revocation retained watch")
				}
				return
			}
			if outcome == "deny" || outcome == "unavailable" {
				auth.answer <- errors.New("local confirmation " + outcome)
			} else {
				auth.answer <- nil
			}
			deadline := time.Now().Add(time.Second)
			for len(m.sessions[v.ID].confirmation) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(m.sessions[v.ID].confirmation) == 0 {
				t.Fatal("fake confirmation did not finish")
			}
			if outcome == "changed identity" {
				target.Started = "reused PID"
			}
			if outcome == "changed mode" {
				mode = attachwatch.ConsentAeon
			}
			appendAttach(t, path, "before activation\n")
			next, err := poll()
			if outcome == "changed identity" || outcome == "changed mode" {
				if err == nil || len(m.sessions) != 0 {
					t.Fatal("changed binding kept authority")
				}
				return
			}
			if outcome != "confirm" && outcome != "policy changed while pending" {
				if err != nil || next.State != "detached" || !strings.Contains(next.Reason, outcome) || len(m.sessions) != 0 {
					t.Fatal("failure not explained and closed")
				}
				return
			}
			if err != nil || next.State != "active" || !sent[len(sent)-1].LocalConfirmed || sent[len(sent)-1].Text != "" {
				t.Fatal("confirmed activation failed", err)
			}
			appendAttach(t, path, "live only\n")
			if _, err = poll(); err != nil || sent[len(sent)-1].Text != "live only\n" {
				t.Fatal("uploaded pre-confirmation history")
			}
		})
	}
}
func TestLocalSocketCannotSupplyOSConfirmation(t *testing.T) {
	for _, field := range []string{`"local_confirmed":true`, `"consent_mode":"aeon"`, `"consent_digest":"pretend"`} {
		// Use the same strict request decoder as the local HTTP entry point.
		r := httptest.NewRequest("POST", "/v1/attach", strings.NewReader(`{"operation":"poll",`+field+`}`))
		var request AttachLocalRequest
		if workorders.Decode(r, &request) == nil {
			t.Fatal("local caller supplied confirmation authority")
		}
	}
}
