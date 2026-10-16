// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/questions"
)

func TestAskStatusDistinguishesQueuedFailedAndReceived(t *testing.T) {
	for _, tc := range []struct{ state, failure, want string }{{"queued", "", "awaiting receiver"}, {"failed", "deadline", "undelivered: deadline"}, {"handed_off", "", "receiver confirmed"}} {
		t.Run(tc.state, func(t *testing.T) {
			var out bytes.Buffer
			rt := runtime{stdout: &out}
			err := rt.printQuestion(questions.Question{ID: "fixture", Pending: []questions.Pending{{Kind: "inbox", State: "delivered", ReceiptState: tc.state, ReceiptFailure: tc.failure}}})
			if err != nil || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("status=%q err=%v", out.String(), err)
			}
		})
	}
}

// Risk: ask status claims delivery merely because the outbox was dispatched,
// or keeps claiming a picked-up answer is waiting on the person.
func TestAskStatusReportsAnswerPickup(t *testing.T) {
	isolate(t)
	const id = "a0000000-0000-4000-8000-000000000001"
	since := time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, receipt, want string
		picked              *time.Time
	}{
		{"pending", "queued", "answered · not yet picked up (since 2026-10-15T19:00:00Z)", nil},
		{"picked up", "queued", "answered · delivered", &since},
		{"confirmed", "handed_off", "answered · delivered", &since},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/questions/"+id+"/status" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				_ = json.NewEncoder(w).Encode(questions.Question{ID: id, State: "answered", Revision: 2,
					Answer:  &questions.Answer{Answer: "Use local storage.", CreatedAt: since},
					Pending: []questions.Pending{{Kind: "inbox", State: "delivered", ReceiptState: tc.receipt, PickedUpAt: tc.picked}}})
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, errOut := runCLI([]string{"aeon", "ask", "status", id}, "")
			if code != 0 || !strings.Contains(out, tc.want) || !strings.Contains(out, "Use local storage.") {
				t.Fatalf("status=%d %q stderr=%s", code, out, errOut)
			}
			assertNoSecret(t, out+errOut)
		})
	}
}
