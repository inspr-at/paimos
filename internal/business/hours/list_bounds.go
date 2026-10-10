// SPDX-License-Identifier: AGPL-3.0-only

package hours

import (
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
)

type listPage[T any] struct {
	items []T
	next  string
}

func listBounds(r *http.Request) (int, string, *time.Time, *time.Time, error) {
	q := r.URL.Query()
	limit := 50
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	after := q.Get("after_id")
	if err != nil || limit < 1 || limit > 100 || (after != "" && !workorders.UUID(after)) {
		return 0, "", nil, nil, fail(400, "invalid list limit or after_id")
	}
	var dates [2]*time.Time
	for i, name := range []string{"since", "until"} {
		if raw := q.Get(name); raw != "" {
			v, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil {
				return 0, "", nil, nil, fail(400, "invalid date filter")
			}
			dates[i] = &v
		}
	}
	if dates[0] != nil && dates[1] != nil && !dates[1].After(*dates[0]) {
		return 0, "", nil, nil, fail(400, "until must follow since")
	}
	for _, name := range []string{"limit", "after_id", "since", "until"} {
		if len(q[name]) > 1 {
			return 0, "", nil, nil, fail(400, "repeated list selector")
		}
	}
	return limit, after, dates[0], dates[1], nil
}
