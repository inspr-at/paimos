// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSessionModelIdentityNormalizationAndResolution(t *testing.T) {
	f := fixture(t)
	var grokID, codexID, cursorID string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, p := range []struct {
			slug, harness, model, effort string
			id                           *string
		}{
			{"identity-grok", "grok", "test-grok-4.7", "xhigh", &grokID},
			{"identity-codex", "codex", "test-sol", "high", &codexID},
			{"identity-cursor", "cursor", "test-grok-4.7-xhigh", "default", &cursorID},
		} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1',$3,'openai',$4,$5,'standard') RETURNING id::text`, f.person.TenantID, p.slug, p.harness, p.model, p.effort).Scan(p.id); err != nil {
				return err
			}
		}
		// Two immutable versions of the same tuple are deliberately ambiguous.
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES
		 ($1,'identity-ambiguous','1','grok','xai','test-ambiguous','high','standard'),
		 ($1,'identity-ambiguous','2','grok','xai','test-ambiguous','high','standard')`, f.person.TenantID)
		return err
	})
	// A unique foreign-tenant match must never resolve a local session.
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'foreign-identity','1','grok','xai','foreign-only','high','standard')`, f.foreign.TenantID)
		return err
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	for i, tc := range []struct {
		name, harness, model, effort, base, normalizedEffort string
		profile                                              any
	}{
		{"suffix", "grok", "test-grok-4.7-xhigh", "", "test-grok-4.7", "xhigh", grokID},
		{"separate", "codex", "test-sol", "high", "test-sol", "high", codexID},
		{"matching", "grok", "test-grok-4.7-xhigh", "xhigh", "test-grok-4.7", "xhigh", grokID},
		{"default", "grok", "test-grok-4.7-xhigh", "default", "test-grok-4.7", "xhigh", grokID},
		{"cursor", "cursor", "test-grok-4.7-xhigh", "", "test-grok-4.7", "xhigh", cursorID},
		{"claude-unknown", "claude", "claude-opus-5-5-xhigh", "", "claude-opus-5-5", "xhigh", nil},
		{"unknown", "grok", "unknown-model-xhigh", "", "unknown-model", "xhigh", nil},
		{"effort-absent", "grok", "test-grok-4.7", "", "test-grok-4.7", "", nil},
		{"conflict", "grok", "test-grok-4.7-xhigh", "high", "test-grok-4.7", "high", nil},
		{"ambiguous", "grok", "test-ambiguous-high", "", "test-ambiguous", "high", nil},
		{"foreign", "grok", "foreign-only-high", "", "foreign-only", "high", nil},
		{"suffix-without-model", "grok", "-xhigh", "", "-xhigh", "", nil},
		{"unrecognized-suffix", "grok", "test-grok-4.7-fast", "high", "test-grok-4.7-fast", "high", nil},
		{"low", "grok", "unknown-low", "", "unknown", "low", nil},
		{"medium", "grok", "unknown-medium", "", "unknown", "medium", nil},
		{"high", "grok", "unknown-high", "", "unknown", "high", nil},
		{"max", "grok", "unknown-max", "", "unknown", "max", nil},
		{"ultra", "grok", "unknown-ultra", "", "unknown", "ultra", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": tc.harness, "host": "build-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": fmt.Sprintf("model-identity-ref-%032d", i), "worker_lease": fmt.Sprintf("model-identity-lease-%032d", i), "model": tc.model}
			if tc.effort != "" {
				registration["reasoning_effort"] = tc.effort
			}
			w := f.call(f.person, "POST", base, registration, "")
			expect(t, w, 201)
			s := decode(t, w)
			var effort any
			if tc.normalizedEffort != "" {
				effort = tc.normalizedEffort
			}
			if s["model"] != tc.base || s["model_raw"] != tc.model || s["reasoning_effort"] != effort || s["model_profile_id"] != tc.profile {
				t.Fatalf("identity: %#v", s)
			}
			// Exact replay remains tied to original registration metadata.
			w = f.call(f.person, "POST", base, registration, "")
			expect(t, w, 201)
			if decode(t, w)["id"] != s["id"] {
				t.Fatal("replay created new session")
			}
		})
	}
}

func TestSessionModelIdentityHeartbeatAndBackfill(t *testing.T) {
	f := fixture(t)
	var profile string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'identity-heartbeat','1','grok','xai','test-heartbeat','xhigh','standard') RETURNING id::text`, f.person.TenantID).Scan(&profile)
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "model-identity-heartbeat-lease-000000000001"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "grok", "host": "build-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "model-identity-heartbeat-ref-000001", "worker_lease": lease, "model": "test-heartbeat-xhigh"}, "")
	expect(t, w, 201)
	s := decode(t, w)
	id := s["id"].(string)
	path := base + "/" + id
	f.tx(t, f.person, func(tx pgx.Tx) error {
		// Emulate a pre-expansion stored row, including its registration digest.
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model='test-heartbeat-xhigh',reasoning_effort=NULL,model_raw=NULL,model_profile_id=NULL WHERE id=$1`, id)
		if err != nil {
			return err
		}
		var n int
		if err = tx.QueryRow(t.Context(), `SELECT aeon_backfill_session_model_profiles()`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("first backfill: %d", n)
		}
		var revision int64
		if err = tx.QueryRow(t.Context(), `SELECT row_version FROM harness_sessions WHERE id=$1`, id).Scan(&revision); err != nil {
			return err
		}
		if err = tx.QueryRow(t.Context(), `SELECT aeon_backfill_session_model_profiles()`).Scan(&n); err != nil {
			return err
		}
		var repeated int64
		if err = tx.QueryRow(t.Context(), `SELECT row_version FROM harness_sessions WHERE id=$1`, id).Scan(&repeated); err != nil {
			return err
		}
		if n != 0 || repeated != revision {
			t.Fatalf("repeat rewrote row: %d %d/%d", n, revision, repeated)
		}
		return nil
	})
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	s = decode(t, w)
	if s["model"] != "test-heartbeat" || s["model_profile_id"] != profile || s["model_raw"] != "test-heartbeat-xhigh" {
		t.Fatal(s)
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "model": "unknown-xhigh"}, lease)
	expect(t, w, 200)
	s = decode(t, w)
	if s["model"] != "unknown" || s["model_profile_id"] != nil || s["reasoning_effort"] != "xhigh" || s["model_raw"] != "unknown-xhigh" {
		t.Fatal(s)
	}
	// Effort-only and ordinary heartbeats must not restore a stale model key.
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2, "reasoning_effort": "high"}, lease)
	expect(t, w, 200)
	s = decode(t, w)
	if s["model_profile_id"] != nil || s["model_raw"] != "unknown-xhigh" || s["reasoning_effort"] != "high" {
		t.Fatal(s)
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 3, "model": "plain-model"}, lease)
	expect(t, w, 200)
	s = decode(t, w)
	if s["reasoning_effort"] != "high" || s["model_profile_id"] != nil || s["model_raw"] != "plain-model" {
		t.Fatal("model-only heartbeat lost the stored effort", s)
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 4, "model": "test-heartbeat-xhigh"}, lease)
	expect(t, w, 200)
	s = decode(t, w)
	if s["reasoning_effort"] != "xhigh" || s["model_profile_id"] != profile {
		t.Fatal("new model suffix did not replace the old effort", s)
	}
}

func TestSessionModelIdentityEffortOnlyHeartbeatPreservesLegacyRaw(t *testing.T) {
	f := fixture(t)
	var profile string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'identity-legacy-heartbeat','1','grok','xai','test-legacy-known','xhigh','standard') RETURNING id::text`, f.person.TenantID).Scan(&profile)
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	for i, tc := range []struct {
		name, model, effort, storedRaw, wantRaw, base string
		profile                                       any
	}{
		{"unknown matching effort", "test-legacy-unknown-xhigh", "xhigh", "", "test-legacy-unknown-xhigh", "test-legacy-unknown", nil},
		{"unknown changed effort", "test-legacy-unknown-xhigh", "high", "", "test-legacy-unknown-xhigh", "test-legacy-unknown", nil},
		{"known model", "test-legacy-known-xhigh", "xhigh", "", "test-legacy-known-xhigh", "test-legacy-known", profile},
		{"existing audit string", "test-legacy-unknown-xhigh", "high", "test-legacy-unknown-ultra", "test-legacy-unknown-ultra", "test-legacy-unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lease := fmt.Sprintf("model-identity-legacy-lease-%032d", i)
			w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "grok", "host": "build-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": fmt.Sprintf("model-identity-legacy-ref-%032d", i), "worker_lease": lease, "model": tc.model}, "")
			expect(t, w, 201)
			id := decode(t, w)["id"].(string)
			path := base + "/" + id
			f.tx(t, f.person, func(tx pgx.Tx) error {
				// Emulate a legacy row left unresolved by the migration.
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model=$2,reasoning_effort=NULL,model_raw=nullif($3,''),model_profile_id=NULL WHERE id=$1`, id, tc.model, tc.storedRaw)
				return err
			})
			w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "reasoning_effort": tc.effort}, lease)
			expect(t, w, 200)
			s := decode(t, w)
			if s["model"] != tc.base || s["model_raw"] != tc.wantRaw || s["reasoning_effort"] != tc.effort || s["model_profile_id"] != tc.profile {
				t.Fatalf("effort-only heartbeat lost legacy identity: %#v", s)
			}
			// A subsequent ordinary heartbeat and read must retain the audit data.
			w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease)
			expect(t, w, 200)
			w = f.call(f.person, "GET", path, nil, "")
			expect(t, w, 200)
			s = decode(t, w)
			if s["model"] != tc.base || s["model_raw"] != tc.wantRaw || s["reasoning_effort"] != tc.effort || s["model_profile_id"] != tc.profile {
				t.Fatalf("stored legacy identity changed: %#v", s)
			}
		})
	}
}

func TestHeartbeatModelSwitchRequiresGenerationLease(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "model-switch-own-lease-000000000000001"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "test-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "model-switch-ref-000000000000000001", "worker_lease": lease, "model": "claude-sonnet", "reasoning_effort": "low"}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	foreignLease := "model-switch-foreign-lease-00000000001"
	other := map[string]any{}
	for k, v := range registration {
		other[k] = v
	}
	other["harness_session_ref"] = "model-switch-other-ref-0000000000001"
	other["worker_lease"] = foreignLease
	expect(t, f.call(f.person, "POST", base, other, ""), 201)
	body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "model": "claude-opus", "reasoning_effort": "high"}
	w = f.call(f.agent, "POST", path+"/heartbeat", body, foreignLease)
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "harness worker proof rejected") {
		t.Fatal("foreign lease rejected for wrong reason", w.Body.String())
	}
	detail := decode(t, f.call(f.person, "GET", path, nil, ""))
	if detail["model"] != "claude-sonnet" || len(detail["metadata_history"].([]any)) != 0 || detail["activity_sequence"] != float64(0) {
		t.Fatal("foreign lease mutated session", detail)
	}
	for _, tc := range []struct{ field, value string }{{"model", strings.Repeat("x", 129)}, {"reasoning_effort", strings.Repeat("x", 41)}} {
		bad := map[string]any{"phase": "working", "activity_sequence": 1, tc.field: tc.value}
		w = f.call(f.agent, "POST", path+"/heartbeat", bad, lease)
		expect(t, w, 400)
		if !strings.Contains(w.Body.String(), "too long") {
			t.Fatal("bounds rejected for wrong reason", w.Body.String())
		}
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", body, lease)
	expect(t, w, 200)
	got := decode(t, w)
	if got["id"] != id || got["model"] != "claude-opus" || got["reasoning_effort"] != "high" {
		t.Fatal("switch failed", got)
	}
	// Replay is tied to the original registration, even after current metadata changes.
	w = f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id {
		t.Fatal("registration replay replaced generation")
	}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease), 200)
	detail = decode(t, f.call(f.person, "GET", path, nil, ""))
	history := detail["metadata_history"].([]any)
	if len(history) != 2 || detail["model"] != "claude-opus" || detail["reasoning_effort"] != "high" {
		t.Fatal("switch not audited or preserved", detail)
	}
	page := decode(t, f.call(f.person, "GET", "/api/harness-sessions", nil, ""))["items"].([]any)
	found := false
	for _, item := range page {
		s := item.(map[string]any)
		if s["id"] == id {
			found = true
			if s["model"] != "claude-opus" || s["reasoning_effort"] != "high" {
				t.Fatal("UI feed has stale identity", s)
			}
		}
	}
	if !found {
		t.Fatal("updated session missing from UI feed")
	}
}
