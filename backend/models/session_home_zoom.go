// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// SessionHomeZoomSnapshot is the closed, bounded semantic-zoom projection.
// Zoom remains a canonical decimal string so the wire never loses precision.
type SessionHomeZoomSnapshot struct {
	SchemaVersion   int                   `json:"schema_version"`
	ProjectID       int64                 `json:"project_id"`
	Zoom            string                `json:"zoom"`
	Band            string                `json:"band"`
	SampleLimit     int                   `json:"sample_limit"`
	SampleTruncated bool                  `json:"sample_truncated"`
	Sessions        []SessionHomeSession  `json:"sessions"`
	SelectedSession *SessionHomeSession   `json:"selected_session"`
	Totals          SessionHomeZoomTotals `json:"totals"`
}

type SessionHomeZoomTotals struct {
	Sessions                int64 `json:"sessions"`
	Unread                  int64 `json:"unread"`
	AttentionSessions       int64 `json:"attention_sessions"`
	ExceptionMessages       int64 `json:"exception_messages"`
	ActionRequests          int64 `json:"action_requests"`
	ExceptionTargets        int64 `json:"exception_targets"`
	SampledExceptionTargets int64 `json:"sampled_exception_targets"`
}
