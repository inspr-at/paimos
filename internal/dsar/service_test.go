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
	if _, err := d.Admin.Exec(ctx, `INSERT INTO person_pause_settings(tenant_id,person_id,default_level,leaving_scope)
		VALUES($1,$2,'wrap_up','{"hosts":["PRIVATE_PAUSE_HOST"]}'),($1,$3,'pause','{"hosts":["OTHER_PAUSE_HOST"]}')`, tid, alias, other); err != nil {
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
	// Recent migrations add person-owned accounts, activity and journal chunks.
	// The subject owns these through an alias or issuer-scoped host session;
	// their free text, verification hash and raw bytes must remain excluded.
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var account, session, sid string
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at)
			VALUES($1,'dsar-linked-account','codex','dsar-daemon',$2,'fixture',$3,now()) RETURNING id::text`, tid, agent, alias).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_person_link_requests(tenant_id,account_id,user_code_hash,account_revision)
			VALUES($1,$2,repeat('e',64),0)`, tid, account); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest,doing,doing_at,tool_activity,tool_activity_at,pause_record,continuation_handover,work_placement,pause_progress)
			VALUES($1,$2,$3,$4,'codex','fixture','unmanaged','worker','fixture-ref','fixture-lease','PRIVATE_ACTIVITY_TEXT',now(),'PRIVATE_TOOL_TEXT',now(),'{"note":"PRIVATE_PAUSE_RECORD"}','{"note":"PRIVATE_HANDOVER"}','{"note":"PRIVATE_PLACEMENT"}','{"note":"PRIVATE_PAUSE_PROGRESS"}') RETURNING id::text`, tid, visibleProject, agent, alias).Scan(&session); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO harness_current_activity(tenant_id,session_id,text,source,at)
			VALUES($1,$2,'PRIVATE_ACTIVITY_HISTORY','agent',now())`, tid, session); err != nil {
			return err
		}
		// Preferences inherit the person's scope even when another person last
		// edited a row. An unrelated person's scope must stay out of the packet.
		for _, person := range []string{subject, other} {
			var scope, kind string
			if err := tx.QueryRow(ctx, `INSERT INTO model_pref_scopes(tenant_id,level,person_id,updated_by)
				VALUES($1,'person',$2,$3) RETURNING id::text`, tid, person, other).Scan(&scope); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT id::text FROM work_kinds WHERE tenant_id=$1 AND slug='backend' AND project_id IS NULL`, tid).Scan(&kind); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO model_pref_rows(tenant_id,scope_id,kind_id,level,updated_by)
				VALUES($1,$2,$3,'person',$4)`, tid, scope, kind, other); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO model_pref_cells(tenant_id,scope_id,kind_id,bucket,mode,family,line,effort,harness)
				VALUES($1,$2,$3,'normal','latest','openai','private-pref-line','high','codex')`, tid, scope, kind); err != nil {
				return err
			}
		}
		// Use the desk service's fixture capability and neutral tree stubs.
		// Visible agent questions belong to the subject through the owned session;
		// hidden questions are included only by the owner's erase review.
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,field_schema)
			VALUES($1,'question','Question','QST','question','{"issue_family":false}'),($1,'decision','Decision','DCS','question','{"issue_family":false}')`, tid); err != nil {
			return err
		}
		for _, fixture := range []struct{ project, asker, session, decider string }{{visibleProject, agent, session, alias}, {hiddenProject, alias, "", alias}, {visibleProject, other, "", other}} {
			var question, answer, asker string
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
				SELECT $1,id,aeon_next_node_key($1,'QST'),'Question',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='question' RETURNING id::text`, tid, fixture.project).Scan(&question); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO desk_questions(tenant_id,project_id,node_id,input,suggested_outcome,suggestion_reason)
				VALUES($1,$2,$3,'{"context":"PRIVATE_DESK_INPUT"}','once','agent_suggestion')`, tid, fixture.project, question); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,comment_node_id,input)
				VALUES($1,$2,$3,$4,gen_random_uuid(),repeat('g',64),NULLIF($5,'')::uuid,$3,'{"context":"PRIVATE_ASKER_INPUT"}') RETURNING id::text`, tid, fixture.project, question, fixture.asker, fixture.session).Scan(&asker); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
				SELECT $1,id,aeon_next_node_key($1,'DCS'),'Decision',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='decision' RETURNING id::text`, tid, question).Scan(&answer); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO desk_answers(tenant_id,project_id,node_id,question_id,revision,request_id,request_digest,decided_by,answer,reason,outcome,deliver_after)
				VALUES($1,$2,$3,$4,2,gen_random_uuid(),repeat('h',64),$5,'PRIVATE_DESK_ANSWER','PRIVATE_DESK_REASON','once',now())`, tid, fixture.project, answer, question, fixture.decider); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO desk_decisions(tenant_id,project_id,question_id,revision,effect_ref)
				VALUES($1,$2,$3,2,'PRIVATE_DECISION_EFFECT')`, tid, fixture.project, question); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO desk_pending(tenant_id,project_id,question_id,revision,asker_id,kind,deliver_after,effect_ref,error_code)
				VALUES($1,$2,$3,2,$4,'inbox',now(),'PRIVATE_PENDING_EFFECT','PRIVATE_PENDING_ERROR')`, tid, fixture.project, question, asker); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO aithema_sessions(tenant_id,sid,project_id,plugin_principal,authorization_bytes,worker_generation,auth_epoch,host_mode,currency,evidence,session_cap)
			VALUES($1,gen_random_uuid(),'fixture','fixture','PRIVATE_AUTH_BYTES',1,1,'review','EUR',false,100) RETURNING sid::text`, tid).Scan(&sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_host_sessions(tenant_id,sid,issuer,requester)
			VALUES($1,$2,'dsar-fixture','00000000-0000-0000-0000-000000000123')`, tid, sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_records(tenant_id,sid,seq,client_event_id,contract,kind,original_bytes,document,content_sha256)
			VALUES($1,$2,1,gen_random_uuid(),'aithema.journal.record','pending_op.content','PRIVATE_RECORD_BYTES','PRIVATE_DOCUMENT_BYTES',repeat('f',64))`, tid, sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_content_events(tenant_id,sid,seq,client_event_id,wire_sha256)
			VALUES($1,$2,1,gen_random_uuid(),repeat('f',64))`, tid, sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_record_chunks(tenant_id,sid,seq,chunk_offset,bytes)
			VALUES($1,$2,1,0,'PRIVATE_RECORD_CHUNK_BYTES')`, tid, sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_uploads(tenant_id,sid,worker_generation,auth_epoch,action,wire_sha256,total,next_offset)
			VALUES($1,$2,1,1,'records',repeat('f',64),1048577,0)`, tid, sid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO aithema_journal_upload_chunks(tenant_id,sid,worker_generation,auth_epoch,action,wire_sha256,chunk_offset,bytes)
			VALUES($1,$2,1,1,'records',repeat('f',64),0,'PRIVATE_UPLOAD_CHUNK_BYTES')`, tid, sid)
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
	for _, table := range []string{"agent_accounts", "account_person_link_requests", "harness_sessions", "harness_current_activity", "aithema_journal_content_events", "aithema_journal_record_chunks", "aithema_journal_uploads", "aithema_journal_upload_chunks", "person_pause_settings", "model_pref_scopes", "model_pref_rows", "model_pref_cells", "desk_askers", "desk_answers", "desk_decisions", "desk_pending"} {
		if len(section(report, table).Records) != 1 || section(report, table).Records[0].Match != "subject-reference" {
			t.Fatalf("new personal domain missing alias/issuer ownership: %s", table)
		}
	}
	if questions := section(report, "desk_questions"); len(questions.Records) != 1 || questions.Records[0].Match != "subject-reference" {
		t.Fatal("question must inherit its asker/session ownership without a text mention")
	}
	if !strings.Contains(string(section(report, "person_pause_settings").Records[0].Data), "wrap_up") {
		t.Fatal("alias pause preference missing from export")
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
	for _, private := range []string{"OTHER_PERSON_PRIVATE", "OTHER_PROFILE_PRIVATE", "FOREIGN_PRIVATE_NAME", foreign, "SECRET_IN_ARBITRARY_JSON", "THIRD_PARTY_MESSAGE_SECRET", "PRIVATE_FREE_TEXT", "PRIVATE_ACTIVITY_TEXT", "PRIVATE_TOOL_TEXT", "PRIVATE_ACTIVITY_HISTORY", "PRIVATE_AUTH_BYTES", "PRIVATE_RECORD_BYTES", "PRIVATE_DOCUMENT_BYTES", "PRIVATE_RECORD_CHUNK_BYTES", "PRIVATE_UPLOAD_CHUNK_BYTES", "PRIVATE_PAUSE_HOST", "OTHER_PAUSE_HOST", "PRIVATE_PAUSE_RECORD", "PRIVATE_HANDOVER", "PRIVATE_PLACEMENT", "PRIVATE_PAUSE_PROGRESS", "private-pref-line", "PRIVATE_DESK_INPUT", "PRIVATE_ASKER_INPUT", "PRIVATE_DESK_ANSWER", "PRIVATE_DESK_REASON", "PRIVATE_DECISION_EFFECT", "PRIVATE_PENDING_EFFECT", "PRIVATE_PENDING_ERROR", strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("g", 64), strings.Repeat("h", 64), hiddenNode} {
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
	for _, table := range []string{"desk_questions", "desk_askers", "desk_answers", "desk_decisions", "desk_pending"} {
		if len(section(plan, table).Records) != 2 {
			t.Fatalf("erase review omitted hidden desk references: %s", table)
		}
	}
	if section(plan, "desk_answers").Hold != "audit-review" || !contains(strings.Join(section(plan, "person_pause_settings").TouchColumns, " "), "leaving_scope") {
		t.Fatal("desk immutability or private leaving scope missing from erase review")
	}
	if section(plan, "time_periods").Hold != "financial-review" || section(plan, "events").Hold != "audit-review" || !contains(strings.Join(section(plan, "personal_profiles").TouchColumns, " "), "first_name") {
		t.Fatal("erase columns or hold review missing")
	}
	if !contains(strings.Join(section(plan, "aithema_deprovisioned_subjects").TouchColumns, " "), "subject") {
		t.Fatal("OIDC subject missing from erase review")
	}
	if !contains(strings.Join(section(plan, "agent_accounts").TouchColumns, " "), "owner_person_id") || !contains(strings.Join(section(plan, "harness_sessions").TouchColumns, " "), "doing") || section(plan, "aithema_journal_record_chunks").Hold != "audit-review" {
		t.Fatal("new ownership/activity columns or journal hold missing from erase review")
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
