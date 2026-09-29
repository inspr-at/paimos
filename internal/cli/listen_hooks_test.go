// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/inbox"
)

const hookProjectID = "00000000-0000-4000-8000-000000000002"

func hookListenServer(t *testing.T, fn http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/kinds":
			fmt.Fprint(w, `{"items":[{"id":"00000000-0000-4000-8000-000000000001","slug":"project"}]}`)
		case "/api/nodes":
			fmt.Fprint(w, `{"items":[{"id":"`+hookProjectID+`","kind_id":"00000000-0000-4000-8000-000000000001","key":"AEON-1","fields":{"project_key":"AEON"}}]}`)
		case "/api/me":
			fmt.Fprint(w, `{"principal":{"id":"00000000-0000-4000-8000-000000000003","name":"receiver"}}`)
		default:
			fn(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	return srv
}

func TestListenFollowLongPoll(t *testing.T) {
	for _, address := range []string{"", "codex:receiver"} {
		t.Run(address, func(t *testing.T) {
			config := setupHookTest(t)
			var waits atomic.Int32
			hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/messages/listen") {
					fmt.Fprint(w, `{"items":[],"next_after":0}`)
					return
				}
				if r.URL.Path != "/api/inbox/messages" {
					t.Error("unexpected listener endpoint")
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("wait_ms") != "25000" || r.URL.Query().Get("session") != hookSessionID {
					t.Error("follow is not session-scoped long-poll")
				}
				if waits.Add(1) == 1 {
					fmt.Fprint(w, `{"items":[],"next_after":42}`)
					return
				}
				if address != "" && r.URL.Query().Get("after") != "42" {
					t.Error("wake cursor was lost")
				}
				http.Error(w, "end fixture", 403)
			})
			args := []string{"aeon", "--config", config, "listen", "--project", "AEON", "--session", hookSessionID, "--follow"}
			if address != "" {
				args = append(args, "--as", address)
			}
			started := time.Now()
			code, _, _ := runCLIWithMessaging(args, "")
			if code != 1 || waits.Load() != 2 || time.Since(started) > time.Second {
				t.Fatalf("follow retained polling sleep: code=%d waits=%d", code, waits.Load())
			}
		})
	}
}

func runCLIWithMessaging(args []string, input string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := RunMessaging(args, strings.NewReader(input), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestResidentDeliveryRetriesContention(t *testing.T) {
	for _, state := range []string{"leased", "foreign_worker", "blocked", "rerouted", "adapter-error"} {
		t.Run(state, func(t *testing.T) {
			config := setupHookTest(t)
			var claims, completed atomic.Int32
			var first, second atomic.Int64
			hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/delivery-claim"):
					n := claims.Add(1)
					if n > 2 {
						http.Error(w, "end fixture", 503)
						return
					}
					work := inbox.DeliveryWork{ID: hookMessageID, State: "pending", LeaseToken: hookSessionID, TargetRef: "fixture-thread", MaximumLevel: "simple", Message: &inbox.CompatMessage{ID: hookMessageID, Body: "hello", Level: "simple"}}
					if n == 1 {
						first.Store(time.Now().UnixNano())
						if state != "rerouted" && state != "adapter-error" {
							work.State = state
						}
					} else {
						second.Store(time.Now().UnixNano())
					}
					_ = json.NewEncoder(w).Encode(work)
				case strings.HasSuffix(r.URL.Path, "/delivery-complete"):
					completed.Add(1)
					fmt.Fprint(w, `{}`)
				case strings.HasSuffix(r.URL.Path, "/delivery-unavailable"):
					fmt.Fprint(w, `{}`)
				default:
					t.Error("unexpected delivery endpoint")
					http.NotFound(w, r)
				}
			})
			attempts := 0
			deliver := func(context.Context, string, inbox.DeliveryWork) (localDeliveryResult, error) {
				attempts++
				if attempts == 1 && state == "rerouted" {
					return localDeliveryResult{}, &localUnavailable{reason: "idle"}
				}
				if attempts == 1 && state == "adapter-error" {
					return localDeliveryResult{}, errors.New("temporary adapter failure")
				}
				return localDeliveryResult{EffectiveLevel: "simple"}, nil
			}
			var out, stderr bytes.Buffer
			code := runMessaging([]string{"aeon", "--config", config, "listen", "--project", "AEON", "--as", "codex:receiver", "--deliver", "codex", "--follow", "--poll-interval", "15ms"}, strings.NewReader(""), &out, &stderr, deliver)
			if code != 1 || claims.Load() != 3 || completed.Load() != 1 || time.Duration(second.Load()-first.Load()) < 10*time.Millisecond {
				t.Fatalf("resident worker exited before recovery: code=%d claims=%d completions=%d stderr=%s", code, claims.Load(), completed.Load(), stderr.String())
			}
		})
	}
}

func TestSessionDeliveryAcknowledgesOnlyHandoff(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			config := setupHookTest(t)
			ref := filepath.Join(t.TempDir(), "target-ref")
			if err := os.WriteFile(ref, []byte("exact-vendor-thread"), 0600); err != nil {
				t.Fatal(err)
			}
			var handed, acked atomic.Bool
			hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == harnessPath(hookProjectID, hookSessionID):
					fmt.Fprint(w, `{"id":"`+hookSessionID+`","harness":"codex"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
					if r.URL.Query().Get("session") != hookSessionID || r.URL.Query().Get("exact_session") != "true" {
						t.Error("exact session filter missing")
					}
					_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{hookFixtureMessage()}, NextAfter: 42})
				case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/messages/"+hookMessageID+"/ack":
					if !handed.Load() {
						t.Error("ack before successful handoff")
					}
					acked.Store(true)
					fmt.Fprint(w, `{}`)
				default:
					t.Error("session delivery must not claim a principal target")
					http.NotFound(w, r)
				}
			})
			deliver := func(ctx context.Context, adapter string, work inbox.DeliveryWork) (localDeliveryResult, error) {
				if adapter != "codex" || work.TargetRef != "exact-vendor-thread" || work.Message == nil || !strings.Contains(work.Message.Body, "Untrusted Aeon") {
					t.Error("wrong session handoff")
				}
				if failed {
					return localDeliveryResult{}, errors.New("unavailable")
				}
				handed.Store(true)
				return localDeliveryResult{EffectiveLevel: "simple"}, nil
			}
			var out, stderr bytes.Buffer
			code := runMessaging([]string{"aeon", "--config", config, "listen", "--project", "AEON", "--session", hookSessionID, "--deliver", "codex", "--target-ref-file", ref}, strings.NewReader(""), &out, &stderr, deliver)
			want := 0
			if failed {
				want = 4
			}
			if code != want || acked.Load() == failed || out.Len() != 0 {
				t.Fatalf("session delivery code=%d ack=%v stderr=%s", code, acked.Load(), stderr.String())
			}
		})
	}
}

func TestHeartbeatControlsPullsExactSession(t *testing.T) {
	config := setupHookTest(t)
	var pulled atomic.Bool
	hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/inbox/messages" {
			if r.URL.Query().Get("session") != hookSessionID {
				t.Error("heartbeat omitted session")
			}
			pulled.Store(true)
			_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{hookFixtureMessage()}})
		} else {
			fmt.Fprint(w, `{"controls":[]}`)
		}
	})
	var out bytes.Buffer
	rt := &runtime{configPath: config, stdout: &out, stderr: io.Discard}
	rt.printHeartbeatControls(context.Background(), hookSessionID, "claude", nil)
	if !pulled.Load() || !strings.Contains(out.String(), "message "+hookMessageID) {
		t.Fatal("heartbeat lost session message hint")
	}
}

func TestMessagingBackoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if waitMessagingRetry(ctx, time.Hour) || time.Since(started) > time.Second {
		t.Fatal("cancellation did not interrupt backoff")
	}
}

func TestSessionDeliveryRefusesUnboundOrMismatchedTarget(t *testing.T) {
	for _, mode := range []string{"missing-reference", "wrong-harness"} {
		t.Run(mode, func(t *testing.T) {
			config := setupHookTest(t)
			var pulled atomic.Bool
			hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == harnessPath(hookProjectID, hookSessionID) {
					fmt.Fprint(w, `{"id":"`+hookSessionID+`","harness":"claude"}`)
					return
				}
				pulled.Store(true)
				http.Error(w, "unexpected inbox access", 500)
			})
			args := []string{"aeon", "--config", config, "listen", "--project", "AEON", "--session", hookSessionID, "--deliver", "codex"}
			if mode == "wrong-harness" {
				ref := filepath.Join(t.TempDir(), "ref")
				if err := os.WriteFile(ref, []byte("fixture-thread"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--target-ref-file", ref)
			}
			code, _, _ := runCLIWithMessaging(args, "")
			if code != 2 || pulled.Load() {
				t.Fatalf("unsafe session delivery binding: code=%d pulled=%v", code, pulled.Load())
			}
		})
	}
}

func TestSessionDeliveryRetriesReadsAndAck(t *testing.T) {
	config := setupHookTest(t)
	ref := filepath.Join(t.TempDir(), "target-ref")
	if err := os.WriteFile(ref, []byte("exact-vendor-thread"), 0600); err != nil {
		t.Fatal(err)
	}
	var status, pulls, acks atomic.Int32
	hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == harnessPath(hookProjectID, hookSessionID):
			if status.Add(1) == 1 {
				http.Error(w, "retry status", 503)
				return
			}
			fmt.Fprint(w, `{"id":"`+hookSessionID+`","harness":"codex"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
			switch pulls.Add(1) {
			case 1:
				http.Error(w, "retry read", 503)
			case 2:
				unbound := hookFixtureMessage()
				unbound.RecipientSessionID, unbound.ID, unbound.SentEventID = nil, "unbound", 41
				_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{unbound, hookFixtureMessage()}, NextAfter: 42})
			default:
				if r.URL.Query().Get("after") != "42" {
					t.Error("cursor advanced before ack or was lost")
				}
				http.Error(w, "end fixture", 403)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/inbox/messages/"+hookMessageID+"/ack":
			if acks.Add(1) < 3 {
				http.Error(w, "retry ack", 503)
				return
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	})
	deliveries := 0
	deliver := func(ctx context.Context, adapter string, work inbox.DeliveryWork) (localDeliveryResult, error) {
		deliveries++
		if work.ID != hookMessageID {
			t.Error("delivered principal-wide message")
		}
		return localDeliveryResult{EffectiveLevel: "simple"}, nil
	}
	var out, stderr bytes.Buffer
	code := runMessaging([]string{"aeon", "--config", config, "listen", "--project", "AEON", "--session", hookSessionID, "--deliver", "codex", "--target-ref-file", ref, "--follow", "--poll-interval", "5ms"}, strings.NewReader(""), &out, &stderr, deliver)
	if code != 1 || status.Load() != 2 || pulls.Load() != 3 || acks.Load() != 3 || deliveries != 1 {
		t.Fatalf("recovery failed: code=%d status=%d reads=%d acks=%d deliveries=%d", code, status.Load(), pulls.Load(), acks.Load(), deliveries)
	}
}

func TestListenFollowRetriesReadsWakeAndAck(t *testing.T) {
	for _, address := range []string{"", "codex:receiver"} {
		t.Run(address, func(t *testing.T) {
			config := setupHookTest(t)
			var reads, wakes, acks atomic.Int32
			hookListenServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if acks.Add(1) == 1 {
						http.Error(w, "retry ack", 502)
						return
					}
					fmt.Fprint(w, `{}`)
					return
				}
				if address != "" && r.URL.Path == "/api/inbox/messages" {
					switch wakes.Add(1) {
					case 1:
						http.Error(w, "retry wake", 503)
					case 2:
						fmt.Fprint(w, `{"items":[],"next_after":99}`)
					default:
						http.Error(w, "end fixture", 403)
					}
					return
				}
				switch reads.Add(1) {
				case 1:
					http.Error(w, "retry read", 429)
				case 2:
					fmt.Fprint(w, `{"items":[{"id":"`+hookMessageID+`","sent_event_id":42,"body":"retry-test-body"}],"next_after":42}`)
				default:
					if r.URL.Query().Get("after") != "42" {
						t.Error("lost cursor after ack")
					}
					if address == "" {
						http.Error(w, "end fixture", 403)
						return
					}
					fmt.Fprint(w, `{"items":[],"next_after":42}`)
				}
			})
			args := []string{"aeon", "--config", config, "listen", "--project", "AEON", "--session", hookSessionID, "--follow", "--ack", "--json", "--poll-interval", "5ms"}
			if address != "" {
				args = append(args, "--as", address)
			}
			code, out, _ := runCLIWithMessaging(args, "")
			if code != 1 || reads.Load() < 3 || acks.Load() != 2 || strings.Count(out, "retry-test-body") != 1 {
				t.Fatalf("listener failed recovery: code=%d reads=%d acks=%d output=%s", code, reads.Load(), acks.Load(), out)
			}
			if address != "" && wakes.Load() != 3 {
				t.Fatal("wake poll did not recover")
			}
		})
	}
}

func TestMessagingPollBackoffAndOneShot(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprint(follow), func(t *testing.T) {
			config := setupHookTest(t)
			var attempts atomic.Int32
			var times []time.Time
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				times = append(times, time.Now())
				if attempts.Add(1) < 3 {
					http.Error(w, "transient", 503)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", "fixture-hook-key")
			rt := &runtime{configPath: config}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := rt.doMessagingPoll(ctx, http.MethodGet, "/fixture", nil, follow, 15*time.Millisecond)
			if !follow {
				if err == nil || attempts.Load() != 1 {
					t.Fatal("one-shot listener retried")
				}
			} else if err != nil || attempts.Load() != 3 || times[1].Sub(times[0]) < 15*time.Millisecond || times[2].Sub(times[1]) < 30*time.Millisecond {
				t.Fatalf("missing exponential backoff: attempts=%d err=%v", attempts.Load(), err)
			}
		})
	}
}

func TestMessagingPollCancellation(t *testing.T) {
	config := setupHookTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		http.Error(w, "transient", 503)
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "fixture-hook-key")
	rt := &runtime{configPath: config}
	start := time.Now()
	err := rt.doMessagingPoll(ctx, http.MethodPost, "/fixture/ack", nil, true, time.Hour)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("retry ignored cancellation: %v", err)
	}
}

func TestTransientMessagingErrors(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		if !transientMessagingError(&client.StatusError{Status: status}) {
			t.Errorf("did not retry HTTP %d", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 409, 422} {
		if transientMessagingError(&client.StatusError{Status: status}) {
			t.Errorf("retried permanent HTTP %d", status)
		}
	}
	if !transientMessagingError(&url.Error{Op: "Get", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}) || !transientMessagingError(io.ErrUnexpectedEOF) {
		t.Fatal("did not retry transport failure")
	}
	if transientMessagingError(&url.Error{Op: "Get", Err: errors.New("unsupported protocol scheme")}) || transientMessagingError(errors.New("decode response")) {
		t.Fatal("retried permanent request error")
	}
}
