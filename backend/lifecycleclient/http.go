// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package lifecycleclient executes authenticated, typed runtime work. Private
// proofs and execution payloads never enter its public evidence or error text.
package lifecycleclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

var ErrTransport = errors.New("runtime authority transport unavailable")
var ErrUnknown = errors.New("runtime execution outcome unknown")
var ErrOwnership = errors.New("runtime authority ownership lost")
var ErrHandoff = errors.New("runtime consumer legacy handoff required")

type HTTP struct {
	base       string
	project    int64
	lease      string
	credential func() (string, error)
	client     *http.Client
}

func NewHTTP(base string, project int64, lease string, credential func() (string, error)) (*HTTP, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) || project <= 0 || !ValidProof(lease) || credential == nil {
		return nil, ErrOwnership
	}
	return &HTTP{base: strings.TrimRight(base, "/"), project: project, lease: lease, credential: credential,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrTransport }}}, nil
}

func NewProof() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrOwnership
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func ValidProof(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
}

// Request accepts only a same-project relative authority route. It never follows
// redirects or incorporates a remote error body in a diagnostic.
func (h *HTTP) Request(ctx context.Context, method, route string, headers map[string]string, input, output any) error {
	if !strings.HasPrefix(route, "/api/projects/"+fmt.Sprint(h.project)+"/") || strings.ContainsAny(route, "\x00\r\n") || strings.Contains(route, "..") {
		return ErrOwnership
	}
	return h.request(ctx, method, route, headers, input, output)
}

func (h *HTTP) request(ctx context.Context, method, route string, headers map[string]string, input, output any) error {
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil || len(raw) > 256<<10 {
			return ErrOwnership
		}
	}
	key, err := h.credential()
	if err != nil || key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return ErrOwnership
	}
	request, err := http.NewRequestWithContext(ctx, method, h.base+route, bytes.NewReader(raw))
	if err != nil {
		return ErrTransport
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set(lifecycleintents.RuntimeLeaseHeader, h.lease)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if (k != lifecycleintents.HarnessLeaseHeader && k != "X-Paimos-Consumer-Lease" && k != "X-Paimos-Consumer-Attempt") || !ValidProof(v) {
			return ErrOwnership
		}
		request.Header.Set(k, v)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return ErrTransport
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case 403:
			return ErrOwnership
		case 409:
			if strings.Contains(route, "/consumers/v1/") {
				raw, e := io.ReadAll(io.LimitReader(response.Body, 513))
				var closed struct {
					Error string `json:"error"`
				}
				if e == nil && len(raw) <= 512 && json.Unmarshal(raw, &closed) == nil {
					switch closed.Error {
					case "consumer_outcome_unknown":
						return ErrUnknown
					case "consumer_handoff_required":
						return ErrHandoff
					}
				}
			}
			return lifecycleintents.ErrConflict
		default:
			return ErrTransport
		}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || (output != nil && json.Unmarshal(raw, output) != nil) {
		return ErrTransport
	}
	return nil
}

type CanonicalTicket struct {
	ID                 int64  `json:"id"`
	ProjectID          int64  `json:"project_id"`
	Key                string `json:"issue_key"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

func (h *HTTP) ReadCanonicalTicket(ctx context.Context, id int64) (CanonicalTicket, error) {
	if id <= 0 {
		return CanonicalTicket{}, ErrOwnership
	}
	var out CanonicalTicket
	err := h.request(ctx, http.MethodGet, fmt.Sprintf("/api/issues/%d", id), nil, nil, &out)
	if err == nil && (out.ID != id || out.ProjectID != h.project) {
		err = ErrOwnership
	}
	if err != nil {
		return CanonicalTicket{}, err
	}
	return out, nil
}
func (h *HTTP) route(s string) string {
	return fmt.Sprintf("/api/projects/%d/lifecycle/v1%s", h.project, s)
}
func (h *HTTP) RegisterRuntime(ctx context.Context, registration lifecycleintents.Registration) (lifecycleintents.Runtime, error) {
	var out lifecycleintents.Runtime
	err := h.Request(ctx, http.MethodPost, h.route("/runtimes"), nil, registration, &out)
	if err == nil && (uuid.Validate(out.ID) != nil || out.ProjectID != h.project || out.Generation != registration.Generation || out.MachineID != registration.Host || out.AccountLabel != registration.AccountLabel || out.SchemaVersion != registration.SchemaVersion || !equal(out.Workspaces, registration.Workspaces) || !equal(out.Profiles, registration.Profiles) || !equal(out.AccountScopes, registration.AccountScopes) || !equal(out.Accounts, registration.Accounts)) {
		err = ErrOwnership
	}
	return out, err
}
func (h *HTTP) RegisterSession(ctx context.Context, runtime string, in lifecycleintents.SessionRegistration, lease string) error {
	if uuid.Validate(runtime) != nil || uuid.Validate(in.SessionID) != nil || uuid.Validate(in.Generation) != nil {
		return ErrOwnership
	}
	var out struct {
		Registered bool `json:"registered"`
	}
	err := h.Request(ctx, http.MethodPost, h.route("/runtimes/"+runtime+"/sessions"), map[string]string{lifecycleintents.HarnessLeaseHeader: lease}, in, &out)
	if err == nil && !out.Registered {
		return ErrOwnership
	}
	return err
}
func (h *HTTP) Claim(ctx context.Context, runtime string) (*lifecycleintents.Intent, error) {
	if uuid.Validate(runtime) != nil {
		return nil, ErrOwnership
	}
	var out struct {
		SchemaVersion int                      `json:"schema_version"`
		Intent        *lifecycleintents.Intent `json:"intent"`
	}
	err := h.Request(ctx, http.MethodPost, h.route("/runtimes/"+runtime+"/claim"), nil, struct{}{}, &out)
	if err == nil && (out.SchemaVersion != 1 || out.Intent != nil && (out.Intent.ProjectID != h.project || out.Intent.Request.RuntimeID != runtime || !acceptedIntent(*out.Intent))) {
		err = ErrOwnership
	}
	return out.Intent, err
}
func (h *HTTP) Transition(ctx context.Context, id string, in lifecycleintents.Transition) (lifecycleintents.Intent, error) {
	if uuid.Validate(id) != nil {
		return lifecycleintents.Intent{}, ErrOwnership
	}
	var out lifecycleintents.Intent
	err := h.Request(ctx, http.MethodPost, h.route("/intents/"+id+"/transition"), nil, in, &out)
	if err == nil && (out.ID != id || out.ProjectID != h.project || out.Request.RuntimeID != in.RuntimeID || out.Request.RuntimeGeneration != in.RuntimeGeneration || !acceptedIntent(out)) {
		err = ErrOwnership
	}
	return out, err
}

// acceptedIntent allows frozen class-only schema 1 and named-account schema 2.
// The claim envelope stays schema 1; unknown schemas and schema/account mismatches fail closed.
func acceptedIntent(in lifecycleintents.Intent) bool {
	switch in.SchemaVersion {
	case lifecycleintents.RuntimeSchemaV1:
		return in.Request.AccountKey == ""
	case lifecycleintents.AccountChoiceSchemaV2:
		return in.Request.AccountKey != ""
	default:
		return false
	}
}
func equal(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }

var _ lifecycleintents.RuntimeAuthority = (*HTTP)(nil)
