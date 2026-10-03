// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Route classification lives on tickets and tasks only. Other kinds may use
// "area" for something else, so their fields are left untouched.
func canonicalRouteFields(p tenant.Principal, kindSlug string, raw, before json.RawMessage) (json.RawMessage, error) {
	if kindSlug != "ticket" && kindSlug != "task" {
		return raw, nil
	}
	next, err := decodeRouteFields(raw)
	if err != nil {
		return nil, err
	}
	var old map[string]any
	if len(before) > 0 && string(before) != "null" {
		old, err = decodeRouteFields(before)
		if err != nil {
			return nil, err
		}
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	// A repeated value keeps its stored provenance. Source, by and at diffs
	// are not a new classification, so only a real value change re-stamps.
	write := false
	for _, group := range routeGroups {
		same, err := sameRouteValue(next, old, group.value)
		if err != nil {
			return nil, err
		}
		if same {
			// A person can confirm a server suggestion by explicitly submitting
			// the value without its provenance. Copying fields is not confirmation.
			unconfirmed := old[group.source] == "suggested" || old[group.source] == "agent" && old[group.value+"_confirmed"] == false
			if unconfirmed && p.Kind == tenant.Person &&
				next[group.value] != nil && next[group.source] == nil && next[group.by] == nil && next[group.at] == nil {
				if err := applyRouteGroup(next, group, p, at); err != nil {
					return nil, err
				}
				write = true
				continue
			}
			if keepRouteProvenance(next, old, group) {
				write = true
			}
			continue
		}
		write = true
		if err := applyRouteGroup(next, group, p, at); err != nil {
			return nil, err
		}
	}
	if !write {
		return raw, nil
	}
	return json.Marshal(next)
}

type routeGroup struct {
	value, source, by, at string
	known                 func(string) bool
}

var routeGroups = []routeGroup{
	{"route_role", "route_role_source", "route_role_by", "route_role_at", modelregistry.KnownRouteRole},
	{"area", "area_source", "area_by", "area_at", validAreaShape},
	{"complexity", "complexity_source", "complexity_by", "complexity_at", func(s string) bool { return s == "S" || s == "M" || s == "L" }},
}

var areaShape = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func validAreaShape(s string) bool { return areaShape.MatchString(s) }

// Validate a changed area inside the node mutation transaction. Unchanged
// legacy or archived areas retain their provenance and remain editable.
func canonicalTicketRouteFields(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind string, parent *string, raw, before json.RawMessage) (json.RawMessage, error) {
	out, err := canonicalRouteFields(p, kind, raw, before)
	if err != nil || kind != "ticket" && kind != "task" {
		return out, err
	}
	next, err := decodeRouteFields(out)
	if err != nil {
		return nil, err
	}
	old, err := decodeRouteFields(before)
	if err != nil {
		return nil, err
	}
	same, err := sameRouteValue(next, old, "area")
	if err != nil || same || next["area"] == nil {
		return out, err
	}
	project, err := routeProject(ctx, tx, parent)
	if err != nil {
		return nil, err
	}
	known, err := modelregistry.KnownRouteArea(ctx, tx, next["area"].(string), project)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, badRequest(routeValueError("area"))
	}
	return out, nil
}

func routeProject(ctx context.Context, tx pgx.Tx, parent *string) (string, error) {
	if parent == nil {
		return "", nil
	}
	var project string
	err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text, CASE WHEN k.slug='project' THEN n.id::text END,'') FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL`, *parent).Scan(&project)
	return project, err
}

func sameRouteValue(next, old map[string]any, key string) (bool, error) {
	nextText, nextSet, err := routeText(next, key)
	if err != nil {
		return false, err
	}
	oldText, oldSet, err := routeText(old, key)
	if err != nil {
		return false, err
	}
	return nextSet == oldSet && nextText == oldText, nil
}

func routeText(m map[string]any, key string) (string, bool, error) {
	if m == nil {
		return "", false, nil
	}
	value, ok := m[key]
	if !ok || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", false, badRequest(routeValueError(key))
	}
	return strings.TrimSpace(text), true, nil
}

// keepRouteProvenance copies stored source, by and at onto an unchanged value.
// Client metadata, including a stripped or forged set, is not adopted.
func keepRouteProvenance(next, old map[string]any, group routeGroup) bool {
	mutated := false
	if text, set, _ := routeText(next, group.value); set && next[group.value] != text {
		next[group.value] = text
		mutated = true
	}
	for _, key := range []string{group.source, group.by, group.at, group.value + "_confirmed"} {
		oldVal, oldSet := presentValue(old, key)
		if !oldSet {
			if _, ok := next[key]; ok {
				delete(next, key)
				mutated = true
			}
			continue
		}
		if !reflect.DeepEqual(next[key], oldVal) {
			next[key] = oldVal
			mutated = true
		}
	}
	return mutated
}

func presentValue(m map[string]any, key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	value, ok := m[key]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

func applyRouteGroup(next map[string]any, group routeGroup, p tenant.Principal, at string) error {
	value, present := next[group.value]
	if !present || value == nil {
		delete(next, group.value)
		delete(next, group.source)
		delete(next, group.by)
		delete(next, group.at)
		delete(next, group.value+"_confirmed")
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return badRequest(routeValueError(group.value))
	}
	text = strings.TrimSpace(text)
	if !group.known(text) {
		return badRequest(routeValueError(group.value))
	}
	if source, supplied := next[group.source]; supplied && source != nil {
		kind, ok := source.(string)
		if !ok || kind != string(p.Kind) {
			return badRequest(group.source + " must match the acting principal kind")
		}
	}
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		return badRequest("route fields require a person or agent principal")
	}
	next[group.value] = text
	next[group.source] = string(p.Kind)
	next[group.by] = p.ID
	next[group.at] = at
	next[group.value+"_confirmed"] = p.Kind == tenant.Person
	return nil
}

func routeValueError(key string) string {
	if key == "route_role" {
		return "route_role must be scout, mechanical, build, build-hard, or review-gate"
	}
	if key == "complexity" {
		return "complexity must be S, M, or L"
	}
	return "area must be an active kind of work visible in this project"
}

func decodeRouteFields(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return nil, badRequest("invalid route fields")
	}
	if fields == nil {
		fields = map[string]any{}
	}
	return fields, nil
}
