// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"math"
	"sort"
	"strings"
	"time"
)

type timingActor struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type timingVisit struct {
	Status     string       `json:"status"`
	From       time.Time    `json:"from"`
	To         *time.Time   `json:"to"`
	Seconds    float64      `json:"seconds"`
	MovedBy    *timingActor `json:"moved_by"`
	NextStatus *string      `json:"next_status"`
	EndedBy    *timingActor `json:"ended_by"`
	Release    *string      `json:"release"`
	Source     string       `json:"source"`
	Again      bool         `json:"again"`
	Ongoing    bool         `json:"ongoing"`
}

type ticketLifeline struct {
	NodeID               string        `json:"node_id"`
	GeneratedAt          time.Time     `json:"generated_at"`
	Visits               []timingVisit `json:"visits"`
	Complete             bool          `json:"complete"`
	Inferred             bool          `json:"inferred"`
	GapCount             int           `json:"gap_count"`
	InitialOffsetSeconds float64       `json:"initial_offset_seconds"`
}

type timingNode struct {
	ID, State, Kind, Priority string
	Created                   time.Time
	Bug                       bool
}

type timingEvent struct {
	ID            int64
	Node, Type    string
	At            time.Time
	Before, After string
	Actor         *timingActor
	Release       *string
}

func timingState(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_"), " ", "_")
	switch s {
	case "active", "inprogress":
		return "in_progress"
	case "canceled":
		return "cancelled"
	}
	return s
}

func timingTerminal(state string) bool {
	switch state {
	case "done", "delivered", "accepted", "cancelled", "archived":
		return true
	}
	return false
}

// Replay source snapshots independently of importer observation events. The
// latter record ingestion time, not the original status-change time.
func deriveLifeline(n timingNode, events []timingEvent, now time.Time) ticketLifeline {
	out := ticketLifeline{NodeID: n.ID, GeneratedAt: now, Visits: []timingVisit{}, Complete: true}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].ID < events[j].ID
		}
		return events[i].At.Before(events[j].At)
	})
	hasSource, hasCreation := false, false
	for _, e := range events {
		hasSource = hasSource || e.Type == "import.history"
		hasCreation = hasCreation || e.Type == "node.created"
	}
	seen := map[string]bool{}
	add := func(state string, at time.Time, actor *timingActor, release *string, source string) {
		state = timingState(state)
		out.Visits = append(out.Visits, timingVisit{Status: state, From: at, MovedBy: actor, Release: release, Source: source, Again: seen[state]})
		seen[state] = true
	}
	for _, e := range events {
		if e.After == "" || e.At.After(now) {
			continue
		}
		if strings.HasPrefix(e.Type, "import.") && e.Type != "import.history" && (hasSource || hasCreation) {
			continue
		}
		source := "event"
		if strings.HasPrefix(e.Type, "import.") {
			source = "imported"
		}
		if e.Type == "node.status_history_backfilled" {
			source = "inferred"
			e.Actor = nil // The recorder is not an historical mover.
		}
		if len(out.Visits) == 0 {
			initial := e.After
			if e.Type != "node.created" && e.Type != "import.history" && e.Before != "" {
				initial, source = e.Before, "inferred"
			}
			if e.Type == "import.node_created" || e.Type == "import.node_updated" || e.Type == "import.parent_changed" {
				source = "inferred"
				e.Actor = nil
			}
			if e.Type == "import.history" {
				out.InitialOffsetSeconds = math.Max(0, e.At.Sub(n.Created).Seconds())
				if out.InitialOffsetSeconds > 3 {
					source = "inferred"
				}
			}
			add(initial, n.Created, e.Actor, nil, source)
		}
		last := &out.Visits[len(out.Visits)-1]
		if e.Before != "" && timingState(e.Before) != last.Status {
			out.GapCount++
			last.Source = "inferred"
		}
		state := timingState(e.After)
		if state == last.Status {
			continue
		}
		if e.At.Before(last.From) {
			out.GapCount++
			continue
		}
		end := e.At
		last.To, last.NextStatus, last.EndedBy = &end, &state, e.Actor
		last.Seconds = end.Sub(last.From).Seconds()
		add(state, end, e.Actor, e.Release, source)
	}
	if len(out.Visits) == 0 {
		add(n.State, n.Created, nil, nil, "inferred")
	}
	last := &out.Visits[len(out.Visits)-1]
	if last.Status != timingState(n.State) {
		out.GapCount++
		last.Source = "inferred"
	}
	if timingTerminal(last.Status) {
		end := last.From
		last.To = &end
	} else {
		last.Ongoing = true
		last.Seconds = math.Max(0, now.Sub(last.From).Seconds())
	}
	for _, v := range out.Visits {
		out.Inferred = out.Inferred || v.Source == "inferred"
	}
	out.Complete = !out.Inferred && out.GapCount == 0
	return out
}

type timingSample struct {
	Step string
	End  time.Time
	Secs float64
}

// Completed work steps sum visits through the latest Done, so a reopen is
// counted once and its rework is retained. Skipped gates produce no samples.
func timingSamples(l ticketLifeline, final string) []timingSample {
	if !l.Complete || len(l.Visits) == 0 {
		return nil
	}
	out := []timingSample{}
	firstIP, lastDone, firstLive, firstAccepted := -1, -1, -1, -1
	for i, v := range l.Visits {
		switch v.Status {
		case "in_progress":
			if firstIP < 0 {
				firstIP = i
			}
		case "done":
			lastDone = i
		case "delivered":
			if firstLive < 0 {
				firstLive = i
			}
		case "accepted":
			if firstAccepted < 0 {
				firstAccepted = i
			}
		}
	}
	created := l.Visits[0].From
	if firstIP >= 0 {
		out = append(out, timingSample{"new_to_picked_up", l.Visits[firstIP].From, l.Visits[firstIP].From.Sub(created).Seconds()})
	}
	sum := func(status string, until int) (float64, bool) {
		var seconds float64
		found := false
		for _, v := range l.Visits[:until] {
			if v.Status == status {
				seconds += v.Seconds
				found = true
			}
		}
		return seconds, found
	}
	if lastDone >= 0 {
		for _, pair := range [][2]string{{"in_progress", "in_progress"}, {"qa", "qa_to_merged"}} {
			if secs, found := sum(pair[0], lastDone); found {
				out = append(out, timingSample{pair[1], l.Visits[lastDone].From, secs})
			}
		}
		if final == "done" {
			out = append(out, timingSample{"created_to_done", l.Visits[lastDone].From, l.Visits[lastDone].From.Sub(created).Seconds()})
		}
	}
	if firstLive >= 0 && final == "delivered" {
		out = append(out, timingSample{"created_to_live", l.Visits[firstLive].From, l.Visits[firstLive].From.Sub(created).Seconds()})
		if secs, found := sum("done", firstLive); found {
			out = append(out, timingSample{"done_to_live", l.Visits[firstLive].From, secs})
		}
	}
	if firstAccepted >= 0 && final == "delivered" {
		if secs, found := sum("delivered", firstAccepted); found {
			out = append(out, timingSample{"live_to_accepted", l.Visits[firstAccepted].From, secs})
		}
	}
	return out
}

type timingStep struct {
	Step    string   `json:"step"`
	N       int      `json:"n"`
	P50     *float64 `json:"p50"`
	P90     *float64 `json:"p90"`
	PrevN   int      `json:"prev_n"`
	PrevP50 *float64 `json:"prev_p50"`
	PrevP90 *float64 `json:"prev_p90"`
}

type timingCoverage struct {
	Tickets  int        `json:"tickets"`
	Complete int        `json:"complete"`
	Inferred int        `json:"inferred"`
	Gaps     int        `json:"gaps"`
	Since    *time.Time `json:"since"`
	Partial  bool       `json:"partial"`
}

type timingBucket struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	State string    `json:"state"`
	N     int       `json:"n"`
	P50   *float64  `json:"p50"`
	P90   *float64  `json:"p90"`
}

type ticketTiming struct {
	ProjectID   string         `json:"project_id"`
	GeneratedAt time.Time      `json:"generated_at"`
	Window      int            `json:"window"`
	FinalStatus string         `json:"final_status"`
	Steps       []timingStep   `json:"steps"`
	Rework      int            `json:"rework"`
	Coverage    timingCoverage `json:"coverage"`
	Buckets     []timingBucket `json:"buckets"`
}

type timingFilter struct {
	Days                     int
	Type, Priority, WorkedBy string
}

func timingQuantiles(values []float64) (*float64, *float64) {
	if len(values) == 0 {
		return nil, nil
	}
	sort.Float64s(values)
	p50 := values[int(math.Ceil(float64(len(values))*.5))-1]
	p90 := values[int(math.Ceil(float64(len(values))*.9))-1]
	return &p50, &p90
}

func aggregateTiming(project string, nodes []timingNode, histories map[string][]timingEvent, filter timingFilter, now time.Time) ticketTiming {
	out := ticketTiming{ProjectID: project, GeneratedAt: now, Window: filter.Days, FinalStatus: "done", Steps: []timingStep{}, Buckets: []timingBucket{}}
	lives := make(map[string]ticketLifeline, len(nodes))
	for _, n := range nodes {
		l := deriveLifeline(n, histories[n.ID], now)
		lives[n.ID] = l
		for _, v := range l.Visits {
			if l.Complete && (v.Status == "delivered" || v.Status == "accepted") && !v.From.Before(now.AddDate(0, 0, -90)) {
				out.FinalStatus = "delivered"
			}
		}
	}
	span := windowSpan(now, filter.Days, false)
	from := span.first
	previous := windowSpan(now, filter.Days, true).first
	samples := []timingSample{}
	for _, n := range nodes {
		l := lives[n.ID]
		if filter.Type != "" && filter.Type != n.Kind && !(n.Kind == "work" && (filter.Type == "bug" && n.Bug || filter.Type == "other" && !n.Bug)) {
			continue
		}
		if filter.Priority != "" && filter.Priority != n.Priority {
			continue
		}
		worker := ""
		for _, v := range l.Visits {
			if v.Status == "in_progress" {
				if v.MovedBy != nil {
					worker = v.MovedBy.Kind
				}
				break
			}
		}
		if filter.WorkedBy != "" && filter.WorkedBy != worker {
			continue
		}
		out.Coverage.Tickets++
		if l.Complete {
			out.Coverage.Complete++
			at := n.Created
			if out.Coverage.Since == nil || at.Before(*out.Coverage.Since) {
				out.Coverage.Since = &at
			}
		}
		if l.Inferred {
			out.Coverage.Inferred++
		}
		if l.GapCount > 0 {
			out.Coverage.Gaps++
		}
		samples = append(samples, timingSamples(l, out.FinalStatus)...)
		if l.Complete {
			for _, v := range l.Visits {
				if v.Status == "qa" && v.NextStatus != nil && *v.NextStatus == "in_progress" && v.To != nil && !v.To.Before(from) && v.To.Before(now) {
					out.Rework++
					break
				}
			}
		}
	}
	out.Coverage.Partial = out.Coverage.Complete != out.Coverage.Tickets || out.Coverage.Since == nil || from.Before(*out.Coverage.Since)
	stepNames := []string{"created_to_done", "new_to_picked_up", "in_progress", "qa_to_merged"}
	if out.FinalStatus == "delivered" {
		stepNames = []string{"created_to_live", "new_to_picked_up", "in_progress", "qa_to_merged", "done_to_live", "live_to_accepted"}
	}
	for _, name := range stepNames {
		current, prior := []float64{}, []float64{}
		for _, s := range samples {
			if s.Step != name {
				continue
			}
			if !s.End.Before(from) && s.End.Before(now) {
				current = append(current, s.Secs)
			}
			if !s.End.Before(previous) && s.End.Before(from) {
				prior = append(prior, s.Secs)
			}
		}
		a, b := timingQuantiles(current)
		c, d := timingQuantiles(prior)
		out.Steps = append(out.Steps, timingStep{name, len(current), a, b, len(prior), c, d})
	}
	// Use Delivery's existing UTC day/week/calendar-month boundaries. A
	// rolling clock-hour bucket would label yesterday's samples as today.
	for _, part := range span.buckets(bucketKind(filter.Days)) {
		start, end := part.first, part.end()
		if end.After(now) {
			end = now
		}
		bucket := timingBucket{From: start, To: end, State: "observed"}
		if out.Coverage.Since == nil || !end.After(*out.Coverage.Since) {
			bucket.State = "no_data"
		} else if start.Before(*out.Coverage.Since) || out.Coverage.Complete != out.Coverage.Tickets {
			bucket.State = "partial"
		}
		values := []float64{}
		for _, s := range samples {
			if s.Step == stepNames[0] && !s.End.Before(start) && s.End.Before(end) {
				values = append(values, s.Secs)
			}
		}
		bucket.N = len(values)
		bucket.P50, bucket.P90 = timingQuantiles(values)
		out.Buckets = append(out.Buckets, bucket)
	}
	return out
}
