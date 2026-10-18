// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package hooknote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The fixture's trust root is its own test image, never the replaceable pin.
func fixtureDaemonTrust(ctx context.Context, p Process) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return err
	}
	return authenticateDaemonImage(ctx, p, []string{hex.EncodeToString(hash.Sum(nil))})
}

func securityFixture(t *testing.T, src NoteSource) (*Session, *Registry) {
	t.Helper()
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := Observe(self.Parent)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-660", Harness: parent, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket := serveHook(t, src, reg)
	session, err := dialWithTrust(t.Context(), socket, selfPin(t), fixtureDaemonTrust)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, reg
}

func TestSubstitutedDaemonMetadataCannotGrantTrust(t *testing.T) {
	EnableForTest(t.Cleanup)
	socket := filepath.Join(shortDir(t), "fake")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	received := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer c.Close()
		buf := make([]byte, 2048)
		n, _ := c.Read(buf)
		received <- buf[:n]
	}()
	// Even exact attacker-chosen metadata for the real kernel peer fails when
	// its executable is absent from the independent release ceiling.
	_, err = Dial(t.Context(), socket, selfPin(t))
	if !errors.Is(err, ErrPeer) {
		t.Fatalf("substitution failure = %v", err)
	}
	select {
	case raw := <-received:
		if len(raw) != 0 {
			t.Fatal("request reached untrusted daemon")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("untrusted connection was not closed")
	}
	self, e := Observe(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	if fixtureDaemonTrust(t.Context(), self) != nil {
		t.Fatal("trusted control failed")
	}
	if authenticateDaemonImage(t.Context(), self, []string{strings.Repeat("0", 64)}) == nil {
		t.Fatal("metadata substituted approved image digest")
	}
}

func TestFreshExchangeRejectsReplayAndChangedGeneration(t *testing.T) {
	for _, mode := range []string{"missing-begin", "wrong-challenge", "wrong-generation", "wrong-epoch", "revoked", "valid"} {
		t.Run(mode, func(t *testing.T) {
			src := &fakeSource{note: ownerNote(), nonce: serverNonce}
			session, reg := securityFixture(t, src)
			challenge := strings.Repeat("ab", 32)
			var begun wireBegin
			if mode != "missing-begin" {
				status, err := session.post(t.Context(), wireRequest{Op: "begin", Event: "PostToolUse", Challenge: challenge}, &begun)
				if err != nil || status != 200 || len(src.offers) != 0 {
					t.Fatal("begin disclosed or failed")
				}
			}
			req := wireRequest{Op: "offer", Challenge: challenge, Generation: begun.Generation, Epoch: begun.Epoch}
			switch mode {
			case "wrong-challenge":
				req.Challenge = strings.Repeat("cd", 32)
			case "wrong-generation":
				req.Generation = genB
			case "wrong-epoch":
				req.Epoch.Counter++
			case "revoked":
				reg.RevokeGeneration(genA)
			}
			var out wireOffer
			status, err := session.post(t.Context(), req, &out)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if status != 200 || out.Note.Body != src.note.Body || len(src.offers) != 1 {
					t.Fatal("valid exchange failed")
				}
				status, err = session.post(t.Context(), req, nil)
				if err != nil || status != 403 || len(src.offers) != 1 {
					t.Fatal("same invocation replayed")
				}
			} else if status != 403 || out.Note.Body != "" || len(src.offers) != 0 {
				t.Fatalf("forged exchange disclosed status=%d offers=%d", status, len(src.offers))
			}
		})
	}
}

type disclosureBarrier struct {
	*fakeSource
	entered    chan struct{}
	release    chan struct{}
	authorized atomic.Bool
}

func (s *disclosureBarrier) Offer(ctx context.Context, b Binding) (Note, string, error) {
	n, nonce, err := s.fakeSource.Offer(ctx, b)
	close(s.entered)
	select {
	case <-s.release:
		return n, nonce, err
	case <-ctx.Done():
		return Note{}, "", ctx.Err()
	}
}
func (s *disclosureBarrier) Validate(context.Context, string, Binding) error {
	if !s.authorized.Load() {
		return ErrRevoked
	}
	return nil
}

func TestConsentWithdrawalAfterFetchPreventsDisclosure(t *testing.T) {
	for _, mode := range []string{"local-revoke", "server-revoke", "new-generation"} {
		t.Run(mode, func(t *testing.T) {
			src := &disclosureBarrier{fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce}, entered: make(chan struct{}), release: make(chan struct{})}
			src.authorized.Store(true)
			session, reg := securityFixture(t, src)
			type result struct {
				note Note
				err  error
			}
			done := make(chan result, 1)
			go func() { n, _, err := session.Offer(t.Context(), Claim{Event: "PostToolUse"}); done <- result{n, err} }()
			select {
			case <-src.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("offer did not enter barrier")
			}
			switch mode {
			case "local-revoke":
				reg.RevokeGeneration(genA)
			case "server-revoke":
				src.authorized.Store(false)
			case "new-generation":
				reg.mu.Lock()
				g := reg.grants[0]
				reg.mu.Unlock()
				g.Binding.MessageGeneration = genB
				reg.Add(g)
			}
			close(src.release)
			select {
			case got := <-done:
				if got.err == nil || got.note.Body != "" || len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeUncertain+":") {
					t.Fatal("withdrawn consent disclosed body or reported shown")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("disclosure did not finish")
			}
		})
	}
}
