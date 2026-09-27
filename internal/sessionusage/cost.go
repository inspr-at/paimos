// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func validateCostFields(fields map[string]json.RawMessage) error {
	if fields == nil {
		return nil
	}
	for _, key := range []string{"cost_usd_total", "total_cost_usd", "cost_usd"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if _, ok := vendorMicros(raw); !ok {
			return fmt.Errorf("%w: vendor cost", ErrMalformed)
		}
	}
	raw, ok := fields["cost"]
	if !ok {
		return nil
	}
	return validateCostObject(raw)
}

func validateCostObject(raw json.RawMessage) error {
	fields, err := decodeObject(raw, allow("amount", "currency"))
	if err != nil {
		return err
	}
	currency, err := parseString(fields["currency"])
	if err != nil || currency != "USD" {
		return fmt.Errorf("%w: cost currency", ErrMalformed)
	}
	if _, ok := vendorMicros(fields["amount"]); !ok {
		return fmt.Errorf("%w: vendor cost", ErrMalformed)
	}
	return nil
}

func hasUsageSignal(fields map[string]json.RawMessage) bool {
	return signalWalk(fields, 0)
}

func signalWalk(fields map[string]json.RawMessage, depth int) bool {
	for key, raw := range fields {
		if usageSignal[key] {
			return true
		}
		raw = bytes.TrimSpace(raw)
		if depth >= 3 || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		nested, err := decodeObject(raw, nil)
		if err != nil || signalWalk(nested, depth+1) {
			return true
		}
	}
	return false
}

var usageSignal = map[string]bool{
	"input_tokens": true, "output_tokens": true, "cached_input_tokens": true,
	"inputTokens": true, "outputTokens": true, "cachedInputTokens": true,
	"cacheReadTokens": true, "cacheWriteTokens": true,
	"cache_read_tokens": true, "cache_write_tokens": true,
	"tokenUsage": true, "total_token_usage": true, "last_token_usage": true,
	"usage": true, "cost_usd_total": true, "total_cost_usd": true, "cost_usd": true,
}
