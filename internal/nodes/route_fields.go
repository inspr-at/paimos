// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
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
	changed := false
	for _, group := range routeGroups {
		if !routeGroupChanged(next, old, group) {
			continue
		}
		changed = true
		if err := applyRouteGroup(next, group, p, at); err != nil {
			return nil, err
		}
	}
	if !changed {
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
	{"area", "area_source", "area_by", "area_at", modelregistry.KnownRouteArea},
}

func routeGroupChanged(next, old map[string]any, group routeGroup) bool {
	for _, key := range []string{group.value, group.source, group.by, group.at} {
		if !reflect.DeepEqual(next[key], old[key]) {
			return true
		}
	}
	return false
}

func applyRouteGroup(next map[string]any, group routeGroup, p tenant.Principal, at string) error {
	value, present := next[group.value]
	if !present || value == nil {
		delete(next, group.value)
		delete(next, group.source)
		delete(next, group.by)
		delete(next, group.at)
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
	return nil
}

func routeValueError(key string) string {
	if key == "route_role" {
		return "route_role must be scout, mechanical, build, build-hard, or review-gate"
	}
	return "area must be backend, frontend, full-stack, infra, design, or docs"
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
