// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
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

func externalStageAdapterTestRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(RequestIDMiddleware)
	router.Route("/api", MountExternalStageContractRoutes)
	return router
}

func externalStageTransportTestRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(RequestIDMiddleware)
	router.Route("/api/agent-mode", MountInternalExternalStageContractRoutes)
	router.Route("/api", MountExternalStageContractRoutes)
	return router
}

func externalStageProductionRouteSlice() http.Handler {
	router := chi.NewRouter()
	router.Use(ClassifiedControlCachePolicyMiddleware)
	router.Use(ControlAwareRecoverer)
	router.Use(RequestIDMiddleware)
	router.NotFound(ControlAwareNotFound)
	router.MethodNotAllowed(ControlAwareMethodNotAllowed)
	router.Route("/api", func(router chi.Router) {
		router.Group(func(router chi.Router) {
			router.Use(auth.AgentModePrivateNoStore)
			router.Use(ExternalStageAPIKeyAuth)
			MountExternalStageContractRoutes(router)
		})
	})
	return router
}

func externalStageRequestWithAuthPrincipal(request *http.Request, principal auth.Principal) *http.Request {
	return request.WithContext(auth.WithPrincipal(request.Context(), principal))
}

func normalizedExternalStageProblem(t *testing.T, recorder *httptest.ResponseRecorder) []byte {
	t.Helper()
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
	return normalized
}

func TestExternalStageRealRouterWrongMethodIsCanonicallyConcealed(t *testing.T) {
	router := externalStageProductionRouteSlice()
	request := httptest.NewRequest(http.MethodPut,
		"/api/external-stage/handoffs/opaque-do-not-echo/reports", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", externalstage.MediaTypeV1)
	request.Header.Set("Accept", externalstage.MediaTypeV1)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d headers=%v body=%s", recorder.Code, http.StatusNotFound,
			recorder.Header(), recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("wrong-method response was not private/no-store: headers=%v", recorder.Header())
	}
	if recorder.Header().Get("Allow") != "" || recorder.Header().Get("X-Permissions-Epoch") != "" {
		t.Fatalf("wrong-method response leaked routing/auth metadata: headers=%v", recorder.Header())
	}
	if strings.Contains(recorder.Body.String(), "opaque-do-not-echo") {
		t.Fatalf("wrong-method response leaked opaque handoff path: %s", recorder.Body.String())
	}

	canonicalRecorder := httptest.NewRecorder()
	writeControlNotFound(canonicalRecorder, httptest.NewRequest(http.MethodGet, request.URL.Path, nil))
	if !bytes.Equal(normalizedExternalStageProblem(t, recorder), normalizedExternalStageProblem(t, canonicalRecorder)) {
		t.Fatalf("wrong-method refusal differs from canonical not-found: wrong=%s canonical=%s",
			recorder.Body.String(), canonicalRecorder.Body.String())
	}
}

type externalStageTransportFixture struct {
	operator       auth.Principal
	reporter       auth.Principal
	deliveryKey    string
	registrationID int64
}

func setupExternalStageTransportFixture(t *testing.T) externalStageTransportFixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.DB.Close()
		db.DB = nil
	})
	result, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('External stage transport','EST')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,810,'Transport status')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := result.LastInsertId()
	deliveryKey := fmt.Sprintf("issue:%d", issueID)
	result, err = db.DB.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		issueID, deliveryKey, projectID)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'system','external-stage-transport',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	systemReporterID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,1,'transport-attempt',zeroblob(32),'attempt_started',?,
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, systemReporterID)
	if err != nil {
		t.Fatal(err)
	}
	attemptEventID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,start_delivery_event_id,
		project_id_at_start,reason_code,created_at) VALUES(?,1,1,?,?,'external_stage_transport',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, attemptEventID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := result.LastInsertId()
	for index, stage := range []string{"specification", "implementation", "qa", "deployment", "verification"} {
		if stage == "deployment" || stage == "verification" {
			_, err = db.DB.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
				applicability,weight,created_at) VALUES(?,?,?,?,'required',50,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
				deliveryID, attemptID, stage, index+1)
		} else {
			_, err = db.DB.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,
				applicability,weight,policy_reference,reason_code,authorized_by_reporter_id,created_at)
				VALUES(?,?,?,?,'not_applicable',0,'external-stage-transport','not_required',?,
				strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, attemptID, stage, index+1, systemReporterID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.DB.Exec(`INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, attemptID); err != nil {
		t.Fatal(err)
	}
	result, err = db.DB.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('external-stage-transport-operator','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := result.LastInsertId()
	if _, err = db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`,
		operatorID, projectID); err != nil {
		t.Fatal(err)
	}
	const sessionCredential = "81000000-0000-4000-8000-000000000811"
	if _, err = db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('external-stage-transport-session',?,datetime('now','+1 hour'),datetime('now'),?)`,
		operatorID, sessionCredential); err != nil {
		t.Fatal(err)
	}
	operator, err := auth.NewSessionPrincipal(sessionCredential, operatorID, operatorID, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.DB.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('external-stage-transport-pharos','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	reporterUserID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'external-stage-transport-pharos',?,'paimos_transport_pharos','*')`,
		reporterUserID, fmt.Sprintf("%064d", 811))
	if err != nil {
		t.Fatal(err)
	}
	reporterAPIKeyID, _ := result.LastInsertId()
	reporter, err := auth.NewAPIKeyPrincipal(reporterAPIKeyID, reporterUserID, auth.ScopeSet{auth.ScopeAll: {}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := externalStageService()
	if err != nil {
		t.Fatal(err)
	}
	registration, err := service.RegisterReporter(t.Context(), externalstage.Principal{
		UserID: operatorID, Kind: string(auth.PrincipalSession), SessionCredentialID: sessionCredential,
	}, deliveryKey, "81000000-0000-4000-8000-000000000812", externalstage.RegisterReporterRequest{
		APIKeyID: reporterAPIKeyID, ReporterClass: externalstage.ReporterClassPharos,
		ReporterRole: externalstage.ReporterRoleOwner, Workflow: "deploy-production", Environment: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.DB.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,
		kind,reporter_id,server_received_at) VALUES(?,2,'transport-deployment',zeroblob(32),'stage_execution_started',?,
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, deliveryID, registration.ReporterID)
	if err != nil {
		t.Fatal(err)
	}
	startEnvelopeID, _ := result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
		event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,semantic_state,
		authority_source_sequence_cutoff,server_received_at) VALUES(?,?,'deployment',1,1,1,?,
		'execution_started',?,'active',0,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		deliveryID, attemptID, startEnvelopeID, registration.ReporterID)
	if err != nil {
		t.Fatal(err)
	}
	executionStartID, _ := result.LastInsertId()
	if _, err = db.DB.Exec(`INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,execution_number,
		authority_epoch,current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,'deployment',1,1,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		deliveryID, attemptID, registration.ReporterID, executionStartID, executionStartID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SealPrerequisites(t.Context(), externalstage.Principal{
		UserID: operatorID, Kind: string(auth.PrincipalSession), SessionCredentialID: sessionCredential,
	}, deliveryKey, "81000000-0000-4000-8000-000000000813", externalstage.SealPrerequisitesRequest{
		StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1, ExpectedAuthorityEpoch: 1,
		Prerequisites: []externalstage.Prerequisite{},
	}); err != nil {
		t.Fatal(err)
	}
	return externalStageTransportFixture{operator: operator, reporter: reporter, deliveryKey: deliveryKey,
		registrationID: registration.RegistrationID}
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
			normalized := normalizedExternalStageProblem(t, recorder)
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
		name       string
		mutate     func(*http.Request)
		body       string
		wantErr    error
		wantStatus int
	}{
		{name: "valid", body: `{"sequence":1}`},
		{name: "empty", body: ` `, wantErr: externalstage.ErrInvalid, wantStatus: http.StatusBadRequest},
		{name: "unknown", body: `{"sequence":1,"extra":true}`, wantErr: externalstage.ErrInvalid, wantStatus: http.StatusBadRequest},
		{name: "duplicate", body: `{"sequence":1,"sequence":2}`, wantErr: externalstage.ErrInvalid, wantStatus: http.StatusBadRequest},
		{name: "second value", body: `{"sequence":1}{"sequence":2}`, wantErr: externalstage.ErrInvalid, wantStatus: http.StatusBadRequest},
		{name: "content type parameter", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Content-Type", externalstage.MediaTypeV1+"; charset=utf-8")
		}, wantErr: errExternalStageContentType, wantStatus: http.StatusUnsupportedMediaType},
		{name: "duplicate content type", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Add("Content-Type", externalstage.MediaTypeV1)
		}, wantErr: errExternalStageContentType, wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing accept", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Del("Accept")
		}, wantErr: errExternalStageAccept, wantStatus: http.StatusNotAcceptable},
		{name: "wrong accept", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Accept", "application/json")
		}, wantErr: errExternalStageAccept, wantStatus: http.StatusNotAcceptable},
		{name: "duplicate accept", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Add("Accept", externalstage.MediaTypeV1)
		}, wantErr: errExternalStageAccept, wantStatus: http.StatusNotAcceptable},
		{name: "content encoding", body: `{"sequence":1}`, mutate: func(r *http.Request) {
			r.Header.Set("Content-Encoding", "identity")
		}, wantErr: errExternalStageContentType, wantStatus: http.StatusUnsupportedMediaType},
		{name: "oversize", body: `{"sequence":1,"` + strings.Repeat("x", externalStageMaxBody) + `":0}`,
			wantErr: errExternalStageTooLarge, wantStatus: http.StatusRequestEntityTooLarge},
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
			if test.wantErr == nil {
				if err != nil || got.Sequence != 1 {
					t.Fatalf("payload=%+v err=%v", got, err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v want=%v", err, test.wantErr)
			}
			writeExternalStageDecodeError(recorder, request, err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestExternalStagePullRejectsEveryBodyEnvelopeBeforeService(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*http.Request)
		body       string
		wantStatus int
	}{
		{name: "body", body: `{}`, wantStatus: http.StatusNotFound},
		{name: "content type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", externalstage.MediaTypeV1) }, wantStatus: http.StatusNotFound},
		{name: "content encoding", mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "identity") }, wantStatus: http.StatusNotFound},
		{name: "missing accept", mutate: func(r *http.Request) { r.Header.Del("Accept") }, wantStatus: http.StatusNotAcceptable},
		{name: "duplicate accept", mutate: func(r *http.Request) { r.Header.Add("Accept", externalstage.MediaTypeV1) }, wantStatus: http.StatusNotAcceptable},
		{name: "wrong accept", mutate: func(r *http.Request) { r.Header.Set("Accept", "application/json") }, wantStatus: http.StatusNotAcceptable},
		{name: "transfer encoding", mutate: func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }, wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/external-stage/handoffs/opaque-do-not-echo", strings.NewReader(test.body))
			request.Header.Set("Accept", externalstage.MediaTypeV1)
			if test.mutate != nil {
				test.mutate(request)
			}
			request = externalStageRequestWithPrincipal(t, request, 8101)
			recorder := httptest.NewRecorder()
			externalStageAdapterTestRouter().ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus || recorder.Header().Get("X-Permissions-Epoch") != "" ||
				strings.Contains(recorder.Body.String(), "opaque-do-not-echo") {
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
		externalStageRates.entries[id] = externalStageRateEntry{window: now, count: 119}
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
	if called != 1 || admittedRecorder.Code != http.StatusOK || admittedRecorder.Header().Get("Retry-After") != "" {
		t.Fatalf("existing key called=%d status=%d", called, admittedRecorder.Code)
	}
	overLimitRecorder := httptest.NewRecorder()
	handler.ServeHTTP(overLimitRecorder, admitted)
	if called != 1 || overLimitRecorder.Code != http.StatusTooManyRequests || overLimitRecorder.Header().Get("Retry-After") != "60" {
		t.Fatalf("over-limit key called=%d status=%d headers=%v", called, overLimitRecorder.Code, overLimitRecorder.Header())
	}

	newKey := externalStageRequestWithPrincipal(t, httptest.NewRequest(http.MethodGet, "/", nil), 1025)
	newRecorder := httptest.NewRecorder()
	handler.ServeHTTP(newRecorder, newKey)
	if called != 1 || newRecorder.Code != http.StatusTooManyRequests || newRecorder.Header().Get("Retry-After") != "60" ||
		newRecorder.Header().Get("X-Permissions-Epoch") != "" {
		t.Fatalf("new key called=%d status=%d headers=%v", called, newRecorder.Code, newRecorder.Header())
	}
}

func TestExternalStageMalformedSecretsShareCanonicalNotFound(t *testing.T) {
	secret := bytes.Repeat([]byte{0xfb}, externalstage.OneTimeSecretBytes)
	canonical := base64.RawURLEncoding.EncodeToString(secret)
	secretCases := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "duplicate", values: []string{canonical, canonical}},
		{name: "padded", values: []string{base64.URLEncoding.EncodeToString(secret)}},
		{name: "noncanonical alphabet", values: []string{base64.RawStdEncoding.EncodeToString(secret)}},
		{name: "oversized", values: []string{strings.Repeat("A", 4096)}},
	}
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "pull", method: http.MethodGet, path: "/api/external-stage/handoffs/opaque-do-not-echo"},
		{name: "accept", method: http.MethodPost, path: "/api/external-stage/handoffs/opaque-do-not-echo/accept", body: `{}`},
		{name: "report", method: http.MethodPost, path: "/api/external-stage/handoffs/opaque-do-not-echo/reports", body: `{}`},
	}
	var canonicalProblem []byte
	for _, route := range routes {
		for _, secretCase := range secretCases {
			t.Run(route.name+"/"+secretCase.name, func(t *testing.T) {
				request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				request.Header.Set("Accept", externalstage.MediaTypeV1)
				if route.method == http.MethodPost {
					request.Header.Set("Content-Type", externalstage.MediaTypeV1)
					request.Header.Set(idempotencyHeader, "81000000-0000-4000-8000-000000000810")
				}
				for _, value := range secretCase.values {
					request.Header.Add(externalstage.HandoffSecretHeader, value)
				}
				request = externalStageRequestWithPrincipal(t, request, 8110)
				recorder := httptest.NewRecorder()
				externalStageAdapterTestRouter().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusNotFound || recorder.Header().Get("X-Permissions-Epoch") != "" ||
					recorder.Header().Get("Allow") != "" || strings.Contains(recorder.Body.String(), "opaque-do-not-echo") ||
					strings.Contains(recorder.Body.String(), canonical) {
					t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
				}
				normalized := normalizedExternalStageProblem(t, recorder)
				if canonicalProblem == nil {
					canonicalProblem = normalized
				} else if !bytes.Equal(canonicalProblem, normalized) {
					t.Fatalf("not-found response drifted: got=%s want=%s", normalized, canonicalProblem)
				}
			})
		}
	}
}

func TestExternalStageMutationPinsNewAndDuplicateStatuses(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, externalstage.OneTimeSecretBytes)
	encodedSecret := base64.RawURLEncoding.EncodeToString(secret)
	tests := []struct {
		name       string
		duplicate  bool
		wantStatus int
	}{
		{name: "new", wantStatus: http.StatusCreated},
		{name: "duplicate", duplicate: true, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/external-stage/handoffs/opaque-do-not-echo/reports", nil)
			request.Header.Set(externalstage.HandoffSecretHeader, encodedSecret)
			request = externalStageRequestWithPrincipal(t, request, 8120)
			recorder := httptest.NewRecorder()
			externalStageMutation(recorder, request, func(externalstage.Principal, []byte) (externalstage.ReportReceipt, error) {
				return externalstage.ReportReceipt{Duplicate: test.duplicate}, nil
			})
			if recorder.Code != test.wantStatus || recorder.Header().Get("Content-Type") != externalstage.MediaTypeV1 ||
				recorder.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func externalStageTransportJSONRequest(t *testing.T, method, path, accept, idempotency string, body any,
	principal auth.Principal) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", externalstage.MediaTypeV1)
	request.Header.Set("Accept", accept)
	request.Header.Set(idempotencyHeader, idempotency)
	return externalStageRequestWithAuthPrincipal(request, principal)
}

func TestExternalStageTransportPinsSuccessAndReplayStatuses(t *testing.T) {
	fixture := setupExternalStageTransportFixture(t)
	router := externalStageTransportTestRouter()
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	createBody := externalstage.CreateHandoffRequest{
		StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1, ExpectedAuthorityEpoch: 1,
		ReporterRegistrationID: fixture.registrationID, ExpiresAt: expiresAt,
	}
	create := func() *httptest.ResponseRecorder {
		request := externalStageTransportJSONRequest(t, http.MethodPost,
			"/api/agent-mode/deliveries/"+fixture.deliveryKey+"/external-stage-handoffs",
			externalstage.MediaTypeV1, "81000000-0000-4000-8000-000000000814", createBody, fixture.operator)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	created := create()
	if created.Code != http.StatusCreated || created.Header().Get("Content-Type") != externalstage.MediaTypeV1 {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	var metadata externalstage.HandoffMetadata
	if err := json.Unmarshal(created.Body.Bytes(), &metadata); err != nil || metadata.HandoffID == "" {
		t.Fatalf("create metadata err=%v", err)
	}
	createReplay := create()
	if createReplay.Code != http.StatusOK {
		t.Fatalf("create replay status=%d headers=%v body=%s", createReplay.Code, createReplay.Header(), createReplay.Body.String())
	}
	credential := func(action, idempotency string, epoch int64) *httptest.ResponseRecorder {
		request := externalStageTransportJSONRequest(t, http.MethodPost,
			"/api/agent-mode/external-stage-handoffs/"+metadata.HandoffID+"/"+action,
			externalstage.SecretMediaTypeV1, idempotency,
			externalstage.CredentialEpochRequest{ExpectedCredentialEpoch: epoch}, fixture.operator)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	minted := credential("mint", "81000000-0000-4000-8000-000000000815", 0)
	if minted.Code != http.StatusCreated || minted.Header().Get("Content-Type") != externalstage.SecretMediaTypeV1 ||
		minted.Body.Len() != externalstage.OneTimeSecretBytes {
		t.Fatalf("mint status=%d headers=%v bytes=%d", minted.Code, minted.Header(), minted.Body.Len())
	}
	secret := append([]byte(nil), minted.Body.Bytes()...)
	defer zeroBytes(secret)
	zeroBytes(minted.Body.Bytes())
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	mutation := func(path, idempotency string, body any) *httptest.ResponseRecorder {
		request := externalStageTransportJSONRequest(t, http.MethodPost, path, externalstage.MediaTypeV1, idempotency,
			body, fixture.reporter)
		request.Header.Set(externalstage.HandoffSecretHeader, base64.RawURLEncoding.EncodeToString(secret))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	acceptPath := "/api/external-stage/handoffs/" + metadata.HandoffID + "/accept"
	acceptBody := externalstage.AcceptRequest{Sequence: 1, ObservedAt: observedAt}
	accepted := mutation(acceptPath, "81000000-0000-4000-8000-000000000816", acceptBody)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("accept status=%d headers=%v body=%s", accepted.Code, accepted.Header(), accepted.Body.String())
	}
	acceptedReplay := mutation(acceptPath, "81000000-0000-4000-8000-000000000816", acceptBody)
	if acceptedReplay.Code != http.StatusOK {
		t.Fatalf("accept replay status=%d headers=%v body=%s", acceptedReplay.Code, acceptedReplay.Header(), acceptedReplay.Body.String())
	}
	reportPath := "/api/external-stage/handoffs/" + metadata.HandoffID + "/reports"
	reportBody := externalstage.ReportRequest{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observedAt}
	reported := mutation(reportPath, "81000000-0000-4000-8000-000000000817", reportBody)
	if reported.Code != http.StatusCreated {
		t.Fatalf("report status=%d headers=%v body=%s", reported.Code, reported.Header(), reported.Body.String())
	}
	reportedReplay := mutation(reportPath, "81000000-0000-4000-8000-000000000817", reportBody)
	if reportedReplay.Code != http.StatusOK {
		t.Fatalf("report replay status=%d headers=%v body=%s", reportedReplay.Code, reportedReplay.Header(), reportedReplay.Body.String())
	}
	rotated := credential("rotate", "81000000-0000-4000-8000-000000000818", 1)
	if rotated.Code != http.StatusCreated || rotated.Header().Get("Content-Type") != externalstage.SecretMediaTypeV1 ||
		rotated.Body.Len() != externalstage.OneTimeSecretBytes {
		t.Fatalf("rotate status=%d headers=%v bytes=%d", rotated.Code, rotated.Header(), rotated.Body.Len())
	}
	currentSecret := append([]byte(nil), rotated.Body.Bytes()...)
	defer zeroBytes(currentSecret)
	zeroBytes(rotated.Body.Bytes())
	pullPath := "/api/external-stage/handoffs/" + metadata.HandoffID
	pull := func(principal auth.Principal, rawSecret []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, pullPath, nil)
		request.Header.Set("Accept", externalstage.MediaTypeV1)
		request.Header.Set(externalstage.HandoffSecretHeader, base64.RawURLEncoding.EncodeToString(rawSecret))
		request = externalStageRequestWithAuthPrincipal(request, principal)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	current := pull(fixture.reporter, currentSecret)
	if current.Code != http.StatusOK || current.Header().Get("Content-Type") != externalstage.MediaTypeV1 {
		t.Fatalf("current epoch pull status=%d headers=%v body=%s", current.Code, current.Header(), current.Body.String())
	}
	oldEpoch := pull(fixture.reporter, secret)
	wrongPrincipal, err := auth.NewAPIKeyPrincipal(fixture.reporter.APIKeyID()+1000, fixture.reporter.UserID(),
		auth.ScopeSet{auth.ScopeAll: {}})
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := pull(wrongPrincipal, currentSecret)
	for name, recorder := range map[string]*httptest.ResponseRecorder{"old epoch": oldEpoch, "wrong api key": wrongKey} {
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("X-Permissions-Epoch") != "" ||
			recorder.Header().Get("Allow") != "" {
			t.Fatalf("%s refusal status=%d headers=%v body=%s", name, recorder.Code, recorder.Header(), recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), base64.RawURLEncoding.EncodeToString(currentSecret)) ||
			strings.Contains(recorder.Body.String(), base64.RawURLEncoding.EncodeToString(secret)) {
			t.Fatalf("%s refusal reflected a handoff secret: %s", name, recorder.Body.String())
		}
	}
	if !bytes.Equal(normalizedExternalStageProblem(t, oldEpoch), normalizedExternalStageProblem(t, wrongKey)) {
		t.Fatalf("old epoch and wrong-key refusals diverged: old=%s wrong=%s", oldEpoch.Body.String(), wrongKey.Body.String())
	}
}

func TestExternalStageServiceConstructionFailureIsPrivate500(t *testing.T) {
	prior := db.DB
	db.DB = nil
	t.Cleanup(func() { db.DB = prior })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/external-stage/handoffs/01ARZ3NDEKTSV4RRFFQ69G5FAV", nil)
	service, ok := externalStageServiceForRequest(recorder, request)
	if ok || service != nil || recorder.Code != http.StatusInternalServerError {
		t.Fatalf("constructor failure service=%v ok=%v status=%d body=%q", service, ok, recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "database") || strings.Contains(recorder.Body.String(), "invalid") {
		t.Fatalf("constructor detail leaked: %q", recorder.Body.String())
	}
}
