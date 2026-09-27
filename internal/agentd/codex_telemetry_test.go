// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/sessionusage"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type codexTelemetryAPI struct {
	*usageTestAPI
	runID string
}

func (a *codexTelemetryAPI) Report(ctx context.Context, _ string, report Telemetry) error {
	return a.remote.Report(ctx, a.runID, report)
}

// The former status-without-status event passed adapter-only tests but the real
// validator returned 400, which stopped a live child. Use the production auth
// middleware and agentruns module here, with real tenant/RLS storage. Only the
// provider and session usage sink are synthetic; no model process is launched.
func TestCodexModelUsageThroughSupervisorAndTelemetryHTTP(t *testing.T) {
	d := dbtest.Open(t)
	s, base, proc := testSupervisor(t)
	defer s.Close(context.Background())
	uuid := func() string {
		v, err := randomID()
		if err != nil {
			t.Fatal(err)
		}
		return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
	}
	tid, personID, agentID, profile, account := uuid(), uuid(), uuid(), uuid(), uuid()
	person := tenant.Principal{TenantID: tid, ID: personID, Kind: tenant.Person}
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), d.Admin, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	prefix := strings.ReplaceAll(tid, "-", "") + "0123456789abcdef"
	const syntheticSecret = "synthetic-telemetry-boundary-key"
	sum := sha256.Sum256([]byte(syntheticSecret))
	token := "aeon_" + prefix + "_" + syntheticSecret
	seed(func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants(id,slug,name) VALUES($1,$2,'UC1 synthetic')`, []any{tid, "uc1-" + tid}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Synthetic'),($1,$3,'agent','Synthetic')`, []any{tid, personID, agentID}},
			{`INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'uc1','1','codex','openai','model','high','strong')`, []any{tid, profile}},
			{`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'Synthetic',$3,$4,$5)`, []any{tid, agentID, prefix, hex.EncodeToString(sum[:]), []string{"run.telemetry", "run.read"}}},
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,$2,'synthetic','codex','daemon',$3,'Synthetic',clock_timestamp(),true,$4)`, []any{tid, account, agentID, s.generation}},
		} {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	dbtest.BindRole(t, d, tid, personID, "admin")
	dbtest.BindRole(t, d, tid, agentID, "member")
	mux := http.NewServeMux()
	workorders.New(d.App).Mount(mux)
	agentruns.New(d.App).Mount(mux)
	call := func(method, path string, body any, status int, dst any) {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("fixture endpoint status %d: %s", w.Code, w.Body.String())
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	var order workorders.Order
	call("POST", "/api/work-orders", map[string]any{"title": "Synthetic", "criteria": []string{"Capture"}, "assignee_principal_id": agentID}, 201, &order)
	call("PATCH", "/api/work-orders/"+order.NodeID, map[string]any{"expected_revision": order.Revision, "status": "ready"}, 200, &order)
	var run agentruns.Run
	call("POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]string{"agent_principal_id": agentID, "model_profile_id": profile}, 201, &run)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='starting',started_at=clock_timestamp(),account_id=$2,daemon_id='daemon',daemon_generation=$3 WHERE id=$1`, run.ID, account, s.generation)
		return err
	})
	authentication, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Mux: mux, Pool: d.App, Middleware: []func(http.Handler) http.Handler{authentication.Middleware}}).Handler()
	var mu sync.Mutex
	var usage []sessionusage.UsageReport
	var statuses []int
	receipts := make(map[string][]byte)
	attempts, replays := 0, 0
	firstAccepted, releaseFirst := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects/project/harness-sessions/session/usage" {
			base.mu.Lock()
			lease := base.harnessRegistration.Lease
			base.mu.Unlock()
			if r.Header.Get("X-Aeon-Worker-Lease") != lease || r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("session binding lost")
			}
			var report sessionusage.UsageReport
			if json.NewDecoder(r.Body).Decode(&report) != nil {
				t.Error("invalid usage request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			payload, err := json.Marshal(report)
			if err != nil {
				t.Error("invalid normalized usage payload")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			attempts++
			prior, replayed := receipts[report.ReportID]
			if replayed && !bytes.Equal(prior, payload) {
				mu.Unlock()
				t.Error("usage receipt replay changed payload")
				w.WriteHeader(http.StatusConflict)
				return
			}
			if replayed {
				replays++
			} else {
				// Match the public endpoint's receipt identity and payload check.
				receipts[report.ReportID] = payload
				usage = append(usage, report)
			}
			first := attempts == 1
			mu.Unlock()
			if first {
				// Commit before losing the response: finish must retry this receipt.
				close(firstAccepted)
				select {
				case <-r.Context().Done():
				case <-releaseFirst:
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"usage": map[string]any{}, "replayed": replayed})
			return
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		mu.Lock()
		statuses = append(statuses, recorder.Code)
		mu.Unlock()
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	defer close(releaseFirst) // Release the handler before server.Close on any fatal.
	remote := NewRemote(server.URL, token)
	remote.daemonID, remote.generation = "daemon", s.generation
	s.api = &codexTelemetryAPI{usageTestAPI: &usageTestAPI{fakeAPI: base, remote: remote}, runID: run.ID}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := s.runs["run"]
	capture, err := sessionusage.NewManagedCodex("synthetic-thread", "model")
	if err != nil {
		t.Fatal(err)
	}
	wire := &wireProcess{threadID: "synthetic-thread", turnID: "synthetic-turn", observe: func(ev AdapterEvent) { s.observe(entry, ev) }}
	cp := &codexProcess{wireProcess: wire, usage: capture, done: make(chan bool, 1), acknowledged: true}
	frame := json.RawMessage(strings.ReplaceAll(lifecycleUsage, "model-a", "model"))
	cp.notification(frame)
	cp.notification(frame) // Cumulative replay cannot count twice.
	select {
	case <-firstAccepted:
	case <-time.After(3 * time.Second):
		t.Fatal("first usage receipt was not accepted")
	}
	if err := finishUsageTest(t, entry.usage); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.stopped:
		t.Fatal("valid model usage stopped child")
	default:
	}
	mu.Lock()
	if len(statuses) != 3 {
		t.Errorf("telemetry posts=%d want started, zero-delta model evidence and one run delta", len(statuses))
	}
	for _, status := range statuses {
		if status != 200 {
			t.Errorf("telemetry rejected: %d", status)
		}
	}
	if attempts != 2 || replays != 1 {
		t.Errorf("interrupted response: transport attempts=%d replays=%d; want 2 and 1", attempts, replays)
	}
	if len(usage) != 1 || usage[0].ReportID == "" || usage[0].Sequence != 1 || usage[0].Model != "model" ||
		usage[0].InputTokens == nil || *usage[0].InputTokens != 100 || usage[0].OutputTokens == nil || *usage[0].OutputTokens != 20 || !usage[0].Provisional {
		t.Errorf("normalized session usage missing or double-counted: applied receipts=%d", len(usage))
	}
	mu.Unlock()
	seed(func(tx pgx.Tx) error {
		var input, output int64
		var model, evidence, status string
		if err := tx.QueryRow(t.Context(), `SELECT input_tokens,output_tokens,effective_model,model_evidence,status FROM agent_runs WHERE id=$1`, run.ID).Scan(&input, &output, &model, &evidence, &status); err != nil {
			return err
		}
		if input != 100 || output != 20 || model != "model" || evidence != "vendor_reported" || status != "running" {
			t.Error("accepted telemetry has wrong totals/model/state")
		}
		return nil
	})
	// A genuine invalid telemetry event still rejects and stops the owned child.
	s.observe(entry, AdapterEvent{Kind: "status"})
	select {
	case <-proc.stopped:
	case <-time.After(time.Second):
		t.Fatal("genuine telemetry failure swallowed")
	}
	select {
	case <-entry.monitorDone:
	case <-time.After(time.Second):
		t.Fatal("monitor did not settle")
	}
	mu.Lock()
	defer mu.Unlock()
	found400 := false
	for _, status := range statuses {
		found400 = found400 || status == 400
	}
	if !found400 {
		t.Fatal("server validator weakened")
	}
}
