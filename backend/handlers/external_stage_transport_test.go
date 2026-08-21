// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func externalStageRequestWithPrincipal(t *testing.T, request *http.Request, keyID int64) *http.Request {
	t.Helper()
	principal, err := auth.NewAPIKeyPrincipal(keyID, keyID, auth.ScopeSet{auth.ScopeAll: {}})
	if err != nil {
		t.Fatal(err)
	}
	return request.WithContext(auth.WithPrincipal(request.Context(), principal))
}

func TestExternalStageAPIKeyAuthConcealsMalformedCredentials(t *testing.T) {
	var canonical []byte
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "basic", values: []string{"Basic Zm9vOmJhcg=="}},
		{name: "empty bearer", values: []string{"Bearer "}},
		{name: "bearer whitespace", values: []string{"Bearer secret with-space"}},
		{name: "duplicate", values: []string{"Bearer first", "Bearer second"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := RequestIDMiddleware(ExternalStageAPIKeyAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			})))
			request := httptest.NewRequest(http.MethodGet, externalstage.ExternalPullPath, nil)
			for _, value := range test.values {
				request.Header.Add("Authorization", value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if called || recorder.Code != http.StatusNotFound {
				t.Fatalf("called=%t status=%d body=%s", called, recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "private, no-store" ||
				recorder.Header().Get("Content-Type") != "application/problem+json" ||
				recorder.Header().Get("X-Permissions-Epoch") != "" ||
				recorder.Header().Get("Allow") != "" ||
				strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "first") {
				t.Fatalf("credential concealment failed: headers=%v body=%s", recorder.Header(), recorder.Body.String())
			}
			var problem map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			delete(problem, "instance")
			delete(problem, "request_id")
			normalized, err := json.Marshal(problem)
			if err != nil {
				t.Fatal(err)
			}
			if canonical == nil {
				canonical = normalized
			} else if !bytes.Equal(canonical, normalized) {
				t.Fatalf("malformed credential response drifted: got=%s want=%s", normalized, canonical)
			}
		})
	}
}

func TestExternalStageSecretRequiresOneCanonicalRawURLValue(t *testing.T) {
	secret := bytes.Repeat([]byte{0xfb}, externalstage.OneTimeSecretBytes)
	canonical := base64.RawURLEncoding.EncodeToString(secret)
	tests := []struct {
		name   string
		values []string
		ok     bool
	}{
		{name: "canonical", values: []string{canonical}, ok: true},
		{name: "missing"},
		{name: "duplicate", values: []string{canonical, canonical}},
		{name: "padded", values: []string{base64.URLEncoding.EncodeToString(secret)}},
		{name: "standard alphabet", values: []string{base64.RawStdEncoding.EncodeToString(secret)}},
		{name: "short", values: []string{base64.RawURLEncoding.EncodeToString(secret[:31])}},
		{name: "long", values: []string{base64.RawURLEncoding.EncodeToString(append(secret, 1))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, externalstage.ExternalPullPath, nil)
			for _, value := range test.values {
				request.Header.Add(externalstage.HandoffSecretHeader, value)
			}
			got, err := externalStageSecret(request)
			if test.ok {
				if err != nil || !bytes.Equal(got, secret) {
					t.Fatalf("secret=%x err=%v", got, err)
				}
				zeroBytes(got)
				return
			}
			if err != externalstage.ErrNotFound || got != nil {
				t.Fatalf("secret=%x err=%v", got, err)
			}
		})
	}
}

func TestExternalStageJSONDecoderPinsEnvelopeAndSingleObject(t *testing.T) {
	type payload struct {
		Sequence int64 `json:"sequence"`
	}
	valid := func(body string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, externalstage.ExternalAcceptPath, strings.NewReader(body))
		request.Header["Content-Type"] = []string{externalstage.MediaTypeV1}
		request.Header["Accept"] = []string{externalstage.MediaTypeV1}
		return request
	}
	tests := []struct {
		name   string
		mutate func(*http.Request)
		body   string
		ok     bool
	}{
		{name: "valid", body: `{"sequence":1}`, ok: true},
		{name: "empty", body: ` `},
		{name: "unknown", body: `{"sequence":1,"extra":true}`},
		{name: "duplicate", body: `{"sequence":1,"sequence":2}`},
		{name: "second value", body: `{"sequence":1}{"sequence":2}`},
		{name: "content type parameter", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Content-Type", externalstage.MediaTypeV1+"; charset=utf-8")
		}},
		{name: "duplicate content type", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Add("Content-Type", externalstage.MediaTypeV1)
		}},
		{name: "wrong accept", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Accept", "application/json")
		}},
		{name: "content encoding", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Content-Encoding", "identity")
		}},
		{name: "oversize", body: `{"sequence":1,"` + strings.Repeat("x", externalStageMaxBody) + `":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid(test.body)
			if test.mutate != nil {
				test.mutate(request)
			}
			recorder := httptest.NewRecorder()
			var got payload
			err := decodeExternalStageJSON(recorder, request, externalstage.MediaTypeV1, externalstage.MediaTypeV1, &got)
			if test.ok {
				if err != nil || got.Sequence != 1 {
					t.Fatalf("payload=%+v err=%v", got, err)
				}
				return
			}
			if err != externalstage.ErrInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestExternalStagePullRejectsEveryBodyEnvelopeBeforeService(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
		body   string
	}{
		{name: "body", body: `{}`},
		{name: "content type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", externalstage.MediaTypeV1) }},
		{name: "content encoding", mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "identity") }},
		{name: "missing accept", mutate: func(r *http.Request) { r.Header.Del("Accept") }},
		{name: "duplicate accept", mutate: func(r *http.Request) { r.Header.Add("Accept", externalstage.MediaTypeV1) }},
		{name: "transfer encoding", mutate: func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, externalstage.ExternalPullPath, strings.NewReader(test.body))
			request.Header.Set("Accept", externalstage.MediaTypeV1)
			if test.mutate != nil {
				test.mutate(request)
			}
			request = externalStageRequestWithPrincipal(t, request, 1)
			recorder := httptest.NewRecorder()
			pullExternalStageHandoff(recorder, request)
			if recorder.Code != http.StatusNotFound || recorder.Header().Get("X-Permissions-Epoch") != "" {
				t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestExternalStageRateLimitKeepsAdmittedKeysAtCapacity(t *testing.T) {
	externalStageRates.Lock()
	previous := externalStageRates.entries
	externalStageRates.entries = make(map[int64]externalStageRateEntry, 1024)
	now := time.Now().UTC()
	for id := int64(1); id <= 1024; id++ {
		externalStageRates.entries[id] = externalStageRateEntry{window: now, count: 1}
	}
	externalStageRates.Unlock()
	t.Cleanup(func() {
		externalStageRates.Lock()
		externalStageRates.entries = previous
		externalStageRates.Unlock()
	})

	called := 0
	handler := externalStageRateLimit(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	admitted := externalStageRequestWithPrincipal(t, httptest.NewRequest(http.MethodGet, "/", nil), 1)
	admittedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(admittedRecorder, admitted)
	if called != 1 || admittedRecorder.Code != http.StatusOK {
		t.Fatalf("existing key called=%d status=%d", called, admittedRecorder.Code)
	}

	newKey := externalStageRequestWithPrincipal(t, httptest.NewRequest(http.MethodGet, "/", nil), 1025)
	newRecorder := httptest.NewRecorder()
	handler.ServeHTTP(newRecorder, newKey)
	if called != 1 || newRecorder.Code != http.StatusNotFound || newRecorder.Header().Get("X-Permissions-Epoch") != "" {
		t.Fatalf("new key called=%d status=%d headers=%v", called, newRecorder.Code, newRecorder.Header())
	}
}
