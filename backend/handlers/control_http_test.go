// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type controlProbeBody struct {
	Action string      `json:"action"`
	Amount json.Number `json:"amount"`
}

const controlBodyCanary = "CANARY-CONTROL-BODY-809"

func controlDecodeRequest(contentType, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/control-commands/17", strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}

func decodeControlProbe(t *testing.T, request *http.Request, maxBytes int64) (*httptest.ResponseRecorder, controlProbeBody, error) {
	t.Helper()
	recorder := httptest.NewRecorder()
	var decoded controlProbeBody
	err := DecodeControlJSON(recorder, request, maxBytes, &decoded)
	return recorder, decoded, err
}

func assertControlRefusal(t *testing.T, err error, want *ControlRequestError) {
	t.Helper()
	var got *ControlRequestError
	if !errors.As(err, &got) {
		t.Fatalf("error %v is not a ControlRequestError", err)
	}
	if got.Code != want.Code || got.Status != want.Status {
		t.Fatalf("refusal = %s/%d, want %s/%d", got.Code, got.Status, want.Code, want.Status)
	}
}

// The framing matrix. Every accepted spelling is listed, so widening it
// later has to be a deliberate edit to this table.
func TestDecodeControlJSONFramingMatrix(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		encoding    string
		extraType   string
		want        *ControlRequestError
	}{
		{name: "canonical media type", contentType: "application/json"},
		{name: "media type case variant", contentType: "Application/JSON", want: ErrControlUnsupportedMediaType},
		{name: "charset parameter is a second spelling", contentType: "application/json; charset=utf-8", want: ErrControlUnsupportedMediaType},
		{name: "charset parameter without space", contentType: "application/json;charset=UTF-8", want: ErrControlUnsupportedMediaType},
		{name: "trailing semicolon", contentType: "application/json;", want: ErrControlUnsupportedMediaType},
		{name: "surrounding whitespace", contentType: " application/json ", want: ErrControlUnsupportedMediaType},
		{name: "comma joined", contentType: "application/json, application/json", want: ErrControlUnsupportedMediaType},
		{name: "problem json is not request json", contentType: "application/problem+json", want: ErrControlUnsupportedMediaType},
		{name: "text json", contentType: "text/json", want: ErrControlUnsupportedMediaType},
		{name: "form encoding", contentType: "application/x-www-form-urlencoded", want: ErrControlUnsupportedMediaType},
		{name: "missing content type", contentType: "", want: ErrControlUnsupportedMediaType},
		{name: "two content-type headers", contentType: "application/json", extraType: "application/json", want: ErrControlUnsupportedMediaType},
		{name: "gzip content encoding", contentType: "application/json", encoding: "gzip", want: ErrControlContentEncoding},
		{name: "identity content encoding", contentType: "application/json", encoding: "identity", want: ErrControlContentEncoding},
		{name: "no content encoding header", contentType: "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := controlDecodeRequest(tc.contentType, `{"action":"run.pause","amount":1}`)
			if tc.encoding != "" {
				request.Header.Set("Content-Encoding", tc.encoding)
			}
			if tc.extraType != "" {
				request.Header.Add("Content-Type", tc.extraType)
			}
			recorder, decoded, err := decodeControlProbe(t, request, 4096)
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q on the decode path", got)
			}
			if tc.want == nil {
				if err != nil {
					t.Fatalf("accepted framing was refused: %v", err)
				}
				if decoded.Action != "run.pause" {
					t.Fatalf("body did not decode: %#v", decoded)
				}
				return
			}
			assertControlRefusal(t, err, tc.want)
		})
	}
}

// The body matrix: anything with two readings refuses.
func TestDecodeControlJSONBodyMatrix(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *ControlRequestError
	}{
		{name: "canonical object", body: `{"action":"run.pause","amount":3}`},
		{name: "empty body", body: ``, want: ErrControlBodyMissing},
		{name: "whitespace body", body: "  \n\t ", want: ErrControlBodyMissing},
		{name: "unknown field", body: `{"action":"run.pause","note":"` + controlBodyCanary + `"}`, want: ErrControlBodyUnknownField},
		{name: "duplicate field", body: `{"action":"run.pause","action":"run.cancel.running"}`, want: ErrControlBodyDuplicateField},
		{name: "case-variant duplicate field", body: `{"action":"run.pause","Action":"run.cancel.running"}`, want: ErrControlBodyDuplicateField},
		{name: "unicode long-s duplicate field", body: `{"status":"accepted","ſtatus":"rejected"}`, want: ErrControlBodyDuplicateField},
		{name: "escaped unicode long-s duplicate field", body: `{"status":"accepted","\u017ftatus":"rejected"}`, want: ErrControlBodyDuplicateField},
		{name: "unicode kelvin duplicate field", body: `{"k":1,"K":2}`, want: ErrControlBodyDuplicateField},
		{name: "duplicate nested field", body: `{"action":"run.pause","amount":1,"extra":{"a":1,"a":2}}`, want: ErrControlBodyDuplicateField},
		{name: "trailing second value", body: `{"action":"run.pause"}{"action":"run.cancel.running"}`, want: ErrControlBodyTrailingContent},
		{name: "trailing garbage", body: `{"action":"run.pause"} ` + controlBodyCanary, want: ErrControlBodyTrailingContent},
		{name: "trailing array", body: `{"action":"run.pause"}[]`, want: ErrControlBodyTrailingContent},
		{name: "malformed json", body: `{"action":`, want: ErrControlBodyMalformed},
		{name: "not an object", body: `"run.pause"`, want: ErrControlBodyMalformed},
		{name: "wrong field type", body: `{"action":42}`, want: ErrControlBodyMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, decoded, err := decodeControlProbe(t, controlDecodeRequest("application/json", tc.body), 4096)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("canonical body was refused: %v", err)
				}
				if decoded.Action != "run.pause" || decoded.Amount.String() != "3" {
					t.Fatalf("body decoded wrong: %#v", decoded)
				}
				return
			}
			assertControlRefusal(t, err, tc.want)
		})
	}

	// An empty Content-Encoding header is present, not absent.
	blankEncoding := controlDecodeRequest("application/json", `{"action":"run.pause"}`)
	blankEncoding.Header.Add("Content-Encoding", "")
	_, _, blankErr := decodeControlProbe(t, blankEncoding, 4096)
	assertControlRefusal(t, blankErr, ErrControlContentEncoding)

	// The nested-duplicate case above only reaches the scanner because
	// "extra" is unknown to the probe struct; assert the scanner runs
	// before the decoder rather than after it.
	_, _, err := decodeControlProbe(t,
		controlDecodeRequest("application/json", `{"unknown":{"a":1,"a":2}}`), 4096)
	assertControlRefusal(t, err, ErrControlBodyDuplicateField)
}

func TestDecodeControlJSONEnforcesTheCallersLimit(t *testing.T) {
	oversized := `{"action":"` + strings.Repeat("x", 400) + `"}`
	_, _, err := decodeControlProbe(t, controlDecodeRequest("application/json", oversized), 64)
	assertControlRefusal(t, err, ErrControlBodyTooLarge)

	// The same body is fine when the caller allows it — the limit is per
	// caller, not a package-wide constant.
	if _, _, err := decodeControlProbe(t, controlDecodeRequest("application/json", oversized), 4096); err != nil {
		t.Fatalf("body within the caller's limit was refused: %v", err)
	}
}

// Deep nesting inside the byte budget must refuse, not recurse until the
// stack gives out.
func TestDecodeControlJSONRefusesDeepNesting(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200)
	_, _, err := decodeControlProbe(t, controlDecodeRequest("application/json", deep), 4096)
	assertControlRefusal(t, err, ErrControlBodyMalformed)
}

// A refusal is private, uncacheable, closed-coded, and quotes nothing the
// caller sent in the body.
func TestWriteControlRequestErrorIsPrivateAndClosed(t *testing.T) {
	pathCanary := "CANARY-CONTROL-PATH-809"
	queryCanary := "CANARY-CONTROL-QUERY-809"
	requestIDCanary := "CANARY-CONTROL-REQUEST-ID-809"
	request := controlDecodeRequest("application/json", `{"note":"`+controlBodyCanary+`"}`)
	request.URL.Path = "/api/control-commands/" + pathCanary
	request.URL.RawQuery = "secret=" + queryCanary
	request.Header.Set(RequestIDHeader, requestIDCanary)
	request.Header.Set(AIRequestIDHeader, requestIDCanary+"-ALT")
	recorder, _, err := decodeControlProbe(t, request, 4096)
	assertControlRefusal(t, err, ErrControlBodyUnknownField)

	WriteControlRequestError(recorder, request, err)
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q on the error path", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	body, _ := io.ReadAll(recorder.Body)
	if !strings.Contains(string(body), ErrControlBodyUnknownField.Code) {
		t.Fatalf("problem body lost its closed code: %s", body)
	}
	for _, canary := range []string{controlBodyCanary, pathCanary, queryCanary, requestIDCanary, "note"} {
		if strings.Contains(string(body), canary) {
			t.Fatalf("problem body quoted caller input %q: %s", canary, body)
		}
	}
	if strings.Contains(string(body), `"instance"`) {
		t.Fatalf("control problem body exposed an instance field: %s", body)
	}
	if reflected := recorder.Header().Get(RequestIDHeader); reflected == "" || strings.Contains(reflected, requestIDCanary) {
		t.Fatalf("control response reflected caller request id: %q", reflected)
	}
	for name, values := range recorder.Header() {
		joined := strings.Join(values, ",")
		for _, canary := range []string{controlBodyCanary, pathCanary, queryCanary, requestIDCanary} {
			if strings.Contains(joined, canary) {
				t.Fatalf("response header %s reflected %q: %q", name, canary, joined)
			}
		}
	}

	// An unclassified error does not get to choose the words.
	generic := httptest.NewRecorder()
	WriteControlRequestError(generic, request, errors.New("boom: "+controlBodyCanary))
	genericBody, _ := io.ReadAll(generic.Body)
	if strings.Contains(string(genericBody), controlBodyCanary) || strings.Contains(string(genericBody), "boom") {
		t.Fatalf("unclassified error text reached the response: %s", genericBody)
	}
	if generic.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("unclassified error response was cacheable")
	}

	// Merely having the exported type does not admit a new code/status pair.
	// Only the package's fixed sentinel values are part of the vocabulary.
	const forgedCanary = "CANARY-FORGED-CONTROL-ERROR-809"
	forged := httptest.NewRecorder()
	WriteControlRequestError(forged, request, &ControlRequestError{
		Status: http.StatusTeapot,
		Code:   forgedCanary,
	})
	if forged.Code != http.StatusBadRequest || strings.Contains(forged.Body.String(), forgedCanary) ||
		!strings.Contains(forged.Body.String(), ErrControlBodyMalformed.Code) {
		t.Fatalf("forged typed refusal escaped the closed vocabulary: status=%d body=%s", forged.Code, forged.Body.String())
	}
}

func TestRequestIDMiddlewareNeverReflectsControlRequestIDs(t *testing.T) {
	const primaryCanary = "CANARY-CONTROL-REQUEST-ID-PRIMARY-809"
	const alternateCanary = "CANARY-CONTROL-REQUEST-ID-ALTERNATE-809"
	request := controlDecodeRequest("application/json", `{}`)
	request.Header.Set(RequestIDHeader, primaryCanary)
	request.Header.Set(AIRequestIDHeader, alternateCanary)

	recorder := httptest.NewRecorder()
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := requestIDFromRequest(r); got == primaryCanary || got == alternateCanary {
			t.Fatalf("control context kept caller request id %q", got)
		}
		WriteControlRequestError(w, r, ErrControlBodyMalformed)
	}))
	handler.ServeHTTP(recorder, request)

	response := recorder.Header().Get(RequestIDHeader) + recorder.Body.String()
	for _, canary := range []string{primaryCanary, alternateCanary} {
		if strings.Contains(response, canary) {
			t.Fatalf("control response reflected caller request id %q: %s", canary, response)
		}
	}
	if recorder.Header().Get(RequestIDHeader) == "" {
		t.Fatal("control response lost its server-generated correlation id")
	}

	// This is a control-only privacy rule. Ordinary API traffic preserves
	// the existing caller-correlation contract.
	ordinary := httptest.NewRecorder()
	ordinaryRequest := httptest.NewRequest(http.MethodGet, "/api/issues/17", nil)
	ordinaryRequest.Header.Set(RequestIDHeader, primaryCanary)
	RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := requestIDFromRequest(r); got != primaryCanary {
			t.Fatalf("ordinary request id = %q, want %q", got, primaryCanary)
		}
	})).ServeHTTP(ordinary, ordinaryRequest)
	if got := ordinary.Header().Get(RequestIDHeader); got != primaryCanary {
		t.Fatalf("ordinary response request id = %q, want %q", got, primaryCanary)
	}
}

func TestControlCachePolicyMiddlewareCoversGateFailures(t *testing.T) {
	recorder := httptest.NewRecorder()
	ControlCachePolicyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})).ServeHTTP(recorder, controlDecodeRequest("application/json", "{}"))
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("gate failure was cacheable: %q", got)
	}
}

func TestClassifiedControlCachePolicyCoversEveryFamilyAndNotNearMisses(t *testing.T) {
	paths := []string{
		"/api/agent-mode/deliveries/PAI-809/control-capability-grants",
		"/api/agent-mode/deliveries/PAI-809/control-commands",
		"/api/agent-mode/control-capability-grants/17",
		"/api/agent-mode/control-commands/17",
		"/api/runs/17/control-capability-leases",
		"/api/runs/17/input-requests",
		"/api/runs/17/control-commands",
		"/api/control-capability-leases/17",
		"/api/control-commands/17",
	}
	handler := ClassifiedControlCachePolicyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "closed refusal", http.StatusForbidden)
	}))
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("classified response Cache-Control = %q", got)
			}
		})
	}

	nearMiss := httptest.NewRecorder()
	handler.ServeHTTP(nearMiss, httptest.NewRequest(http.MethodPost, "/api/control-commands/17/extra", nil))
	if got := nearMiss.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("near-miss cache policy was overwritten: %q", got)
	}
}

// ── Idempotency-Key ──────────────────────────────────────────────────

const (
	canonicalControlUUID = "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	canonicalControlULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func idempotencyRequest(values ...string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/control-commands/17", nil)
	for _, value := range values {
		request.Header.Add(idempotencyHeader, value)
	}
	return request
}

func TestControlIdempotencyKeyAcceptsOnlyCanonicalForms(t *testing.T) {
	accepted := []string{
		canonicalControlUUID,
		canonicalControlULID,
		// Every assigned UUID version, and every RFC variant nibble.
		"00000000-0000-1000-8000-000000000000",
		"ffffffff-ffff-2fff-9fff-ffffffffffff",
		"0123abcd-4567-3890-abcd-ef0123456789",
		"0123abcd-4567-4890-bbcd-ef0123456789",
		"0123abcd-4567-5890-8bcd-ef0123456789",
		"0123abcd-4567-6890-9bcd-ef0123456789",
		"0123abcd-4567-7890-abcd-ef0123456789",
		"0123abcd-4567-8890-bbcd-ef0123456789",
		// ULID boundaries: smallest and largest legal timestamp prefix.
		"00000000000000000000000000",
		"7ZZZZZZZZZZZZZZZZZZZZZZZZZ",
	}
	for _, key := range accepted {
		t.Run("accept/"+key, func(t *testing.T) {
			digest, err := ControlIdempotencyKeyDigest(idempotencyRequest(key))
			if err != nil {
				t.Fatalf("canonical key %q was refused: %v", key, err)
			}
			if digest != sha256.Sum256([]byte(key)) {
				t.Fatal("digest is not the SHA-256 of the validated header")
			}
			// Nothing but the digest comes back, so there is no raw value
			// for a caller to log by accident.
			if strings.Contains(string(digest[:]), key) {
				t.Fatal("raw key survived into the digest")
			}
		})
	}
}

// The mutation table: each entry is the canonical key with exactly one
// thing changed.
func TestControlIdempotencyKeyMutationTable(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"uppercase uuid", "9F1C2D3E-4A5B-4C6D-8E7F-0A1B2C3D4E5F"},
		{"mixed-case uuid", "9f1c2d3e-4a5b-4C6D-8e7f-0a1b2c3d4e5f"},
		{"unhyphenated uuid", "9f1c2d3e4a5b4c6d8e7f0a1b2c3d4e5f"},
		{"braced uuid", "{9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f}"},
		{"urn uuid", "urn:uuid:9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"},
		{"version 0", "9f1c2d3e-4a5b-0c6d-8e7f-0a1b2c3d4e5f"},
		{"version 9", "9f1c2d3e-4a5b-9c6d-8e7f-0a1b2c3d4e5f"},
		{"version f", "9f1c2d3e-4a5b-fc6d-8e7f-0a1b2c3d4e5f"},
		{"variant 0", "9f1c2d3e-4a5b-4c6d-0e7f-0a1b2c3d4e5f"},
		{"variant 7", "9f1c2d3e-4a5b-4c6d-7e7f-0a1b2c3d4e5f"},
		{"variant c", "9f1c2d3e-4a5b-4c6d-ce7f-0a1b2c3d4e5f"},
		{"variant f", "9f1c2d3e-4a5b-4c6d-fe7f-0a1b2c3d4e5f"},
		{"non-hex digit", "9g1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"},
		{"dash moved", "9f1c2d3e4-a5b-4c6d-8e7f-0a1b2c3d4e5f"},
		{"underscore separators", "9f1c2d3e_4a5b_4c6d_8e7f_0a1b2c3d4e5f"},
		{"one char short", "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5"},
		{"one char long", "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5ff"},
		{"leading space", " " + canonicalControlUUID},
		{"trailing space", canonicalControlUUID + " "},
		{"trailing tab", canonicalControlUUID + "\t"},
		{"comma joined uuids", canonicalControlUUID + "," + canonicalControlUUID},
		{"comma-space joined uuids", canonicalControlUUID + ", " + canonicalControlUUID},
		{"lowercase ulid", "01arz3ndektsv4rrffq69g5fav"},
		{"ulid with I", "01ARZ3NDEKTSV4RRFFQ69G5FAI"},
		{"ulid with L", "01ARZ3NDEKTSV4RRFFQ69G5FAL"},
		{"ulid with O", "01ARZ3NDEKTSV4RRFFQ69G5FAO"},
		{"ulid with U", "01ARZ3NDEKTSV4RRFFQ69G5FAU"},
		{"ulid overflowing timestamp", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"ulid starting Z", "ZZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"ulid one char short", "01ARZ3NDEKTSV4RRFFQ69G5FA"},
		{"ulid one char long", "01ARZ3NDEKTSV4RRFFQ69G5FAVV"},
		{"ulid with inner space", "01ARZ3NDEKTSV4RRFFQ69G5FA "},
		{"hyphenated ulid", "01ARZ3ND-EKTSV4RRFFQ69G5FA"},
		{"empty", ""},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ControlIdempotencyKeyDigest(idempotencyRequest(tc.key)); err == nil {
				t.Fatalf("non-canonical key %q was accepted", tc.key)
			} else {
				assertControlRefusal(t, err, ErrControlIdempotencyKeyInvalid)
			}
		})
	}
}

func TestControlIdempotencyKeyHeaderCardinality(t *testing.T) {
	if _, err := ControlIdempotencyKeyDigest(idempotencyRequest()); err == nil {
		t.Fatal("missing Idempotency-Key was accepted")
	} else {
		assertControlRefusal(t, err, ErrControlIdempotencyKeyMissing)
	}

	// Two header lines are two decisions; neither may win.
	duplicate := idempotencyRequest(canonicalControlUUID, canonicalControlULID)
	if _, err := ControlIdempotencyKeyDigest(duplicate); err == nil {
		t.Fatal("duplicate Idempotency-Key headers were accepted")
	} else {
		assertControlRefusal(t, err, ErrControlIdempotencyKeyInvalid)
	}

	// Two identical header lines are still two lines.
	repeated := idempotencyRequest(canonicalControlUUID, canonicalControlUUID)
	if _, err := ControlIdempotencyKeyDigest(repeated); err == nil {
		t.Fatal("repeated identical Idempotency-Key headers were accepted")
	}

	// The alias header rejects rather than being ignored, so a caller
	// cannot believe it sent a key the server never saw.
	aliased := idempotencyRequest(canonicalControlUUID)
	aliased.Header.Set(controlIdempotencyAliasHeader, canonicalControlUUID)
	if _, err := ControlIdempotencyKeyDigest(aliased); err == nil {
		t.Fatal("X-Idempotency-Key alias was accepted alongside the canonical header")
	}

	aliasOnly := idempotencyRequest()
	aliasOnly.Header.Set(controlIdempotencyAliasHeader, canonicalControlUUID)
	if _, err := ControlIdempotencyKeyDigest(aliasOnly); err == nil {
		t.Fatal("X-Idempotency-Key alias stood in for the canonical header")
	}

	if _, err := ControlIdempotencyKeyDigest(nil); err == nil {
		t.Fatal("nil request produced a digest")
	}
}

// ── enum / integer grammar ───────────────────────────────────────────

func TestStrictControlEnumIsByteExact(t *testing.T) {
	allowed := []string{"run.pause", "run.resume"}
	if got, err := StrictControlEnum("run.pause", allowed); err != nil || got != "run.pause" {
		t.Fatalf("exact enum was refused: %q %v", got, err)
	}
	for _, value := range []string{
		"Run.Pause", "RUN.PAUSE", " run.pause", "run.pause ", "run.pause\n",
		"run·pause", "run.paused", "", "*",
	} {
		if _, err := StrictControlEnum(value, allowed); err == nil {
			t.Fatalf("non-canonical enum %q was accepted", value)
		} else {
			assertControlRefusal(t, err, ErrControlEnumInvalid)
		}
	}
	if _, err := StrictControlEnum("run.pause", nil); err == nil {
		t.Fatal("empty allowed set accepted a value")
	}
}

func TestStrictControlInt64UsesCanonicalGrammar(t *testing.T) {
	accepted := map[string]int64{
		"0":                    0,
		"1":                    1,
		"42":                   42,
		"-1":                   -1,
		"9223372036854775807":  9223372036854775807,
		"-9223372036854775808": -9223372036854775808,
	}
	for literal, want := range accepted {
		got, err := StrictControlInt64(json.Number(literal))
		if err != nil || got != want {
			t.Fatalf("canonical integer %q -> %d, %v", literal, got, err)
		}
	}
	for _, literal := range []string{
		"1.0", "1e3", "1E3", "+1", "01", "007", "-0", "-01", "0x10",
		" 1", "1 ", "1,000", "", "-", "NaN", "Infinity",
		"9223372036854775808", "-9223372036854775809",
	} {
		if _, err := StrictControlInt64(json.Number(literal)); err == nil {
			t.Fatalf("non-canonical integer %q was accepted", literal)
		} else {
			assertControlRefusal(t, err, ErrControlIntegerInvalid)
		}
	}
}
