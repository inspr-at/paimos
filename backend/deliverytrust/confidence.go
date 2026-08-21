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

import "math"

func classifySourceConfidence(value float64) (ConfidenceLabel, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 1 {
		return ConfidenceUnknown, false
	}
	switch {
	case value < 0.5:
		return ConfidenceLow, true
	case value < 0.8:
		return ConfidenceMedium, true
	default:
		return ConfidenceHigh, true
	}
}

func historyConfidence(inliers int) (float64, ConfidenceLabel) {
	switch {
	case inliers >= 30:
		return 0.90, ConfidenceHigh
	case inliers >= 10:
		return 0.65, ConfidenceMedium
	case inliers >= 5:
		return 0.25, ConfidenceLow
	default:
		return 0, ConfidenceUnknown
	}
}

func downgradeConfidence(value float64, label ConfidenceLabel) (float64, ConfidenceLabel) {
	switch label {
	case ConfidenceHigh:
		return math.Min(value, 0.65), ConfidenceMedium
	case ConfidenceMedium:
		return math.Min(value, 0.25), ConfidenceLow
	case ConfidenceLow:
		return 0, ConfidenceUnknown
	default:
		return 0, ConfidenceUnknown
	}
}

func weakerConfidence(aValue float64, aLabel ConfidenceLabel, bValue float64, bLabel ConfidenceLabel) (float64, ConfidenceLabel) {
	if confidenceRank(aLabel) < confidenceRank(bLabel) {
		return aValue, aLabel
	}
	if confidenceRank(bLabel) < confidenceRank(aLabel) {
		return bValue, bLabel
	}
	if aValue <= bValue {
		return aValue, aLabel
	}
	return bValue, bLabel
}

func confidenceRank(label ConfidenceLabel) int {
	switch label {
	case ConfidenceLow:
		return 1
	case ConfidenceMedium:
		return 2
	case ConfidenceHigh:
		return 3
	default:
		return 0
	}
}
