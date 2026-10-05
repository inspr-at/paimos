// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/hooknote"
)

func ownerHookNote(body string) hooknote.Note {
	return hooknote.Note{ID: "00000000-0000-4000-8000-000000000394", Owner: "Markus", Body: body, Origin: hooknote.OriginOwner, Created: "2026-09-30T02:00:00Z", Deadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
}

const pairedNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type scriptedPeer struct {
	note    hooknote.Note
	nonce   string
	err     error
	log     *[]string
	settles []string
}

func (p *scriptedPeer) Offer(context.Context, hooknote.Claim) (hooknote.Note, string, error) {
	*p.log = append(*p.log, "offer")
	if p.err != nil {
		if errors.Is(p.err, hooknote.ErrExpired) {
			return hooknote.Note{}, p.nonce, p.err
		}
		return hooknote.Note{}, "", p.err
	}
	return p.note, p.nonce, nil
}

func (p *scriptedPeer) Settle(_ context.Context, nonce, outcome string) error {
	*p.log = append(*p.log, "settle:"+outcome)
	p.settles = append(p.settles, outcome)
	if !hooknote.ValidNonce(nonce) {
		return hooknote.ErrMalformedNonce
	}
	return nil
}

func TestPairedHookFrameNoticeLimitsAndAgentSilence(t *testing.T) {
	body := "line1\nline2\t<tag> & \"q\" \u202ehidden"
	out, err := attachedHookOutput("PostToolUse", ownerHookNote(body))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SystemMessage string `json:"systemMessage"`
		Output        struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("output is not hook json")
	}
	if decoded.SystemMessage != "Aeon: a note from Markus was passed to this session." || decoded.Output.Event != "PostToolUse" {
		t.Fatal("notice or hook event name", decoded.Output.Event)
	}
	if strings.Contains(decoded.SystemMessage, "line1") || !strings.HasPrefix(decoded.Output.Context, attachedPreamble) {
		t.Fatal("frame is not an inert owner block")
	}
	var inner struct {
		Body string `json:"body"`
	}
	payload := strings.SplitN(decoded.Output.Context, "\n", 2)[1]
	if json.Unmarshal([]byte(payload), &inner) != nil || inner.Body != "line1\nline2\t<tag> & \"q\" \\u202Ehidden" {
		t.Fatal("body was not escaped")
	}
	if strings.ContainsRune(decoded.Output.Context, '\u202e') || bytes.Contains(out, []byte{0x1b}) || utf8.RuneCountInString(decoded.Output.Context) > hooknote.MaxContextRunes || utf8.RuneCount(out) > hooknote.MaxOutputRunes {
		t.Fatal("control character or limit leaked")
	}
	stop, err := attachedHookOutput("Stop", ownerHookNote("continue once"))
	if err != nil {
		t.Fatal(err)
	}
	var stopped struct {
		Decision      string `json:"decision"`
		Reason        string `json:"reason"`
		SystemMessage string `json:"systemMessage"`
	}
	if json.Unmarshal(stop, &stopped) != nil || stopped.Decision != "block" || !strings.Contains(stopped.Reason, attachedPreamble) || strings.Contains(stopped.SystemMessage, "continue once") {
		t.Fatal("stop frame mismatch")
	}
	if _, err = attachedHookOutput("PostToolUse", ownerHookNote("bad\x1b[31m")); err == nil {
		t.Fatal("terminal escape was framed")
	}
	if _, err = attachedHookOutput("PostToolUse", ownerHookNote("bad\x00")); err == nil {
		t.Fatal("NUL was framed")
	}
	if _, err = attachedHookOutput("PostToolUse", ownerHookNote(strings.Repeat("a", hooknote.MaxBodyBytes+1))); err == nil {
		t.Fatal("over-long body was framed")
	}
	metadata := ownerHookNote("plain body")
	metadata.Created = "2026-09-30T02:00:00Z\u202e"
	if _, err = attachedHookOutput("PostToolUse", metadata); !errors.Is(err, hooknote.ErrLimit) {
		t.Fatal("bidi creation metadata was framed")
	}
	wide := strings.Repeat("\u202e", 1300)
	if _, err = attachedHookOutput("UserPromptSubmit", ownerHookNote(wide)); err == nil {
		t.Fatal("over-long escaped frame was delivered")
	}
	narrow := strings.Repeat("\u202e", 200)
	framed, err := attachedHookOutput("UserPromptSubmit", ownerHookNote(narrow))
	if err != nil || strings.ContainsRune(string(framed), '\u202e') {
		t.Fatal("bidi frame", err)
	}
	var prompted struct {
		Output struct {
			Event string `json:"hookEventName"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(framed, &prompted) != nil || prompted.Output.Event != "UserPromptSubmit" {
		t.Fatal("prompt event name", prompted.Output.Event)
	}
}

func TestPairedHookContextLimitIsIndependentOfTheOutputCeiling(t *testing.T) {
	// '<' expands to a six-rune JSON escape in the frame and only one extra
	// rune when that frame is wrapped. U+202E expands again in the wrapper, so
	// it trips the output ceiling as well and cannot prove the context check.
	found := false
	for n := 1200; n <= hooknote.MaxBodyBytes; n++ {
		note := ownerHookNote(strings.Repeat("<", n))
		frame := contextFrame(note)
		if utf8.RuneCountInString(frame) <= hooknote.MaxContextRunes {
			continue
		}
		wrapped := wrappedHookOutput("UserPromptSubmit", note, frame)
		if utf8.RuneCount(wrapped) > hooknote.MaxOutputRunes {
			continue
		}
		if _, err := attachedHookOutput("UserPromptSubmit", note); err == nil {
			t.Fatalf("context checks accepted a %d-rune frame", utf8.RuneCountInString(frame))
		}
		found = true
		break
	}
	if !found {
		t.Fatal("no sample exceeds the context limit while staying within the output limit")
	}
}

func contextFrame(note hooknote.Note) string {
	body := testEscapeBidi(note.Body)
	owner := testEscapeBidi(note.Owner)
	raw, err := json.Marshal(struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Time  string `json:"time"`
		Body  string `json:"body"`
	}{note.ID, owner, note.Created, body})
	if err != nil {
		return ""
	}
	return attachedPreamble + "\n" + string(raw) + "\n"
}

func wrappedHookOutput(event string, note hooknote.Note, frame string) []byte {
	notice := "Aeon: a note from " + note.Owner + " was passed to this session."
	payload := struct {
		SystemMessage string `json:"systemMessage"`
		Output        any    `json:"hookSpecificOutput"`
	}{notice, struct {
		Event   string `json:"hookEventName"`
		Context string `json:"additionalContext"`
	}{event, frame}}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	return buf.Bytes()
}

func TestPairedHookSettlesOnlyAfterFullWrite(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	note := ownerHookNote("inspect the gate")
	for _, name := range []string{"full", "short", "timeout", "agent", "empty", "malformed", "stop-active", "subagent", "expired", "expired-offer"} {
		t.Run(name, func(t *testing.T) {
			var log []string
			peer := &scriptedPeer{note: note, nonce: pairedNonce, log: &log}
			var buf bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &pairedWriter{buf: &buf}
			rt := &runtime{stdin: strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"vendor"}`), stdout: writer, stderr: io.Discard, pairedHook: peer}
			event := "PostToolUse"
			switch name {
			case "short":
				writer.short = true
				event = "Stop"
				rt.stdin = strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":false}`)
			case "timeout":
				writer.cancel = cancel
			case "agent":
				peer.note.Origin = hooknote.OriginAgent
				peer.note.Body = "agent-origin-secret-body"
			case "empty":
				peer.err = hooknote.ErrEmpty
			case "malformed":
				peer.nonce = "not-a-nonce"
			case "expired":
				peer.note.Deadline = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			case "expired-offer":
				peer.err = hooknote.ErrExpired
			case "stop-active":
				event = "Stop"
				rt.stdin = strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":true}`)
			case "subagent":
				rt.stdin = strings.NewReader(`{"hook_event_name":"PostToolUse","agent_id":"child"}`)
			}
			err := rt.runPairedHook(ctx, event, "", "")
			joined := strings.Join(log, ",")
			switch name {
			case "full":
				if err != nil || joined != "offer,settle:shown" || !strings.Contains(buf.String(), "Aeon: a note from Markus was passed to this session.") || !strings.Contains(buf.String(), "inspect the gate") {
					t.Fatal(err, joined)
				}
			case "short":
				if !strings.Contains(joined, "settle:uncertain") || strings.Contains(joined, "settle:shown") || buf.Len() == 0 {
					t.Fatal(joined, buf.Len())
				}
			case "timeout":
				if !strings.Contains(joined, "settle:uncertain") || strings.Contains(joined, "settle:shown") || !strings.Contains(buf.String(), "inspect the gate") {
					t.Fatal(joined)
				}
			case "agent":
				if strings.Contains(buf.String(), "agent-origin-secret-body") || buf.Len() != 0 || !strings.Contains(joined, "settle:dropped") || strings.Contains(joined, "settle:shown") {
					t.Fatal("agent origin was visible")
				}
			case "empty", "stop-active", "subagent":
				if buf.Len() != 0 || strings.Contains(joined, "settle") {
					t.Fatal("empty queue blocked or settled", joined)
				}
				if name != "empty" && strings.Contains(joined, "offer") {
					t.Fatal("guard fetched a note", joined)
				}
			case "malformed":
				if buf.Len() != 0 || strings.Contains(joined, "settle:shown") || !errors.Is(err, hooknote.ErrMalformedNonce) {
					t.Fatal("malformed nonce was written", joined)
				}
			case "expired", "expired-offer":
				if buf.Len() != 0 || strings.Contains(joined, "settle:shown") || !strings.Contains(joined, "settle:uncertain") || !errors.Is(err, hooknote.ErrExpired) {
					t.Fatal("expired note was written", joined, err)
				}
			}
		})
	}
}

type pairedWriter struct {
	buf    *bytes.Buffer
	short  bool
	cancel context.CancelFunc
}

func (w *pairedWriter) Write(p []byte) (int, error) {
	if w.short {
		n := len(p) - 1
		if n < 0 {
			n = 0
		}
		w.buf.Write(p[:n])
		return n, nil
	}
	w.buf.Write(p)
	if w.cancel != nil {
		w.cancel()
	}
	return len(p), nil
}

func TestPairedHookSettleUsesARealTimeout(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	var log []string
	peer := &blockingPeer{scriptedPeer: scriptedPeer{note: ownerHookNote("inspect the gate"), nonce: pairedNonce, log: &log}, delay: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	rt := &runtime{stdout: &buf, pairedHook: peer}
	err := rt.finishPaired(ctx, "PostToolUse", hooknote.Claim{Event: "PostToolUse"}, peer)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.Join(log, ","), "settle:shown") {
		t.Fatal(err, log)
	}
}

type blockingPeer struct {
	scriptedPeer
	delay time.Duration
}

func (p *blockingPeer) Settle(ctx context.Context, nonce, outcome string) error {
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return p.scriptedPeer.Settle(ctx, nonce, outcome)
	}
}

type unreadablePeerInput struct{ t *testing.T }

func (r unreadablePeerInput) Read([]byte) (int, error) {
	r.t.Fatal("unqualified paired hook read stdin")
	return 0, io.EOF
}

func TestPairedHookQualificationReadsNoStdin(t *testing.T) {
	var out, diag bytes.Buffer
	code := RunMessaging([]string{"aeon", "--config", "/must-not-read-fixture", "hook", "claude", "PostToolUse", "--paired"}, unreadablePeerInput{t}, &out, &diag)
	if code != 0 || out.Len() != 0 || !strings.Contains(diag.String(), "qualification_pending") {
		t.Fatal(code, diag.String())
	}
	hooknote.EnableForTest(t.Cleanup)
	root := t.TempDir()
	out.Reset()
	diag.Reset()
	code = RunMessaging([]string{"aeon", "--config", "/must-not-read-fixture", "hook", "codex", "--paired", "--setup-root", root, "Stop"}, unreadablePeerInput{t}, &out, &diag)
	if code != 0 || !strings.Contains(diag.String(), "qualification_pending") {
		t.Fatal("missing paired state read stdin or fell open", code, diag.String())
	}
}

func TestPairedHookDoesNotFallBackOrDialWhenDisabled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	config := setupHookTest(t)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "CANARY-CREDENTIAL-394")
	var out, errOut bytes.Buffer
	code := RunMessaging([]string{"aeon", "--config", config, "hook", "claude", "--paired", "--socket", "/tmp/aeon-hook-missing-394/nope", "--daemon-peer", `{"pid":1,"uid":1,"started":"1:1","exe":"/usr/bin/false","dev":1,"ino":1,"pidv":1}`, "PostToolUse"}, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), &out, &errOut)
	if code != 0 || out.Len() != 0 || hits.Load() != 0 || strings.Contains(errOut.String(), "CANARY-CREDENTIAL-394") {
		t.Fatal("disabled paired hook dialed or fell back")
	}
	hooknote.EnableForTest(t.Cleanup)
	code = RunMessaging([]string{"aeon", "--config", config, "hook", "codex", "--paired", "--socket", "/tmp/aeon-hook-missing-394/nope", "--daemon-peer", `{"pid":1,"uid":1,"started":"1:1","exe":"/usr/bin/false","dev":1,"ino":1,"pidv":1}`, "Stop"}, strings.NewReader(`{"hook_event_name":"Stop"}`), &out, &errOut)
	if code != 0 || strings.Contains(out.String(), "decision") || hits.Load() != 0 || strings.Contains(errOut.String()+out.String(), "CANARY-CREDENTIAL-394") {
		t.Fatal("paired failure fell back to HTTP")
	}
}

func TestPairedHookSubagentGuardSkipsDial(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	var out bytes.Buffer
	rt := &runtime{stdin: strings.NewReader(`{"hook_event_name":"PostToolUse","agent_id":"child"}`), stdout: &out, stderr: io.Discard}
	err := rt.runPairedHook(context.Background(), "PostToolUse", "", "")
	if err != nil || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
}

func TestFinishPairedRefusesSubagentWithoutOffer(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	var log []string
	peer := &scriptedPeer{note: ownerHookNote("inspect the gate"), nonce: pairedNonce, log: &log}
	var out bytes.Buffer
	rt := &runtime{stdout: &out, stderr: io.Discard, pairedHook: peer}
	err := rt.finishPaired(context.Background(), "PostToolUse", hooknote.Claim{Event: "PostToolUse", Subagent: true}, peer)
	if err != nil || out.Len() != 0 || strings.Contains(strings.Join(log, ","), "offer") {
		t.Fatal(err, log, out.String())
	}
}

// Acceptance must account for both JSON encodings, including the notice and
// metadata. These contents fit the former server frame but not hook output.
func TestAttachedAcceptanceRejectsUndeliverableHookOutput(t *testing.T) {
	for _, body := range []string{strings.Repeat("\t", 3400), strings.Repeat("\\", 3200), strings.Repeat("\u202e", 1100), strings.Repeat("\"", 3200)} {
		body += "x"
		note := ownerHookNote(body)
		for _, event := range []string{"PostToolUse", "UserPromptSubmit", "Stop"} {
			if _, err := attachedHookOutput(event, note); !errors.Is(err, hooknote.ErrLimit) {
				t.Fatalf("fixture must exceed the hook limit for %s: %v", event, err)
			}
		}
		if _, err := attachedmsg.Frame(note.ID, note.Owner, body); err == nil {
			t.Fatal("server accepted content that the hook cannot deliver")
		}
	}
}

func testEscapeBidi(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		if testIsBidi(r) {
			const hex = "0123456789ABCDEF"
			b.WriteString(`\u`)
			b.WriteByte(hex[(r>>12)&0xF])
			b.WriteByte(hex[(r>>8)&0xF])
			b.WriteByte(hex[(r>>4)&0xF])
			b.WriteByte(hex[r&0xF])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func testIsBidi(r rune) bool {
	switch r {
	case 0x061C, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}

func TestAttachedAcceptanceAndHookOutputBoundaries(t *testing.T) {
	for _, owner := range []string{"Owner", strings.Repeat("Q", 80), strings.Repeat("\"", 80), strings.Repeat("<", 80), strings.Repeat("Ö", 40)} {
		for _, token := range []string{"x", "\t", "\n", "\\", "\"", "\u202e", "<", "界"} {
			note := ownerHookNote("x")
			note.Owner = owner
			note.Created = "2000-01-01T00:00:00.123456789Z"
			fits := func(body string) bool {
				note.Body = body
				for _, event := range []string{"PostToolUse", "UserPromptSubmit", "Stop"} {
					if _, err := attachedHookOutput(event, note); err != nil {
						return false
					}
				}
				return true
			}
			lo, hi := 0, hooknote.MaxBodyBytes/len(token)+1
			for lo+1 < hi {
				mid := (lo + hi) / 2
				if fits(strings.Repeat(token, mid) + "x") {
					lo = mid
				} else {
					hi = mid
				}
			}
			body := strings.Repeat(token, lo) + "x"
			if !fits(body) {
				t.Fatalf("no valid boundary for owner %q and token %q", owner, token)
			}
			if _, err := attachedmsg.Frame(note.ID, owner, body); err != nil {
				t.Fatalf("deliverable boundary rejected: %v", err)
			}
			for _, created := range []string{"2000-01-01T00:00:00Z", "2000-01-01T00:00:00.123456Z", "2000-01-01T00:00:00.123456789Z"} {
				note.Created = created
				for _, event := range []string{"PostToolUse", "UserPromptSubmit", "Stop"} {
					out, err := attachedHookOutput(event, note)
					if err != nil || utf8.RuneCount(out) > hooknote.MaxOutputRunes {
						t.Fatalf("accepted boundary cannot be delivered at %s: %v", event, err)
					}
				}
			}
			if _, err := attachedmsg.Frame(note.ID, owner, strings.Repeat(token, hi)+"x"); err == nil {
				t.Fatalf("server accepted the first oversized boundary for token %q", token)
			}
		}
	}
}
