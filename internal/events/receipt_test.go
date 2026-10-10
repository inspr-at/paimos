// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMutationReceiptIsExactBoundedAndSuccessOnly(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status, count int
		want          string
	}{
		{"accepted", 200, 2, "11,10"}, {"no change", 204, 0, ""}, {"rollback", 409, 2, ""}, {"overflow", 200, maxReceiptEvents + 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := WithMutationReceipt(func(w http.ResponseWriter, r *http.Request) {
				for i := 0; i < tc.count; i++ {
					RecordMutation(r.Context(), int64(10+i))
				}
				w.WriteHeader(tc.status)
			})
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest("PATCH", "/", nil))
			if got := response.Header().Get(MutationEventHeader); got != tc.want {
				t.Fatalf("receipt = %q, want %q", got, tc.want)
			}
		})
	}
}
