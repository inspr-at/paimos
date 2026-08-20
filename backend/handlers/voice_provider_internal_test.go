package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/agentmode"
)

func TestTruncateVoiceUTF8NeverSplitsCodePoint(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxBytes int
		want     string
	}{
		{"two-byte-at-boundary", "abéz", 3, "ab"},
		{"two-byte-exact", "abéz", 4, "abé"},
		{"three-byte-at-boundary", "ab€z", 4, "ab"},
		{"three-byte-exact", "ab€z", 5, "ab€"},
		{"four-byte-at-boundary", "ab🙂z", 5, "ab"},
		{"four-byte-exact", "ab🙂z", 6, "ab🙂"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := truncateVoiceUTF8(test.value, test.maxBytes)
			if got != test.want || !utf8.ValidString(got) || len(got) > test.maxBytes {
				t.Fatalf("truncate(%q,%d)=%q valid=%t bytes=%d want=%q", test.value, test.maxBytes,
					got, utf8.ValidString(got), len(got), test.want)
			}
		})
	}
}

func TestRenderAgentModeStatusUsesOnlyClosedStructuredFacts(t *testing.T) {
	percent := 42
	landing := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	row := agentmode.DeliveryRow{
		IssueKey: "SAFE-42", Title: "TITLE-CANARY", StatusText: "STATUS-CANARY",
		Stage:     agentmode.StageSummary{Key: "implementation"},
		Activity:  agentmode.Activity{Kind: "working", Text: "ACTIVITY-CANARY"},
		Freshness: agentmode.Freshness{State: "fresh"},
		Attention: agentmode.RowAttention{Reason: "blocked"}, Health: agentmode.HealthHealthy,
		Blockers: []agentmode.SafeBlocker{{Kind: "dependency", Text: "BLOCKER-CANARY"}},
		Trust:    agentmode.SafeTrust{ConfidenceLabel: "medium", Suppression: "blocked", Basis: "BASIS-CANARY"},
		Progress: &agentmode.Progress{Percent: &percent, Trusted: true},
		ETA:      &agentmode.ETA{LandingAt: &landing, Trusted: true},
	}
	narration := renderAgentModeStatus("en", row)
	for _, want := range []string{"SAFE-42", "Blockers: 1", "Trust confidence: medium", "Estimate suppressed: blocked"} {
		if !strings.Contains(narration, want) {
			t.Fatalf("narration=%q missing %q", narration, want)
		}
	}
	for _, forbidden := range []string{"TITLE-CANARY", "STATUS-CANARY", "ACTIVITY-CANARY", "BLOCKER-CANARY", "BASIS-CANARY", "42 percent", "2026-08-21"} {
		if strings.Contains(narration, forbidden) {
			t.Fatalf("narration leaked %q: %q", forbidden, narration)
		}
	}
}

func TestTrustedAgentModeEstimateWithholdsUnknownConfidenceBounds(t *testing.T) {
	optimistic := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	pessimistic := optimistic.Add(6 * time.Hour)
	row := agentmode.DeliveryRow{
		Health:    agentmode.HealthHealthy,
		Freshness: agentmode.Freshness{State: "fresh"},
		Trust:     agentmode.SafeTrust{ConfidenceLabel: "unknown", RangeOnly: true},
		ETA:       &agentmode.ETA{OptimisticAt: &optimistic, PessimisticAt: &pessimistic, Trusted: true},
	}
	if got := strings.Join(trustedAgentModeEstimate("en", row), " "); got != "" {
		t.Fatalf("unknown-confidence estimate was narrated: %q", got)
	}
	row.Trust.ConfidenceLabel = "low"
	if got := strings.Join(trustedAgentModeEstimate("en", row), " "); !strings.Contains(got, "2026-08-21 09:00 UTC") || !strings.Contains(got, "2026-08-21 15:00 UTC") {
		t.Fatalf("low-confidence range=%q", got)
	}
}

func TestSynthesizeWithElevenLabsRejectsNonAudioAndOversizedBodies(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantError   string
	}{
		{"html-success", "text/html; charset=utf-8", "<html>provider error</html>", "non-audio"},
		{"oversized-audio", "audio/mpeg", strings.Repeat("x", (8<<20)+1), "exceeds 8 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = w.Write([]byte(test.body))
			}))
			defer upstream.Close()
			settings := VoiceSettings{APIKey: "safe-test-key", BaseURL: upstream.URL,
				TTSVoiceID: "voice", TTSModel: "model"}
			if _, err := synthesizeWithElevenLabs(context.Background(), settings, "server-owned template", "en"); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error=%v want substring %q", err, test.wantError)
			}
		})
	}
}
