// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// PAI-808. Agent Mode voice is intentionally separate from Voice Intake's
// HTTP handlers. It reuses only the provider and paid-call gates below those
// handlers: audio/transcripts are ephemeral and TTS is built solely from
// freshly authorized, server-owned structured facts.
package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
)

const agentModeVoiceJSONMaxBytes = 8 << 10

var agentModeVoiceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

var agentModeVoiceMediaTypes = map[string]bool{
	"audio/webm": true, "video/webm": true,
	"audio/mp4": true, "video/mp4": true,
	"audio/mpeg": true, "audio/wav": true, "audio/x-wav": true, "audio/ogg": true,
}

// TranscribeAgentModeVoice handles POST /api/agent-mode/voice/transcribe.
// ElevenLabs Scribe is batch-final, so this endpoint never claims interim
// segments: every successful response has final=true and a new utterance id.
func TranscribeAgentModeVoice(w http.ResponseWriter, r *http.Request) {
	language, ok := agentModeVoiceLanguageQuery(w, r)
	if !ok {
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	contentType = strings.ToLower(contentType)
	if err != nil || !agentModeVoiceMediaTypes[contentType] {
		jsonError(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	audio, err := io.ReadAll(io.LimitReader(r.Body, intakeAudioMaxBytes+1))
	if err != nil {
		jsonError(w, "read failed", http.StatusBadRequest)
		return
	}
	if len(audio) > intakeAudioMaxBytes {
		jsonError(w, "audio exceeds 12 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if len(audio) == 0 {
		jsonError(w, "empty audio", http.StatusUnprocessableEntity)
		return
	}
	user := auth.GetUser(r)
	if user == nil || user.ID <= 0 {
		httpcontract.WriteAgentModeNotFound(w, r)
		return
	}
	utteranceID, err := newAgentModeUtteranceID()
	if err != nil {
		log.Printf("agent mode voice: utterance id: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	settings, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("agent mode voice: load settings: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !settings.Available() {
		jsonError(w, "voice input is not configured", http.StatusServiceUnavailable)
		return
	}

	estimatedSeconds := voiceEstimateSeconds(len(audio))
	release, admitted := voiceAdmit(w, r, user, voiceActionAgentModeSTT, estimatedSeconds)
	if !admitted {
		return
	}
	defer release()

	started := time.Now()
	text, upstreamErr := transcribeVoice(r.Context(), settings, contentType, audio, language)
	latency := time.Since(started)
	outcome, errorClass := "ok", ""
	if upstreamErr != nil {
		outcome, errorClass = "fail_upstream", "upstream"
	} else if strings.TrimSpace(text) == "" {
		outcome = "no_op"
	}
	var billedSeconds int64
	if upstreamErr == nil {
		billedSeconds = estimatedSeconds
	}
	recordVoiceAICall(r.Context(), aiCallArgs{
		RequestID: newAIRequestID(), UserID: &user.ID, ActionKey: voiceActionAgentModeSTT,
		SubAction: language, Surface: "agent_mode", Provider: settings.Provider, Model: settings.STTModel,
		PromptTokens: int(billedSeconds), CostMicroUSD: billedSeconds * voiceSTTMicroUSDPerSecond,
		Outcome: outcome, ErrorClass: errorClass, LatencyMs: latency.Milliseconds(),
	})
	if upstreamErr != nil {
		log.Printf("agent mode voice: transcription upstream error: %s", sttErrSafe(upstreamErr))
		jsonError(w, "voice transcription failed", http.StatusBadGateway)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		jsonError(w, "no speech detected", http.StatusUnprocessableEntity)
		return
	}
	if len(text) > intakeChunkMaxBytes {
		text = truncateVoiceUTF8(text, intakeChunkMaxBytes)
	}
	jsonOK(w, struct {
		UtteranceID string `json:"utterance_id"`
		Text        string `json:"text"`
		Final       bool   `json:"final"`
	}{UtteranceID: utteranceID, Text: text, Final: true})
}

func agentModeVoiceLanguageQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		jsonError(w, "language must be en or de", http.StatusBadRequest)
		return "", false
	}
	if len(values) != 1 || len(values["language"]) != 1 {
		jsonError(w, "language must be en or de", http.StatusBadRequest)
		return "", false
	}
	language := values.Get("language")
	if language != "en" && language != "de" {
		jsonError(w, "language must be en or de", http.StatusBadRequest)
		return "", false
	}
	return language, true
}

func newAgentModeUtteranceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "utt_" + hex.EncodeToString(raw[:]), nil
}

type agentModeSpeakInput struct {
	Template         string   `json:"template"`
	DeliveryID       string   `json:"delivery_id"`
	DeliveryRevision string   `json:"delivery_revision"`
	CandidateIDs     []string `json:"candidate_ids"`
	Locale           string   `json:"locale"`
}

// SpeakAgentModeVoice handles POST /api/agent-mode/voice/speak. There is no
// caller-text field by design. The request identifies a closed template and
// current authorized facts; the server constructs every spoken byte.
func SpeakAgentModeVoice(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeAgentModeSpeakInput(w, r)
	if !ok {
		return
	}
	user := auth.GetUser(r)
	if user == nil || user.ID <= 0 {
		httpcontract.WriteAgentModeNotFound(w, r)
		return
	}
	row, candidates, err := loadAgentModeVoiceTargets(r, user.ID, input.DeliveryID, input.CandidateIDs)
	if err != nil {
		agentModeVoiceReadError(w, r, err)
		return
	}
	if row.DeliveryRevision != input.DeliveryRevision {
		problemJSON(w, r, ProblemDetails{Status: http.StatusConflict, Code: "stale_delivery_revision",
			Detail: "delivery revision is no longer current"})
		return
	}
	if input.Template == "note_ready" && !row.Capabilities.Comment {
		httpcontract.WriteAgentModeNotFound(w, r)
		return
	}
	// Configuration and paid-call gates intentionally follow every target,
	// revision, candidate, and capability check. Hidden/revoked targets cannot
	// be used to probe whether voice is configured or budget remains.
	settings, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("agent mode voice: load settings: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !settings.Available() {
		jsonError(w, "voice output is not configured", http.StatusServiceUnavailable)
		return
	}
	text := renderAgentModeVoiceTemplate(input.Template, input.Locale, row, candidates)
	characters := int64(len([]rune(text)))
	release, admitted := voiceAdmit(w, r, user, voiceActionAgentModeTTS, characters)
	if !admitted {
		return
	}
	defer release()

	started := time.Now()
	audio, upstreamErr := synthesizeVoice(r.Context(), settings, text, input.Locale)
	latency := time.Since(started)
	outcome, errorClass := "ok", ""
	if upstreamErr != nil {
		outcome, errorClass = "fail_upstream", "upstream"
	}
	var billedCharacters int64
	if upstreamErr == nil {
		billedCharacters = characters
	}
	issueID, projectID := row.IssueID, row.ProjectID
	recordVoiceAICall(r.Context(), aiCallArgs{
		RequestID: newAIRequestID(), UserID: &user.ID, ActionKey: voiceActionAgentModeTTS,
		SubAction: input.Template, Surface: "agent_mode", IssueID: &issueID, ProjectID: &projectID,
		Provider: settings.Provider, Model: settings.TTSModel,
		PromptTokens: int(billedCharacters), CostMicroUSD: billedCharacters * voiceTTSMicroUSDPerChar,
		Outcome: outcome, ErrorClass: errorClass, LatencyMs: latency.Milliseconds(),
	})
	if upstreamErr != nil {
		log.Printf("agent mode voice: synthesis upstream error: %s", sttErrSafe(upstreamErr))
		jsonError(w, "voice synthesis failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Language", input.Locale)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(audio)))
	_, _ = w.Write(audio)
}

func decodeAgentModeSpeakInput(w http.ResponseWriter, r *http.Request) (agentModeSpeakInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, agentModeVoiceJSONMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input agentModeSpeakInput
	if err := decoder.Decode(&input); err != nil {
		jsonError(w, "invalid Agent Mode voice template request", http.StatusBadRequest)
		return agentModeSpeakInput{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		jsonError(w, "invalid Agent Mode voice template request", http.StatusBadRequest)
		return agentModeSpeakInput{}, false
	}
	if (input.Template != "status" && input.Template != "note_ready" && input.Template != "clarification") ||
		!agentModeVoiceIDPattern.MatchString(input.DeliveryID) ||
		!validAgentModeVoiceRevision(input.DeliveryRevision) ||
		(input.Locale != "en" && input.Locale != "de") || input.CandidateIDs == nil || len(input.CandidateIDs) > 3 {
		jsonError(w, "invalid Agent Mode voice template request", http.StatusBadRequest)
		return agentModeSpeakInput{}, false
	}
	if input.Template == "clarification" {
		if len(input.CandidateIDs) == 0 {
			jsonError(w, "clarification requires one to three candidates", http.StatusBadRequest)
			return agentModeSpeakInput{}, false
		}
	} else if len(input.CandidateIDs) != 0 {
		jsonError(w, "candidate_ids are allowed only for clarification", http.StatusBadRequest)
		return agentModeSpeakInput{}, false
	}
	seen := make(map[string]bool, len(input.CandidateIDs))
	for _, candidateID := range input.CandidateIDs {
		if !agentModeVoiceIDPattern.MatchString(candidateID) || seen[candidateID] {
			jsonError(w, "candidate_ids must be unique delivery identifiers", http.StatusBadRequest)
			return agentModeSpeakInput{}, false
		}
		seen[candidateID] = true
	}
	return input, true
}

func validAgentModeVoiceRevision(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func loadAgentModeVoiceTargets(r *http.Request, userID int64, deliveryID string, candidateIDs []string) (agentmode.DeliveryRow, []agentmode.DeliveryRow, error) {
	// One Reader call gives the primary and every candidate a single SQLite
	// authorization/fact snapshot. Sequential detail reads leave a TOCTOU gap
	// where a candidate can be revoked or become terminal after its individual
	// check but before paid synthesis.
	detailKeys := make([]string, 0, len(candidateIDs)+1)
	detailKeys = append(detailKeys, deliveryID)
	for _, candidateID := range candidateIDs {
		if candidateID != deliveryID {
			detailKeys = append(detailKeys, candidateID)
		}
	}
	snapshot, err := agentmode.NewReader(db.DB, agentmode.ReaderOptions{Freshness: deliveryFreshnessPolicy()}).Read(
		r.Context(), agentmode.Request{UserID: userID, DetailDeliveryKeys: detailKeys,
			Filters: agentmode.Filters{Attention: "all", Health: "all", SelectedDelivery: deliveryID}})
	if err != nil {
		return agentmode.DeliveryRow{}, nil, err
	}
	var primary *agentmode.DeliveryRow
	activeByID := make(map[string]agentmode.DeliveryRow, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		activeByID[row.DeliveryID] = row
		if row.DeliveryID == deliveryID {
			copy := row
			primary = &copy
		}
	}
	if primary == nil && snapshot.SelectedOutside != nil && snapshot.SelectedOutside.Row.DeliveryID == deliveryID {
		copy := snapshot.SelectedOutside.Row
		primary = &copy
	}
	if primary == nil {
		return agentmode.DeliveryRow{}, nil, agentmode.ErrNotFound
	}
	candidates := make([]agentmode.DeliveryRow, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		candidate, exists := activeByID[candidateID]
		if !exists {
			return agentmode.DeliveryRow{}, nil, agentmode.ErrNotFound
		}
		candidates = append(candidates, candidate)
	}
	return *primary, candidates, nil
}

func agentModeVoiceReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, agentmode.ErrNotFound) || errors.Is(err, agentmode.ErrUnauthorized) {
		httpcontract.WriteAgentModeNotFound(w, r)
		return
	}
	if errors.Is(err, agentmode.ErrInvalid) {
		jsonError(w, "invalid Agent Mode voice template request", http.StatusBadRequest)
		return
	}
	log.Printf("agent mode voice: authorized delivery read: %v", err)
	problemJSON(w, r, ProblemDetails{Status: http.StatusInternalServerError,
		Detail: "Agent Mode voice facts unavailable"})
}

func renderAgentModeVoiceTemplate(template, locale string, row agentmode.DeliveryRow, candidates []agentmode.DeliveryRow) string {
	switch template {
	case "note_ready":
		if locale == "de" {
			return "Interne Notiz für " + row.IssueKey + " ist bereit. Bestätigen oder abbrechen."
		}
		return "Internal note for " + row.IssueKey + " is ready. Confirm or cancel."
	case "clarification":
		parts := make([]string, 0, len(candidates))
		for index, candidate := range candidates {
			parts = append(parts, fmt.Sprintf("%d: %s", index+1, candidate.IssueKey))
		}
		if locale == "de" {
			return "Bitte auswählen. " + strings.Join(parts, ". ") + "."
		}
		return "Please choose. " + strings.Join(parts, ". ") + "."
	default:
		return renderAgentModeStatus(locale, row)
	}
}

func renderAgentModeStatus(locale string, row agentmode.DeliveryRow) string {
	stage := localizedVoiceFact(locale, "stage", row.Stage.Key)
	activity := localizedVoiceFact(locale, "activity", row.Activity.Kind)
	freshness := localizedVoiceFact(locale, "freshness", row.Freshness.State)
	attention := localizedVoiceFact(locale, "attention", row.Attention.Reason)
	confidence := localizedVoiceFact(locale, "confidence", row.Trust.ConfidenceLabel)
	suppression := localizedVoiceFact(locale, "suppression", row.Trust.Suppression)
	if locale == "de" {
		parts := []string{row.IssueKey + ". Phase: " + stage + ".", "Aktivität: " + activity + ".",
			"Aktualität: " + freshness + ".", fmt.Sprintf("Blocker: %d.", len(row.Blockers)), "Vertrauen: " + confidence + "."}
		if attention != "" {
			parts = append(parts, "Hinweis: "+attention+".")
		}
		if row.Trust.Suppression != "" {
			parts = append(parts, "Schätzung unterdrückt: "+suppression+".")
		}
		parts = append(parts, trustedAgentModeEstimate(locale, row)...)
		return strings.Join(parts, " ")
	}
	parts := []string{row.IssueKey + ". Stage: " + stage + ".", "Activity: " + activity + ".",
		"Freshness: " + freshness + ".", fmt.Sprintf("Blockers: %d.", len(row.Blockers)), "Trust confidence: " + confidence + "."}
	if attention != "" {
		parts = append(parts, "Attention: "+attention+".")
	}
	if row.Trust.Suppression != "" {
		parts = append(parts, "Estimate suppressed: "+suppression+".")
	}
	parts = append(parts, trustedAgentModeEstimate(locale, row)...)
	return strings.Join(parts, " ")
}

func trustedAgentModeEstimate(locale string, row agentmode.DeliveryRow) []string {
	if row.Freshness.State != "fresh" || row.Health != agentmode.HealthHealthy || row.Trust.Suppression != "" {
		return nil
	}
	confidence := row.Trust.ConfidenceLabel
	preciseConfidence := confidence == "high" || confidence == "medium"
	parts := []string{}
	if preciseConfidence && row.Progress != nil && row.Progress.Trusted && row.Progress.Percent != nil {
		if locale == "de" {
			parts = append(parts, fmt.Sprintf("Fortschritt: %d Prozent.", *row.Progress.Percent))
		} else {
			parts = append(parts, fmt.Sprintf("Progress: %d percent.", *row.Progress.Percent))
		}
	}
	if row.ETA == nil || !row.ETA.Trusted {
		return parts
	}
	if confidence == "low" && row.Trust.RangeOnly && row.ETA.OptimisticAt != nil && row.ETA.PessimisticAt != nil {
		if locale == "de" {
			parts = append(parts, "Geschätztes Zeitfenster: "+voiceTime(*row.ETA.OptimisticAt)+" bis "+voiceTime(*row.ETA.PessimisticAt)+".")
		} else {
			parts = append(parts, "Estimated window: "+voiceTime(*row.ETA.OptimisticAt)+" to "+voiceTime(*row.ETA.PessimisticAt)+".")
		}
		return parts
	}
	if preciseConfidence && row.ETA.LandingAt != nil {
		if locale == "de" {
			parts = append(parts, "Geschätzter Abschluss: "+voiceTime(*row.ETA.LandingAt)+".")
		} else {
			parts = append(parts, "Estimated landing: "+voiceTime(*row.ETA.LandingAt)+".")
		}
	}
	return parts
}

func voiceTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04 UTC")
}

func localizedVoiceFact(locale, kind, value string) string {
	if value == "" {
		return ""
	}
	translations := map[string]map[string]string{
		"stage": {
			"specification": "Spezifikation", "implementation": "Umsetzung", "qa": "Qualitätssicherung",
			"deployment": "Bereitstellung", "verification": "Überprüfung", "unknown": "unbekannt",
		},
		"activity": {
			"working": "in Arbeit", "blocked": "blockiert", "waiting": "wartet", "idle": "inaktiv",
			"testing": "Test", "deploying": "Bereitstellung", "verifying": "Überprüfung", "unknown": "unbekannt",
		},
		"freshness": {"fresh": "aktuell", "aging": "alternd", "stale": "veraltet", "unknown": "unbekannt"},
		"attention": {
			"blocked": "blockiert", "waiting_needs_input": "Eingabe erforderlich", "failed_needs_retry": "Wiederholung erforderlich",
			"stale_no_signal": "kein aktuelles Signal", "unknown_reporter": "Quelle unbekannt", "deployed_unverified": "bereitgestellt, nicht überprüft",
		},
		"confidence": {"unknown": "unbekannt", "low": "niedrig", "medium": "mittel", "high": "hoch"},
		"suppression": {
			"terminal_complete": "abgeschlossen", "cancelled": "abgebrochen", "terminal_failed": "fehlgeschlagen",
			"waiting_on_human": "wartet auf Eingabe", "blocked": "blockiert", "stale": "veraltet",
			"unknown_reporter": "Quelle unbekannt", "no_signal": "kein Signal", "estimate_expired": "Schätzung abgelaufen",
			"outlier_heavy": "zu viele Ausreißer", "insufficient_basis": "unzureichende Grundlage",
			"missing_contributor": "fehlende Grundlage",
		},
	}
	if locale == "de" {
		if translated := translations[kind][value]; translated != "" {
			return translated
		}
		return "unbekannt"
	}
	english := map[string]map[string]string{
		"stage":      {"specification": "Specification", "implementation": "Implementation", "qa": "QA", "deployment": "Deployment", "verification": "Verification", "unknown": "unknown"},
		"activity":   {"working": "working", "blocked": "blocked", "waiting": "waiting", "idle": "idle", "testing": "testing", "deploying": "deploying", "verifying": "verifying", "unknown": "unknown"},
		"freshness":  {"fresh": "fresh", "aging": "aging", "stale": "stale", "unknown": "unknown"},
		"attention":  {"blocked": "blocked", "waiting_needs_input": "input required", "failed_needs_retry": "retry required", "stale_no_signal": "no recent signal", "unknown_reporter": "reporter unknown", "deployed_unverified": "deployed, not verified"},
		"confidence": {"unknown": "unknown", "low": "low", "medium": "medium", "high": "high"},
		"suppression": {
			"terminal_complete": "complete", "cancelled": "cancelled", "terminal_failed": "failed",
			"waiting_on_human": "waiting on input", "blocked": "blocked", "stale": "stale",
			"unknown_reporter": "reporter unknown", "no_signal": "no signal", "estimate_expired": "estimate expired",
			"outlier_heavy": "too many outliers", "insufficient_basis": "insufficient basis",
			"missing_contributor": "missing contributor",
		},
	}
	if translated := english[kind][value]; translated != "" {
		return translated
	}
	return "unknown"
}
