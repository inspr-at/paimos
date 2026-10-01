// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelreport"
)

var errModelInvalid = errors.New("model invalid")

// A generic 400, quota error, transport error or malformed RPC is not evidence
// of an invalid model. Classification discards the vendor text immediately.
func explicitModelInvalid(raw json.RawMessage) bool {
	var body struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	for _, code := range []string{strings.Trim(string(body.Code), `"`), body.Type} {
		if code == "model_not_found" || code == "invalid_model" || code == "unsupported_model" {
			return true
		}
	}
	text := strings.ToLower(body.Message)
	return strings.Contains(text, "model") && (strings.Contains(text, "does not exist") || strings.Contains(text, "model not found") || strings.Contains(text, "unsupported model") || strings.Contains(text, "invalid model") || strings.Contains(text, "model is not supported"))
}

func modelStartError(message string, err error) error {
	if errors.Is(err, errModelInvalid) {
		return errors.Join(errors.New(message), errModelInvalid)
	}
	return errors.New(message)
}

func (r *Remote) ReportHarnessModels(ctx context.Context, s HarnessSession, observations []modelreport.Observation) error {
	return r.harnessWorker(ctx, s, "/model-reports", observations, nil)
}

func (s *Supervisor) reportModels(entry *owned, observations []modelreport.Observation) {
	reporter, ok := s.api.(interface {
		ReportHarnessModels(context.Context, HarnessSession, []modelreport.Observation) error
	})
	if !ok || len(observations) == 0 {
		return
	}
	entry.mu.Lock()
	session := entry.harness
	entry.mu.Unlock()
	if session.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Discovery must never prevent owned run servicing. Retryable current model
	// sightings also flow in heartbeats; invalid evidence carries stable run ids.
	_ = reporter.ReportHarnessModels(ctx, session, observations)
}

func (s *Supervisor) reportModelState(entry *owned, status string, requested ...string) {
	entry.mu.Lock()
	session := entry.harness
	runID := entry.record.RunID
	entry.mu.Unlock()
	// Only an explicit startup rejection can use the requested pin as evidence;
	// Codex intentionally withholds session model metadata until confirmation.
	if status == "invalid" && len(requested) == 2 {
		session.Model, session.ReasoningEffort = requested[0], requested[1]
	}
	if !modelreport.ValidTuple(session.Model, session.ReasoningEffort) {
		return
	}
	s.reportModels(entry, []modelreport.Observation{{ReportID: modelreport.EvidenceID(runID + "/" + status + "/" + session.Model + "/" + session.ReasoningEffort), Harness: session.Harness, Model: session.Model, Effort: session.ReasoningEffort, Status: status}})
}

// Codex's authenticated app-server model/list is a content-free harness probe,
// not a completion request. Unsupported versions simply omit discovery.
func reportCodexModels(ctx context.Context, p *wireProcess, observe func(AdapterEvent), runID string) {
	after := ""
	for page := 0; page < 10; page++ {
		args := map[string]any{"limit": 100}
		if after != "" {
			args["cursor"] = after
		}
		raw, err := p.request(ctx, "jsonrpc", "model/list", args)
		if err != nil {
			return
		}
		var body struct {
			Data []struct {
				Model   string `json:"model"`
				Efforts []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			Next string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &body) != nil {
			return
		}
		reports := []modelreport.Observation{}
		for _, m := range body.Data {
			for _, e := range m.Efforts {
				if !modelreport.ValidTuple(m.Model, e.Effort) {
					continue
				}
				reports = append(reports, modelreport.Observation{ReportID: modelreport.EvidenceID(runID + "/list/" + m.Model + "/" + e.Effort), Harness: "codex", Model: m.Model, Effort: e.Effort, Status: "advertised"})
			}
		}
		for len(reports) > 0 {
			n := min(200, len(reports))
			observe(AdapterEvent{ModelReports: reports[:n]})
			reports = reports[n:]
		}
		if body.Next == "" || body.Next == after {
			return
		}
		after = body.Next
	}
}
