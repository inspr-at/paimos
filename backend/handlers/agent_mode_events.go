// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/db"
)

const (
	agentModePollInterval      = time.Second
	agentModeHeartbeatInterval = 15 * time.Second
)

func AgentModeEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		agentModeError(w, r, fmt.Errorf("agent mode streaming unavailable: response writer does not flush"))
		return
	}
	resume, forcedReset, headerPresent := agentModeResumeToken(r)
	values, queryResume, queryReset, queryErr := parseAgentModeEventQuery(r.URL.RawQuery, headerPresent)
	if queryErr != nil {
		agentModeError(w, r, queryErr)
		return
	}
	if !headerPresent {
		resume, forcedReset = queryResume, queryReset
	}
	request, err := agentModeRequestValues(r, values, false, false)
	if err != nil {
		agentModeError(w, r, err)
		return
	}
	if forcedReset {
		setAgentModeEventHeaders(w)
		writeAgentModeReset(w, flusher)
		return
	}
	streamer := agentmode.NewStreamer(db.DB, agentmode.StreamerOptions{Freshness: deliveryFreshnessPolicy()})
	var session *agentmode.StreamSession
	compatible := []agentmode.Request{request}
	if request.Filters.ProjectID != nil {
		projectID := *request.Filters.ProjectID
		projectRoute := request
		projectRoute.RouteProjectID = &projectID
		projectRoute.Filters.ProjectID = nil
		compatible = append(compatible, projectRoute)
	}
	session, err = streamer.OpenCompatible(r.Context(), compatible, resume)
	if err != nil && !errors.Is(err, agentmode.ErrReset) {
		agentModeError(w, r, err)
		return
	}
	setAgentModeEventHeaders(w)
	if errors.Is(err, agentmode.ErrReset) {
		writeAgentModeReset(w, flusher)
		return
	}
	defer session.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = fmt.Fprint(w, ":connected\n\n")
	flusher.Flush()

	poll := time.NewTicker(agentModePollInterval)
	heartbeat := time.NewTicker(agentModeHeartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()
	drain := func() bool {
		batch, err := session.Drain(r.Context())
		if err != nil {
			writeAgentModeReset(w, flusher)
			return false
		}
		if batch.Kind == "" {
			return true
		}
		writeAgentModeBatch(w, flusher, batch)
		return true
	}
	if !drain() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-session.Wake():
			if !drain() {
				return
			}
		case <-poll.C:
			if !drain() {
				return
			}
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// Last-Event-ID always wins, including when it is present but empty or
// duplicated. A malformed authoritative header produces the same reset as an
// invalid query cursor and is never replaced by the query value.
func agentModeResumeToken(r *http.Request) (token string, forcedReset, headerPresent bool) {
	key := textproto.CanonicalMIMEHeaderKey("Last-Event-ID")
	if values, present := r.Header[key]; present {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", true, true
		}
		return strings.TrimSpace(values[0]), false, true
	}
	return "", false, false
}

// parseAgentModeEventQuery parses the non-cursor query exactly once. Cursor
// pairs are partitioned first so an authoritative Last-Event-ID can ignore
// them completely without also hiding malformed result-shaping filters.
func parseAgentModeEventQuery(raw string, ignoreCursor bool) (url.Values, string, bool, error) {
	nonCursor := make([]string, 0)
	cursors := make([]string, 0)
	cursorMalformed := false
	if raw != "" {
		for _, pair := range strings.Split(raw, "&") {
			keyRaw, valueRaw, hasValue := strings.Cut(pair, "=")
			key, err := url.QueryUnescape(keyRaw)
			if err != nil {
				return nil, "", false, agentmode.ErrInvalid
			}
			if key != "cursor" {
				nonCursor = append(nonCursor, pair)
				continue
			}
			if ignoreCursor {
				continue
			}
			if !hasValue || strings.Contains(pair, ";") {
				cursorMalformed = true
				continue
			}
			value, err := url.QueryUnescape(valueRaw)
			if err != nil {
				cursorMalformed = true
				continue
			}
			cursors = append(cursors, strings.TrimSpace(value))
		}
	}
	values, err := url.ParseQuery(strings.Join(nonCursor, "&"))
	if err != nil {
		return nil, "", false, agentmode.ErrInvalid
	}
	if ignoreCursor {
		return values, "", false, nil
	}
	if cursorMalformed || len(cursors) > 1 || (len(cursors) == 1 && cursors[0] == "") {
		return values, "", true, nil
	}
	if len(cursors) == 1 {
		return values, cursors[0], false, nil
	}
	return values, "", false, nil
}

func setAgentModeEventHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeAgentModeReset(w http.ResponseWriter, flusher http.Flusher) {
	payload := struct {
		SchemaVersion int    `json:"schema_version"`
		Reason        string `json:"reason"`
	}{SchemaVersion: agentmode.SchemaVersion, Reason: "resync_required"}
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: reset\ndata: %s\n\n", raw)
	flusher.Flush()
}

func writeAgentModeBatch(w http.ResponseWriter, flusher http.Flusher, batch agentmode.StreamBatch) {
	payload := struct {
		SchemaVersion int                    `json:"schema_version"`
		Hints         []agentmode.StreamHint `json:"hints,omitempty"`
	}{SchemaVersion: agentmode.SchemaVersion, Hints: batch.Hints}
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\nid: %s\ndata: %s\n\n", batch.Kind, batch.Cursor, raw)
	flusher.Flush()
}
