// SPDX-License-Identifier: AGPL-3.0-only

// Package dsar implements the offline, owner-operated manual DSAR kit. It has
// no HTTP surface, retention scheduler, or destructive operation. Documents,
// conversations and arbitrary tenant fields produce review locators rather
// than unreviewed content containing credentials or third-party information.
package dsar

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// inventoryJSON is deliberately static. Adding a schema column requires a
// privacy decision here, even when its name evades personal-data heuristics.
// Export is a positive allowlist; Metadata is the explicit non-personal
// allowlist. Review covers free text, JSON, files and subject references.
// Secret covers credentials and authentication material and is never read.
//
//go:embed inventory.json
var inventoryJSON string

// Domain is a table's read adapter and erase-review policy. Space-separated
// column lists keep the complete inventory inspectable in one small file.
type Domain struct {
	Table    string  `json:"table"`
	Export   string  `json:"export"`
	Review   string  `json:"review"`
	Secret   string  `json:"secret"`
	Metadata string  `json:"metadata"`
	Subjects string  `json:"subjects"`
	Locator  string  `json:"locator"`
	Parent   *Parent `json:"parent,omitempty"`
	Hold     string  `json:"hold"`
}

// Parent follows an explicit ownership relation, always within the tenant.
type Parent struct {
	Table  string `json:"table"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

// Inventory returns an independent copy for future DSAR/retention adapters.
func Inventory() ([]Domain, error) {
	var domains []Domain
	if err := json.Unmarshal([]byte(inventoryJSON), &domains); err != nil {
		return nil, err
	}
	if err := validateInventory(domains); err != nil {
		return nil, err
	}
	return domains, nil
}

func validateInventory(domains []Domain) error {
	identifier := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	byTable := make(map[string]Domain, len(domains))
	for _, d := range domains {
		if !identifier.MatchString(d.Table) {
			return errors.New("invalid inventory table")
		}
		if _, exists := byTable[d.Table]; exists {
			return fmt.Errorf("duplicate inventory table %s", d.Table)
		}
		byTable[d.Table] = d
		seen := make(map[string]bool)
		for _, list := range []string{d.Export, d.Review, d.Secret, d.Metadata} {
			for _, c := range strings.Fields(list) {
				if !identifier.MatchString(c) || seen[c] {
					return fmt.Errorf("invalid or duplicate inventory column %s.%s", d.Table, c)
				}
				seen[c] = true
			}
		}
		for _, c := range strings.Fields(d.Subjects + " " + d.Locator) {
			if !seen[c] || contains(d.Secret, c) {
				return fmt.Errorf("unsafe inventory selector/locator %s.%s", d.Table, c)
			}
		}
		if d.Hold != "none" && d.Hold != "financial-review" && d.Hold != "audit-review" {
			return fmt.Errorf("unknown hold policy %s", d.Table)
		}
	}
	for _, d := range domains {
		if d.Parent == nil {
			continue
		}
		p := d.Parent
		parent, ok := byTable[p.Table]
		if !ok || !contains(d.columns(), p.Local) || !contains(parent.columns(), p.Remote) || contains(d.Secret, p.Local) || contains(parent.Secret, p.Remote) {
			return fmt.Errorf("invalid inventory parent for %s", d.Table)
		}
		visited := map[string]bool{d.Table: true}
		for current := parent; current.Parent != nil; current = byTable[current.Parent.Table] {
			if visited[current.Table] {
				return fmt.Errorf("cyclic inventory parent for %s", d.Table)
			}
			visited[current.Table] = true
		}
	}
	return nil
}

func contains(list, value string) bool {
	for _, field := range strings.Fields(list) {
		if field == value {
			return true
		}
	}
	return false
}

func (d Domain) columns() string {
	return d.Export + " " + d.Review + " " + d.Secret + " " + d.Metadata
}

// CheckSchema fails closed when a migration introduces an unregistered table
// or column. It checks the migrated catalog, so quoted names, ALTER TABLE,
// renames and SQL syntax changes cannot bypass a source-text heuristic.
func CheckSchema(ctx context.Context, tx pgx.Tx, domains []Domain) error {
	rows, err := tx.Query(ctx, `SELECT c.table_name,c.column_name
		FROM information_schema.columns c JOIN information_schema.tables t
		ON t.table_schema=c.table_schema AND t.table_name=c.table_name
		WHERE c.table_schema='public' AND t.table_type='BASE TABLE'
		AND c.table_name <> 'schema_migrations' ORDER BY c.table_name,c.ordinal_position`)
	if err != nil {
		return err
	}
	defer rows.Close()
	registered := make(map[string]string, len(domains))
	for _, d := range domains {
		registered[d.Table] = d.columns()
	}
	var missing []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		if !contains(registered[table], column) {
			missing = append(missing, table+"."+column)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("DSAR inventory missing schema columns: %s", strings.Join(missing, ", "))
	}
	return nil
}

func column(alias, name string) string {
	return pgx.Identifier{alias, name}.Sanitize()
}

// ownership reads only registered subject references. Parent chains are
// explicit: owning a contact or session is never inferred from its name.
func ownership(d Domain, domains map[string]Domain, alias string, depth int) string {
	var conditions []string
	for _, c := range strings.Fields(d.Subjects) {
		conditions = append(conditions, column(alias, c)+"::text = ANY($2::text[])")
	}
	if d.Table == "invites" {
		conditions = append(conditions, "lower("+column(alias, "email")+") = ANY($3::text[])")
	}
	if d.Table == "nodes" {
		conditions = append(conditions, `EXISTS (SELECT 1 FROM crm_contact_principals cp WHERE cp.tenant_id=$1::uuid
			AND cp.contact_node_id=`+column(alias, "id")+` AND cp.principal_id::text=ANY($2::text[]))`)
	}
	if d.Parent != nil {
		parentAlias := fmt.Sprintf("p%d", depth)
		parent := domains[d.Parent.Table]
		conditions = append(conditions, "EXISTS (SELECT 1 FROM "+pgx.Identifier{parent.Table}.Sanitize()+" "+parentAlias+
			" WHERE "+column(parentAlias, "tenant_id")+"=$1::uuid AND "+column(parentAlias, d.Parent.Remote)+"="+column(alias, d.Parent.Local)+
			" AND ("+ownership(parent, domains, parentAlias, depth+1)+"))")
	}
	if len(conditions) == 0 {
		return "false"
	}
	return "(" + strings.Join(conditions, " OR ") + ")"
}

// possibleMentions returns locators only. Arbitrary text/JSON can contain
// someone else's data or secrets, and never enters a machine export. Name-only
// mentions and binary objects require the explicit manual checklist as well.
func possibleMentions(d Domain) string {
	if d.Table == "principals" || d.Table == "identities" || d.Table == "personal_profiles" || d.Table == "person_host_labels" {
		return "false"
	}
	var fields []string
	for _, c := range strings.Fields(d.Review) {
		if !contains(d.Subjects, c) {
			fields = append(fields, "coalesce("+column("t", c)+"::text,'')")
		}
	}
	if len(fields) == 0 {
		return "false"
	}
	return "EXISTS (SELECT 1 FROM unnest($2::text[] || $3::text[]) needle WHERE needle<>'' AND (" +
		strings.Join(mapMentions(fields), " OR ") + "))"
}

func mapMentions(fields []string) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		result = append(result, "strpos(lower("+field+"),lower(needle))>0")
	}
	return result
}

func projection(d Domain, list string) string {
	var fields []string
	for _, c := range strings.Fields(list) {
		value := column("t", c)
		// Do not export another person's identifier on a multi-person row.
		if contains(d.Subjects, c) && !(d.Table == "principals" && c == "id") {
			value = "CASE WHEN " + value + "::text=ANY($2::text[]) THEN " + value + " END"
		}
		fields = append(fields, "'"+c+"'", value)
	}
	if len(fields) == 0 {
		return "'{}'::jsonb"
	}
	return "jsonb_strip_nulls(jsonb_build_object(" + strings.Join(fields, ",") + "))"
}

func adapterQuery(d Domain, domains map[string]Domain, erase bool) string {
	direct := "coalesce(" + ownership(d, domains, "t", 0) + ",false)"
	mentions := possibleMentions(d)
	where := "t.tenant_id=$1::uuid"
	if d.Table == "tenants" {
		where = "t.id=$1::uuid"
	}
	if d.Table == "identities" {
		// Global identity directory: the ownership join is the tenant boundary.
		where = "true"
	}
	data := "'{}'::jsonb"
	if !erase {
		data = "CASE WHEN " + direct + " THEN " + projection(d, d.Export) + " ELSE '{}'::jsonb END"
	}
	return "SELECT jsonb_build_object('_snapshot_row',t.ctid::text) || " + projection(d, d.Locator) + ", " + data + ", " + direct +
		" FROM " + pgx.Identifier{d.Table}.Sanitize() + " t WHERE " + where + " AND cardinality($2::text[])>0 AND cardinality($3::text[])>=0 AND (" + direct + " OR " + mentions + ") LIMIT 10001"
}
