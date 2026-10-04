// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"github.com/inspr-at/paimos/internal/questions"
	"strings"
	"testing"
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
