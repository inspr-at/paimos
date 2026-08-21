// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-809 — strict request primitives for the supervisory-control surface.
//
// Every other write path in this product is forgiving: it tolerates a
// charset parameter, ignores a field it doesn't know, takes the last of
// two identically-named keys, accepts "1.0" where it wanted 1. That is
// the right posture for a CRUD API used by browsers and scripts.
//
// It is the wrong posture here. A control request cancels a run, answers
// a prompt, or hands a machine a capability, and a second, differently
// spelled interpretation of the same bytes is exactly how an operator
// confirms one thing and authorizes another. So on this surface anything
// with two readings is refused, and refusal is cheap: the caller is our
// own UI or our own runner, both of which emit one canonical form.
//
// The rules, in one place so a later route lane inherits them rather
// than re-deriving them:
//
//   - the response is private and uncacheable, on success and on error;
//   - the media type is exactly application/json, with no parameters;
//   - no content encoding — the body is read, not decoded;
//   - the body is bounded by a limit the caller chooses;
//   - unknown fields, duplicate names, and a trailing second value all
//     reject, rather than silently picking a reading;
//   - enums match byte for byte and integers use canonical grammar;
//   - exactly one Idempotency-Key, canonical UUID or canonical ULID, and
//     callers receive only its SHA-256 — the raw key never reaches a log
//     line or a column.
//
// Deliberately absent: routes, DTOs, statuses, and any use of the
// generic response-cache IdempotencyMiddleware. That middleware stores
// whole request and response bodies keyed by the raw header value, which
// is the opposite of what this surface may do.

package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/backend/httpcontract"
)

// ControlIdempotencyDigestSize is the length of the digest callers get
// back in place of a raw Idempotency-Key.
const ControlIdempotencyDigestSize = sha256.Size

// controlIdempotencyAliasHeader is the one plausible alternate spelling
// of the canonical header. Accepting it would mean two headers could
// name the same decision, so its presence rejects the request outright
// rather than being ignored (which would let a caller believe it sent a
// key when the server saw none).
const controlIdempotencyAliasHeader = "X-Idempotency-Key"

// controlJSONMediaType is the only accepted media type. Parameters —
// including charset — are refused: "application/json; charset=utf-8" and
// "application/json" would otherwise be two spellings of one contract.
const controlJSONMediaType = "application/json"

// controlJSONMaxDepth bounds nesting during the duplicate-name scan. A
// body inside the caller's byte budget can still nest deeply enough to
// exhaust the stack, and no control payload is anywhere near this deep.
const controlJSONMaxDepth = 32

// ControlRequestError is a refusal with a closed, safe reason code.
//
// The code vocabulary is fixed at compile time and carries nothing from
// the request, so it is safe to log, return, and count. Decoder error
// text — which quotes field names and offsets — is deliberately dropped
// at the point of classification and never travels in this value.
type ControlRequestError struct {
	Status int
	Code   string
}

func (e *ControlRequestError) Error() string { return e.Code }

// The closed refusal vocabulary.
var (
	ErrControlUnsupportedMediaType  = &ControlRequestError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type"}
	ErrControlContentEncoding       = &ControlRequestError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_content_encoding"}
	ErrControlBodyTooLarge          = &ControlRequestError{Status: http.StatusRequestEntityTooLarge, Code: "request_entity_too_large"}
	ErrControlBodyMissing           = &ControlRequestError{Status: http.StatusBadRequest, Code: "request_body_required"}
	ErrControlBodyMalformed         = &ControlRequestError{Status: http.StatusBadRequest, Code: "malformed_json"}
	ErrControlBodyUnknownField      = &ControlRequestError{Status: http.StatusBadRequest, Code: "unknown_field"}
	ErrControlBodyDuplicateField    = &ControlRequestError{Status: http.StatusBadRequest, Code: "duplicate_field"}
	ErrControlBodyTrailingContent   = &ControlRequestError{Status: http.StatusBadRequest, Code: "trailing_content"}
	ErrControlIdempotencyKeyMissing = &ControlRequestError{Status: http.StatusBadRequest, Code: "idempotency_key_required"}
	ErrControlIdempotencyKeyInvalid = &ControlRequestError{Status: http.StatusBadRequest, Code: "invalid_idempotency_key"}
	ErrControlEnumInvalid           = &ControlRequestError{Status: http.StatusBadRequest, Code: "invalid_enum_value"}
	ErrControlIntegerInvalid        = &ControlRequestError{Status: http.StatusBadRequest, Code: "invalid_integer_value"}
)

var closedControlRequestErrors = []*ControlRequestError{
	ErrControlUnsupportedMediaType,
	ErrControlContentEncoding,
	ErrControlBodyTooLarge,
	ErrControlBodyMissing,
	ErrControlBodyMalformed,
	ErrControlBodyUnknownField,
	ErrControlBodyDuplicateField,
	ErrControlBodyTrailingContent,
	ErrControlIdempotencyKeyMissing,
	ErrControlIdempotencyKeyInvalid,
	ErrControlEnumInvalid,
	ErrControlIntegerInvalid,
}

// SetControlCachePolicy marks a control response private and uncacheable.
// Call it before writing anything — including an error — so a refusal on
// the way in is never stored by a proxy or a browser.
func SetControlCachePolicy(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
}

// ControlCachePolicyMiddleware is the mountable form of
// SetControlCachePolicy, for a route group whose every response — including
// the ones written by gates above the handler — must be uncacheable.
func ControlCachePolicyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetControlCachePolicy(w)
		next.ServeHTTP(w, r)
	})
}

// ClassifiedControlCachePolicyMiddleware is the global safety net. Mounting it
// before logging, recovery, auth, and routing guarantees the policy even for a
// 401, 403, 404, 405, panic recovery, or a future control handler that forgets
// to call the local helper. Structural near-misses retain ordinary caching.
func ClassifiedControlCachePolicyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpcontract.IsControlRequest(r) {
			SetControlCachePolicy(w)
		}
		next.ServeHTTP(w, r)
	})
}

// WriteControlRequestError renders a refusal as problem+json with the
// closed code and no request-derived detail text. Any error that is not
// a *ControlRequestError is reported as a generic bad request: an
// unclassified failure must not get to choose the words.
func WriteControlRequestError(w http.ResponseWriter, r *http.Request, err error) {
	SetControlCachePolicy(w)
	refusal := closedControlRequestError(err)
	requestID := trustedControlResponseRequestID(r)
	w.Header().Set(RequestIDHeader, requestID)
	// Do not pass r to the shared problem writer: ordinary API problems
	// intentionally echo RequestURI as `instance`, which would disclose a
	// delivery/run/command identifier and the query string on this surface.
	// Supplying the server-minted request id explicitly also prevents the
	// writer from consulting any pre-existing, caller-authored header.
	writeProblem(w, nil, ProblemDetails{
		Status:    refusal.Status,
		Code:      refusal.Code,
		Detail:    controlRefusalDetail(refusal.Code),
		RequestID: requestID,
	})
}

func closedControlRequestError(err error) *ControlRequestError {
	for _, known := range closedControlRequestErrors {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrControlBodyMalformed
}

// trustedControlResponseRequestID returns only an id installed in context by
// RequestIDMiddleware. A direct handler test or an incorrectly composed route
// gets a fresh server id; request headers are never a fallback on this surface.
func trustedControlResponseRequestID(r *http.Request) string {
	if r != nil {
		if value, ok := r.Context().Value(requestIDContextKey{}).(string); ok &&
			isSafeControlRequestID(value) {
			return value
		}
	}
	return newAIRequestID()
}

func isSafeControlRequestID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 64 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9',
			char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char == '-', char == '_', char == '.', char == ':':
		default:
			return false
		}
	}
	return true
}

// controlRefusalDetail maps a closed code to fixed prose. A map, not a
// format string, so no request byte can reach the response text.
func controlRefusalDetail(code string) string {
	switch code {
	case ErrControlUnsupportedMediaType.Code:
		return "Content-Type must be exactly application/json"
	case ErrControlContentEncoding.Code:
		return "Content-Encoding is not supported on this endpoint"
	case ErrControlBodyTooLarge.Code:
		return "request body exceeds the limit for this endpoint"
	case ErrControlBodyMissing.Code:
		return "a JSON request body is required"
	case ErrControlBodyUnknownField.Code:
		return "request body contains a field this endpoint does not accept"
	case ErrControlBodyDuplicateField.Code:
		return "request body names the same field twice"
	case ErrControlBodyTrailingContent.Code:
		return "request body must contain exactly one JSON value"
	case ErrControlIdempotencyKeyMissing.Code:
		return "exactly one Idempotency-Key header is required"
	case ErrControlIdempotencyKeyInvalid.Code:
		return "Idempotency-Key must be a canonical lowercase UUID or uppercase ULID"
	case ErrControlEnumInvalid.Code:
		return "value is not one of the accepted values for this field"
	case ErrControlIntegerInvalid.Code:
		return "value is not a canonical integer"
	default:
		return "request body is not valid JSON"
	}
}

// DecodeControlJSON is the only way a control handler should read a body.
//
// maxBytes is per caller because the right ceiling differs by route: a
// command decision is a few hundred bytes, a lease renewal fewer. The
// limit is enforced by MaxBytesReader against w, so an oversized body is
// cut off rather than buffered.
//
// dst should use json.Number for integral fields and plain strings for
// enums, then run them through StrictControlInt64 / StrictControlEnum —
// encoding/json's own number and string handling is too permissive to be
// the last word on this surface.
func DecodeControlJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	SetControlCachePolicy(w)
	if err := requireControlJSONFraming(r); err != nil {
		return err
	}
	if r.Body == nil {
		return ErrControlBodyMissing
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrControlBodyTooLarge
		}
		// A read that failed for any other reason (client hang-up,
		// truncated frame) is indistinguishable from a malformed body
		// as far as this endpoint is concerned, and the underlying
		// error text is not ours to surface.
		return ErrControlBodyMalformed
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return ErrControlBodyMissing
	}
	if err := rejectDuplicateJSONNames(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		// Classify, then discard: the decoder names the offending field
		// and byte offset, which is caller input echoed back.
		if strings.Contains(err.Error(), "unknown field") {
			return ErrControlBodyUnknownField
		}
		return ErrControlBodyMalformed
	}
	// Exactly one value. A second value, or any trailing byte that is not
	// whitespace, means two readings of one request.
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return ErrControlBodyTrailingContent
	}
	return nil
}

// requireControlJSONFraming enforces the media type and the absence of a
// content encoding, before a single body byte is read.
func requireControlJSONFraming(r *http.Request) error {
	if len(r.Header.Values("Content-Encoding")) > 0 {
		return ErrControlContentEncoding
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return ErrControlUnsupportedMediaType
	}
	// No mime.ParseMediaType: it would accept parameters or normalize
	// spelling. The frozen control contract has exactly one byte spelling.
	if values[0] != controlJSONMediaType {
		return ErrControlUnsupportedMediaType
	}
	return nil
}

// rejectDuplicateJSONNames fails a body that names one field twice in the
// same object, at any depth.
//
// encoding/json keeps the last of two identical names and matches names
// case-insensitively, so both {"a":1,"a":2} and {"a":1,"A":2} decode
// cleanly into one field with the second value winning. On a control
// request that is a smuggling primitive: what an operator reviews and
// what the server applies can differ. Names therefore collide
// case-insensitively here, which is stricter than the decoder and
// deliberately so.
func rejectDuplicateJSONNames(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanJSONValueForDuplicates(dec, 0); err != nil {
		return err
	}
	return nil
}

func scanJSONValueForDuplicates(dec *json.Decoder, depth int) error {
	if depth > controlJSONMaxDepth {
		return ErrControlBodyMalformed
	}
	token, err := dec.Token()
	if err != nil {
		return ErrControlBodyMalformed
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := []string{}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return ErrControlBodyMalformed
			}
			name, ok := nameToken.(string)
			if !ok {
				return ErrControlBodyMalformed
			}
			for _, previous := range seen {
				// encoding/json matches struct fields with Unicode simple-fold
				// semantics. EqualFold mirrors that equivalence, so aliases such
				// as ASCII 's' and long-s cannot evade duplicate detection.
				if strings.EqualFold(previous, name) {
					return ErrControlBodyDuplicateField
				}
			}
			seen = append(seen, name)
			if err := scanJSONValueForDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanJSONValueForDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		// A closing delimiter cannot start a value.
		return ErrControlBodyMalformed
	}
	// Consume the matching close delimiter.
	if _, err := dec.Token(); err != nil {
		return ErrControlBodyMalformed
	}
	return nil
}

// ControlIdempotencyKeyDigest validates the request's Idempotency-Key and
// returns only its SHA-256.
//
// The raw key identifies a decision a human made and is therefore treated
// like a credential: it is validated in place and immediately reduced to a
// digest. Callers get the digest, so there is no raw value available to
// put in a log line, an error body, or a column.
//
// Accepted, exactly:
//
//   - a canonical RFC 9562 UUID — lowercase hex, 8-4-4-4-12, version
//     nibble 1–8, RFC variant (8, 9, a, or b);
//   - a canonical Crockford ULID — 26 uppercase symbols from the alphabet
//     without I, L, O, or U, first symbol 0–7 so the timestamp cannot
//     overflow 48 bits.
//
// Everything else refuses: surrounding or embedded whitespace, a
// comma-joined list, two header lines, the urn:uuid: or braced forms, an
// unhyphenated 32-hex string, an uppercase UUID, a lowercase ULID, and
// the X-Idempotency-Key alias.
func ControlIdempotencyKeyDigest(r *http.Request) ([ControlIdempotencyDigestSize]byte, error) {
	var digest [ControlIdempotencyDigestSize]byte
	if r == nil {
		return digest, ErrControlIdempotencyKeyMissing
	}
	if len(r.Header.Values(controlIdempotencyAliasHeader)) > 0 {
		return digest, ErrControlIdempotencyKeyInvalid
	}
	values := r.Header.Values(idempotencyHeader)
	switch len(values) {
	case 0:
		return digest, ErrControlIdempotencyKeyMissing
	case 1:
		// The one accepted shape.
	default:
		// Two header lines are two decisions; neither wins.
		return digest, ErrControlIdempotencyKeyInvalid
	}
	key := values[0]
	if !canonicalUUIDKey(key) && !canonicalULIDKey(key) {
		return digest, ErrControlIdempotencyKeyInvalid
	}
	return sha256.Sum256([]byte(key)), nil
}

func canonicalUUIDKey(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			// Version nibble: 1–8 are assigned; 0 and 9–f are not a
			// version this product will ever mint.
			if c < '1' || c > '8' {
				return false
			}
		case 19:
			// RFC variant (10xx).
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return false
			}
		default:
			if !isLowerHex(c) {
				return false
			}
		}
	}
	return true
}

// crockfordULIDAlphabet is Crockford base32 without I, L, O, and U, in
// canonical uppercase. Lowercase is a second spelling of the same value
// and is refused rather than folded.
const crockfordULIDAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func canonicalULIDKey(value string) bool {
	if len(value) != 26 {
		return false
	}
	// First symbol caps the 48-bit timestamp: '7' is the largest value
	// that cannot overflow, so '8'–'Z' here is not a valid ULID.
	if value[0] < '0' || value[0] > '7' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if strings.IndexByte(crockfordULIDAlphabet, value[i]) < 0 {
			return false
		}
	}
	return true
}

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// StrictControlEnum accepts a value only if it is byte-for-byte one of
// the allowed spellings. No trimming, no case folding, no aliases: on
// this surface "Accepted" and "accepted " are not the enum, they are
// evidence that the caller and the server disagree about the contract.
func StrictControlEnum(value string, allowed []string) (string, error) {
	if !slices.Contains(allowed, value) {
		return "", ErrControlEnumInvalid
	}
	return value, nil
}

// StrictControlInt64 accepts only canonical JSON integer grammar:
// an optional minus sign, then 0 or a digit string with no leading zero.
// "1.0", "1e3", "+1", "01", "-0", and anything out of int64 range refuse
// — each is a second spelling of a number, and a control payload that can
// be re-spelled can be re-read.
func StrictControlInt64(value json.Number) (int64, error) {
	literal := value.String()
	if !canonicalIntegerLiteral(literal) {
		return 0, ErrControlIntegerInvalid
	}
	parsed, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		return 0, ErrControlIntegerInvalid
	}
	return parsed, nil
}

func canonicalIntegerLiteral(literal string) bool {
	digits := literal
	negative := false
	if strings.HasPrefix(digits, "-") {
		negative = true
		digits = digits[1:]
	}
	if digits == "" {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	if len(digits) > 1 && digits[0] == '0' {
		return false
	}
	// "-0" is a second spelling of zero.
	return !(negative && digits == "0")
}
