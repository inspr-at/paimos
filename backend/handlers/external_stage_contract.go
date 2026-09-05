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

package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const externalStageMaxBody = 64 << 10

var (
	errExternalStageContentType     = errors.New("external stage content type")
	errExternalStageAccept          = errors.New("external stage accept")
	errExternalStageTooLarge        = errors.New("external stage body too large")
	errExternalStageResponseWritten = errors.New("external stage response already written")
)

func externalStageMountPath(path, prefix string) string { return strings.TrimPrefix(path, prefix) }

func externalStageService() (*externalstage.Service, error) {
	return externalStageServiceWithAuthorizer(nil)
}

func externalStageServiceWithAuthorizer(authorizer delivery.Authorizer) (*externalstage.Service, error) {
	return externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Observer: agentmode.NotifyChange,
		DeliveryAuthorizer: authorizer, DeliveryFreshness: deliveryFreshnessPolicy(),
	})
}

func externalStageServiceForRequest(w http.ResponseWriter, r *http.Request) (*externalstage.Service, bool) {
	service, err := externalStageServiceWithAuthorizer(delivery.AuthorizerFunc(func(_ context.Context, req delivery.AuthorizationRequest) error {
		principal, ok := auth.GetPrincipal(r)
		if !ok || req.Actor.Type != "user" || req.Actor.OpaqueKey != "user:"+strconv.FormatInt(principal.UserID(), 10) {
			return errors.New("authenticated operator identity required")
		}
		return nil
	}))
	if err != nil {
		writeExternalStageStatus(w, r, http.StatusInternalServerError)
		return nil, false
	}
	return service, true
}

func externalStagePrincipal(r *http.Request) (externalstage.Principal, bool) {
	p, ok := auth.GetPrincipal(r)
	if !ok {
		return externalstage.Principal{}, false
	}
	return externalstage.Principal{UserID: p.UserID(), Kind: string(p.Kind()),
		SessionCredentialID: p.SessionCredentialID(), APIKeyID: p.APIKeyID()}, true
}

// ExternalStageAPIKeyAuth has no session fallback and intentionally maps all
// authentication failures to the canonical private 404 without an epoch hint.
func ExternalStageAPIKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			writeControlNotFound(w, r)
			return
		}
		raw := strings.TrimPrefix(values[0], "Bearer ")
		if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
			writeControlNotFound(w, r)
			return
		}
		user, principal, err := auth.ResolveAPIKeyPrincipal(raw)
		if err != nil || principal.Kind() != auth.PrincipalAPIKey {
			writeControlNotFound(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), auth.UserKey, user)
		ctx = auth.WithAccessCache(ctx)
		ctx = auth.WithScopes(ctx, principal.Scopes())
		ctx = auth.WithPrincipal(ctx, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type externalStageRateEntry struct {
	window time.Time
	count  int
}

var externalStageRates = struct {
	sync.Mutex
	entries map[int64]externalStageRateEntry
}{entries: make(map[int64]externalStageRateEntry)}

var internalExternalStageRates = struct {
	sync.Mutex
	entries map[string]externalStageRateEntry
}{entries: make(map[string]externalStageRateEntry)}

// The map is capped and stale entries are shed before admitting a new key.
func externalStageRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.GetPrincipal(r)
		if !ok || principal.Kind() != auth.PrincipalAPIKey {
			writeControlNotFound(w, r)
			return
		}
		now := time.Now().UTC()
		externalStageRates.Lock()
		_, admitted := externalStageRates.entries[principal.APIKeyID()]
		if !admitted && len(externalStageRates.entries) >= 1024 {
			for id, entry := range externalStageRates.entries {
				if now.Sub(entry.window) >= time.Minute {
					delete(externalStageRates.entries, id)
				}
			}
			if len(externalStageRates.entries) >= 1024 {
				externalStageRates.Unlock()
				w.Header().Set("Retry-After", "60")
				writeExternalStageStatus(w, r, http.StatusTooManyRequests)
				return
			}
		}
		entry := externalStageRates.entries[principal.APIKeyID()]
		if entry.window.IsZero() || now.Sub(entry.window) >= time.Minute {
			entry = externalStageRateEntry{window: now}
		}
		entry.count++
		externalStageRates.entries[principal.APIKeyID()] = entry
		externalStageRates.Unlock()
		if entry.count > 120 {
			w.Header().Set("Retry-After", "60")
			writeExternalStageStatus(w, r, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func internalExternalStageRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.GetPrincipal(r)
		if !ok {
			writeControlNotFound(w, r)
			return
		}
		var admissionKey string
		switch principal.Kind() {
		case auth.PrincipalSession:
			if principal.SessionCredentialID() != "" {
				admissionKey = "session:" + principal.SessionCredentialID()
			}
		case auth.PrincipalAPIKey:
			if principal.APIKeyID() > 0 {
				admissionKey = "api-key:" + strconv.FormatInt(principal.APIKeyID(), 10)
			}
		}
		if admissionKey == "" {
			writeControlNotFound(w, r)
			return
		}
		now := time.Now().UTC()
		internalExternalStageRates.Lock()
		_, admitted := internalExternalStageRates.entries[admissionKey]
		if !admitted && len(internalExternalStageRates.entries) >= 1024 {
			for key, entry := range internalExternalStageRates.entries {
				if now.Sub(entry.window) >= time.Minute {
					delete(internalExternalStageRates.entries, key)
				}
			}
			if len(internalExternalStageRates.entries) >= 1024 {
				internalExternalStageRates.Unlock()
				w.Header().Set("Retry-After", "60")
				writeExternalStageStatus(w, r, http.StatusTooManyRequests)
				return
			}
		}
		entry := internalExternalStageRates.entries[admissionKey]
		if entry.window.IsZero() || now.Sub(entry.window) >= time.Minute {
			entry = externalStageRateEntry{window: now}
		}
		entry.count++
		internalExternalStageRates.entries[admissionKey] = entry
		internalExternalStageRates.Unlock()
		if entry.count > 120 {
			w.Header().Set("Retry-After", "60")
			writeExternalStageStatus(w, r, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func MountInternalExternalStageContractRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireControlAuthority(auth.ScopeAgentControlsWrite, false))
		r.Use(internalExternalStageRateLimit)
		r.Post(externalStageMountPath(externalstage.InternalCreatePath, "/api/agent-mode"), createExternalStageHandoff)
		r.Post(externalStageMountPath(externalstage.InternalMintPath, "/api/agent-mode"), mintExternalStageHandoff)
		r.Post(externalStageMountPath(externalstage.InternalRotatePath, "/api/agent-mode"), rotateExternalStageHandoff)
		r.Post(externalStageMountPath(externalstage.InternalRevokePath, "/api/agent-mode"), revokeExternalStageHandoff)
		r.Get(externalStageMountPath(externalstage.AdminRegistrationsPath, "/api/agent-mode"), listExternalStageRegistrations)
		r.Post(externalStageMountPath(externalstage.AdminRegistrationsPath, "/api/agent-mode"), registerExternalStageReporter)
		r.Post(externalStageMountPath(externalstage.AdminRegistrationRevokePath, "/api/agent-mode"), revokeExternalStageReporter)
		r.Post(externalStageMountPath(externalstage.AdminPrerequisiteSetsPath, "/api/agent-mode"), sealExternalStagePrerequisites)
		r.Post(externalStageMountPath(externalstage.AdminOwnerActivationsPath, "/api/agent-mode"), activateExternalStageOwner)
	})
}

func MountExternalStageContractRoutes(r chi.Router) {
	r.Use(externalStageRateLimit)
	r.Get(externalStageMountPath(externalstage.ExternalPullPath, "/api"), pullExternalStageHandoff)
	r.Post(externalStageMountPath(externalstage.ExternalAcceptPath, "/api"), acceptExternalStageHandoff)
	r.Post(externalStageMountPath(externalstage.ExternalReportPath, "/api"), reportExternalStageHandoff)
}

func exactHeader(r *http.Request, name, value string) bool {
	values := r.Header.Values(name)
	return len(values) == 1 && values[0] == value
}

func externalStageExternalMedia(r *http.Request, hasBody bool) (string, error) {
	acceptValues := r.Header.Values("Accept")
	if hasBody {
		contentTypeValues := r.Header.Values("Content-Type")
		if len(contentTypeValues) != 1 ||
			(contentTypeValues[0] != externalstage.MediaTypeV1 && contentTypeValues[0] != externalstage.MediaTypeV2) {
			return "", errExternalStageContentType
		}
	}
	if len(acceptValues) != 1 || (acceptValues[0] != externalstage.MediaTypeV1 && acceptValues[0] != externalstage.MediaTypeV2) {
		return "", errExternalStageAccept
	}
	if hasBody && !exactHeader(r, "Content-Type", acceptValues[0]) {
		return "", errExternalStageContentType
	}
	return acceptValues[0], nil
}

func decodeExternalStageJSON(w http.ResponseWriter, r *http.Request, contentType, accept string, dst any) error {
	if !exactHeader(r, "Content-Type", contentType) || len(r.Header.Values("Content-Encoding")) != 0 {
		return errExternalStageContentType
	}
	if !exactHeader(r, "Accept", accept) {
		return errExternalStageAccept
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, externalStageMaxBody))
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return errExternalStageTooLarge
	}
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return externalstage.ErrInvalid
	}
	if err := rejectDuplicateJSONNames(raw); err != nil {
		return externalstage.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return externalstage.ErrInvalid
	}
	if err := decoder.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return externalstage.ErrInvalid
	}
	return nil
}

func writeExternalStageDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errExternalStageContentType):
		writeExternalStageStatus(w, r, http.StatusUnsupportedMediaType)
	case errors.Is(err, errExternalStageAccept):
		writeExternalStageStatus(w, r, http.StatusNotAcceptable)
	case errors.Is(err, errExternalStageTooLarge):
		writeExternalStageStatus(w, r, http.StatusRequestEntityTooLarge)
	default:
		writeExternalStageStatus(w, r, http.StatusBadRequest)
	}
}

func externalStageIdempotency(r *http.Request) (string, error) {
	if _, err := ControlIdempotencyKeyDigest(r); err != nil {
		return "", externalstage.ErrInvalid
	}
	return r.Header.Get(idempotencyHeader), nil
}

func externalStageSecret(r *http.Request) ([]byte, error) {
	values := r.Header.Values(externalstage.HandoffSecretHeader)
	if len(values) != 1 || len(values[0]) != base64.RawURLEncoding.EncodedLen(externalstage.OneTimeSecretBytes) {
		return nil, externalstage.ErrNotFound
	}
	raw, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(raw) != externalstage.OneTimeSecretBytes || base64.RawURLEncoding.EncodeToString(raw) != values[0] {
		zeroBytes(raw)
		return nil, externalstage.ErrNotFound
	}
	return raw, nil
}

func writeExternalStageJSON(w http.ResponseWriter, status int, media string, value any) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", media)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeExternalStageStatus(w http.ResponseWriter, r *http.Request, status int) {
	requestID := trustedControlResponseRequestID(r)
	w.Header().Set(RequestIDHeader, requestID)
	SetControlCachePolicy(w)
	writeProblem(w, nil, ProblemDetails{Status: status, Code: "external_stage_refused",
		Detail: "external stage request was refused", RequestID: requestID})
}

func writeExternalStageServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, externalstage.ErrNotFound):
		writeControlNotFound(w, r)
	case errors.Is(err, externalstage.ErrInvalid):
		writeExternalStageStatus(w, r, http.StatusBadRequest)
	case errors.Is(err, externalstage.ErrConflict):
		writeExternalStageStatus(w, r, http.StatusConflict)
	case errors.Is(err, externalstage.ErrUnavailable):
		writeExternalStageStatus(w, r, http.StatusServiceUnavailable)
	default:
		writeExternalStageStatus(w, r, http.StatusInternalServerError)
	}
}

func createExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	var body externalstage.CreateHandoffRequest
	if err := decodeExternalStageJSON(w, r, externalstage.MediaTypeV1, externalstage.MediaTypeV1, &body); err != nil {
		writeExternalStageDecodeError(w, r, err)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.CreateHandoff(r.Context(), p, chi.URLParam(r, "deliveryKey"), idem, body)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Duplicate {
		status = http.StatusOK
	}
	writeExternalStageJSON(w, status, externalstage.MediaTypeV1, out.HandoffMetadata)
}

func mintExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	credentialExternalStageHandoff(w, r, false)
}
func rotateExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	credentialExternalStageHandoff(w, r, true)
}
func credentialExternalStageHandoff(w http.ResponseWriter, r *http.Request, rotate bool) {
	var body externalstage.CredentialEpochRequest
	if err := decodeExternalStageJSON(w, r, externalstage.MediaTypeV1, externalstage.SecretMediaTypeV1, &body); err != nil {
		writeExternalStageDecodeError(w, r, err)
		return
	}
	if _, err := externalStageIdempotency(r); err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	secret, err := service.Mint(r.Context(), p, chi.URLParam(r, "handoffID"), body.ExpectedCredentialEpoch, rotate)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	defer zeroBytes(secret)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", externalstage.SecretMediaTypeV1)
	w.Header().Set("Content-Length", strconv.Itoa(externalstage.OneTimeSecretBytes))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(secret)
}

func revokeExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	var body externalstage.RevokeHandoffRequest
	if err := decodeExternalStageJSON(w, r, externalstage.MediaTypeV1, externalstage.MediaTypeV1, &body); err != nil {
		writeExternalStageDecodeError(w, r, err)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.Revoke(r.Context(), p, chi.URLParam(r, "handoffID"), idem, body.ExpectedCredentialEpoch)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, 200, externalstage.MediaTypeV1, out)
}

func pullExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	media, negotiationErr := externalStageExternalMedia(r, false)
	if negotiationErr != nil {
		writeExternalStageDecodeError(w, r, negotiationErr)
		return
	}
	if len(r.Header.Values("Content-Type")) != 0 || len(r.Header.Values("Content-Encoding")) != 0 ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeControlNotFound(w, r)
		return
	}
	secret, err := externalStageSecret(r)
	if err != nil {
		writeControlNotFound(w, r)
		return
	}
	defer zeroBytes(secret)
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.Pull(r.Context(), p, chi.URLParam(r, "handoffID"), secret)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	if media == externalstage.MediaTypeV2 {
		fixture := "sha256:" + contracts.ExternalStageV2FixtureDigestHex
		writeExternalStageJSON(w, 200, media, externalstage.NewPullResponseV2(out, fixture))
		return
	}
	writeExternalStageJSON(w, 200, media, out)
}

func acceptExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	media, negotiationErr := externalStageExternalMedia(r, true)
	if negotiationErr != nil {
		writeExternalStageDecodeError(w, r, negotiationErr)
		return
	}
	var body externalstage.AcceptRequest
	if err := decodeExternalStageJSON(w, r, media, media, &body); err != nil {
		writeExternalStageDecodeError(w, r, err)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	externalStageMutationWithMedia(w, r, media, func(p externalstage.Principal, secret []byte) (externalstage.ReportReceipt, error) {
		service, ok := externalStageServiceForRequest(w, r)
		if !ok {
			return externalstage.ReportReceipt{}, errExternalStageResponseWritten
		}
		return service.Accept(r.Context(), p, chi.URLParam(r, "handoffID"), idem, secret, body)
	})
}
func reportExternalStageHandoff(w http.ResponseWriter, r *http.Request) {
	media, negotiationErr := externalStageExternalMedia(r, true)
	if negotiationErr != nil {
		writeExternalStageDecodeError(w, r, negotiationErr)
		return
	}
	var bodyV1 externalstage.ReportRequest
	var bodyV2 externalstage.ReportRequestV2
	decodeTarget := any(&bodyV1)
	if media == externalstage.MediaTypeV2 {
		decodeTarget = &bodyV2
	}
	if err := decodeExternalStageJSON(w, r, media, media, decodeTarget); err != nil {
		writeExternalStageDecodeError(w, r, err)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	externalStageMutationWithMedia(w, r, media, func(p externalstage.Principal, secret []byte) (externalstage.ReportReceipt, error) {
		service, ok := externalStageServiceForRequest(w, r)
		if !ok {
			return externalstage.ReportReceipt{}, errExternalStageResponseWritten
		}
		if media == externalstage.MediaTypeV2 {
			return service.ReportV2(r.Context(), p, chi.URLParam(r, "handoffID"), idem, secret, bodyV2)
		}
		return service.Report(r.Context(), p, chi.URLParam(r, "handoffID"), idem, secret, bodyV1)
	})
}
func externalStageMutation(w http.ResponseWriter, r *http.Request, fn func(externalstage.Principal, []byte) (externalstage.ReportReceipt, error)) {
	externalStageMutationWithMedia(w, r, externalstage.MediaTypeV1, fn)
}

func externalStageMutationWithMedia(w http.ResponseWriter, r *http.Request, media string, fn func(externalstage.Principal, []byte) (externalstage.ReportReceipt, error)) {
	secret, err := externalStageSecret(r)
	if err != nil {
		writeControlNotFound(w, r)
		return
	}
	defer zeroBytes(secret)
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	out, err := fn(p, secret)
	if err != nil {
		if errors.Is(err, errExternalStageResponseWritten) {
			return
		}
		writeExternalStageServiceError(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Duplicate {
		status = http.StatusOK
	}
	writeExternalStageJSON(w, status, media, out)
}
func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func decodeExternalStageAdmin(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeExternalStageJSON(w, r, "application/json", "application/json", dst)
}
func registerExternalStageReporter(w http.ResponseWriter, r *http.Request) {
	var body externalstage.RegisterReporterRequest
	if err := decodeExternalStageAdmin(w, r, &body); err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, _ := externalStagePrincipal(r)
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.RegisterReporter(r.Context(), p, chi.URLParam(r, "deliveryKey"), idem, body)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, 201, "application/json", out)
}
func listExternalStageRegistrations(w http.ResponseWriter, r *http.Request) {
	p, _ := externalStagePrincipal(r)
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.ListReporters(r.Context(), p, chi.URLParam(r, "deliveryKey"))
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, 200, "application/json", out)
}
func revokeExternalStageReporter(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if err := decodeExternalStageAdmin(w, r, &body); err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "registrationID"), 10, 64)
	if err != nil {
		writeControlNotFound(w, r)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, _ := externalStagePrincipal(r)
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.RevokeReporter(r.Context(), p, chi.URLParam(r, "deliveryKey"), idem, id)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, 200, "application/json", out)
}
func sealExternalStagePrerequisites(w http.ResponseWriter, r *http.Request) {
	var body externalstage.SealPrerequisitesRequest
	if err := decodeExternalStageAdmin(w, r, &body); err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, 400)
		return
	}
	p, _ := externalStagePrincipal(r)
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.SealPrerequisites(r.Context(), p, chi.URLParam(r, "deliveryKey"), idem, body)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, 201, "application/json", out)
}

func activateExternalStageOwner(w http.ResponseWriter, r *http.Request) {
	var body externalstage.ActivateOwnerRequest
	if err := decodeExternalStageAdmin(w, r, &body); err != nil {
		writeExternalStageStatus(w, r, http.StatusBadRequest)
		return
	}
	idem, err := externalStageIdempotency(r)
	if err != nil {
		writeExternalStageStatus(w, r, http.StatusBadRequest)
		return
	}
	p, ok := externalStagePrincipal(r)
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	service, ok := externalStageServiceForRequest(w, r)
	if !ok {
		return
	}
	out, err := service.ActivateOwner(r.Context(), p, chi.URLParam(r, "deliveryKey"), idem, body)
	if err != nil {
		writeExternalStageServiceError(w, r, err)
		return
	}
	writeExternalStageJSON(w, http.StatusCreated, "application/json", out)
}
