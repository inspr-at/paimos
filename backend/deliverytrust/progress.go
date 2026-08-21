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
	"math/big"
)

type progressResult struct {
	percent   int
	source    *SourceAttribution
	backslide bool
}

// weightedProgress calculates an exact rational weighted value and floors only
// once, after N/A weights have been removed. It deliberately has no time input:
// elapsed wall-clock time can never advance delivery progress.
func weightedProgress(
	policy []StagePolicy,
	stages []StageInput,
	estimates []estimateAnalysis,
	completed bool,
) (progressResult, error) {
	if completed {
		return progressResult{percent: 100}, nil
	}

	totalWeight := int64(0)
	weighted := new(big.Rat)
	result := progressResult{}
	for i := range policy {
		if !policy[i].Required {
			continue
		}
		totalWeight += int64(policy[i].Weight)

		stageProgress := new(big.Rat)
		switch {
		case stages[i].Completion.Eligible:
			stageProgress.SetInt64(100)
		case estimates[i].maxProgress != nil:
			value := new(big.Rat).SetFloat64(*estimates[i].maxProgress)
			if value == nil {
				return progressResult{}, fmt.Errorf("%w: non-finite stage progress", ErrInvalidInput)
			}
			stageProgress.Set(value)
			if result.source == nil {
				result.source = estimates[i].progressAttribution
			}
			result.backslide = result.backslide || estimates[i].backslide
		}
		term := new(big.Rat).Mul(stageProgress, new(big.Rat).SetInt64(int64(policy[i].Weight)))
		weighted.Add(weighted, term)
	}
	if totalWeight <= 0 {
		return progressResult{}, fmt.Errorf("%w: no required stage weight", ErrInvalidInput)
	}
	weighted.Quo(weighted, new(big.Rat).SetInt64(totalWeight))
	percent := new(big.Int).Quo(weighted.Num(), weighted.Denom())
	if !percent.IsInt64() {
		return progressResult{}, fmt.Errorf("%w: progress overflow", ErrInvalidInput)
	}
	result.percent = int(percent.Int64())
	if result.percent > 99 {
		result.percent = 99
	}
	return result, nil
}
