// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package deliverytrust

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var opaquePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|credential)\s*[:=]`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

func validOpaque(value string) bool { return opaquePattern.MatchString(value) }

func validStage(stage StageKey) bool {
	for _, candidate := range canonicalStages {
		if stage == candidate {
			return true
		}
	}
	return false
}

func validateSafeBasis(value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || len([]byte(value)) > 240 || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: estimate basis is not bounded single-line UTF-8", ErrInvalidInput)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: estimate basis contains control characters", ErrInvalidInput)
		}
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return fmt.Errorf("%w: estimate basis contains secret-like text", ErrInvalidInput)
		}
	}
	return nil
}

func scopeMatches(actual, expected Scope) bool { return actual == expected }

func validateScope(scope Scope, reporter ReporterKind, started bool) error {
	switch reporter {
	case ReporterAgentRun, ReporterExternal, ReporterUser, ReporterSystem, ReporterUnknown:
	default:
		return fmt.Errorf("%w: invalid reporter kind", ErrInvalidInput)
	}
	for _, identity := range []string{scope.AttemptID, scope.PlanID} {
		if !validOpaque(identity) {
			return fmt.Errorf("%w: invalid attempt or plan identity", ErrInvalidInput)
		}
	}
	if !started {
		if reporter == ReporterAgentRun || reporter == ReporterExternal {
			return fmt.Errorf("%w: eligible reporter lacks execution authority", ErrInvalidInput)
		}
		if scope.ExecutionID != "" || scope.AuthorityID != "" || scope.ResetID != "" || scope.ReporterID != "" || scope.RunLinkID != "" {
			return fmt.Errorf("%w: unstarted stage carries execution authority", ErrInvalidInput)
		}
		return nil
	}
	for _, identity := range []string{scope.ExecutionID, scope.AuthorityID, scope.ResetID, scope.ReporterID} {
		if !validOpaque(identity) {
			return fmt.Errorf("%w: invalid execution authority identity", ErrInvalidInput)
		}
	}
	switch reporter {
	case ReporterAgentRun:
		if !validOpaque(scope.RunLinkID) {
			return fmt.Errorf("%w: current agent authority lacks run link", ErrInvalidInput)
		}
	case ReporterExternal:
		if scope.RunLinkID != "" {
			return fmt.Errorf("%w: external authority carries a run link", ErrInvalidInput)
		}
	case ReporterUser, ReporterSystem, ReporterUnknown:
		if scope.RunLinkID != "" {
			return fmt.Errorf("%w: ineligible reporter carries a run link", ErrInvalidInput)
		}
	}
	return nil
}

func publicScope(scope Scope) *PublicScope {
	if scope.AttemptID == "" || scope.PlanID == "" {
		return nil
	}
	return &PublicScope{
		AttemptID: scope.AttemptID, PlanID: scope.PlanID,
		ExecutionID: scope.ExecutionID, AuthorityID: scope.AuthorityID, ResetID: scope.ResetID,
	}
}

func checkedAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func saturatingSubtractToZero(value, subtract int64) int64 {
	if subtract <= 0 {
		return value
	}
	if subtract >= value {
		return 0
	}
	return value - subtract
}

func nonnegativeSecondsBetween(start, end time.Time) int64 {
	if !end.After(start) {
		return 0
	}
	a, b := start.Unix(), end.Unix()
	if a < 0 && b > math.MaxInt64+a {
		return math.MaxInt64
	}
	seconds := b - a
	if end.Nanosecond() < start.Nanosecond() {
		seconds--
	}
	if seconds < 0 {
		return 0
	}
	return seconds
}

func addSeconds(base time.Time, seconds int64) (time.Time, bool) {
	unix, ok := checkedAdd(base.Unix(), seconds)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(unix, int64(base.Nanosecond())).UTC(), true
}

func appendUniqueFlag(flags []Flag, candidate Flag) []Flag {
	for _, flag := range flags {
		if flag == candidate {
			return flags
		}
	}
	return append(flags, candidate)
}

func minimumFuture(now time.Time, values ...time.Time) *time.Time {
	var selected *time.Time
	for _, value := range values {
		if value.IsZero() || !value.After(now) {
			continue
		}
		value = value.UTC()
		if selected == nil || value.Before(*selected) {
			copy := value
			selected = &copy
		}
	}
	return selected
}
