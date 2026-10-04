// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

func proofTestRemote(t *testing.T, handle func(attachwatch.DeviceRequest) (int, string)) *agentd.Remote {
	t.Helper()
	c := client.New("https://paired.invalid", "fixture")
	c.HTTP.Transport = pairingTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		var in attachwatch.DeviceRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error("invalid fixture request")
			return nil, err
		}
		status, body := handle(in)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &agentd.Remote{Client: c}
}

const proofRegistered = `{"state":"registered","local_consent_proof_version":2}`
const proofUnknown = `{"error":"daemon poll key rejected","attach_refusal":"poll_key_unknown"}`

func TestPairedAttachReadsStartupAuthorityOnceAndRefreshesRecovery(t *testing.T) {
	reads, registrations, exchanges := 0, 0, 0
	remote := proofTestRemote(t, func(in attachwatch.DeviceRequest) (int, string) {
		if in.Operation == "register" {
			registrations++
			want := "startup"
			if registrations > 1 {
				want = "fresh"
			}
			if in.DeviceProof != want {
				t.Error("registration did not use the current read")
			}
			return 200, proofRegistered
		}
		exchanges++
		if exchanges == 1 {
			return 403, proofUnknown
		}
		return 200, `{"state":"pending"}`
	})
	cfg, err := pairedAttachConfigWithProof(agentsetup.RuntimeConfig{Origin: remote.Client.BaseURL}, remote, &attachRecoveryClock{}, func() (string, string, error) {
		reads++
		if reads == 1 {
			return "fixture-host", "startup", nil
		}
		return "fixture-host", "fresh", nil
	})
	if err != nil || reads != 1 || registrations != 1 || cfg.Host != "fixture-host" {
		t.Fatal("startup did not obtain host and proof in exactly one read")
	}
	view, err := cfg.Exchange(t.Context(), attachwatch.DeviceRequest{Operation: "request"})
	if err != nil || view.State != "pending" || reads != 2 || registrations != 2 || exchanges != 2 {
		t.Fatal("recovery did not obtain exactly one fresh authority read")
	}
}

func TestPairedAttachBlockedStorageReleasesGateAndDiscardsAuthority(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				released := false
				defer func() {
					if !released {
						close(release)
					}
				}()
				reads, registrations, recoveryReads := 0, 0, 0
				startupComplete := false
				remote := proofTestRemote(t, func(in attachwatch.DeviceRequest) (int, string) {
					if in.Operation == "register" {
						registrations++
						if in.DeviceProof == "abandoned" {
							t.Error("abandoned authority was sent")
						}
						return 200, proofRegistered
					}
					if in.Operation == "poll" {
						return 200, `{"state":"pending"}`
					}
					return 403, proofUnknown
				})
				clock := &attachRecoveryClock{}
				cfg, err := pairedAttachConfigWithProof(agentsetup.RuntimeConfig{Origin: remote.Client.BaseURL}, remote, clock, func() (string, string, error) {
					reads++
					if startupComplete {
						recoveryReads++
					}
					if recoveryReads == 1 {
						close(entered)
						// Deliberately synchronous and cancellation-unaware, as a
						// stalled store syscall or Keychain read is in production.
						<-release
						return "fixture", "abandoned", nil
					}
					return "fixture", "fresh", nil
				})
				if err != nil {
					t.Fatal("startup failed")
				}
				startupComplete = true
				startupReads := reads
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, err := cfg.Exchange(ctx, attachwatch.DeviceRequest{Operation: "request"})
					result <- err
				}()
				<-entered
				want := context.Canceled
				if deadline {
					// Virtual time; no wall-clock deadline or timing threshold.
					time.Sleep(20 * time.Second)
					want = context.DeadlineExceeded
				} else {
					cancel()
				}
				synctest.Wait()
				select {
				case err := <-result:
					if !errors.Is(err, want) {
						t.Error("blocked authority read lost cancellation/deadline cause")
					}
				default:
					t.Error("blocked authority read held the transport gate after cancellation/deadline")
					close(release)
					released = true
					<-result // join the blocked call before this test completes
					return
				}
				// Ordinary exchanges can proceed before storage unblocks.
				view, err := cfg.Exchange(t.Context(), attachwatch.DeviceRequest{Operation: "poll"})
				if err != nil || view.State != "pending" {
					t.Error("cancelled storage read did not release the transport gate")
				}
				// Repeated recovery attempts queue behind the ONE stalled worker.
				for i := 0; i < 4; i++ {
					clock.now = clock.now.Add(8 * time.Second)
					ctx, stop := context.WithCancel(t.Context())
					go func() {
						_, err := cfg.Exchange(ctx, attachwatch.DeviceRequest{Operation: "request"})
						result <- err
					}()
					synctest.Wait()
					stop()
					if err := <-result; !errors.Is(err, context.Canceled) {
						t.Error("queued authority read ignored cancellation")
					}
				}
				if reads != startupReads+1 || registrations != 1 {
					t.Error("stalled storage spawned extra reads or registrations")
				}
				close(release)
				released = true
				synctest.Wait()
				clock.now = clock.now.Add(8 * time.Second)
				_, err = cfg.Exchange(t.Context(), attachwatch.DeviceRequest{Operation: "request"})
				var status *client.StatusError
				if !errors.As(err, &status) || status.AttachRefusal != attachwatch.RefusalPollKeyUnknown || reads != startupReads+2 || registrations != 2 {
					t.Error("subsequent recovery did not read fresh authority exactly once")
				}
			})
		})
	}
}

func TestAttachProofReaderDropsAbandonedResultAndReadsFresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		reads := 0
		reader := newAttachProofReader(func() (string, string, error) {
			reads++
			if reads == 1 {
				close(entered)
				<-release
				return "fixture", "abandoned", nil
			}
			return "fixture", "fresh", nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { _, _, err := reader.read(ctx); result <- err }()
		<-entered
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Error("read ignored cancellation")
		}
		close(release)
		synctest.Wait()
		host, proof, err := reader.read(t.Context())
		if err != nil || host != "fixture" || proof != "fresh" || reads != 2 {
			t.Fatal("new read reused abandoned authority")
		}
	})
}
