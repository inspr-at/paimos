// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

const MutationEventHeader = "X-Aeon-Event-Ids"
const maxReceiptEvents = 1024

type receiptKey struct{}
type mutationReceipt struct {
	ids      []int64
	overflow bool
}

// RecordMutation records the exact appended ID in this request's receipt.
// It is optional for non-HTTP callers. A failed transaction gets no 2xx receipt.
func RecordMutation(ctx context.Context, id int64) {
	receipt, _ := ctx.Value(receiptKey{}).(*mutationReceipt)
	if receipt == nil || id <= 0 || receipt.overflow {
		return
	}
	if len(receipt.ids) == maxReceiptEvents {
		receipt.ids = nil
		receipt.overflow = true
		return
	}
	receipt.ids = append(receipt.ids, id)
}

// WithMutationReceipt leaves the response body unchanged. The handler must write
// success only after committing; unsuccessful and empty receipts have no header.
func WithMutationReceipt(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		receipt := &mutationReceipt{}
		next(&receiptWriter{ResponseWriter: w, receipt: receipt}, r.WithContext(context.WithValue(r.Context(), receiptKey{}, receipt)))
	}
}

type receiptWriter struct {
	http.ResponseWriter
	receipt *mutationReceipt
	written bool
}

func (w *receiptWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if status >= 200 && status < 300 && !w.receipt.overflow && len(w.receipt.ids) > 0 {
		ids := make([]string, len(w.receipt.ids))
		for i, id := range w.receipt.ids {
			ids[len(ids)-i-1] = strconv.FormatInt(id, 10)
		}
		w.Header().Set(MutationEventHeader, strings.Join(ids, ","))
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *receiptWriter) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
