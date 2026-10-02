// SPDX-License-Identifier: AGPL-3.0-only
package dsar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/tenant"
)

type Options struct {
	Tenant  string
	ActorID string
	Person  string
	Erase   bool
}

const ownerQuery = `SELECT EXISTS (SELECT 1 FROM principals p JOIN role_bindings b
	ON b.tenant_id=p.tenant_id AND b.principal_id=p.id JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
	WHERE p.tenant_id=$1::uuid AND p.id=$2::uuid AND p.kind='person' AND p.status='active' AND p.linked_to IS NULL
	AND b.scope_type='workspace' AND r.builtin AND r.key='owner')`

type Subject struct {
	ID      string   `json:"principal_id"`
	Aliases []string `json:"principal_ids"`
	Emails  []string `json:"emails"`
}

type Record struct {
	Locator json.RawMessage `json:"locator"`
	Data    json.RawMessage `json:"data,omitempty"`
	Match   string          `json:"match"`
}

type Section struct {
	Table           string   `json:"table"`
	Records         []Record `json:"records"`
	ReviewColumns   []string `json:"manual_review_columns"`
	ExcludedColumns []string `json:"excluded_credential_columns"`
	TouchColumns    []string `json:"manual_touch_columns"`
	Action          string   `json:"manual_action"`
	Hold            string   `json:"hold"`
}

// Report is an operator review packet, never an assertion that arbitrary
// documents have been reviewed or that an erasure is legally permitted.
type Report struct {
	Version         int       `json:"format_version"`
	Operation       string    `json:"operation"`
	DryRun          bool      `json:"dry_run"`
	TenantID        string    `json:"tenant_id"`
	Subject         Subject   `json:"subject"`
	GeneratedAt     time.Time `json:"generated_at"`
	Sections        []Section `json:"sections"`
	ManualChecklist []string  `json:"manual_checklist"`
}

// Collect opens its own read-only snapshot and never migrates, writes an audit
// event, creates a principal, or calls an external identity provider. Database
// access is the offline operator boundary; the named actor must additionally
// be a live, unlinked person with the tenant's built-in workspace owner role.
func Collect(ctx context.Context, pool *pgxpool.Pool, opts Options) (Report, error) {
	var report Report
	if pool == nil || opts.Tenant == "" || !uuidLike(opts.ActorID) || strings.TrimSpace(opts.Person) == "" {
		return report, errors.New("tenant, owner principal UUID and person UUID/email are required")
	}
	if caller, ok := tenant.PrincipalFrom(ctx); ok && (caller.Kind != tenant.Person || caller.ID != opts.ActorID) {
		return report, errors.New("DSAR is an offline owner operation; actor must be the calling person")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return report, errors.New("open DSAR read-only transaction failed")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var bypass bool
	if err := tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&bypass); err != nil || bypass {
		return report, errors.New("DSAR requires a database role that cannot bypass row-level security")
	}
	var tenantID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, opts.Tenant).Scan(&tenantID); err != nil {
		return report, errors.New("tenant not found")
	}
	if caller, ok := tenant.PrincipalFrom(ctx); ok && caller.TenantID != tenantID {
		return report, errors.New("actor tenant mismatch")
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.system','on',true),
		set_config('aeon.visible_projects','*',true),set_config('aeon.principal_ids','',true),set_config('statement_timeout','30s',true)`, tenantID); err != nil {
		return report, errors.New("scope DSAR transaction failed")
	}
	var authorized bool
	if err := tx.QueryRow(ctx, ownerQuery, tenantID, opts.ActorID).Scan(&authorized); err != nil || !authorized {
		return report, errors.New("DSAR requires an active workspace owner person in the selected tenant")
	}
	domains, err := Inventory()
	if err != nil {
		return report, err
	}
	if err := CheckSchema(ctx, tx, domains); err != nil {
		return report, err
	}
	subject, err := resolveSubject(ctx, tx, tenantID, opts.Person)
	if err != nil {
		return report, err
	}
	if !opts.Erase {
		// Project visibility belongs to the subject, not the operator. Private
		// preferences for linked aliases still belong to this same person.
		if _, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,NULL)`,
			tenantID, subject.ID); err != nil {
			return report, errors.New("set subject visibility failed")
		}
	}
	// Erase planning keeps the operator's project visibility. Both paths use
	// the subject's alias family for private per-person policies.
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.principal_ids',$1,true)`, "{"+strings.Join(subject.Aliases, ",")+"}"); err != nil {
		return report, errors.New("set subject mapping failed")
	}
	report = Report{Version: 1, Operation: "export", TenantID: tenantID, Subject: subject, GeneratedAt: time.Now().UTC(), Sections: []Section{}, ManualChecklist: []string{
		"Verify the requester's identity and document the request, lawful basis, deadline and delivery decision in a restricted case record.",
		"This packet needs manual supplementation: review the listed free text, JSON, transcripts, messages, snapshots, file/avatar bytes and external identity-provider records. Never deliver credentials or third-party information the subject cannot see.",
		"UUID/email mention matches are candidates, not confirmed ownership. Search for name-only references and tenant-defined field mappings too; absence of a match is not absence of personal data.",
		"The _snapshot_row locator identifies a physical row only in this read snapshot. Re-resolve logical keys and ownership before any manual action; never use that locator as a later deletion instruction.",
		"Record any statutory, litigation or contractual hold and its actual expiry before manual deletion/anonymisation. Financial records need a BAO section 132 assessment; audit immutability is a technical constraint, not a blanket statutory hold.",
		"Global identities may serve other tenants. Preserve other memberships; review session/key revocation, retained event/snapshot copies, backups, file storage and integrations separately. This command changes none of them.",
	}}
	if opts.Erase {
		report.Operation = "erase"
		report.DryRun = true
	}
	byTable := make(map[string]Domain, len(domains))
	for _, d := range domains {
		byTable[d.Table] = d
	}
	count := 0
	for _, d := range domains {
		section, err := readDomain(ctx, tx, d, byTable, tenantID, subject, opts.Erase)
		if err != nil {
			return Report{}, err
		}
		count += len(section.Records)
		if count > 50000 {
			return Report{}, errors.New("DSAR packet exceeds 50000 records; narrow the manual case before retrying")
		}
		report.Sections = append(report.Sections, section)
	}
	// Roll back even on success. Execute records collection in a separate
	// minimal write transaction before releasing the packet.
	return report, nil
}

func resolveSubject(ctx context.Context, tx pgx.Tx, tenantID, person string) (Subject, error) {
	person = strings.TrimSpace(person)
	if !uuidLike(person) && !strings.Contains(person, "@") {
		return Subject{}, errors.New("person must be a principal UUID or email")
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT coalesce(p.linked_to,p.id)::text FROM principals p
		LEFT JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1::uuid AND p.kind='person'
		AND (p.id::text=lower($2) OR lower(p.email)=lower($2) OR lower(i.email)=lower($2))`, tenantID, person)
	if err != nil {
		return Subject{}, errors.New("resolve DSAR subject failed")
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Subject{}, errors.New("read DSAR subject failed")
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Subject{}, errors.New("read DSAR subject failed")
	}
	if len(ids) == 0 {
		return Subject{}, errors.New("person not found in the selected tenant")
	}
	if len(ids) > 1 {
		return Subject{}, errors.New("email maps to multiple people; use a principal UUID")
	}
	subject := Subject{ID: ids[0], Aliases: []string{}, Emails: []string{}}
	rows, err = tx.Query(ctx, `SELECT p.id::text,coalesce(p.email,''),coalesce(i.email,'') FROM principals p
		LEFT JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1::uuid AND p.kind='person'
		AND (p.id=$2::uuid OR p.linked_to=$2::uuid) ORDER BY p.id`, tenantID, subject.ID)
	if err != nil {
		return Subject{}, errors.New("resolve DSAR aliases failed")
	}
	defer rows.Close()
	emails := make(map[string]bool)
	for rows.Next() {
		var id, email, identityEmail string
		if err := rows.Scan(&id, &email, &identityEmail); err != nil {
			return Subject{}, errors.New("read DSAR aliases failed")
		}
		subject.Aliases = append(subject.Aliases, id)
		for _, e := range []string{email, identityEmail} {
			e = strings.ToLower(strings.TrimSpace(e))
			if e != "" {
				emails[e] = true
			}
		}
	}
	if rows.Err() != nil {
		return Subject{}, errors.New("read DSAR aliases failed")
	}
	for e := range emails {
		subject.Emails = append(subject.Emails, e)
	}
	sort.Strings(subject.Emails)
	return subject, nil
}

func readDomain(ctx context.Context, tx pgx.Tx, d Domain, domains map[string]Domain, tenantID string, subject Subject, erase bool) (Section, error) {
	section := Section{Table: d.Table, Records: []Record{}, ReviewColumns: strings.Fields(d.Review), ExcludedColumns: strings.Fields(d.Secret), TouchColumns: strings.Fields(d.Export + " " + d.Review + " " + d.Secret), Action: "review then delete/anonymise subject fields; preserve other people and references", Hold: d.Hold}
	if d.Hold == "financial-review" {
		section.Action = "keep pending owner assessment of statutory retention and actual expiry; anonymise only unneeded fields"
	}
	if d.Hold == "audit-review" {
		section.Action = "keep pending audit/hold assessment; append-only storage needs a separately approved redaction procedure"
	}
	if d.Table == "identities" {
		section.Action = "review shared global identity; keep while other tenants need it"
	}
	if d.Secret != "" {
		section.Action += "; revoke authentication material through its owner workflow, never export it"
	}
	rows, err := tx.Query(ctx, adapterQuery(d, domains, erase), tenantID, subject.Aliases, subject.Emails)
	if err != nil {
		return section, fmt.Errorf("DSAR adapter %s failed (no bundle emitted)", d.Table)
	}
	defer rows.Close()
	for rows.Next() {
		var record Record
		var own bool
		if err := rows.Scan(&record.Locator, &record.Data, &own); err != nil {
			return section, fmt.Errorf("DSAR adapter %s could not read a record", d.Table)
		}
		record.Match = "possible-mention"
		if own {
			record.Match = "subject-reference"
		}
		if erase {
			record.Data = nil
		}
		section.Records = append(section.Records, record)
		if len(section.Records) > 10000 {
			return section, fmt.Errorf("DSAR adapter %s exceeds 10000 records; no partial bundle emitted", d.Table)
		}
	}
	if rows.Err() != nil {
		return section, fmt.Errorf("DSAR adapter %s could not finish", d.Table)
	}
	// SQL plans may return a different physical order on each invocation.
	sort.Slice(section.Records, func(i, j int) bool { return string(section.Records[i].Locator) < string(section.Records[j].Locator) })
	return section, nil
}

func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
