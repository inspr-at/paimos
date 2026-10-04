// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/stepup"
)

const stepID = "11111111-1111-4111-8111-111111111111"
const stepComputer = "22222222-2222-4222-8222-222222222222"
const stepSignature = "MAYCAQECAQE="

func stepFixture(t *testing.T) (*StepUpManager, *stepup.Challenge, *int) {
	t.Helper()
	c := &stepup.Challenge{ComputerID: stepComputer, Nonce: strings.Repeat("a", 64), ActionDigest: strings.Repeat("b", 64), Summary: "Allow an agent to create a key for Fixture?", ExpiresAt: time.Now().Add(time.Minute)}
	prompts := new(int)
	m, err := NewStepUpManager(StepUpConfig{Origin: "https://paired.test", ComputerID: stepComputer, Ready: true,
		Fetch: func(context.Context, string) (stepup.Challenge, error) { return *c, nil },
		Sign: func(ctx context.Context, hash []byte, reason string) (string, error) {
			*prompts++
			if reason != c.Summary || !bytes.Equal(hash, c.Hash()) {
				t.Error("prompt/hash did not come from paired server")
			}
			return stepSignature, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.peer = func(*http.Request) (attachObservation, error) {
		return attachObservation{Process: attachwatch.Process{PID: 42, UID: os.Getuid(), Started: "synthetic-start"}}, nil
	}
	t.Cleanup(m.Close)
	return m, c, prompts
}

func TestStepUpSignsServerChallengeAndRefusesReplay(t *testing.T) {
	m, c, prompts := stepFixture(t)
	// A real synthetic key proves the exact bytes passed to the enclave signer.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Sign = func(ctx context.Context, hash []byte, reason string) (string, error) {
		*prompts++
		if reason != c.Summary {
			t.Error("wrong Touch ID reason")
		}
		raw, err := ecdsa.SignASN1(rand.Reader, key, hash)
		return base64.StdEncoding.EncodeToString(raw), err
	}
	r := httptest.NewRequest("POST", "/v1/step-up", nil)
	out, err := m.confirm(r, stepID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(out.Signature)
	if out.ChallengeID != stepID || !ecdsa.VerifyASN1(&key.PublicKey, c.Hash(), raw) || *prompts != 1 {
		t.Fatal("invalid signed action")
	}
	if _, err = m.confirm(r, stepID); err == nil || *prompts != 1 {
		t.Fatal("replay prompted or signed")
	}
}

func TestStepUpRejectsUntrustedFieldsBeforeFetch(t *testing.T) {
	for _, extra := range []string{`"summary":"Read status"`, `"nonce":"x"`, `"action_digest":"x"`, `"signature":"x"`, `"confirmed":true`, `"computer_id":"x"`, `"origin":"https://attacker.test"`} {
		m, _, prompts := stepFixture(t)
		m.cfg.Fetch = func(context.Context, string) (stepup.Challenge, error) {
			t.Error("untrusted request reached server")
			return stepup.Challenge{}, nil
		}
		r := httptest.NewRequest("POST", "/v1/step-up", strings.NewReader(`{"challenge_id":"`+stepID+`",`+extra+`}`))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		m.serve(w, r, "fixture")
		if w.Code != 400 || *prompts != 0 {
			t.Fatal("agent supplied confirmation data")
		}
	}
}

func TestStepUpRequiresAuthenticatedKernelPeer(t *testing.T) {
	m, _, prompts := stepFixture(t)
	m.peer = stepUpPeer
	for _, token := range []string{"", "fixture"} {
		r := httptest.NewRequest("POST", "/v1/step-up", strings.NewReader(`{"challenge_id":"`+stepID+`"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		m.serve(w, r, "fixture")
		if w.Code == 200 || *prompts != 0 {
			t.Fatal("missing kernel peer authorized signing")
		}
	}
}

func TestStepUpChangedPeerBeforeOrDuringPromptFailsClosed(t *testing.T) {
	for _, at := range []int{2, 3} {
		m, _, prompts := stepFixture(t)
		n := 0
		m.peer = func(*http.Request) (attachObservation, error) {
			n++
			started := "original"
			if n >= at {
				started = "reused-pid"
			}
			return attachObservation{Process: attachwatch.Process{PID: 42, Started: started}}, nil
		}
		if out, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepID); err == nil || out.Signature != "" {
			t.Fatal("changed peer got proof")
		}
		if at == 2 && *prompts != 0 {
			t.Fatal("changed peer prompted")
		}
	}
}

func TestStepUpBadServerChallengesNeverPrompt(t *testing.T) {
	for name, change := range map[string]func(*stepup.Challenge){
		"wrong computer":    func(c *stepup.Challenge) { c.ComputerID = stepID },
		"expired":           func(c *stepup.Challenge) { c.ExpiresAt = time.Unix(1, 0) },
		"overlong expiry":   func(c *stepup.Challenge) { c.ExpiresAt = time.Now().Add(time.Hour) },
		"invalid nonce":     func(c *stepup.Challenge) { c.Nonce = "x" },
		"invalid digest":    func(c *stepup.Challenge) { c.ActionDigest = "x" },
		"empty summary":     func(c *stepup.Challenge) { c.Summary = "" },
		"oversized summary": func(c *stepup.Challenge) { c.Summary = strings.Repeat("a", 257) },
		"control summary":   func(c *stepup.Challenge) { c.Summary = "Safe\nDanger" },
		"bidi summary":      func(c *stepup.Challenge) { c.Summary = "Safe\u202edanger" },
	} {
		t.Run(name, func(t *testing.T) {
			m, c, prompts := stepFixture(t)
			change(c)
			if _, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepID); err == nil || *prompts != 0 {
				t.Fatal("invalid server challenge prompted")
			}
		})
	}
}

func TestStepUpDeniedTimeoutUnavailableAndInvalidSignerFailClosed(t *testing.T) {
	for _, failure := range []error{errors.New("denied"), context.DeadlineExceeded, errors.New("unavailable"), nil} {
		m, _, _ := stepFixture(t)
		m.cfg.Sign = func(context.Context, []byte, string) (string, error) { return "", failure }
		if out, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepID); err == nil || out.Signature != "" {
			t.Fatal("signer failure produced proof")
		}
	}
}

func TestStepUpExpiryDuringPromptAndCloseDiscardProof(t *testing.T) {
	for _, closeDaemon := range []bool{false, true} {
		m, c, _ := stepFixture(t)
		m.cfg.Sign = func(context.Context, []byte, string) (string, error) {
			if closeDaemon {
				m.Close()
			} else {
				m.now = func() time.Time { return c.ExpiresAt }
			}
			return stepSignature, nil
		}
		if out, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepID); err == nil || out.Signature != "" {
			t.Fatal("late proof released")
		}
	}
}

func TestStepUpSerializesPromptsAndBoundsReplayCache(t *testing.T) {
	m, _, _ := stepFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	m.cfg.Sign = func(context.Context, []byte, string) (string, error) {
		close(entered)
		<-release
		return stepSignature, nil
	}
	done := make(chan error, 1)
	go func() { _, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepID); done <- err }()
	<-entered
	_, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepComputer)
	close(release)
	if err == nil {
		t.Error("parallel prompt allowed")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		m.attempted[string(rune(i))] = time.Now().Add(time.Minute)
	}
	if _, err := m.confirm(httptest.NewRequest("POST", "/", nil), stepComputer); err == nil {
		t.Fatal("unbounded replay cache")
	}
}

func TestStepUpLocalResponseDoesNotExposeChallenge(t *testing.T) {
	m, c, _ := stepFixture(t)
	r := httptest.NewRequest("POST", "/v1/step-up", strings.NewReader(`{"challenge_id":"`+stepID+`"}`))
	r.Header.Set("Authorization", "Bearer fixture")
	w := httptest.NewRecorder()
	m.serve(w, r, "fixture")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("confirmation failed")
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out) != 2 || out["signature"] != stepSignature {
		t.Fatal("wrong local response")
	}
	for _, private := range []string{c.Nonce, c.ActionDigest, c.Summary} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("server challenge leaked to agent")
		}
	}
}
