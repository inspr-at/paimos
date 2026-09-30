// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type httpError struct {
	status int
	msg    string
	code   string
	node   *nodeJSON
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) *httpError {
	return &httpError{status: http.StatusBadRequest, msg: msg}
}
func notFound(msg string) *httpError { return &httpError{status: http.StatusNotFound, msg: msg} }
func conflict(msg string) *httpError { return &httpError{status: http.StatusConflict, msg: msg} }
func unprocessable(msg string) *httpError {
	return &httpError{status: http.StatusUnprocessableEntity, msg: msg}
}

func unprocessableCoded(msg, code string) *httpError {
	return &httpError{status: http.StatusUnprocessableEntity, msg: msg, code: code}
}

func conflictCoded(msg, code string) *httpError {
	return &httpError{status: http.StatusConflict, msg: msg, code: code}
}

// errorBody adds an optional code without changing errors that only have a sentence.
// A set node and an empty code stay {"error","node"}, as before.
type errorBody struct {
	Error string    `json:"error"`
	Code  string    `json:"code,omitempty"`
	Node  *nodeJSON `json:"node,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteError(w, status, msg)
}

func writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		if he.code != "" || he.node != nil {
			writeJSON(w, he.status, errorBody{Error: he.msg, Code: he.code, Node: he.node})
			return
		}
		writeError(w, he.status, he.msg)
		return
	}
	slog.Error("nodes", "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func requirePrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.TenantID == "" || p.ID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return tenant.Principal{}, false
	}
	return p, true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil || len(b) == 0 {
		writeError(w, http.StatusBadRequest, "bad request")
		return nil, false
	}
	return b, true
}

func decodeJSON(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return badRequest("bad request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return badRequest("bad request")
	}
	return nil
}

func decodeObject(b []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return nil, badRequest("bad request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, badRequest("bad request")
	}
	return raw, nil
}
