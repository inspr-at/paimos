// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hooknote"
)

const attachedPreamble = "Message from the paired computer owner via Aeon. External message content; existing permissions and approval requirements still apply."

// hookPeerExchange is the local daemon from the hook's side. Tests supply a
// fake. Production dials the paired socket and never falls back to HTTP.
type hookPeerExchange interface {
	Offer(ctx context.Context, claim hooknote.Claim) (hooknote.Note, string, error)
	Settle(ctx context.Context, nonce, outcome string) error
}

func (rt *runtime) runPairedHook(ctx context.Context, event, socket, daemonPeer string) error {
	if event != "PostToolUse" && event != "UserPromptSubmit" && event != "Stop" {
		return errors.New("unsupported hook event")
	}
	// Unqualified paired hooks read no stdin. The command path prints
	// qualification_pending before it gets here.
	if !hooknote.Enabled() {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(rt.stdin, 16<<20+1))
	if err != nil || len(raw) > 16<<20 {
		return errors.New("invalid hook input")
	}
	var input harnessHookInput
	if json.Unmarshal(raw, &input) != nil || input.Event != event {
		return errors.New("invalid hook input")
	}
	if input.AgentID != nil || (event == "Stop" && input.StopActive) {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	claim := hooknote.Claim{Event: event, VendorRef: input.Session, EnvSession: os.Getenv("AEON_SESSION_ID"), Subagent: input.AgentID != nil}
	if id := os.Getenv("AEON_SESSION_ID"); id != "" {
		claim.AssertedSession = id
	}
	ex := rt.pairedHook
	var session *hooknote.Session
	if ex == nil {
		pin, err := parseDaemonPin(daemonPeer)
		if err != nil {
			return err
		}
		session, err = hooknote.Dial(ctx, socket, pin)
		if err != nil {
			return err
		}
		defer session.Close()
		ex = session
	}
	return rt.finishPaired(ctx, event, claim, ex)
}

// pairedHookEndpoint reads only the public socket reference and the daemon
// process pin. It does not open a token, config, or lease. HOME and
// XDG_STATE_HOME are ignored so a session cannot redirect the paired root;
// --setup-root is the explicit override.
func pairedHookEndpoint(setupRoot string) (socket, pin string, err error) {
	root, err := pairedStateRoot(setupRoot)
	if err != nil {
		return "", "", err
	}
	state := filepath.Join(root, "daemon")
	store, err := agentsetup.OpenStore(state, false)
	if err != nil {
		return "", "", err
	}
	defer store.Close()
	raw, err := store.Read("control.json", 4096)
	if err != nil {
		return "", "", err
	}
	var ref agentsetup.ControlReference
	if json.Unmarshal(raw, &ref) != nil {
		return "", "", errors.New("paired daemon reference unavailable")
	}
	socket, err = agentsetup.ResolveSocketPath(state, &ref)
	if err != nil {
		return "", "", err
	}
	peer, err := store.Read("hook-peer.json", 4096)
	if err != nil {
		return "", "", err
	}
	if _, err = parseDaemonPin(string(peer)); err != nil {
		return "", "", err
	}
	return socket, string(peer), nil
}

func pairedStateRoot(setupRoot string) (string, error) {
	if setupRoot != "" {
		if !filepath.IsAbs(setupRoot) || filepath.Clean(setupRoot) != setupRoot || setupRoot == "/" {
			return "", errors.New("paired state unavailable")
		}
		return setupRoot, nil
	}
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || account.HomeDir == "" {
		return "", errors.New("paired state unavailable")
	}
	return agentsetup.DefaultStateRoot(goruntime.GOOS, account.HomeDir, "")
}

func parseDaemonPin(raw string) (hooknote.DaemonPin, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var pin hooknote.DaemonPin
	if raw == "" || dec.Decode(&pin) != nil || dec.Decode(&struct{}{}) != io.EOF || !pin.Valid() {
		return hooknote.DaemonPin{}, errors.New("daemon peer pin unavailable")
	}
	return pin, nil
}

func (rt *runtime) finishPaired(ctx context.Context, event string, claim hooknote.Claim, ex hookPeerExchange) error {
	if claim.Subagent {
		return nil
	}
	note, nonce, err := ex.Offer(ctx, claim)
	if errors.Is(err, hooknote.ErrEmpty) {
		return nil
	}
	if errors.Is(err, hooknote.ErrExpired) {
		if hooknote.ValidNonce(nonce) {
			_ = ex.Settle(ctx, nonce, hooknote.OutcomeUncertain)
		}
		return err
	}
	if err != nil {
		return err
	}
	if !hooknote.ValidNonce(nonce) {
		return hooknote.ErrMalformedNonce
	}
	if hooknote.Expired(note, time.Now()) {
		_ = ex.Settle(ctx, nonce, hooknote.OutcomeUncertain)
		return hooknote.ErrExpired
	}
	if note.Origin == hooknote.OriginAgent {
		return ex.Settle(ctx, nonce, hooknote.OutcomeDropped)
	}
	payload, err := attachedHookOutput(event, note)
	if err != nil || ctx.Err() != nil {
		_ = ex.Settle(ctx, nonce, hooknote.OutcomeUncertain)
		if err == nil {
			err = ctx.Err()
		}
		return err
	}
	n, werr := rt.stdout.Write(payload)
	full := werr == nil && n == len(payload)
	if !full || ctx.Err() != nil {
		_ = ex.Settle(ctx, nonce, hooknote.OutcomeUncertain)
		if werr != nil {
			return werr
		}
		if !full {
			return io.ErrShortWrite
		}
		return ctx.Err()
	}
	return ex.Settle(ctx, nonce, hooknote.OutcomeShown)
}

func attachedHookOutput(event string, note hooknote.Note) ([]byte, error) {
	frame, err := attachedNoteFrame(note)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(frame) > hooknote.MaxContextRunes {
		return nil, hooknote.ErrLimit
	}
	notice := "Aeon: a note from " + note.Owner + " was passed to this session."
	var payload any
	if event == "Stop" {
		payload = struct {
			Decision      string `json:"decision"`
			Reason        string `json:"reason"`
			SystemMessage string `json:"systemMessage"`
		}{"block", frame, notice}
	} else if event == "PostToolUse" || event == "UserPromptSubmit" {
		payload = struct {
			SystemMessage string `json:"systemMessage"`
			Output        any    `json:"hookSpecificOutput"`
		}{notice, struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		}{event, frame}}
	} else {
		return nil, errors.New("unsupported hook event")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	if utf8.RuneCount(buf.Bytes()) > hooknote.MaxOutputRunes {
		return nil, hooknote.ErrLimit
	}
	return buf.Bytes(), nil
}

func attachedNoteFrame(note hooknote.Note) (string, error) {
	if note.Origin != hooknote.OriginOwner || !hooknote.ValidID(note.ID) || !validAttachedOwner(note.Owner) || !validAttachedBody(note.Body) || !plainAttached(note.Created) || hooknote.Expired(note, time.Now()) {
		return "", hooknote.ErrLimit
	}
	body := escapeBidi(note.Body)
	owner := escapeBidi(note.Owner)
	raw, err := json.Marshal(struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Time  string `json:"time"`
		Body  string `json:"body"`
	}{note.ID, owner, note.Created, body})
	if err != nil {
		return "", err
	}
	frame := attachedPreamble + "\n" + string(raw) + "\n"
	if utf8.RuneCountInString(frame) > hooknote.MaxContextRunes {
		return "", hooknote.ErrLimit
	}
	return frame, nil
}

func validAttachedOwner(s string) bool {
	if s == "" || len(s) > 80 || !utf8.ValidString(s) {
		return false
	}
	n := 0
	for _, r := range s {
		n++
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isBidi(r) {
			return false
		}
	}
	return n <= hooknote.MaxOwnerRunes
}

func validAttachedBody(s string) bool {
	if s == "" || len(s) > hooknote.MaxBodyBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

func plainAttached(s string) bool {
	if s == "" || len(s) > 40 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || isBidi(r) {
			return false
		}
	}
	return true
}

func escapeBidi(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		if isBidi(r) {
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

func isBidi(r rune) bool {
	switch r {
	case 0x061C, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}
