// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/tenant"
)

func parseHumanCheck(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil, badRequest("human_check must be text or null")
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 500 {
		return nil, badRequest("human_check needs 1 to 500 characters")
	}
	return &text, nil
}

// Completion metadata is always authored by the server. Field replacement must
// preserve it and cannot turn an agent's assertion into a person's check.
func humanCheckFields(p tenant.Principal, fields, old json.RawMessage, before, after *string, changed bool) (json.RawMessage, error) {
	var next, previous map[string]json.RawMessage
	if err := json.Unmarshal(fields, &next); err != nil {
		return nil, err
	}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &previous); err != nil {
			return nil, err
		}
	}
	if next == nil {
		next = map[string]json.RawMessage{}
	}
	supplied := next["human_check_completed"]
	stored := previous["human_check_completed"]
	if len(supplied) > 0 && !bytes.Equal(supplied, []byte("null")) {
		var a, b any
		_ = json.Unmarshal(supplied, &a)
		_ = json.Unmarshal(stored, &b)
		if !reflectJSONEqual(a, b) {
			return nil, badRequest("human_check_completed is server-written")
		}
	}
	if len(stored) > 0 {
		next["human_check_completed"] = stored
	} else {
		delete(next, "human_check_completed")
	}
	if changed {
		if before != nil && after == nil {
			if p.Kind != tenant.Person {
				return nil, &httpError{status: 403, msg: "only a person can mark a human check checked"}
			}
			value, err := json.Marshal(map[string]string{"text": *before, "by": p.ID, "at": time.Now().UTC().Format(time.RFC3339Nano)})
			if err != nil {
				return nil, err
			}
			next["human_check_completed"] = value
		} else if after != nil {
			delete(next, "human_check_completed")
		}
	}
	return json.Marshal(next)
}

func reflectJSONEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

const pendingHumanCheckSQL = `(n.human_check IS NOT NULL AND btrim(n.human_check)<>'')`
