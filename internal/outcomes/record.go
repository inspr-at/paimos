// SPDX-License-Identifier: AGPL-3.0-only

package outcomes

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxPayload   = 2048
	maxKeyLen    = 200
	minKeyLen    = 8
	maxRulesLen  = 128
	maxSummary   = 280
	maxModel     = 80
	maxRoute     = 64
	maxName      = 80
	defaultLimit = 50
	maxLimit     = 100
)

var fingerprintRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

var manualKinds = map[string]bool{
	"review_verdict": true,
	"fix_round":      true,
	"ci_result":      true,
	"revert":         true,
}

type fail struct {
	status int
	msg    string
}

func (e *fail) Error() string { return e.msg }

func invalid(msg string) error { return &fail{status: http.StatusBadRequest, msg: msg} }

type reviewPayload struct {
	ReviewID            string `json:"review_id,omitempty"`
	FindingsFingerprint string `json:"findings_fingerprint,omitempty"`
	Verdict             string `json:"verdict"`
	ReviewerModel       string `json:"reviewer_model,omitempty"`
	Route               string `json:"route,omitempty"`
	AuthorFamily        string `json:"author_family,omitempty"`
	Round               *int   `json:"round,omitempty"`
	BlockingCount       *int   `json:"blocking_count,omitempty"`
	Findings            *int   `json:"findings,omitempty"`
	Summary             string `json:"summary,omitempty"`
}

type fixPayload struct {
	Round   *int   `json:"round"`
	Summary string `json:"summary,omitempty"`
}

type ciPayload struct {
	AttemptID string `json:"attempt_id,omitempty"`
	Result    string `json:"result"`
	Repo      string `json:"repo"`
	Number    *int   `json:"number"`
	Name      string `json:"name,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type revertPayload struct {
	Summary string `json:"summary"`
	Target  string `json:"target,omitempty"`
}

type digestInput struct {
	Kind         string          `json:"kind"`
	TicketNodeID string          `json:"ticket_node_id"`
	SessionID    string          `json:"session_id,omitempty"`
	RulesVersion string          `json:"rules_version,omitempty"`
	Payload      json.RawMessage `json:"payload"`
}

func canonicalPayload(kind string, raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	if trimmed[0] != '{' {
		return nil, invalid("payload must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var out any
	switch kind {
	case "review_verdict":
		var body reviewPayload
		if err := dec.Decode(&body); err != nil {
			return nil, payloadErr(err)
		}
		if err := oneValue(dec); err != nil {
			return nil, err
		}
		verdict, err := oneOf("verdict", body.Verdict, "ok", "changes")
		if err != nil {
			return nil, err
		}
		if body.FindingsFingerprint != "" && !fingerprintRE.MatchString(body.FindingsFingerprint) {
			return nil, invalid("findings_fingerprint must be lowercase SHA-256")
		}
		if body.ReviewID != "" && !validUUID(body.ReviewID) {
			return nil, invalid("review_id must be a UUID")
		}
		body.ReviewID = strings.ToLower(body.ReviewID)
		body.Verdict = verdict
		if body.ReviewerModel, err = plain("reviewer_model", body.ReviewerModel, maxModel, false); err != nil {
			return nil, err
		}
		if body.Route, err = plain("route", body.Route, maxRoute, false); err != nil {
			return nil, err
		}
		if body.AuthorFamily, err = plain("author_family", body.AuthorFamily, maxRoute, false); err != nil {
			return nil, err
		}
		if err = bounded("round", body.Round, 1, 99, false); err != nil {
			return nil, err
		}
		if err = bounded("blocking_count", body.BlockingCount, 0, 999, false); err != nil {
			return nil, err
		}
		if err = bounded("findings", body.Findings, 0, 999, false); err != nil {
			return nil, err
		}
		if body.Summary, err = plain("summary", body.Summary, maxSummary, false); err != nil {
			return nil, err
		}
		out = body
	case "fix_round":
		var body fixPayload
		if err := dec.Decode(&body); err != nil {
			return nil, payloadErr(err)
		}
		if err := oneValue(dec); err != nil {
			return nil, err
		}
		if err := bounded("round", body.Round, 1, 99, true); err != nil {
			return nil, err
		}
		summary, err := plain("summary", body.Summary, maxSummary, false)
		if err != nil {
			return nil, err
		}
		body.Summary = summary
		out = body
	case "ci_result":
		var body ciPayload
		if err := dec.Decode(&body); err != nil {
			return nil, payloadErr(err)
		}
		if err := oneValue(dec); err != nil {
			return nil, err
		}
		result, err := oneOf("result", body.Result, "pass", "fail")
		if err != nil {
			return nil, err
		}
		if body.AttemptID != "" && !validUUID(body.AttemptID) {
			return nil, invalid("attempt_id must be a UUID")
		}
		body.AttemptID = strings.ToLower(body.AttemptID)
		body.Result = result
		if body.Repo, err = plain("repo", body.Repo, maxKeyLen, true); err != nil {
			return nil, err
		}
		if err = bounded("number", body.Number, 1, 99999999, true); err != nil {
			return nil, err
		}
		if body.Name, err = plain("name", body.Name, maxName, false); err != nil {
			return nil, err
		}
		if body.Summary, err = plain("summary", body.Summary, maxSummary, false); err != nil {
			return nil, err
		}
		out = body
	case "revert":
		var body revertPayload
		if err := dec.Decode(&body); err != nil {
			return nil, payloadErr(err)
		}
		if err := oneValue(dec); err != nil {
			return nil, err
		}
		summary, err := plain("summary", body.Summary, maxSummary, true)
		if err != nil {
			return nil, err
		}
		body.Summary = summary
		if body.Target, err = plain("target", body.Target, maxName, false); err != nil {
			return nil, err
		}
		out = body
	default:
		return nil, invalid("kind must be review_verdict, fix_round, ci_result or revert")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxPayload {
		return nil, invalid("payload is too large")
	}
	return encoded, nil
}

func payloadErr(err error) error {
	if strings.Contains(err.Error(), "unknown field") {
		return invalid("unknown payload field")
	}
	return invalid("invalid payload")
}

func oneValue(dec *json.Decoder) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid("invalid payload")
	}
	return nil
}

func oneOf(field, value string, allowed ...string) (string, error) {
	value = strings.TrimSpace(value)
	for _, item := range allowed {
		if value == item {
			return value, nil
		}
	}
	return "", invalid(field + " must be " + strings.Join(allowed, " or "))
}

func plain(field, value string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", invalid(field + " is required")
		}
		return "", nil
	}
	if utf8.RuneCountInString(value) > max {
		return "", invalid(field + " is too long")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", invalid(field + " has unsupported characters")
		}
	}
	return value, nil
}

func bounded(field string, value *int, min, max int, required bool) error {
	if value == nil {
		if required {
			return invalid(field + " is required")
		}
		return nil
	}
	if *value < min || *value > max {
		return invalid(field + " is out of range")
	}
	return nil
}

func cleanRules(value string, present bool) (string, error) {
	if !present {
		return "", nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", invalid("rules_version is empty")
	}
	if utf8.RuneCountInString(value) > maxRulesLen {
		return "", invalid("rules_version is too long")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", invalid("rules_version has unsupported characters")
		}
	}
	return value, nil
}

func cleanIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", invalid("idempotency key is required")
	}
	if strings.HasPrefix(value, "auto:") {
		return "", invalid("idempotency key is reserved")
	}
	if len(value) < minKeyLen || len(value) > maxKeyLen {
		return "", invalid("idempotency key must be 8 to 200 characters")
	}
	for i, r := range value {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '/' || r == '-'
		if i == 0 {
			ok = r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		}
		if !ok {
			return "", invalid("idempotency key has unsupported characters")
		}
	}
	return value, nil
}

func requestDigest(kind, ticketID, sessionID, rules string, payload json.RawMessage) []byte {
	raw, _ := json.Marshal(digestInput{
		Kind: kind, TicketNodeID: ticketID, SessionID: sessionID, RulesVersion: rules, Payload: payload,
	})
	sum := md5.Sum(raw)
	return sum[:]
}
