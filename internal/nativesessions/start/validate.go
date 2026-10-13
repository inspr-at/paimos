// SPDX-License-Identifier: AGPL-3.0-only

package start

import (
	"encoding/json"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

func validate(v *nativecontract.Validator, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	return v.ValidateJSON(name, raw)
}
