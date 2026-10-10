// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHeartbeatProjectIdentityAvoidsRichListsAndRechecksAccess(t *testing.T) {
	var calls []hbCall
	var revoked atomic.Bool
	var lookups, richReads, beats atomic.Int32
	srv := hbServer(t, &calls, func(r *http.Request, _ map[string]any, w http.ResponseWriter) bool {
		switch r.URL.Path {
		case "/api/projects/lookup":
			lookups.Add(1)
			if r.URL.Query().Get("ref") != "AEON" {
				t.Error("lookup lost the project reference")
			}
			if revoked.Load() {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"project key \"AEON\" not found"}`)
				return true
			}
		case "/api/nodes", "/api/kinds", "/api/projects":
			richReads.Add(1)
		default:
			if strings.HasSuffix(r.URL.Path, "/heartbeat") {
				beats.Add(1)
			}
		}
		return false
	})
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	opts := heartbeatTestOptions(t.TempDir())
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer session.hold.release()
	for range 2 {
		if err := rt.heartbeatBeat(context.Background(), opts, heartbeatDeps{}, &session); err != nil {
			t.Fatal(err)
		}
	}
	revoked.Store(true)
	if err := rt.heartbeatBeat(context.Background(), opts, heartbeatDeps{}, &session); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("revoked identity lookup: %v", err)
	}
	if lookups.Load() != 4 || richReads.Load() != 0 || beats.Load() != 2 {
		t.Fatalf("lookup=%d rich=%d heartbeat=%d, want 4/0/2", lookups.Load(), richReads.Load(), beats.Load())
	}
	t.Log("registration + 3 beats: 4 exact lookups, 0 kind/project/node lists; revoked lookup prevents the third heartbeat write")
}

func TestListenSetupUsesOneExactProjectLookup(t *testing.T) {
	for _, program := range []string{"aeon", "paimos"} {
		t.Run(program, func(t *testing.T) {
			isolate(t)
			var lookups, richReads, inboxReads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/projects/lookup":
					lookups.Add(1)
					if r.URL.Query().Get("ref") != "Old & alias" {
						t.Error("exact reference was not URL-encoded")
					}
					fmt.Fprint(w, `{"id":"`+transcriptProjectID+`","key":"PRJ-1","title":"AEON","state":"active"}`)
				case "/api/me":
					fmt.Fprint(w, `{"principal":{"id":"44444444-4444-4444-8444-444444444444","name":"worker"}}`)
				case "/api/nodes", "/api/kinds", "/api/projects":
					richReads.Add(1)
					http.Error(w, "rich lookup forbidden by fixture", 500)
				case "/api/projects/" + transcriptProjectID + "/messages/listen":
					inboxReads.Add(1)
					fmt.Fprint(w, `{"items":[],"next_after":0}`)
				default:
					t.Errorf("unexpected setup request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			t.Setenv("PAIMOS_URL", srv.URL)
			t.Setenv("PAIMOS_API_KEY", testKey)
			code, _, stderr := runCLIWithMessaging([]string{program, "listen", "--project", "Old & alias", "--as", "codex:worker"}, "")
			if code != 0 || lookups.Load() != 1 || richReads.Load() != 0 || inboxReads.Load() != 1 {
				t.Fatalf("listen code=%d lookup=%d rich=%d inbox=%d: %s", code, lookups.Load(), richReads.Load(), inboxReads.Load(), stderr)
			}
		})
	}
}

func TestProjectIdentityErrorsNeverFallBackToRichLists(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusConflict, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/projects/lookup" {
					t.Errorf("fallback request: %s", r.URL.Path)
				}
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(map[string]string{"error": "lookup fixture failure", "id": "invalid"})
			}))
			defer srv.Close()
			rt, _, _ := heartbeatRuntime(t, srv)
			if _, err := rt.harnessProjectCtx(context.Background(), "AEON"); err == nil {
				t.Fatal("lookup failure or invalid identity accepted")
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d, want 1", requests.Load())
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := rt.harnessProjectCtx(ctx, "AEON"); err != context.Canceled {
				t.Fatalf("pre-canceled lookup: %v", err)
			}
			if _, err := rt.harnessProjectCtx(context.Background(), strings.Repeat("x", 1025)); err == nil {
				t.Fatal("oversized reference accepted")
			}
			if requests.Load() != 1 {
				t.Fatal("invalid local lookup performed HTTP work")
			}
		})
	}
}
