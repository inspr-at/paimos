// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"encoding/json"
	"testing"
)

func TestExactAPINodeChangesCompareNumbersExactly(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		changed       bool
	}{
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`, true},
		{`{"nested":[{"n":9007199254740992}]}`, `{"nested":[{"n":9007199254740993}]}`, true},
		{`{"n":1,"other":2}`, `{"other":2.0,"n":1e0}`, false},
		{`{"n":1e1000000}`, `{"n":10e999999}`, false},
	} {
		before := objectOf(nodeSnapshot(`"fields":` + tc.before))
		after := objectOf(nodeSnapshot(`"fields":` + tc.after))
		change, ok := nodeChangeOf(before, after)
		if ok != tc.changed {
			t.Fatalf("%s -> %s: %+v, changed=%t", tc.before, tc.after, change, ok)
		}
		if tc.changed && len(change.Fields) != 1 {
			t.Fatalf("wrong fields: %+v", change)
		}
	}
	if sameJSON(json.RawMessage(`9007199254740992`), json.RawMessage(`9007199254740993`)) {
		t.Fatal("large numbers compare equal")
	}
}
