// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package workerfleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerFleetDocsPreserveV1AndExplainTrustGate(t *testing.T) {
	for _, name := range []string{"AGENT_INTEGRATION.md", "api-minimal.md"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, required := range []string{"worker-fleet/v1", "worker-fleet/v2", "runtime_provenance_trust", "managed_reporter", "untrusted"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omits worker-fleet contract %q", name, required)
			}
		}
		if !strings.Contains(text, "heartbeat") || !strings.Contains(text, "suppress") {
			t.Fatalf("%s does not explain the reporter-evidence suppression gate", name)
		}
	}
}
