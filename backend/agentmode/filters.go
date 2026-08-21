// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	deliveryKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	laneKeyPattern     = regexp.MustCompile(`^project:([1-9][0-9]*)/(?:epic:([1-9][0-9]*)|ungrouped)$`)
)

var allowedStates = map[string]bool{
	"pending": true, "active": true, "completed": true, "failed_needs_retry": true,
	"deployed_unverified": true, "cancelled": true, "unknown": true,
}

type Filters struct {
	ProjectID        *int64
	LaneKey          string
	States           []string
	Attention        string
	Health           string
	Query            string
	SelectedDelivery string
}

func ParseFilters(values url.Values) (Filters, error) {
	var out Filters
	allowedKeys := map[string]bool{"project_id": true, "lane_key": true, "state": true, "attention": true,
		"health": true, "q": true, "selected_delivery": true, "cursor": true}
	for key, entries := range values {
		if !allowedKeys[key] {
			return Filters{}, fmt.Errorf("%w: unsupported query parameter", ErrInvalid)
		}
		if key != "state" && len(entries) != 1 {
			return Filters{}, fmt.Errorf("%w: duplicate scalar query parameter", ErrInvalid)
		}
	}
	if raw := values.Get("project_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return Filters{}, fmt.Errorf("%w: project_id must be positive", ErrInvalid)
		}
		out.ProjectID = &id
	}
	if raw := values.Get("lane_key"); raw != "" {
		if !laneKeyPattern.MatchString(raw) {
			return Filters{}, fmt.Errorf("%w: invalid lane_key", ErrInvalid)
		}
		out.LaneKey = raw
	}
	seenState := map[string]bool{}
	for _, state := range values["state"] {
		if !allowedStates[state] {
			return Filters{}, fmt.Errorf("%w: unsupported state", ErrInvalid)
		}
		if !seenState[state] {
			seenState[state] = true
			out.States = append(out.States, state)
		}
	}
	sort.Strings(out.States)
	out.Attention = values.Get("attention")
	if out.Attention == "" {
		out.Attention = "all"
	}
	if out.Attention != "all" && out.Attention != "required" {
		return Filters{}, fmt.Errorf("%w: unsupported attention filter", ErrInvalid)
	}
	out.Health = values.Get("health")
	if out.Health == "" {
		out.Health = "all"
	}
	if out.Health != "all" && out.Health != "attention" && out.Health != "blocked" && out.Health != "stale" {
		return Filters{}, fmt.Errorf("%w: unsupported health filter", ErrInvalid)
	}
	out.Query = strings.TrimSpace(values.Get("q"))
	if err := validateSearch(out.Query); err != nil {
		return Filters{}, err
	}
	out.SelectedDelivery = strings.TrimSpace(values.Get("selected_delivery"))
	if out.SelectedDelivery != "" && !deliveryKeyPattern.MatchString(out.SelectedDelivery) {
		return Filters{}, fmt.Errorf("%w: invalid selected_delivery", ErrInvalid)
	}
	return out, nil
}

func validateSearch(query string) error {
	if !utf8.ValidString(query) || len([]byte(query)) > MaxSearchBytes {
		return fmt.Errorf("%w: q is not bounded UTF-8", ErrInvalid)
	}
	for _, r := range query {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: q contains control characters", ErrInvalid)
		}
	}
	return nil
}

// CanonicalFingerprint binds every result-shaping filter and deliberately
// excludes SelectedDelivery, which is a lookup hint rather than membership.
func (f Filters) CanonicalFingerprint() [32]byte {
	var b strings.Builder
	b.WriteString("agent-mode-filters-v1\x00")
	if f.ProjectID != nil {
		b.WriteString(strconv.FormatInt(*f.ProjectID, 10))
	}
	b.WriteByte(0)
	b.WriteString(f.LaneKey)
	b.WriteByte(0)
	states := append([]string(nil), f.States...)
	sort.Strings(states)
	for _, state := range states {
		b.WriteString(state)
		b.WriteByte(0)
	}
	b.WriteString(f.Attention)
	b.WriteByte(0)
	b.WriteString(f.Health)
	b.WriteByte(0)
	b.WriteString(strings.ToLower(strings.TrimSpace(f.Query)))
	return sha256.Sum256([]byte(b.String()))
}

func RouteFingerprint(projectID *int64, detailDelivery string) [32]byte {
	var b strings.Builder
	b.WriteString("agent-mode-route-v1\x00")
	if projectID == nil {
		b.WriteString("all")
	} else {
		b.WriteString("project:")
		b.WriteString(strconv.FormatInt(*projectID, 10))
	}
	b.WriteByte(0)
	if detailDelivery != "" {
		b.WriteString("detail:")
		b.WriteString(detailDelivery)
	}
	return sha256.Sum256([]byte(b.String()))
}

func requestFingerprints(request Request) ([32]byte, [32]byte, error) {
	normalizedFilters := request.Filters
	if request.RouteProjectID != nil {
		if request.Filters.ProjectID != nil && *request.RouteProjectID != *request.Filters.ProjectID {
			return [32]byte{}, [32]byte{}, fmt.Errorf("%w: route and filter project differ", ErrInvalid)
		}
		// A project route is an authorization/audience boundary. A redundant
		// same-project query value does not create another result-filter scope.
		normalizedFilters.ProjectID = nil
	}
	// Detail is a selection/lookup surface, not a distinct event scope. The
	// sole canonical events endpoint shares its global result-filter binding,
	// just as selected_delivery is excluded from the filter digest.
	return RouteFingerprint(request.RouteProjectID, ""), normalizedFilters.CanonicalFingerprint(), nil
}

func permissionFingerprint(userID, epoch int64, basis string) ([32]byte, error) {
	if userID <= 0 || epoch < 0 {
		return [32]byte{}, fmt.Errorf("%w: invalid permission identity", ErrInvariant)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("agent-mode-permissions-v1\x00"))
	var buf [16]byte
	if written, err := binary.Encode(buf[:], binary.BigEndian, [2]int64{userID, epoch}); err != nil || written != len(buf) {
		return [32]byte{}, fmt.Errorf("%w: permission identity encoding", ErrInvariant)
	}
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(basis))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func (f Filters) matches(row DeliveryRow) bool {
	if f.ProjectID != nil && row.ProjectID != *f.ProjectID {
		return false
	}
	if f.LaneKey != "" && row.LaneKey != f.LaneKey {
		return false
	}
	if len(f.States) > 0 {
		matched := false
		for _, state := range f.States {
			matched = matched || row.state == state
		}
		if !matched {
			return false
		}
	}
	if f.Attention == "required" && row.Attention.Level == 0 {
		return false
	}
	switch f.Health {
	case "attention":
		if row.Attention.Level == 0 {
			return false
		}
	case "blocked":
		if row.attentionFlags.Blocked == 0 {
			return false
		}
	case "stale":
		if row.attentionFlags.StaleNoSignal == 0 {
			return false
		}
	}
	if f.Query != "" {
		needle := strings.ToLower(f.Query)
		allowlist := []string{row.IssueKey, row.Title, row.ProjectKey, row.ProjectName, row.Activity.Text, row.StatusText}
		allowlist = append(allowlist, row.Tags...)
		matched := false
		for _, value := range allowlist {
			if strings.Contains(strings.ToLower(value), needle) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
