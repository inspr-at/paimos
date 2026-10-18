// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestActivityCursorBindsFiltersAndBoundsInput(t *testing.T) {
	values := url.Values{"view": {"automatic"}, "rule": {"progress"}, "q": {"literal %_ search"}, "limit": {"1"}}
	q, err := parseActivityQuery(values)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 4, 12, 0, 0, 123456000, time.UTC)
	values.Set("cursor", q.cursor(activityItem{EventID: 42, At: at}))
	got, err := parseActivityQuery(values)
	if err != nil || got.at == nil || !got.at.Equal(at) || got.id != 42 {
		t.Fatalf("cursor did not round-trip: %+v %v", got, err)
	}
	for _, change := range []struct{ key, value string }{{"view", "people"}, {"rule", "accept"}, {"q", "other"}, {"project_id", "11111111-1111-4111-8111-111111111111"}} {
		copy := url.Values{}
		for key, value := range values {
			copy[key] = append([]string(nil), value...)
		}
		copy.Set(change.key, change.value)
		if _, err := parseActivityQuery(copy); err == nil {
			t.Fatalf("cursor accepted different %s", change.key)
		}
	}
	for _, query := range []string{"view=unknown", "rule=unknown", "project_id=bad", "limit=0", "limit=101", "limit=", "cursor=", "cursor=bad", "q=" + strings.Repeat("x", 201), "q=" + url.QueryEscape(strings.Repeat("ü", 101)), "cursor=" + strings.Repeat("x", 257)} {
		v, _ := url.ParseQuery(query)
		if _, err := parseActivityQuery(v); err == nil {
			t.Fatalf("accepted unbounded/invalid input %s", query[:min(len(query), 40)])
		}
	}
}
