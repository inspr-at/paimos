// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func TestOrchestratorAttentionTargetAdministrationRequiresSuperAdmin(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	ts := newTestServer(t)
	promoteToSuperAdmin(t, "admin")
	projectID, _ := seedOrchestratorAgent(t, ts, "OAT", "amy")
	response := ts.post(t, agentsURL(projectID), ts.adminCookie, map[string]any{"name": "worker"})
	assertStatus(t, response, http.StatusCreated)
	response.Body.Close()
	setOrchestrator(t, ts, 0, projectID, "amy", "Amy")
	targetPath := fmt.Sprintf("/api/projects/%d/message-targets", projectID)
	orchestratorTarget := map[string]any{
		"address": "codex:amy", "adapter": "codex", "target_kind": "codex_thread",
		"target_ref": "01a059fb-4bf4-4881-a38a-7a2e8e60af50", "maximum_level": "simple", "role": "primary",
	}
	created := ts.post(t, targetPath, ts.adminCookie, orchestratorTarget)
	assertStatus(t, created, http.StatusCreated)
	var original agentmessage.Target
	decode(t, created, &original)

	if _, err := db.DB.Exec(`UPDATE users SET role='admin',role_key='admin',is_super_admin=0 WHERE username='admin'`); err != nil {
		t.Fatal(err)
	}
	for label, response := range map[string]*http.Response{
		"inspect": ts.get(t, targetPath+"?address=codex:amy", ts.adminCookie),
		"replace": ts.post(t, targetPath, ts.adminCookie, map[string]any{
			"address": "codex:amy", "adapter": "codex", "target_kind": "codex_thread",
			"target_ref": "01a059fb-4bf4-4881-a38a-7a2e8e60af51", "maximum_level": "simple", "role": "primary",
		}),
		"requeue": ts.post(t, targetPath+"/requeue", ts.adminCookie, map[string]any{"address": "codex:amy"}),
		"managed harness replacement": ts.post(t, fmt.Sprintf("/api/projects/%d/harness-sessions", projectID), ts.adminCookie, map[string]any{
			"agent_name": "amy", "harness": "codex", "host": "test-host",
			"harness_session_ref": "01a059fb-4bf4-4881-a38a-7a2e8e60af53",
			"worker_lease":        "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE",
			"management_mode":     "managed", "role": "worker", "steer_mode": "owned",
			"advertised_capabilities": map[string]any{"inbox": true, "status": true, "steer": true},
		}),
	} {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "agent_attention_target_forbidden") {
			t.Fatalf("%s status=%d body=%s", label, response.StatusCode, body)
		}
	}
	listed := ts.get(t, targetPath, ts.adminCookie)
	assertStatus(t, listed, http.StatusOK)
	allBody, _ := io.ReadAll(listed.Body)
	listed.Body.Close()
	if strings.Contains(string(allBody), original.ID) || strings.Contains(string(allBody), "codex:amy") {
		t.Fatalf("ordinary admin list leaked protected target: %s", allBody)
	}
	targets, err := agentmessage.NewService(db.DB).ListTargets(t.Context(), projectID, "codex:amy")
	if err != nil || len(targets) != 1 || targets[0].ID != original.ID || !targets[0].Enabled || targets[0].Version != 1 {
		t.Fatalf("protected target mutated: targets=%#v err=%v", targets, err)
	}

	ordinary := ts.post(t, targetPath, ts.adminCookie, map[string]any{
		"address": "codex:worker", "adapter": "codex", "target_kind": "codex_thread",
		"target_ref": "01a059fb-4bf4-4881-a38a-7a2e8e60af52", "maximum_level": "simple", "role": "primary",
	})
	assertStatus(t, ordinary, http.StatusCreated)
	ordinary.Body.Close()
	ordinaryList := ts.get(t, targetPath+"?address=codex:worker", ts.adminCookie)
	assertStatus(t, ordinaryList, http.StatusOK)
	ordinaryList.Body.Close()
	ordinaryHarness := ts.post(t, fmt.Sprintf("/api/projects/%d/harness-sessions", projectID), ts.adminCookie, map[string]any{
		"agent_name": "worker", "harness": "codex", "host": "test-host",
		"harness_session_ref": "01a059fb-4bf4-4881-a38a-7a2e8e60af54",
		"worker_lease":        "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI",
		"management_mode":     "managed", "role": "worker", "steer_mode": "owned",
		"advertised_capabilities": map[string]any{"inbox": true, "status": true, "steer": true},
	})
	assertStatus(t, ordinaryHarness, http.StatusCreated)
	ordinaryHarness.Body.Close()
	ordinaryRequeue := ts.post(t, targetPath+"/requeue", ts.adminCookie, map[string]any{"address": "codex:worker"})
	assertStatus(t, ordinaryRequeue, http.StatusOK)
	ordinaryRequeue.Body.Close()
}
