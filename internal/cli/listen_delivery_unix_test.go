// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

// Risk: an interrupted delivery listener waits forever for a vendor CLI whose
// descendant keeps the output pipes open. The listener owns its shutdown, so
// the command watchdog never exits for it, and the descendant outlives it.
func TestSignalDeliveryListenerEndsVendorDescendants(t *testing.T) {
	if os.Getenv("AEON_SIGNAL_CHILD") == "1" {
		runDeliverySignalChild()
		return
	}
	for _, mode := range []string{"deliver", "session-deliver"} {
		t.Run(mode, func(t *testing.T) {
			fixture := processtest.New(t)
			bin := t.TempDir()
			// Private synthetic executable; no vendor binary or account is accessed.
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"+fixture.Script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ref := filepath.Join(t.TempDir(), "target-ref")
			if err := os.WriteFile(ref, []byte("fixture-thread"), 0600); err != nil {
				t.Fatal(err)
			}
			// SIGINT only after the descendant holds the inherited pipes.
			code, output := interruptChildAfter(t, "TestSignalDeliveryListenerEndsVendorDescendants", mode, []string{"PATH=" + bin, "AEON_SIGNAL_REF=" + ref}, fixture.Ready)
			if code != 130 || !strings.Contains(output, "aeon: interrupted") {
				t.Fatalf("interrupted delivery listener: exit %d\n%s", code, output)
			}
			fixture.AssertExited(t)
		})
	}
}

// runDeliverySignalChild hands one pending message to the codex adapter and
// waits there; the parent interrupts it once the vendor descendant runs.
func runDeliverySignalChild() {
	mode := os.Getenv("AEON_SIGNAL_MODE")
	base := "/api/projects/" + transcriptProjectID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/projects/lookup":
			_, _ = w.Write([]byte(`{"id":"` + transcriptProjectID + `"}`))
		case r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"` + transcriptProjectID + `","name":"worker"}}`))
		case mode == "deliver" && r.Method == http.MethodPost && r.URL.Path == base+"/messages/delivery-claim":
			_ = json.NewEncoder(w).Encode(inbox.DeliveryWork{ID: hookMessageID, LeaseToken: "fixture-lease", State: "pending", TargetRef: "fixture-thread", MaximumLevel: "simple", Message: &inbox.CompatMessage{ID: hookMessageID, Body: "hello", Level: "simple"}})
		case mode == "session-deliver" && r.Method == http.MethodGet && r.URL.Path == harnessPath(transcriptProjectID, hookSessionID):
			_, _ = w.Write([]byte(`{"id":"` + hookSessionID + `","harness":"codex","management_mode":"unmanaged","phase":"idle"}`))
		case mode == "session-deliver" && r.Method == http.MethodGet && r.URL.Path == "/api/inbox/messages":
			_ = json.NewEncoder(w).Encode(inbox.Page{Items: []inbox.Message{hookFixtureMessage()}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_ = os.Setenv("AEON_URL", srv.URL)
	_ = os.Setenv("AEON_API_KEY", "fixture-signal-key")
	args := []string{"aeon", "listen", "--project", "AEON", "--as", "codex:receiver", "--deliver", "codex", "--follow"}
	if mode == "session-deliver" {
		args = []string{"aeon", "listen", "--project", "AEON", "--session", hookSessionID, "--deliver", "codex", "--target-ref-file", os.Getenv("AEON_SIGNAL_REF"), "--follow"}
	}
	var stdout, stderr bytes.Buffer
	code := RunMessaging(args, strings.NewReader(""), &stdout, &stderr)
	fmt.Fprint(os.Stderr, stdout.String()+stderr.String())
	os.Exit(code)
}
