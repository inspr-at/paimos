//go:build darwin || linux

// SPDX-License-Identifier: AGPL-3.0-only
//
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
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
	peer := attachObservation{Process: attachwatch.Process{PID: 3000, UID: os.Getuid(), Started: "cli-start", Executable: exe, CWD: root}, Parent: 1, TTY: true}
	target := attachObservation{Process: attachwatch.Process{PID: 4000, UID: os.Getuid(), Started: "agent-start", Executable: exe, CWD: root}, Parent: 1, TTY: true}
	var sent []attachwatch.DeviceRequest
	failNetwork := false
	exchange := func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
		sent = append(sent, in)
		if failNetwork {
			return attachwatch.View{}, errors.New("offline")
		}
		state := "pending"
		var until *time.Time
		if in.Operation == "poll" {
			state = "active"
			v := time.Now().Add(attachwatch.Lease)
			until = &v
		}
		return attachwatch.View{RequestID: in.RequestID, Digest: in.Snapshot.Digest(), Snapshot: in.Snapshot, State: state, UserCode: "123456789", LeaseUntil: until}, nil
	}
	m, err := NewAttachManager(AttachConfig{Origin: "https://paired.test", ComputerID: "11111111-1111-4111-8111-111111111111", Host: "fixture", Workspace: root, Executables: map[string]string{"codex": exe}, Exchange: exchange})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(t.Context())
	m.observe = func(pid int) (attachObservation, error) {
		if pid == target.PID {
			return target, nil
		}
		if pid == peer.PID {
			return peer, nil
		}
		return attachObservation{}, errors.New("unknown fixture PID")
	}
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
	v, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: v.ID, Digest: v.Digest})
	if err != nil {
		t.Fatal(err)
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
