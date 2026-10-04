// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Roadmap publication is a person decision on work items and legacy tickets.
// Tasks and other kinds may carry the same key for something else, so their fields
// stay untouched.
// False and a missing flag are the same unpublished state: an agent may send
// false on an ordinary save. Turning the flag on, or clearing a stored true,
// is refused for every principal that is not a person.
func canonicalRoadmapPublication(p tenant.Principal, kindSlug string, raw, before json.RawMessage) (json.RawMessage, error) {
	if kindSlug != "work" && kindSlug != "ticket" {
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
	nextPub, err := roadmapPublished(next)
	if err != nil {
		return nil, err
	}
	oldPub, err := roadmapPublished(old)
	if err != nil {
		return nil, err
	}
	if nextPub && oldPub {
		if source, by, at, ok := storedRoadmapProvenance(old); ok {
			if next["roadmap_public_source"] == source && next["roadmap_public_by"] == by && next["roadmap_public_at"] == at {
				return raw, nil
			}
			next["roadmap_public_source"] = source
			next["roadmap_public_by"] = by
			next["roadmap_public_at"] = at
			return json.Marshal(next)
		}
		// A stored true without person provenance is not a publication. An
		// agent cannot finish it. A person stamps it.
		if p.Kind != tenant.Person {
			stripRoadmapProvenance(next)
			return json.Marshal(next)
		}
		return stampRoadmapPublication(next, p)
	}
	if nextPub != oldPub && p.Kind != tenant.Person {
		return nil, &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	if nextPub {
		return stampRoadmapPublication(next, p)
	}
	return clearRoadmapPublication(next, raw)
}

func roadmapPublished(fields map[string]any) (bool, error) {
	if fields == nil {
		return false, nil
	}
	value, ok := fields["roadmap_public"]
	if !ok {
		return false, nil
	}
	if value == nil {
		return false, badRequest("roadmap_public must be a boolean")
	}
	flag, ok := value.(bool)
	if !ok {
		return false, badRequest("roadmap_public must be a boolean")
	}
	return flag, nil
}

func storedRoadmapProvenance(fields map[string]any) (source, by, at string, ok bool) {
	if fields == nil {
		return "", "", "", false
	}
	source, sourceOK := fields["roadmap_public_source"].(string)
	by, byOK := fields["roadmap_public_by"].(string)
	at, atOK := fields["roadmap_public_at"].(string)
	if !sourceOK || !byOK || !atOK || source != string(tenant.Person) || by == "" || at == "" {
		return "", "", "", false
	}
	return source, by, at, true
}

func stampRoadmapPublication(next map[string]any, p tenant.Principal) (json.RawMessage, error) {
	if supplied, ok := next["roadmap_public_source"]; ok && supplied != nil {
		kind, isString := supplied.(string)
		if !isString || kind != string(tenant.Person) {
			return nil, badRequest("roadmap_public_source must match the acting principal kind")
		}
	}
	if p.Kind != tenant.Person {
		return nil, &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	next["roadmap_public"] = true
	next["roadmap_public_source"] = string(tenant.Person)
	next["roadmap_public_by"] = p.ID
	next["roadmap_public_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	return json.Marshal(next)
}

func clearRoadmapPublication(next map[string]any, raw json.RawMessage) (json.RawMessage, error) {
	if !roadmapProvenanceSent(next) {
		return raw, nil
	}
	stripRoadmapProvenance(next)
	return json.Marshal(next)
}

func roadmapProvenanceSent(fields map[string]any) bool {
	if fields == nil {
		return false
	}
	for _, key := range []string{"roadmap_public_source", "roadmap_public_by", "roadmap_public_at"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func stripRoadmapProvenance(fields map[string]any) {
	delete(fields, "roadmap_public_source")
	delete(fields, "roadmap_public_by")
	delete(fields, "roadmap_public_at")
}

// withoutInheritedRoadmapPublication drops a publication that rode along from
// another kind. Only a later person action on a ticket sets the keys again.
// Invalid JSON is left untouched so the caller can report it as a field block.
func withoutInheritedRoadmapPublication(raw json.RawMessage) (json.RawMessage, bool, error) {
	fields, err := decodeRouteFields(raw)
	if err != nil {
		return raw, false, nil
	}
	cleared := false
	for _, key := range []string{"roadmap_public", "roadmap_public_source", "roadmap_public_by", "roadmap_public_at"} {
		if _, ok := fields[key]; ok {
			delete(fields, key)
			cleared = true
		}
	}
	if !cleared {
		return raw, false, nil
	}
	stored, err := json.Marshal(fields)
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
}
