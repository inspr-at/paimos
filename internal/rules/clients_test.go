// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Every snapshot is valid, including metadata that expands sixfold in JSON.
func exactClientSnapshots(size int) []Snapshot {
	return exactClientSnapshotsWithCount(size, size/400+1)
}

func exactClientSnapshotsWithCount(size, n int) []Snapshot {
	snaps := []Snapshot{floorSnapshot()}
	remaining := size - len(SessionHeader) - len(ruleLine(snaps[0].Rules[0]))
	rules := make([]Rule, n)
	for i := range rules {
		rules[i] = testRule(fmt.Sprintf("large-%03d", i), "")
		rules[i].Why = strings.Repeat("<", 1024)
		rules[i].Source.Reference = strings.Repeat("&", 512)
		remaining -= len(ruleLine(rules[i]))
	}
	for i := range rules {
		n := remaining / (len(rules) - i)
		rules[i].Text = strings.Repeat("<", n)
		remaining -= n
	}
	for i := 0; i < len(rules); i += MaxRules {
		snaps = append(snaps, testSnapshot(fmt.Sprintf("p%d", i), Scope{Layer: "project", ProjectID: testProject}, rules[i:min(i+MaxRules, len(rules))]...))
	}
	return snaps
}

func TestCacheEnvelopeFitsEscapedStoreMaximum(t *testing.T) {
	snaps := exactClientSnapshotsWithCount(MaxBudgetBytes, maxBudgetRules-1)
	for i := 1; i < len(snaps); i++ {
		for j := range snaps[i].Rules {
			r := &snaps[i].Rules[j]
			r.Why = strings.Repeat("<", 700)
			r.Source.Revision = strings.Repeat(">", 128)
			r.Source.Identity = strings.Repeat("&", 96)
		}
		snaps[i].SHA256 = SnapshotDigest(snaps[i])
	}
	if err := storeBudget(context.Background(), snaps); err != nil {
		t.Fatal("fixture exceeds store limits", err)
	}
	m, err := MergeWithin(testContext(), snaps, time.Now(), Budget{MaxBytes: MaxBudgetBytes})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCache("https://aeon.test", m, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 16<<20 {
		t.Fatal("fixture must include worst-case source metadata expansion")
	}
	if _, err := DecodeCache(raw, "https://aeon.test", m.Context, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacy, err := MergeForClient(testContext(), snaps, time.Now(), Budget{MaxBytes: MaxBudgetBytes}, LegacyMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	oldCache, err := EncodeCache("https://aeon.test", legacy, time.Now())
	if err != nil || len(oldCache) > 512*1024 || legacy.ByteSize > LegacyMaxBytes {
		t.Fatal("legacy envelope overflow", len(oldCache), err)
	}
	if !strings.Contains(legacy.Body, "Compatibility cut") || !containsFloor(legacy.Body, legacy.Floor) {
		t.Fatal("legacy cut lost note or floor")
	}
}

func TestClientCutAndCacheBoundaries(t *testing.T) {
	now := time.Now()
	for _, size := range []int{LegacyMaxBytes, LegacyMaxBytes + 1, MaxBudgetBytes, MaxBudgetBytes + 1} {
		for _, limit := range []int{MinBudgetBytes, LegacyMaxBytes, 32000, MaxBudgetBytes} {
			t.Run(fmt.Sprintf("%d/client-%d", size, limit), func(t *testing.T) {
				m, err := MergeForClient(testContext(), exactClientSnapshots(size), now, Budget{MaxBytes: MaxBudgetBytes}, limit)
				if size > MaxBudgetBytes {
					if !isCode(err, "rules_budget_exceeded") {
						t.Fatalf("overflow accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if m.ByteSize > limit || m.ByteSize != len(m.Body) || !containsFloor(m.Body, m.Floor) {
					t.Fatal("unbounded or missing floor")
				}
				if strings.Contains(m.Body, "Compatibility cut") != (size > limit) {
					t.Fatal("compatibility note missing or spurious")
				}
				if size <= limit && m.ByteSize != size {
					t.Fatal("unnecessary cut")
				}
				raw, err := EncodeCache("https://aeon.test", m, now)
				if err != nil {
					t.Fatal(err)
				}
				if size == MaxBudgetBytes && limit == MaxBudgetBytes && len(raw) <= 512*1024 {
					t.Fatal("fixture must exceed old cache envelope")
				}
				got, err := DecodeCache(raw, "https://aeon.test", m.Context, now)
				if err != nil || got.Body != m.Body {
					t.Fatalf("cache round trip: %v", err)
				}
				stale, err := MarkStale(raw, "https://aeon.test", m.Context, now)
				if err != nil {
					t.Fatal(err)
				}
				if got, err = Offline(stale, "https://aeon.test", m.Context, m.Floor, now); err != nil || got.Body != m.Body {
					t.Fatal("offline changed received bytes", err)
				}
			})
		}
	}
}

func TestClientCutNeverDropsLockedRules(t *testing.T) {
	snaps := exactClientSnapshots(18000)
	for i := range snaps[1].Rules {
		snaps[1].Rules[i].Strength = "locked"
	}
	snaps[1].SHA256 = SnapshotDigest(snaps[1])
	if _, err := MergeForClient(testContext(), snaps, time.Now(), Budget{MaxBytes: MaxBudgetBytes}, LegacyMaxBytes); !isCode(err, "client_floor_too_large") {
		t.Fatalf("locked rules cut: %v", err)
	}
	// A locked lower-precedence rule is retained even when earlier normal rules fill the cut.
	snaps = exactClientSnapshots(18000)
	last := len(snaps[1].Rules) - 1
	snaps[1].Rules[last].Strength = "locked"
	snaps[1].SHA256 = SnapshotDigest(snaps[1])
	m, err := MergeForClient(testContext(), snaps, time.Now(), Budget{MaxBytes: MaxBudgetBytes}, LegacyMaxBytes)
	if err != nil || !strings.Contains(m.Body, ruleLine(snaps[1].Rules[last])) {
		t.Fatal("lost locked rule", err)
	}
}

func TestClientCutPriorityIsDeterministicAcrossEveryLayer(t *testing.T) {
	c := testContext()
	c.TaskID = "10000000-0000-4000-8000-000000000005"
	scopes := []Scope{
		{Layer: "company"},
		{Layer: "project", ProjectID: c.ProjectID},
		{Layer: "person", OwnerID: c.PersonID},
		{Layer: "agent", Role: c.Role},
		{Layer: "agent", OwnerID: c.PersonID, AgentID: c.AgentID},
		{Layer: "agent", ProjectID: c.ProjectID, OwnerID: c.PersonID, AgentID: c.AgentID, TaskID: c.TaskID},
	}
	snaps := []Snapshot{floorSnapshot()}
	// Reverse identity order relative to layer priority. Four rules from each
	// layer fill a 2,000-byte client, making the choice observable.
	for i, scope := range scopes {
		rs := []Rule{}
		for j := 3; j >= 0; j-- {
			rs = append(rs, testRule(fmt.Sprintf("layer-%d-%d", 5-i, j), strings.Repeat("界", 120)))
		}
		snaps = append(snaps, testSnapshot(fmt.Sprintf("layer-%d", i), scope, rs...))
	}
	now := time.Now()
	first, err := MergeForClient(c, snaps, now, Budget{MaxBytes: MaxBudgetBytes}, MinBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if first.ByteSize > MinBudgetBytes || len(first.Rules) != 5 {
		t.Fatalf("cut size/rules: %d/%d", first.ByteSize, len(first.Rules))
	}
	for _, r := range first.Rules {
		if r.Strength != "locked" && !strings.HasPrefix(r.Identity, "layer-5-") {
			t.Fatalf("lower layer displaced company rule: %s", r.Identity)
		}
	}
	for i := 0; i < len(snaps)/2; i++ {
		snaps[i], snaps[len(snaps)-1-i] = snaps[len(snaps)-1-i], snaps[i]
	}
	second, err := MergeForClient(c, snaps, now, Budget{MaxBytes: MaxBudgetBytes}, MinBudgetBytes)
	if err != nil || second.Body != first.Body || second.SHA256 != first.SHA256 {
		t.Fatal("snapshot order changed delivery", err)
	}
	// Larger clients add the remaining priorities in the documented order.
	for i := 1; i < len(scopes); i++ {
		limit := first.ByteSize + i*4*len(ruleLine(snaps[0].Rules[0]))
		got, err := MergeForClient(c, snaps, now, Budget{MaxBytes: MaxBudgetBytes}, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got.Rules {
			if r.Strength != "locked" && int(r.Identity[6]-'0') < 5-i {
				t.Fatalf("priority %d displaced an earlier layer: %s", i, r.Identity)
			}
		}
	}
}

func TestClientMaximumHeaders(t *testing.T) {
	for _, value := range []string{"", "2000", "12000", "12001", "64000", "1999", "512000", "512001", "bogus", "64000,12000"} {
		r := httptest.NewRequest("GET", "/", nil)
		if value != "" {
			r.Header.Set(ClientMaximumHeader, value)
		}
		n, err := RequestMaximum(r)
		valid := value == "" || value == "2000" || value == "12000" || value == "12001" || value == "64000" || value == "512000"
		if (err == nil) != valid || (value == "" && n != LegacyMaxBytes) {
			t.Fatalf("header %q: %d %v", value, n, err)
		}
	}
}

func TestFullSizeCompanyFloorCanBePinned(t *testing.T) {
	snaps := exactClientSnapshots(MaxBudgetBytes)
	for i := range snaps {
		snaps[i].Scope = Scope{Layer: "company"}
		for j := range snaps[i].Rules {
			snaps[i].Rules[j].Strength = "locked"
		}
		snaps[i].SHA256 = SnapshotDigest(snaps[i])
	}
	m, err := MergeForClient(testContext(), snaps, time.Now(), Budget{MaxBytes: MaxBudgetBytes}, MaxBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFloor([]byte(m.Floor), digest([]byte(m.Floor))); err != nil {
		t.Fatal("valid 500 KB online file cannot be pinned", err)
	}
}

func TestSessionFileLimitMatchesHarnessDefaults(t *testing.T) {
	if SessionFileLimit("codex") != CodexProjectDocMaxBytes || CodexProjectDocMaxBytes != 32*1024 {
		t.Fatalf("codex limit %d", SessionFileLimit("codex"))
	}
	if CodexProjectDocMaxBytes < LegacyMaxBytes || CodexProjectDocMaxBytes > MaxBytes {
		t.Fatal("codex limit is outside the reported range")
	}
	for _, harness := range []string{"claude", "claude-code", "cursor", "grok", "pi", ""} {
		if SessionFileLimit(harness) != MaxBytes {
			t.Fatalf("%s limit %d", harness, SessionFileLimit(harness))
		}
	}
}

func TestClientGateFloorAndBlockerCap(t *testing.T) {
	w := newBatchWorld(t, "rules-client-floor")
	admin := w.principal(tenant.Person, "owner", "admin")
	member := w.principal(tenant.Person, "member", "member")
	agent := w.principal(tenant.Agent, "worker", "admin")
	insert := func(host string, maximum int) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,max_session_file_bytes,rules_client_version) VALUES($1,$2,$3,'codex',$4,'unmanaged','worker',convert_to($4,'UTF8'),convert_to($4,'UTF8'),$5,'fixture-version')`, w.tid, w.project, agent.ID, host, maximum)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(p tenant.Principal) BudgetView {
		t.Helper()
		var b BudgetView
		if err := json.Unmarshal(w.call(p, "GET", "/api/rules/budget", nil, 200), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	insert("low-client", 8000)
	if b := read(admin); b.CeilingBytes != MaxBudgetBytes || len(b.BlockingClients) != 1 || b.BlockingClients[0].Maximum != 8000 || b.BlockingClientsMore != 0 {
		t.Fatalf("low report pulled the ceiling: %+v", b)
	}
	if b := read(admin); b.BlockingClients[0].DeliveredMaxBytes != 8000 || !b.BlockingClients[0].Truncated {
		t.Fatalf("low client delivery: %+v", b)
	}
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: LegacyMaxBytes}, 200)
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBudgetBytes}, 200)
	var rejected Error
	if err := json.Unmarshal(w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBudgetBytes + 1}, 400), &rejected); err != nil || rejected.Code != "invalid_budget" {
		t.Fatalf("stable ceiling error: %+v %v", rejected, err)
	}
	for i := 0; i <= maxBlockingClients; i++ {
		insert(fmt.Sprintf("b-%03d", i), LegacyMaxBytes)
	}
	b := read(admin)
	if b.CeilingBytes != MaxBudgetBytes || len(b.BlockingClients) != maxBlockingClients || b.BlockingClientsMore != 2 || b.BlockingClients[0].Host != "b-000" || b.BlockingClients[maxBlockingClients-1].Host != "b-049" {
		t.Fatalf("blocker cap: ceiling %d listed %d more %d first %q", b.CeilingBytes, len(b.BlockingClients), b.BlockingClientsMore, b.BlockingClients[0].Host)
	}
	for _, client := range b.BlockingClients {
		if client.Host == "b-050" {
			t.Fatal("listed the omitted client")
		}
	}
	if hidden := read(member); len(hidden.BlockingClients) != 0 || hidden.BlockingClientsMore != 0 {
		t.Fatal("capped inventory leaked")
	}
}

func TestClientReportsDoNotBlockEachOther(t *testing.T) {
	w := newBatchWorld(t, "rules-client-lock")
	agent := w.principal(tenant.Agent, "worker", "admin")
	ids := [2]string{}
	for i := range ids {
		host := fmt.Sprintf("lock-%d", i)
		err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,max_session_file_bytes) VALUES($1,$2,$3,'claude',$4,'unmanaged','worker',convert_to($4,'UTF8'),convert_to($4,'UTF8'),12000) RETURNING id::text`, w.tid, w.project, agent.ID, host).Scan(&ids[i])
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	report := func(n int) ClientReport {
		return ClientReport{MaxSessionFileBytes: &n}
	}
	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(stop)
	errCh := make(chan error, 1)
	go func() {
		errCh <- db.InTenant(dbtest.Seed(context.Background()), w.d.App, w.tid, func(tx pgx.Tx) error {
			n := 32000
			if err := RecordClientReport(context.Background(), tx, ids[0], report(n)); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("first report did not acquire the shared lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := db.InTenant(dbtest.Seed(ctx), w.d.App, w.tid, func(tx pgx.Tx) error {
		n := 48000
		return RecordClientReport(ctx, tx, ids[1], report(n))
	})
	if err != nil {
		stop()
		t.Fatal("shared report blocked on the other heartbeat", err)
	}
	err = db.InTenant(dbtest.Seed(context.Background()), w.d.App, w.tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `SET LOCAL lock_timeout = '250ms'`); err != nil {
			return err
		}
		return lockClientGate(context.Background(), tx)
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		stop()
		t.Fatalf("budget admission did not take the exclusive lock: %v", err)
	}
	stop()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	var got [2]*int
	err = w.d.Admin.QueryRow(t.Context(), `SELECT (SELECT max_session_file_bytes FROM harness_sessions WHERE id=$1), (SELECT max_session_file_bytes FROM harness_sessions WHERE id=$2)`, ids[0], ids[1]).Scan(&got[0], &got[1])
	if err != nil || got[0] == nil || *got[0] != 32000 || got[1] == nil || *got[1] != 48000 {
		t.Fatalf("reports: %+v %v", got, err)
	}
}

func TestBudgetSevenDayClientGateAndDelivery(t *testing.T) {
	w := newBatchWorld(t, "rules-client-gate")
	admin := w.principal(tenant.Person, "owner", "admin")
	member := w.principal(tenant.Person, "member", "member")
	agent := w.principal(tenant.Agent, "worker", "admin")
	agent.KeyCreatorID = admin.ID
	floor := w.set(admin, w.layer(admin, Scope{Layer: "company"}), "Floor", lockedRule("safe", "Keep safety."))
	w.publish(admin, floor, "260929120000.0.0")
	read := func(p tenant.Principal) BudgetView {
		t.Helper()
		var b BudgetView
		if err := json.Unmarshal(w.call(p, "GET", "/api/rules/budget", nil, 200), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if read(admin).CeilingBytes != MaxBudgetBytes {
		t.Fatal("empty inventory reduced the product ceiling")
	}
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBudgetBytes}, 200)
	addClient := func(host string, maximum any, age string) string {
		t.Helper()
		var id string
		err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,max_session_file_bytes,rules_client_version,created_at) VALUES($1,$2,$3,'codex',$4,'unmanaged','worker',convert_to($4,'UTF8'),convert_to($4,'UTF8'),$5,'fixture-version',clock_timestamp()-$6::interval) RETURNING id::text`, w.tid, w.project, agent.ID, host, maximum, age).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	addClient("updated", MaxBytes, "0 days")
	old := addClient("legacy", nil, "6 days")
	addClient("expired", nil, "8 days")
	var otherTenant string
	if err := w.d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('rules-client-other','Other tenant') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, otherTenant, func(tx pgx.Tx) error {
		ceiling, blockers, err := clientCeiling(t.Context(), tx)
		if err == nil && (ceiling != MaxBudgetBytes || len(blockers) != 0) {
			return fmt.Errorf("client inventory crossed tenants")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if b := read(admin); b.CeilingBytes != MaxBudgetBytes || len(b.BlockingClients) != 2 || b.BlockingClients[0].Host != "legacy" || b.BlockingClients[0].Version != "fixture-version" {
		t.Fatalf("gate: %+v", b)
	}
	if len(read(member).BlockingClients) != 0 {
		t.Fatal("tenant inventory leaked to rules reader")
	}
	// Stopping/archiving keeps the client in the observation window, without blocking saves.
	if _, err := w.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),archived_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest=ref_digest,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBudgetBytes}, 200)
	if _, err := w.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-interval '8 days' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	if read(admin).CeilingBytes != MaxBudgetBytes {
		t.Fatal("expired client reduced the product ceiling")
	}
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBudgetBytes}, 200)
	large := w.set(admin, w.layer(admin, Scope{Layer: "project", ProjectID: w.project}), "Large", bulky("project", 60)...)
	w.publish(admin, large, "260929120001.0.0")
	path := fmt.Sprintf("/api/rules/merged?project_id=%s&person_id=%s&role=builder&harness=codex", w.project, admin.ID)
	var legacy Merged
	if err := json.Unmarshal(w.call(admin, "GET", path, nil, 200), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ByteSize > LegacyMaxBytes || !strings.Contains(legacy.Body, "Compatibility cut") {
		t.Fatal("legacy delivery unbounded")
	}
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), admin))
	req.Header.Set(ClientMaximumHeader, "64000")
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	var full Merged
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &full) != nil || full.ByteSize <= LegacyMaxBytes || strings.Contains(full.Body, "Compatibility cut") {
		t.Fatalf("upgraded delivery: %d %s", rec.Code, rec.Body)
	}
	// Managed delivery uses the same workspace budget and stores the actual manifest.
	err := db.InTenant(dbtest.Seed(t.Context()), w.d.App, w.tid, func(tx pgx.Tx) error {
		m, err := ForManagedSession(t.Context(), tx, agent, w.project, "", "codex", MaxBytes)
		if err != nil {
			return err
		}
		if m.Body != full.Body {
			t.Fatal("managed delivery differs")
		}
		_, proven, err := ServedSetVersions(t.Context(), tx, m.Context, m.Version, m.SHA256, m.ByteSize)
		if !proven {
			t.Fatal("managed manifest missing")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
