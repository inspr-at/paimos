// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"gopkg.in/yaml.v3"
)

func (f *fixture) registerNative(t *testing.T, owner tenant.Principal, project, ref, vendor string) (string, string) {
	t.Helper()
	lease := "native-context-test-lease-" + uid()
	in := map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": ref, "worker_lease": lease, "advertised_capabilities": []string{"inbox"}}
	if vendor != "" {
		in["vendor_session_ref"] = vendor
	}
	w := f.call(owner, "POST", "/api/projects/"+project+"/harness-sessions", in, "")
	expect(t, w, http.StatusCreated)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("registration returned no ID")
	}
	return out.ID, lease
}

// legacyNative retains the binding-time rejection coverage for registrations
// already stored before the creation guard existed. New HTTP registrations
// reject conflicting ownership earlier (registration_alias_test.go).
func (f *fixture) legacyNative(t *testing.T, owner tenant.Principal, project, ref, vendor string) (string, string) {
	t.Helper()
	id, lease := f.session(t, owner, project, "worker", "unmanaged")
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00" + ref))
	var vendorDigest []byte
	if vendor != "" {
		sum := sha256.Sum256([]byte("aeon.harness.ref\x00" + vendor))
		vendorDigest = sum[:]
	}
	f.preGuardMutation(t, `UPDATE harness_sessions SET ref_digest=$2,vendor_ref_digest=$3 WHERE id=$1`, id, refDigest[:], vendorDigest)
	return id, lease
}

func TestNativeContextSurvivesRegistrationGenerations(t *testing.T) {
	for _, scenario := range []string{"person", "role", "project", "vendor_alias", "ref_to_vendor", "vendor_to_ref"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			original := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "assignment-a"))
			ref, vendor := "native-context-ref-"+uid(), ""
			if scenario == "vendor_alias" || scenario == "vendor_to_ref" {
				vendor = "native-context-vendor-" + uid()
			}
			first, _ := f.registerNative(t, f.alice, f.project, ref, vendor)
			bound := f.bind(t, f.alice, original, first, "0")
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, first); err != nil {
				t.Fatal(err)
			}
			owner, project := f.alice, f.project
			if scenario == "person" || strings.Contains(scenario, "vendor") {
				owner = f.bob
			}
			if scenario == "project" {
				project = f.secondProject
			}
			target := f.thread(t, owner, f.role(t, owner, project, "worker", "assignment-b"))
			nextRef, nextVendor := ref, vendor
			switch scenario {
			case "vendor_alias":
				nextRef = "replacement-ref-" + uid()
			case "ref_to_vendor":
				nextRef, nextVendor = "replacement-ref-"+uid(), ref
			case "vendor_to_ref":
				nextRef, nextVendor = vendor, ""
			}
			second, _ := f.legacyNative(t, owner, project, nextRef, nextVendor)
			if first == second {
				t.Fatal("fixture did not create a replacement generation")
			}
			rejected := f.call(owner, "POST", "/api/chat-threads/"+target.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": second}, "")
			expect(t, rejected, 404)
			if !strings.Contains(rejected.Body.String(), "chat binding unavailable") {
				t.Fatal("binding rejected for the wrong reason")
			}
			unchanged := decode[Thread](t, f.call(owner, "GET", "/api/chat-threads/"+target.ID, nil, ""))
			if unchanged.BindingEpoch != "0" || unchanged.Revision != "1" {
				t.Fatal("failed binding changed the target")
			}
			var claimed int
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts WHERE role_id=$1`, target.Role.ID).Scan(&claimed); err != nil || claimed != 0 {
				t.Fatalf("rejected binding retained native aliases: count=%d err=%v", claimed, err)
			}
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, second); err != nil {
				t.Fatal(err)
			}
			// The same native context can resume its own lasting conversation.
			third, lease := f.registerNative(t, f.alice, f.project, ref, vendor)
			resumed := f.bind(t, f.alice, bound, third, "1")
			if resumed.ID != original.ID || resumed.BindingEpoch != "2" {
				t.Fatal("same-person/role continuation lost its identity")
			}
			decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{original.ID, third, "2"}, lease))
			var history int
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_session_bindings WHERE role_id=$1`, original.Role.ID).Scan(&history); err != nil || history != 2 {
				t.Fatalf("binding history lost: count=%d err=%v", history, err)
			}
			// A genuinely fresh reference is available to the other assignment.
			fresh, _ := f.registerNative(t, owner, project, "fresh-native-ref-"+uid(), "")
			f.bind(t, owner, target, fresh, "0")
		})
	}
}

type controlledChatBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *controlledChatBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines chan time.Time
}

func (w *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines <- deadline
	return nil
}

func TestIncompleteBodyDeadlinePrecedesDatabaseAcquisition(t *testing.T) {
	f := newFixture(t)
	ctx, expire := context.WithCancelCause(tenant.WithPrincipal(t.Context(), f.alice))
	defer expire(context.Canceled)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	body := &controlledChatBody{ReadCloser: reader, started: make(chan struct{})}
	r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/chat-roles", body).WithContext(ctx)
	w := &deadlineRecorder{httptest.NewRecorder(), make(chan time.Time, 4)}
	done := make(chan struct{})
	go func() { f.mux.ServeHTTP(w, r); close(done) }()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("body read never began")
	}
	if got := f.d.App.Stat().AcquiredConns(); got != 0 {
		t.Errorf("incomplete body acquired %d database connections", got)
	}
	select {
	case deadline := <-w.deadlines:
		if deadline.IsZero() {
			t.Error("body read has no deadline")
		}
	default:
		t.Error("HTTP body deadline was not established before reading")
	}
	// Establish expiry explicitly; wall-clock timers only guard against hangs.
	expire(context.DeadlineExceeded)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = writer.CloseWithError(os.ErrDeadlineExceeded)
		<-done
		t.Fatal("incomplete body outlived its deadline")
	}
	expect(t, w.ResponseRecorder, http.StatusRequestTimeout)
	if got := f.d.App.Stat().AcquiredConns(); got != 0 {
		t.Fatalf("expired body retained %d database connections", got)
	}
	if _, err := writer.Write([]byte("late body")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expired read was not closed: %v", err)
	}
	// No connection or transaction remains occupied by the stalled upload.
	f.role(t, f.bob, f.project, "lead", "lead")
}

func TestOfflineWaitContractBoundsAndImmutableReceiptDeadline(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	policy, ok := document.Components.Schemas["ChatOfflineWaitPolicy"]
	if !ok {
		t.Fatal("chat contract cannot express the accepted offline-wait policy")
	}
	props := policy["properties"].(map[string]any)
	wait := props["offline_wait_seconds"].(map[string]any)
	if wait["type"] != "integer" || wait["minimum"] != 600 || wait["maximum"] != 86400 || wait["default"] != 3600 {
		t.Fatalf("offline-wait bounds differ from the approved choice: %v", wait)
	}
	for _, method := range []string{"get", "put"} {
		if _, ok := document.Paths["/chat/offline-wait-policy"][method]; !ok {
			t.Fatalf("missing person/workspace policy %s contract", method)
		}
	}
	receipt := document.Components.Schemas["ChatReceipt"]
	deadline, ok := receipt["properties"].(map[string]any)["delivery_deadline"].(map[string]any)
	if !ok || deadline["type"] != "string" || deadline["format"] != "date-time" || deadline["readOnly"] != true || deadline["x-aeon-immutable"] != true {
		t.Fatal("receipt lacks the immutable accepted delivery deadline")
	}
	required := receipt["required"].([]any)
	found := false
	for _, property := range required {
		found = found || property == "delivery_deadline"
	}
	if !found {
		t.Fatal("accepted delivery deadline is optional")
	}
}
