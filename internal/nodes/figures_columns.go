// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type figureNumbers struct {
	Count   int      `json:"count"`
	Total   float64  `json:"total"`
	Median  *float64 `json:"median"`
	Average *float64 `json:"average"`
}

type figureValue struct {
	Value         string   `json:"value"`
	Kind          string   `json:"kind,omitempty"`
	Count         int      `json:"count"`
	Hours         float64  `json:"hours"`
	MeasuredCost  *float64 `json:"measured_cost,omitempty"`
	EstimatedCost *float64 `json:"estimated_cost,omitempty"`
}

// Typical displayed values can include estimated rows, but expose no mixed
// total. Measured and estimated totals remain separate cohorts.
type figureTypical struct {
	Count       int      `json:"count"`
	Median      *float64 `json:"median"`
	Average     *float64 `json:"average"`
	Approximate bool     `json:"approximate"`
}

type figureFinish struct {
	At  time.Time `json:"at"`
	Key string    `json:"key"`
}

type columnFigure struct {
	Rows             int            `json:"rows"`
	Included         int            `json:"included"`
	WithoutValue     int            `json:"without_value"`
	Others           int            `json:"others"`
	Partial          bool           `json:"partial"`
	Values           []*figureValue `json:"values,omitempty"`
	Numbers          *figureNumbers `json:"numbers,omitempty"`
	Measured         *figureNumbers `json:"measured,omitempty"`
	Estimated        *figureNumbers `json:"estimated,omitempty"`
	Typical          *figureTypical `json:"typical,omitempty"`
	WeightedPercent  *float64       `json:"weighted_percent,omitempty"`
	ReadingCount     *int           `json:"reading_count,omitempty"`
	From             *time.Time     `json:"from,omitempty"`
	To               *time.Time     `json:"to,omitempty"`
	MedianAgeSeconds *float64       `json:"median_age_seconds,omitempty"`
	Stale            *int           `json:"stale,omitempty"`
	Running          *int           `json:"running,omitempty"`
	NextFinish       *figureFinish  `json:"next_finish,omitempty"`
	LastFinish       *figureFinish  `json:"last_finish,omitempty"`
	Parents          *int           `json:"parents,omitempty"`
	Leaves           *int           `json:"leaves,omitempty"`
	FirstKey         string         `json:"first_key,omitempty"`
	LastKey          string         `json:"last_key,omitempty"`
}

func figureStats(values []float64) *figureNumbers {
	out := &figureNumbers{Count: len(values)}
	if len(values) == 0 {
		return out
	}
	values = slices.Clone(values)
	sort.Float64s(values)
	for _, v := range values {
		out.Total += v
	}
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + median) / 2
	}
	average := out.Total / float64(len(values))
	out.Median, out.Average = &median, &average
	return out
}

func figureLabel(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, key := range []string{"name", "label", "version"} {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func figureCost(v *string) *float64 {
	if v == nil {
		return nil
	}
	n, err := strconv.ParseFloat(*v, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return nil
	}
	return &n
}

func fillFigureColumns(out *projectFigures, rows []figureRow, costVisible bool, now time.Time) {
	// Accumulate all values before taking top N. The tail is represented by
	// Others; missing values have their own count and never enter that tail.
	counts := map[string]map[string]*figureValue{}
	for _, name := range []string{"status", "priority", "assignee", "epic", "release", "suggested_release", "tag", "cost_unit", "model", "estimate", "tokens", "eta_ready", "progress", "key", "created_at", "updated_at"} {
		out.Columns[name] = &columnFigure{}
		counts[name] = map[string]*figureValue{}
	}
	if costVisible {
		out.Columns["cost"] = &columnFigure{}
	}
	var hours, progress, agesCreated, agesUpdated, tokensMeasured, tokensEstimated, costMeasured, costEstimated []float64
	var weighted, weight float64
	staleCreated, staleUpdated, running := 0, 0, 0
	add := func(name, label string, row *figureRow, costMeasured, costEstimated *float64) {
		if label == "" || label == "none" {
			out.Columns[name].WithoutValue++
			return
		}
		v := counts[name][label]
		if v == nil {
			v = &figureValue{Value: label}
			if name == "assignee" {
				v.Kind = row.assigneeKind
			}
			counts[name][label] = v
		}
		v.Count++
		if row.hours != nil && row.bucket != "cancelled" && row.bucket != "archived" {
			v.Hours += *row.hours
		}
		if costMeasured != nil {
			if v.MeasuredCost == nil {
				v.MeasuredCost = new(float64)
			}
			*v.MeasuredCost += *costMeasured
		}
		if costEstimated != nil {
			if v.EstimatedCost == nil {
				v.EstimatedCost = new(float64)
			}
			*v.EstimatedCost += *costEstimated
		}
	}
	for i := range rows {
		row := &rows[i]
		for _, name := range []string{"key", "created_at", "updated_at"} {
			out.Columns[name].Rows++
			out.Columns[name].Included++
		}
		if i == 0 {
			out.Columns["key"].FirstKey = row.item.Key
		}
		out.Columns["key"].LastKey = row.item.Key
		for _, spec := range []struct {
			name string
			at   time.Time
		}{{"created_at", row.item.CreatedAt}, {"updated_at", row.item.UpdatedAt}} {
			c := out.Columns[spec.name]
			at := spec.at
			if c.From == nil || at.Before(*c.From) {
				c.From = &at
			}
			if c.To == nil || at.After(*c.To) {
				c.To = &at
			}
		}
		agesCreated = append(agesCreated, max(0, now.Sub(row.item.CreatedAt).Seconds()))
		agesUpdated = append(agesUpdated, max(0, now.Sub(row.item.UpdatedAt).Seconds()))
		open := row.bucket == "open" || row.bucket == "in_progress"
		if open && !row.item.CreatedAt.After(now.AddDate(0, 0, -14)) {
			staleCreated++
		}
		if open && !row.item.UpdatedAt.After(now.AddDate(0, 0, -7)) {
			staleUpdated++
		}
		if !row.leaf {
			continue
		}
		out.Scope.Leaves++
		closed := row.bucket == "cancelled" || row.bucket == "archived"
		if closed {
			out.Scope.Closed++
		}
		for name, c := range out.Columns {
			if name == "key" || name == "created_at" || name == "updated_at" {
				continue
			}
			c.Rows++
			if (name == "estimate" || name == "progress") && closed {
				continue
			}
			c.Included++
		}
		if !closed {
			group := "open"
			switch row.bucket {
			case "done":
				group = "done_not_live"
				if row.status == "delivered" || row.status == "accepted" || !out.MarksReleases {
					group = "completed"
				}
			case "in_progress":
				group = "in_progress"
			}
			g := out.Groups[group]
			g.Tickets++
			g.Statuses[row.status]++
			if row.hours == nil {
				out.WithoutEstimate++
			} else {
				g.Hours += *row.hours
				out.TotalHours += *row.hours
				hours = append(hours, *row.hours)
				if open {
					out.OpenEstimatedLeaves++
				}
			}
			if row.eta != nil && row.eta.Progress != nil {
				pct := float64(*row.eta.Progress)
				progress = append(progress, pct)
				if row.hours != nil {
					weighted += pct * *row.hours
					weight += *row.hours
				}
			}
		}
		add("status", row.status, row, nil, nil)
		add("priority", figureLabel(row.labels["priority"]), row, nil, nil)
		add("assignee", row.assignee, row, nil, nil)
		add("epic", row.ep, row, nil, nil)
		add("release", figureLabel(row.labels["release"]), row, nil, nil)
		add("suggested_release", figureLabel(row.labels["suggested_release"]), row, nil, nil)
		tags, _ := row.labels["tags"].([]any)
		seenTags := map[string]bool{}
		for _, tag := range tags {
			label := figureLabel(tag)
			if label != "" && !seenTags[label] {
				add("tag", label, row, nil, nil)
				seenTags[label] = true
			}
		}
		if len(seenTags) == 0 {
			out.Columns["tag"].WithoutValue++
		}
		var measured, estimated *float64
		model := ""
		if p := row.plan; p != nil {
			if p.Tokens.Spent != nil {
				tokensMeasured = append(tokensMeasured, float64(*p.Tokens.Spent))
			} else if p.Tokens.Estimated != nil {
				tokensEstimated = append(tokensEstimated, float64(*p.Tokens.Estimated))
			}
			if p.Tokens.Spent == nil && p.Tokens.Estimated == nil {
				out.Columns["tokens"].WithoutValue++
			}
			out.Columns["tokens"].Partial = out.Columns["tokens"].Partial || p.Tokens.Unreported > 0 || p.RouteTruncated
			if len(p.Models) > 0 {
				model = p.Models[0].Model
			} else if p.Snapshot != nil {
				if p.Snapshot.Route != nil {
					model = p.Snapshot.Route.Model
				}
			} else if p.Route != nil {
				model = p.Route.Model
			}
			if costVisible && p.Cost != nil {
				measured, estimated = figureCost(p.Cost.ListSpent), figureCost(p.Cost.ListEstimated)
				if measured != nil {
					estimated = nil
				}
				out.Columns["cost"].Partial = out.Columns["cost"].Partial || p.Cost.ListUnpriced || p.Tokens.Unreported > 0 || p.RouteTruncated
			}
		} else {
			out.Columns["tokens"].WithoutValue++
		}
		add("model", model, row, nil, nil)
		if costVisible {
			if measured != nil {
				costMeasured = append(costMeasured, *measured)
			}
			if estimated != nil {
				costEstimated = append(costEstimated, *estimated)
			}
			if measured == nil && estimated == nil {
				out.Columns["cost"].WithoutValue++
			}
		}
		unit := figureLabel(row.labels["cost_unit"])
		if unit == "" {
			if legacy, ok := row.labels["classic"].(map[string]any); ok {
				unit = figureLabel(legacy["cost_unit"])
			}
		}
		add("cost_unit", unit, row, measured, estimated)
		if row.eta != nil {
			c := out.Columns["eta_ready"]
			if row.eta.Running {
				running++
			}
			if row.eta.ReadyAt != nil {
				at := *row.eta.ReadyAt
				if c.NextFinish == nil || at.Before(c.NextFinish.At) {
					c.NextFinish = &figureFinish{at, row.item.Key}
				}
				if c.LastFinish == nil || at.After(c.LastFinish.At) {
					c.LastFinish = &figureFinish{at, row.item.Key}
				}
			} else {
				c.WithoutValue++
			}
		}
	}
	out.Scope.Parents = out.Scope.Rows - out.Scope.Leaves
	out.Approximate = out.TotalHours > 0
	out.Columns["key"].Parents, out.Columns["key"].Leaves = &out.Scope.Parents, &out.Scope.Leaves
	out.Columns["estimate"].Numbers = figureStats(hours)
	out.Columns["estimate"].WithoutValue = out.WithoutEstimate
	out.Columns["tokens"].Measured, out.Columns["tokens"].Estimated = figureStats(tokensMeasured), figureStats(tokensEstimated)
	typical := func(measured, estimated []float64) *figureTypical {
		s := figureStats(append(slices.Clone(measured), estimated...))
		return &figureTypical{s.Count, s.Median, s.Average, len(estimated) > 0}
	}
	out.Columns["tokens"].Typical = typical(tokensMeasured, tokensEstimated)
	if costVisible {
		out.Columns["cost"].Measured, out.Columns["cost"].Estimated = figureStats(costMeasured), figureStats(costEstimated)
		out.Columns["cost"].Typical = typical(costMeasured, costEstimated)
		out.Columns["cost_unit"].Partial = out.Columns["cost"].Partial
	}
	p := out.Columns["progress"]
	p.Numbers = figureStats(progress)
	p.ReadingCount = &p.Numbers.Count
	p.WithoutValue = p.Included - p.Numbers.Count
	if weight > 0 {
		pct := weighted / weight
		p.WeightedPercent = &pct
	}
	out.Columns["created_at"].MedianAgeSeconds = figureStats(agesCreated).Median
	out.Columns["updated_at"].MedianAgeSeconds = figureStats(agesUpdated).Median
	out.Columns["created_at"].Stale, out.Columns["updated_at"].Stale = &staleCreated, &staleUpdated
	out.Columns["eta_ready"].Running = &running
	for name, values := range counts {
		c := out.Columns[name]
		for _, value := range values {
			c.Values = append(c.Values, value)
		}
		sort.Slice(c.Values, func(i, j int) bool {
			if c.Values[i].Count != c.Values[j].Count {
				return c.Values[i].Count > c.Values[j].Count
			}
			return c.Values[i].Value < c.Values[j].Value
		})
		top := 0
		switch name {
		case "assignee", "model", "cost_unit":
			top = 3
		case "epic", "release", "suggested_release":
			top = 2
		case "tag":
			top = 5
		}
		if top > 0 && len(c.Values) > top {
			for _, v := range c.Values[top:] {
				c.Others += v.Count
			}
			c.Values = c.Values[:top]
		}
	}
}
