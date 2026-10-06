// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"
)

// BufferRequestBody finishes bounded network reads before transaction admission.
// Handlers can then decode JSON or raw uploads from memory under their write
// fences. Authorization must still be checked in the final transaction.
func BufferRequestBody(w http.ResponseWriter, r *http.Request, limit int64) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	defer controller.SetReadDeadline(time.Time{})
	body := r.Body
	defer body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, body, limit))
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}
