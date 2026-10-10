//go:build darwin || linux

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

func attachFixtureFile(t *testing.T, initial string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "transcript.jsonl")
	if err = os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func appendAttach(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
func TestAttachTailPinnedNewRecordsAndRedaction(t *testing.T) {
	path := attachFixtureFile(t, "old conversation\npartial historical")
	tail, err := openAttachTail(path, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer tail.close()
	appendAttach(t, path, " suffix\nnew turn\nPASSWORD=fixture-only\n-----BEGIN PRIVATE KEY-----\nfixture-key-line\n-----END PRIVATE KEY-----\n\x1b[31mescape\n")
	text, err := tail.next()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "historical") || strings.Contains(text, "old conversation") || strings.Contains(text, "fixture") || strings.Contains(text, "escape") || !strings.Contains(text, "new turn") {
		t.Fatal("history, redaction or escape filter failed")
	}
	appendAttach(t, path, `{"type":"response_item","payload":{"type":"message","content":[{"type":"output_text","text":"Hello\\nworld"}]}}`+"\n")
	text, err = tail.next()
	if err != nil || !strings.Contains(text, "Hello") {
		t.Fatal("known JSONL message was not mirrored")
	}
	appendAttach(t, path, `{"type":"assistant","message":{"content":[{"type":"text","text":"\u0061pi_key: fixture-secret"}]}}`+"\n")
	text, err = tail.next()
	if err != nil || strings.Contains(text, "fixture-secret") {
		t.Fatal("escaped JSON bypassed redaction")
	}
	appendAttach(t, path, strings.Repeat("x", attachwatch.MaxText+100)+"\nafter oversized\n")
	text, err = tail.next()
	if err != nil || strings.Contains(text, strings.Repeat("x", 64)) || !strings.Contains(text, "after oversized") {
		t.Fatal("oversized record not bounded")
	}
	appendAttach(t, path, "not complete")
	text, err = tail.next()
	if err != nil || text != "" {
		t.Fatal("partial record leaked")
	}
	appendAttach(t, path, " yet\n")
	text, err = tail.next()
	if err != nil || text != "not complete yet\n" {
		t.Fatal("partial record not assembled")
	}
	if err = os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = tail.next(); err == nil {
		t.Fatal("truncation rewound transcript")
	}
}
func TestAttachTailRefusesLinksReplacementAndNonregular(t *testing.T) {
	path := attachFixtureFile(t, "")
	root := filepath.Dir(path)
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if tail, err := openAttachTail(link, os.Getuid()); err == nil {
		tail.close()
		t.Fatal("symlink accepted")
	}
	ancestor := filepath.Join(root, "alias")
	if err := os.Symlink(root, ancestor); err != nil {
		t.Fatal(err)
	}
	if tail, err := openAttachTail(filepath.Join(ancestor, "transcript.jsonl"), os.Getuid()); err == nil {
		tail.close()
		t.Fatal("ancestor symlink accepted")
	}
	if tail, err := openAttachTail(root, os.Getuid()); err == nil {
		tail.close()
		t.Fatal("directory accepted")
	}
	tail, err := openAttachTail(path, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer tail.close()
	if err = os.Link(path, filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err = tail.next(); err == nil {
		t.Fatal("new hardlink accepted")
	}
	if other, err := openAttachTail(path, os.Getuid()); err == nil {
		other.close()
		t.Fatal("hardlink opened")
	}
	replacement := attachFixtureFile(t, "")
	other, err := openAttachTail(replacement, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	if err = os.Rename(replacement, replacement+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(replacement, []byte("replacement data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = other.next(); err == nil {
		t.Fatal("replacement reopened")
	}
}

// Consent, lease and ancestry fixtures model a verified Codex CLI image.
// Unsigned macOS images are covered by the identity refusal regressions.
func attachCodexSignatureFixture(context.Context, string) (attachSignature, error) {
	return attachSignature{TeamID: attachVendorTeam(Codex), Identifier: attachVendorIdentifier(Codex), Signed: true}, nil
}

func TestAttachLocalConsentPeerPollAndNoReplay(t *testing.T) {
	t.Setenv("AEON_URL", "https://unpaired.invalid")
	path := attachFixtureFile(t, "old turns\n")
	root := filepath.Dir(path)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	peer := attachObservation{Process: attachwatch.Process{PID: 3000, UID: os.Getuid(), Started: "cli-start", Executable: exe, CWD: root}, Parent: 2000, Session: 2000, TTY: true}
	leader := attachObservation{Process: attachwatch.Process{PID: 2000, UID: os.Getuid(), Started: "terminal-start", Executable: exe, CWD: root}, Parent: 1, Session: 2000, TTY: true}
	target := attachObservation{Process: attachwatch.Process{PID: 4000, UID: os.Getuid(), Started: "agent-start", Executable: exe, CWD: root}, Parent: 1, TTY: true}
	var sent []attachwatch.DeviceRequest
	failNetwork := false
	serverExpiry := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	exchange := func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
		sent = append(sent, in)
		if failNetwork {
			return attachwatch.View{}, ErrAttachExchange
		}
		state := "pending"
		var until *time.Time
		if in.Operation == "poll" {
			state = "active"
			v := time.Now().Add(attachwatch.Lease)
			until = &v
		}
		return attachwatch.View{ConsentMode: attachwatch.ConsentAeon, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Snapshot.Digest(), attachwatch.ConsentAeon), RequestID: in.RequestID, Digest: in.Snapshot.Digest(), Snapshot: in.Snapshot, State: state, UserCode: "123456789", LeaseUntil: until, ExpiresAt: serverExpiry}, nil
	}
	m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture", Workspace: root, Executables: map[string]string{"codex": exe}, Exchange: exchange})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(t.Context())
	m.signature = attachCodexSignatureFixture
	m.observe = func(pid int) (attachObservation, error) {
		if pid == target.PID {
			return target, nil
		}
		if pid == peer.PID {
			return peer, nil
		}
		if pid == leader.PID {
			return leader, nil
		}
		return attachObservation{}, errors.New("unknown fixture PID")
	}
	m.ancestry = m.observe
	req := AttachLocalRequest{Operation: "preview", PID: target.PID, Harness: "codex", ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", Transcript: path}
	v, err := m.handle(t.Context(), peer, req)
	if err != nil {
		t.Fatal(err)
	}
	if v.Origin != "https://paired.test" {
		t.Fatal("environment changed paired origin")
	}
	if len(sent) != 0 {
		t.Fatal("preview joined without local consent")
	}
	badPeer := peer
	badPeer.Started = "reused-cli-pid"
	if _, err = m.handle(t.Context(), badPeer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest}); err == nil {
		t.Fatal("other peer confirmed")
	}
	if v.ExpiresAt != nil {
		t.Fatal("a preview has no server expiry yet")
	}
	v, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
	if err != nil {
		t.Fatal(err)
	}
	// The helper tells the person how long the code lives from the server's expiry.
	if v.ExpiresAt == nil || !v.ExpiresAt.Equal(serverExpiry) || v.Code != "123456789" {
		t.Fatalf("confirmed view lacks the server expiry or code: %+v", v)
	}
	appendAttach(t, path, "before browser approval\n")
	poll := func() (AttachLocalView, error) {
		m.sessions[v.ID].touched = time.Now().Add(-2 * time.Second)
		return m.handle(t.Context(), peer, AttachLocalRequest{Operation: "poll", ID: v.ID, Digest: v.Digest})
	}
	if _, err = poll(); err != nil {
		t.Fatal(err)
	}
	appendAttach(t, path, "live text\n")
	if _, err = poll(); err != nil {
		t.Fatal(err)
	}
	if sent[len(sent)-1].Text != "live text\n" {
		t.Fatal("history leaked or live turn lost")
	}
	target.Started = "reused-agent-pid"
	if _, err = poll(); err == nil {
		t.Fatal("PID reuse retained lease")
	}
	if sent[len(sent)-1].Operation != "detach" {
		t.Fatal("PID reuse did not revoke")
	}
	if len(m.sessions) != 0 {
		t.Fatal("ended watch retained local state")
	}
	target.Started = "agent-start"
	v, err = m.handle(t.Context(), peer, req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
	if err != nil {
		t.Fatal(err)
	}
	failNetwork = true
	if _, err = poll(); err == nil || len(m.sessions) != 0 {
		t.Fatal("offline watch retained authority")
	}
	// No project hook or journal was ever written.
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("attach wrote local hooks or journals")
	}
}

// Only recognized owner causes get a fixed next step. Either way the refused
// request leaves no attach state or transcript reader behind.
func TestAttachRefusalNamesOwnerCausesAndClearsLocalState(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"full list":         {&client.StatusError{Status: 429, Message: attachwatch.LiveLimitMessage}, "attach requests already wait for approval in Aeon"},
		"other limit":       {&client.StatusError{Status: 429, Message: "computer attach limit reached"}, "Stop an attached helper"},
		"other status":      {&client.StatusError{Status: 403, Message: attachwatch.LiveLimitMessage}, "approve or decline one"},
		"offline":           {errors.Join(ErrAttachExchange, errors.New("private transport failure")), "Check the connection"},
		"registration lost": {&client.StatusError{Status: 403, Message: "daemon poll key rejected"}, "restart agentd"},
		"draining":          {&client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalDraining}, "let owned work finish"},
		"enrollment":        {&client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalEnrollment}, "aeon-agentd add-harness"},
		"version":           {&client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalVersion}, "Update Aeon and paimos-agentd"},
		"pairing":           {&client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalPairing}, "pair this computer again"},
		"revoked bearer":    {&client.StatusError{Status: 401, Message: "unauthorized"}, "pairing no longer authenticates"},
		"ticket":            {&client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalTicket}, "Check the ticket belongs to the selected project"},
		"ticket access":     {&client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalTicket}, "owner has project access"},
		"expired":           {&client.StatusError{Status: 410, AttachRefusal: attachwatch.RefusalExpired}, "approve the new code"},
		"wrong status":      {&client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalVersion}, "inspect the attach request in the server logs"},
		"unrecognized":      {&client.StatusError{Status: 409, AttachRefusal: "private arbitrary server payload"}, "inspect the attach request in the server logs"},
	} {
		t.Run(name, func(t *testing.T) {
			path := attachFixtureFile(t, "old turns\n")
			root := filepath.Dir(path)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			exe, err = filepath.EvalSymlinks(exe)
			if err != nil {
				t.Fatal(err)
			}
			peer := attachObservation{Process: attachwatch.Process{PID: 3000, UID: os.Getuid(), Started: "cli-start", Executable: exe, CWD: root}, Parent: 2000, Session: 2000, TTY: true}
			leader := attachObservation{Process: attachwatch.Process{PID: 2000, UID: os.Getuid(), Started: "terminal-start", Executable: exe, CWD: root}, Parent: 1, Session: 2000, TTY: true}
			target := attachObservation{Process: attachwatch.Process{PID: 4000, UID: os.Getuid(), Started: "agent-start", Executable: exe, CWD: root}, Parent: 1, TTY: true}
			m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture", Workspace: root, Executables: map[string]string{"codex": exe}, Exchange: func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
				return attachwatch.View{}, tc.err
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close(t.Context())
			m.signature = attachCodexSignatureFixture
			m.observe = func(pid int) (attachObservation, error) {
				switch pid {
				case target.PID:
					return target, nil
				case peer.PID:
					return peer, nil
				case leader.PID:
					return leader, nil
				}
				return attachObservation{}, errors.New("unknown fixture PID")
			}
			m.ancestry = m.observe
			v, err := m.handle(t.Context(), peer, AttachLocalRequest{Operation: "preview", PID: target.PID, Harness: "codex", ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", Transcript: path})
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if len(m.sessions) != 0 {
				t.Fatal("a refused request kept local state")
			}
			// What the helper prints must pass the attach diagnostic filter unchanged.
			w := httptest.NewRecorder()
			http.Error(w, err.Error(), 409)
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Fatal("the local reply lost the message")
			}
		})
	}
}

func TestDisabledAttachKeepsRefusalBehindLocalAuthentication(t *testing.T) {
	for _, cause := range []error{
		&client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalVersion},
		&client.StatusError{Status: 400, Message: "invalid attach request"},
		&client.StatusError{Status: 409, Message: "update agentd to attach protocol 2"},
		&AttachLocalError{Code: "attach_version_mismatch", Hint: "Update the server and restart agentd."},
	} {
		m := DisabledAttachManager(cause)
		_, err := m.handle(t.Context(), attachObservation{}, AttachLocalRequest{Operation: "preview"})
		var detail *AttachLocalError
		if !errors.As(err, &detail) || detail.Code != "attach_version_mismatch" || len(m.sessions) != 0 {
			t.Fatal("disabled manager lost the fixed repair or created attach state")
		}
		for _, bearer := range []string{"", "wrong", "fixture-local-token"} {
			r := httptest.NewRequest("POST", "/v1/attach", strings.NewReader(`{"operation":"preview"}`))
			if bearer != "" {
				r.Header.Set("Authorization", "Bearer "+bearer)
			}
			w := httptest.NewRecorder()
			m.serve(w, r, "fixture-local-token")
			if w.Code != 403 || strings.Contains(w.Body.String(), "attach_version_mismatch") || strings.Contains(w.Body.String(), "Update") {
				t.Fatal("disabled cause leaked before bearer and kernel-peer checks")
			}
		}
		m.Close(t.Context())
	}
}
func TestAttachInjectedJoinAndClientOptionsFailClosed(t *testing.T) {
	peer := attachObservation{Process: attachwatch.Process{PID: 3, UID: 10}, Parent: 2}
	agent := attachObservation{Process: attachwatch.Process{PID: 2, UID: 10}, Parent: 1}
	observe := func(pid int) (attachObservation, error) {
		if pid == 3 {
			return peer, nil
		}
		return agent, nil
	}
	if independentAttachPeer(peer, agent, observe) {
		t.Fatal("injected child joined its parent agent")
	}
	var r AttachLocalRequest
	decoder := json.NewDecoder(strings.NewReader(`{"operation":"confirm","url":"https://other.test","device_proof":"untrusted"}`))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) == nil {
		t.Fatal("request origin/credentials accepted")
	}
}

func TestAttachBearerAloneCannotJoin(t *testing.T) {
	m := &AttachManager{}
	r := httptest.NewRequest("POST", "/v1/attach", strings.NewReader(`{"operation":"confirm"}`))
	r.Header.Set("Authorization", "Bearer fixture-local-token")
	w := httptest.NewRecorder()
	m.serve(w, r, "fixture-local-token")
	if w.Code != 403 {
		t.Fatal("readable same-user bearer bypassed kernel peer checks")
	}
}

func TestAttachRootAncestorIsAcceptedWithoutPaths(t *testing.T) {
	uid := 10
	proc := func(pid, owner, parent, session int) attachObservation {
		return attachObservation{Process: attachwatch.Process{PID: pid, UID: owner, Started: "start"}, Parent: parent, Session: session, TTY: true}
	}
	peer, target := proc(30, uid, 20, 10), proc(40, uid, 50, 40)
	graph := map[int]attachObservation{
		30: peer, 20: proc(20, uid, 10, 10), 10: proc(10, 0, 1, 10),
		40: target, 50: proc(50, 0, 1, 40),
	}
	observe := func(pid int) (attachObservation, error) {
		p, ok := graph[pid]
		if !ok {
			return attachObservation{}, errors.New("missing fixture process")
		}
		return p, nil
	}
	if !independentAttachPeer(peer, target, observe) {
		t.Fatal("root sshd or sudo ancestor refused")
	}
	foreign := graph[50]
	foreign.UID = uid + 1
	graph[50] = foreign
	if independentAttachPeer(peer, target, observe) {
		t.Fatal("foreign ancestor accepted")
	}
}

func TestAttachSessionAncestryRejectsPTYBypasses(t *testing.T) {
	for _, attack := range []string{"independent terminal", "injected child", "setsid helper", "double fork dead leader", "target session", "descendant session leader", "lost tty", "ancestry cycle"} {
		t.Run(attack, func(t *testing.T) {
			peer := attachObservation{Process: attachwatch.Process{PID: 30, UID: 10, Started: "helper"}, Parent: 20, Session: 20, TTY: true}
			target := attachObservation{Process: attachwatch.Process{PID: 40, UID: 10, Started: "target"}, Parent: 1, Session: 40, TTY: true}
			leader := attachObservation{Process: attachwatch.Process{PID: 20, UID: 10, Started: "leader"}, Parent: 1, Session: 20, TTY: true}
			switch attack {
			case "injected child":
				peer.Parent = target.PID
			case "setsid helper":
				peer.Parent, peer.Session = 1, peer.PID
			case "double fork dead leader":
				peer.Parent, peer.Session = 1, 99
			case "target session":
				peer.Parent, peer.Session = 1, target.PID
			case "descendant session leader":
				peer.Parent, leader.Parent = 1, target.PID
			case "lost tty":
				peer.TTY = false
			case "ancestry cycle":
				leader.Parent = peer.PID
			}
			observe := func(pid int) (attachObservation, error) {
				for _, p := range []attachObservation{peer, target, leader} {
					if p.PID == pid {
						return p, nil
					}
				}
				return attachObservation{}, errors.New("dead fixture process")
			}
			if got := independentAttachPeer(peer, target, observe); got != (attack == "independent terminal") {
				t.Fatal("helper ancestry/session decision incorrect")
			}
		})
	}
}

func TestAttachRechecksAncestryAndSessionOnConfirmAndEveryPoll(t *testing.T) {
	for _, stage := range []string{"confirm", "activation", "upload"} {
		for _, attack := range []string{"parent becomes target", "setsid", "dead leader", "leader becomes target child"} {
			t.Run(stage+"/"+attack, func(t *testing.T) {
				path := attachFixtureFile(t, "")
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
				leader := attachObservation{Process: attachwatch.Process{PID: 20, UID: os.Getuid(), Started: "leader"}, Parent: 1, Session: 20, TTY: true}
				target := attachObservation{Process: attachwatch.Process{PID: 40, UID: os.Getuid(), Started: "target", Executable: exe, CWD: root}, Parent: 1}
				dead := false
				var sent []attachwatch.DeviceRequest
				m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture", Workspace: root, Executables: map[string]string{"codex": exe}, Exchange: func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
					sent = append(sent, in)
					state := "pending"
					if in.Operation == "poll" {
						state = "active"
					}
					return attachwatch.View{ConsentMode: attachwatch.ConsentAeon, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Snapshot.Digest(), attachwatch.ConsentAeon), RequestID: in.RequestID, Digest: in.Digest, Snapshot: in.Snapshot, State: state}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close(t.Context())
				m.signature = attachCodexSignatureFixture
				m.observe = func(pid int) (attachObservation, error) {
					for _, p := range []attachObservation{peer, target, leader} {
						if pid == p.PID && !(dead && pid == leader.PID) {
							return p, nil
						}
					}
					return attachObservation{}, errors.New("dead fixture process")
				}
				m.ancestry = m.observe
				v, err := m.handle(t.Context(), peer, AttachLocalRequest{Operation: "preview", PID: target.PID, Harness: "codex", ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", Transcript: path})
				if err != nil {
					t.Fatal(err)
				}
				op := "confirm"
				if stage != "confirm" {
					if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: op, ID: v.ID, Digest: v.Digest}); err != nil {
						t.Fatal(err)
					}
					op = "poll"
				}
				if stage == "upload" {
					m.sessions[v.ID].touched = time.Now().Add(-2 * time.Second)
					if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: op, ID: v.ID, Digest: v.Digest}); err != nil {
						t.Fatal(err)
					}
					appendAttach(t, path, "must never upload\n")
				}
				sent = nil
				switch attack {
				case "parent becomes target":
					peer.Parent = target.PID
				case "setsid":
					peer.Parent, peer.Session = 1, peer.PID
				case "dead leader":
					peer.Parent, dead = 1, true
				case "leader becomes target child":
					peer.Parent, leader.Parent = 1, target.PID
				}
				signatureCalls := 0
				m.signature = func(ctx context.Context, pid string) (attachSignature, error) {
					signatureCalls++
					return attachCodexSignatureFixture(ctx, pid)
				}
				m.sessions[v.ID].touched = time.Now().Add(-2 * time.Second)
				if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: op, ID: v.ID, Digest: v.Digest}); err == nil || len(m.sessions) != 0 || signatureCalls != 0 {
					t.Fatal("changed helper ancestry retained watch or reached signature verification")
				}
				for _, request := range sent {
					if request.Operation != "detach" || request.Text != "" {
						t.Fatal("unsafe helper requested or fed watch")
					}
				}
			})
		}
	}
}

func TestAttachRedactionCredentialVariants(t *testing.T) {
	for _, line := range []string{
		"apiKey=fixture", "accessToken: fixture", "refreshToken=fixture", "clientSecret: fixture", "privateKey=fixture",
		"secret=fixture", "token = fixture", `"token": "fixture"`, `setting {'secret': 'fixture'}`,
		"postgres://reader:fixture@db.test/app", "https://reader@host.test/path", "//reader:fixture@host.test/", "ssh://reader:fixture@host.test",
	} {
		t.Run(line, func(t *testing.T) {
			for _, structured := range []bool{false, true} {
				raw := []byte(line)
				if structured {
					raw, _ = json.Marshal(map[string]any{"type": "assistant", "message": map[string]string{"content": line}})
				}
				var r attachRedactor
				if text, ok := r.record(raw); !ok || text != "[redacted]" {
					t.Fatal("credential variant not redacted")
				}
			}
		})
	}
	for _, line := range []string{"api\u0301Key=fixture", "to\u034fen=fixture", strings.Repeat("abcdefgh\u0301", 8), "innocent e\u0301 text"} {
		var r attachRedactor
		if text, ok := r.record([]byte(line)); ok || text != "" {
			t.Fatal("nonspacing mark line was displayed")
		}
		raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]string{"content": line}})
		if text, ok := r.record(raw); ok || text != "" {
			t.Fatal("JSON nonspacing mark line was displayed")
		}
	}
	var r attachRedactor
	if _, ok := r.line([]byte("-----BEGIN PRIVATE KEY-----\u0301")); ok {
		t.Fatal("nonspacing mark header displayed")
	}
	if text, ok := r.line([]byte("short fixture key body")); !ok || text != "[redacted]" {
		t.Fatal("dropped header lost private-key redaction state")
	}
}
