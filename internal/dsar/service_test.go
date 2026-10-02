// SPDX-License-Identifier: AGPL-3.0-only
package dsar

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestManualKitIsolationAliasesHoldsAndCommands(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	tid, err := tenantbootstrap.Create(ctx, d.App, "dsar-kit", "DSAR fixture")
	if err != nil {
		t.Fatal(err)
	}
	foreignTenant, err := tenantbootstrap.Create(ctx, d.App, "dsar-other", "Other fixture")
	if err != nil {
		t.Fatal(err)
	}
	var owner, subject, alias, other, admin, agent, foreign, identity, aliasIdentity string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email,display_name) VALUES('dsar-fixture','00000000-0000-0000-0000-000000000123','subject@example.test','Subject Identity') RETURNING id::text`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email,display_name) VALUES('dsar-alias-fixture','alias','alias@example.test','Alias Identity') RETURNING id::text`).Scan(&aliasIdentity); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		for _, p := range []struct {
			id         *string
			name, kind string
		}{{&owner, "Owner", "person"}, {&subject, "Subject Name", "person"}, {&other, "OTHER_PERSON_PRIVATE", "person"}, {&admin, "Admin", "person"}, {&agent, "Agent", "agent"}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, p.kind, p.name).Scan(p.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE principals SET email='subject@example.test',identity_id=$2::uuid WHERE id=$1::uuid`, subject, identity); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email,linked_to,identity_id) VALUES($1,'person','Alias Name','alias@example.test',$2,$3) RETURNING id::text`, tid, subject, aliasIdentity).Scan(&alias); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,first_name,last_name) VALUES($1,$2,'Subject','Family'),($1,$3,'Alias','Family'),($1,$4,'OTHER_PROFILE_PRIVATE','Hidden')`, tid, subject, alias, other); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'list:fixture','{"layout":"SECRET_IN_ARBITRARY_JSON"}')`, tid, alias); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES(repeat('c',64),$1,$2,$3,now()+interval '1 day')`, identity, tid, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Fixture key','fixture',repeat('d',64),'{}',$2)`, tid, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inbox_messages(tenant_id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key)
		VALUES($1,$2,$3,1,'THIRD_PARTY_MESSAGE_SECRET','dsar-fixture')`, tid, other, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO time_periods(tenant_id,principal_id,starts_at,ends_at) VALUES($1,$2,now(),now()+interval '1 day')`, tid, alias); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, admin, "admin")
	// Fixture writers have system visibility, but this private table also
	// requires the owning person. Seed through the test-only admin connection.
	if _, err := d.Admin.Exec(ctx, `INSERT INTO person_host_labels(tenant_id,person_id,host,label) VALUES($1,$2,'host-fixture','My workstation')`, tid, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO aithema_deprovisioned_subjects(tenant_id,issuer,subject)
		VALUES($1,'dsar-fixture','00000000-0000-0000-0000-000000000123'),($1,'different-issuer','00000000-0000-0000-0000-000000000123')`, tid); err != nil {
		t.Fatal(err)
	}
	// Share the global identity across tenants: the other membership must never
	// appear in either packet or be mistaken for a same-tenant alias.
	if err := db.InTenant(ctx, d.App, foreignTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email,identity_id) VALUES($1,'person','FOREIGN_PRIVATE_NAME','subject@example.test',$2) RETURNING id::text`, foreignTenant, identity).Scan(&foreign)
	}); err != nil {
		t.Fatal(err)
	}
	var visibleProject, hiddenProject, visibleNode, hiddenNode string
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		for _, p := range []struct {
			id  *string
			key string
		}{{&visibleProject, "VP-1"}, {&hiddenProject, "HP-1"}} {
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,'fixture project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid, p.key).Scan(p.id); err != nil {
				return err
			}
		}
		for _, p := range []struct {
			id          *string
			key, parent string
		}{{&visibleNode, "VN-1", visibleProject}, {&hiddenNode, "HN-1", hiddenProject}} {
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,body,parent_id,fields) SELECT $1,id,$2,'private title',$3,$4,'{}' FROM node_kinds WHERE tenant_id=$1 AND slug='ticket' RETURNING id::text`, tid, p.key, "mentions subject@example.test plus PRIVATE_FREE_TEXT", p.parent).Scan(p.id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, tid, subject, visibleProject)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	countState := func() string {
		var state string
		if err := d.Admin.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM principals),(SELECT count(*) FROM identities),(SELECT count(*) FROM schema_migrations),(SELECT count(*) FROM sessions),(SELECT count(*) FROM agent_keys),(SELECT sum(revision) FROM personal_profiles))::text`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	countEvents := func() int {
		var count int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := countState()
	beforeEvents := countEvents()
	opts := Options{Tenant: "dsar-kit", ActorID: owner, Person: "SUBJECT@example.test"}
	report, err := Collect(t.Context(), d.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.Subject.ID != subject || len(report.Subject.Aliases) != 2 || report.DryRun {
		t.Fatal("incorrect subject mapping")
	}
	if len(section(report, "principals").Records) != 2 || len(section(report, "identities").Records) != 2 || len(section(report, "personal_profiles").Records) != 2 || len(section(report, "person_host_labels").Records) != 1 {
		t.Fatal("aliases or private self data missing")
	}
	if len(section(report, "inbox_messages").Records) != 1 || len(section(report, "agent_keys").Records) != 1 || len(section(report, "sessions").Records) != 1 {
		t.Fatal("personal adapter missing")
	}
	if len(section(report, "aithema_deprovisioned_subjects").Records) != 1 {
		t.Fatal("OIDC subject/issuer mapping was not exact")
	}
	oidc := section(report, "aithema_deprovisioned_subjects")
	var locator map[string]string
	if err := json.Unmarshal(oidc.Records[0].Locator, &locator); err != nil || locator["issuer"] != "dsar-fixture" || locator["subject"] != "00000000-0000-0000-0000-000000000123" || !contains(strings.Join(oidc.ReviewColumns, " "), "subject") {
		t.Fatalf("OIDC logical locator or manual review subject missing: %v", err)
	}
	nodeSection := section(report, "nodes")
	if len(nodeSection.Records) != 1 || !strings.Contains(string(nodeSection.Records[0].Locator), visibleNode) {
		t.Fatal("subject project visibility was not enforced")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"OTHER_PERSON_PRIVATE", "OTHER_PROFILE_PRIVATE", "FOREIGN_PRIVATE_NAME", foreign, "SECRET_IN_ARBITRARY_JSON", "THIRD_PARTY_MESSAGE_SECRET", "PRIVATE_FREE_TEXT", strings.Repeat("c", 64), strings.Repeat("d", 64), hiddenNode} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("packet disclosed an excluded value")
		}
	}
	if !bytes.Contains(raw, []byte("Subject Name")) || !bytes.Contains(raw, []byte("alias@example.test")) {
		t.Fatal("own identity data absent")
	}
	opts.Person = alias
	if fromAlias, err := Collect(t.Context(), d.App, opts); err != nil || fromAlias.Subject.ID != subject {
		t.Fatalf("resolve alias: %v", err)
	}
	opts.Erase = true
	plan, err := Collect(t.Context(), d.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Operation != "erase" || len(section(plan, "nodes").Records) != 2 {
		t.Fatal("erase plan must include hidden references without deleting")
	}
	if section(plan, "time_periods").Hold != "financial-review" || section(plan, "events").Hold != "audit-review" || !contains(strings.Join(section(plan, "personal_profiles").TouchColumns, " "), "first_name") {
		t.Fatal("erase columns or hold review missing")
	}
	if !contains(strings.Join(section(plan, "aithema_deprovisioned_subjects").TouchColumns, " "), "subject") {
		t.Fatal("OIDC subject missing from erase review")
	}
	for _, s := range plan.Sections {
		for _, r := range s.Records {
			if len(r.Data) != 0 {
				t.Fatal("erase plan included data values")
			}
		}
	}
	for _, badActor := range []string{subject, admin, agent, foreign} {
		denied := opts
		denied.ActorID = badActor
		if _, err := Collect(t.Context(), d.App, denied); err == nil {
			t.Fatal("non-owner authorized")
		}
	}
	if _, err := Collect(t.Context(), d.Admin, opts); err == nil || !strings.Contains(err.Error(), "cannot bypass") {
		t.Fatal("superuser database connection accepted")
	}
	if _, err := Collect(tenant.WithPrincipal(t.Context(), tenant.Principal{ID: owner, TenantID: tid, Kind: tenant.Agent}), d.App, opts); err == nil {
		t.Fatal("agent caller authorized")
	}
	foreignOpts := opts
	foreignOpts.Tenant = "dsar-other"
	if _, err := Collect(t.Context(), d.App, foreignOpts); err == nil {
		t.Fatal("cross-tenant owner authorized")
	}
	unknown := opts
	unknown.Person = foreign
	if _, err := Collect(t.Context(), d.App, unknown); err == nil {
		t.Fatal("cross-tenant subject resolved")
	}
	if countState() != before || countEvents() != beforeEvents {
		t.Fatal("read-only collection changed stored state")
	}
	assertAudit := func(got Report, expected int, kind string) {
		t.Helper()
		if countEvents() != expected {
			t.Fatal("successful command must append exactly one event")
		}
		var actorID, tenantID, eventType string
		var after json.RawMessage
		var minimal bool
		if err := d.Admin.QueryRow(ctx, `SELECT actor_principal_id::text,tenant_id::text,type,after,
			before IS NULL AND node_id IS NULL AND metadata IS NULL AND undo_of IS NULL
			FROM events WHERE type LIKE 'dsar.%' ORDER BY at DESC LIMIT 1`).Scan(&actorID, &tenantID, &eventType, &after, &minimal); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(after, &payload); err != nil {
			t.Fatal(err)
		}
		records := 0
		for _, s := range got.Sections {
			records += len(s.Records)
		}
		if actorID != owner || tenantID != tid || eventType != kind || !minimal || len(payload) != 5 || payload["subject_principal_id"] != subject || payload["operation"] != got.Operation || payload["dry_run"] != got.DryRun || payload["record_count"] != float64(records) || payload["table_count"] != float64(len(got.Sections)) {
			t.Fatal("audit must contain only actor, tenant, subject reference, kind and accurate counts")
		}
	}
	// Both command operations, including a private file and no-overwrite.
	for _, op := range []string{"export", "erase"} {
		args := []string{op, "--tenant", "dsar-kit", "--actor-principal-id", owner, "--person", subject}
		if op == "erase" {
			args = append(args, "--dry-run")
		}
		command, err := Parse(args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		expected := countEvents() + 1
		// A different connection must see the committed event before any byte
		// reaches the writer; an audit in the read snapshot cannot satisfy this.
		writer := auditWriter(func(content []byte) (int, error) {
			if countEvents() != expected {
				t.Fatal("packet emitted before audit committed")
			}
			return out.Write(content)
		})
		if err := Execute(t.Context(), d.App, command, writer); err != nil {
			t.Fatal(err)
		}
		var got Report
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Operation != op {
			t.Fatalf("%s command JSON: %v", op, err)
		}
		kind := "dsar.export.collected"
		if op == "erase" {
			kind = "dsar.erase.planned"
		}
		assertAudit(got, expected, kind)
		command.Output = filepath.Join(t.TempDir(), "packet.json")
		if err := Execute(t.Context(), d.App, command, &out); err != nil {
			t.Fatal(err)
		}
		stat, err := os.Stat(command.Output)
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatalf("private file permissions: %v", err)
		}
		content, err := os.ReadFile(command.Output)
		if err != nil || json.Unmarshal(content, &got) != nil {
			t.Fatal("private file did not contain a packet")
		}
		assertAudit(got, expected+1, kind)
		if err := Execute(t.Context(), d.App, command, &out); err == nil {
			t.Fatal("existing output overwritten")
		}
		if countEvents() != expected+1 {
			t.Fatal("output path failure appended a success event")
		}
	}
	if countState() != before {
		t.Fatal("manual DSAR commands changed state beyond the audit log")
	}
	// Fail both event insertion and its deferred commit. Each failure must
	// roll back the audit (including the event counter) and release no packet.
	for _, deferred := range []bool{false, true} {
		trigger := `CREATE TRIGGER dsar_audit_failure BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION dsar_reject_audit()`
		if deferred {
			trigger = `CREATE CONSTRAINT TRIGGER dsar_audit_failure AFTER INSERT ON events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dsar_reject_audit()`
		}
		if _, err := d.Admin.Exec(ctx, `CREATE OR REPLACE FUNCTION dsar_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type LIKE 'dsar.%' THEN RAISE EXCEPTION 'fixture audit refusal'; END IF; RETURN NEW; END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(ctx, trigger); err != nil {
			t.Fatal(err)
		}
		eventsBeforeFailure := countEvents()
		var counterBefore int64
		if err := d.Admin.QueryRow(ctx, `SELECT last_id FROM event_counters WHERE tenant_id=$1`, tid).Scan(&counterBefore); err != nil {
			t.Fatal(err)
		}
		for _, erase := range []bool{false, true} {
			for _, path := range []string{"-", filepath.Join(t.TempDir(), "blocked.json")} {
				var out bytes.Buffer
				command := Command{Options: Options{Tenant: "dsar-kit", ActorID: owner, Person: subject, Erase: erase}, Output: path}
				if err := Execute(t.Context(), d.App, command, &out); err == nil || !strings.Contains(err.Error(), "audit") || out.Len() != 0 {
					t.Fatalf("audit failure released output: %v", err)
				}
				if path != "-" {
					stat, err := os.Stat(path)
					if err != nil || stat.Size() != 0 {
						t.Fatal("audit failure wrote personal data to file")
					}
				}
			}
		}
		var counterAfter int64
		if err := d.Admin.QueryRow(ctx, `SELECT last_id FROM event_counters WHERE tenant_id=$1`, tid).Scan(&counterAfter); err != nil {
			t.Fatal(err)
		}
		if countEvents() != eventsBeforeFailure || counterAfter != counterBefore || countState() != before {
			t.Fatal("failed audit left committed state")
		}
		if _, err := d.Admin.Exec(ctx, `DROP TRIGGER dsar_audit_failure ON events`); err != nil {
			t.Fatal(err)
		}
	}
	// A failed collection also records nothing and releases no bytes.
	var failedOutput bytes.Buffer
	if err := Execute(t.Context(), d.App, Command{Options: unknown, Output: "-"}, &failedOutput); err == nil || failedOutput.Len() != 0 || countEvents() != beforeEvents+4 {
		t.Fatal("failed collection emitted output or a success event")
	}
	// A duplicate email is ambiguous rather than selecting the first person.
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET email='subject@example.test' WHERE id=$1::uuid`, other); err != nil {
		t.Fatal(err)
	}
	opts.Person = "subject@example.test"
	if _, err := Collect(t.Context(), d.App, opts); err == nil || !strings.Contains(err.Error(), "multiple people") {
		t.Fatalf("ambiguous email accepted: %v", err)
	}
	dbtest.BindRole(t, d, tid, other, "owner")
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, owner); err != nil {
		t.Fatal(err)
	}
	opts.Person = subject
	if _, err := Collect(t.Context(), d.App, opts); err == nil {
		t.Fatal("deactivated owner authorized")
	}
	if err := auditCollection(t.Context(), d.App, owner, report); err == nil || countEvents() != beforeEvents+4 {
		t.Fatal("audit did not recheck owner authorization")
	}
}

type auditWriter func([]byte) (int, error)

func (w auditWriter) Write(content []byte) (int, error) { return w(content) }

func section(report Report, table string) Section {
	for _, s := range report.Sections {
		if s.Table == table {
			return s
		}
	}
	return Section{}
}
