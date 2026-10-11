// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelreport"
)

var errCatalogProtocol = errors.New("harness catalog protocol unavailable")

func catalogFact(m *modelreport.CatalogModel, name, path string, value any) {
	raw, _ := json.Marshal(value)
	m.Facts[name] = modelreport.CatalogField{Value: raw, Path: path}
}
func catalogCopy(m *modelreport.CatalogModel, row map[string]json.RawMessage, name, path, key string) {
	if raw, ok := row[key]; ok {
		m.Facts[name] = modelreport.CatalogField{Value: raw, Path: path}
	}
}

func parseCodexCatalog(raw []byte) ([]modelreport.CatalogModel, string, error) {
	var body struct {
		Data []map[string]json.RawMessage `json:"data"`
		Next string                       `json:"nextCursor"`
	}
	if len(raw) > modelreport.CatalogBytesMax || decodeProbeJSON(raw, &body) != nil || body.Data == nil || len(body.Data) > 100 || len(body.Next) > 512 {
		return nil, "", errCatalogProtocol
	}
	out := []modelreport.CatalogModel{}
	for _, row := range body.Data {
		m := modelreport.CatalogModel{Facts: map[string]modelreport.CatalogField{}}
		if json.Unmarshal(row["model"], &m.Model) != nil {
			return nil, "", errCatalogProtocol
		}
		for name, key := range map[string]string{"display_name": "displayName", "default_level": "defaultReasoningEffort", "hidden": "hidden", "successor": "upgrade"} {
			catalogCopy(&m, row, name, key, key)
		}
		if levels, ok := row["supportedReasoningEfforts"]; ok && string(levels) != "null" {
			var options []struct {
				Effort string `json:"reasoningEffort"`
			}
			if json.Unmarshal(levels, &options) != nil || options == nil || len(options) > 32 {
				return nil, "", errCatalogProtocol
			}
			names := []string{}
			for _, o := range options {
				names = append(names, o.Effort)
			}
			catalogFact(&m, "native_levels", "supportedReasoningEfforts[].reasoningEffort", names)
		}
		if value, ok := row["upgradeInfo"]; ok && string(value) != "null" {
			var info map[string]json.RawMessage
			if json.Unmarshal(value, &info) != nil {
				return nil, "", errCatalogProtocol
			}
			if _, ok := m.Facts["successor"]; !ok || string(m.Facts["successor"].Value) == "null" {
				catalogCopy(&m, info, "successor", "upgradeInfo.model", "model")
			}
			if at, ok := info["retirementAt"]; ok && string(at) != "null" {
				var epoch int64
				if json.Unmarshal(at, &epoch) == nil {
					if epoch < 0 || epoch > 253402300799 {
						return nil, "", errCatalogProtocol
					}
					catalogFact(&m, "retirement_at", "upgradeInfo.retirementAt", time.Unix(epoch, 0).UTC().Format(time.RFC3339))
				} else {
					catalogCopy(&m, info, "retirement_at", "upgradeInfo.retirementAt", "retirementAt")
				}
			}
		}
		out = append(out, m)
	}
	if modelreport.ValidateCatalog("fresh", out) != nil {
		return nil, "", errCatalogProtocol
	}
	return out, body.Next, nil
}

func parseClaudeCatalog(raw []byte) ([]modelreport.CatalogModel, error) {
	var rows []map[string]json.RawMessage
	if len(raw) > modelreport.CatalogBytesMax || decodeProbeJSON(raw, &rows) != nil || rows == nil || len(rows) > modelreport.CatalogModelsMax {
		return nil, errCatalogProtocol
	}
	out := []modelreport.CatalogModel{}
	for _, row := range rows {
		m := modelreport.CatalogModel{Facts: map[string]modelreport.CatalogField{}}
		if json.Unmarshal(row["value"], &m.Model) != nil {
			return nil, errCatalogProtocol
		}
		for name, key := range map[string]string{"display_name": "displayName", "native_levels": "supportedEffortLevels"} {
			catalogCopy(&m, row, name, key, key)
		}
		out = append(out, m)
	}
	if modelreport.ValidateCatalog("fresh", out) != nil {
		return nil, errCatalogProtocol
	}
	return out, nil
}

// Cursor's documented models command may expose only IDs and labels. No suffix
// vocabulary is a native level source. Unrecognized output fails closed.
func parseCursorCatalog(raw []byte) ([]modelreport.CatalogModel, error) {
	if len(raw) > modelreport.CatalogBytesMax || bytes.ContainsAny(raw, "\x00\x1b") {
		return nil, errCatalogProtocol
	}
	out := []modelreport.CatalogModel{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "Available models:" || line == "Available models" {
			continue
		}
		id, label, hasLabel := strings.Cut(line, " - ")
		if !modelreport.ValidTuple(id, "default") {
			return nil, errCatalogProtocol
		}
		m := modelreport.CatalogModel{Model: id, Facts: map[string]modelreport.CatalogField{}}
		if hasLabel {
			catalogFact(&m, "display_name", "models.label", label)
		}
		out = append(out, m)
		if len(out) > modelreport.CatalogModelsMax {
			return nil, errCatalogProtocol
		}
	}
	if len(out) == 0 || modelreport.ValidateCatalog("fresh", out) != nil {
		return nil, errCatalogProtocol
	}
	return out, nil
}

// OpenCode emits an exact provider/id line followed by a pretty-printed JSON
// object for --verbose. JSON decoding, not brace counting, frames each object.
func parseOpenCodeCatalog(raw []byte) ([]modelreport.CatalogModel, error) {
	if len(raw) > modelreport.CatalogBytesMax || bytes.ContainsAny(raw, "\x00\x1b") {
		return nil, errCatalogProtocol
	}
	out := []modelreport.CatalogModel{}
	for len(bytes.TrimSpace(raw)) > 0 {
		raw = bytes.TrimSpace(raw)
		newline := bytes.IndexByte(raw, '\n')
		if newline < 0 {
			return nil, errCatalogProtocol
		}
		id := strings.TrimSpace(string(raw[:newline]))
		raw = raw[newline+1:]
		if !strings.Contains(id, "/") || !modelreport.ValidTuple(id, "default") {
			return nil, errCatalogProtocol
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		var row map[string]json.RawMessage
		if dec.Decode(&row) != nil || row == nil {
			return nil, errCatalogProtocol
		}
		length := int(dec.InputOffset())
		if rejectDuplicateKeys(raw[:length]) != nil {
			return nil, errCatalogProtocol
		}
		raw = raw[length:]
		m := modelreport.CatalogModel{Model: id, Facts: map[string]modelreport.CatalogField{}}
		catalogCopy(&m, row, "display_name", "name", "name")
		var capabilities map[string]json.RawMessage
		if v, ok := row["capabilities"]; ok && string(v) != "null" {
			if json.Unmarshal(v, &capabilities) != nil {
				return nil, errCatalogProtocol
			}
			catalogCopy(&m, capabilities, "tools", "capabilities.toolcall", "toolcall")
			catalogCopy(&m, capabilities, "thinking", "capabilities.reasoning", "reasoning")
		}
		// Variant names are launch configuration keys, not reasoning effort names.
		// Keep levels unknown unless the harness explicitly exposes native levels.
		out = append(out, m)
		if len(out) > modelreport.CatalogModelsMax {
			return nil, errCatalogProtocol
		}
	}
	if modelreport.ValidateCatalog("fresh", out) != nil {
		return nil, errCatalogProtocol
	}
	return out, nil
}

func parsePiCatalog(raw []byte, provider string) ([]modelreport.CatalogModel, error) {
	var body struct {
		Models []struct {
			Provider  string  `json:"provider"`
			ID        string  `json:"id"`
			Name      *string `json:"name"`
			Reasoning *bool   `json:"reasoning"`
		} `json:"models"`
	}
	if len(raw) > modelreport.CatalogBytesMax || decodeProbeJSON(raw, &body) != nil || body.Models == nil || len(body.Models) > modelreport.CatalogModelsMax {
		return nil, errCatalogProtocol
	}
	out := []modelreport.CatalogModel{}
	for _, row := range body.Models {
		if row.Provider != provider {
			continue
		}
		m := modelreport.CatalogModel{Model: row.Provider + "/" + row.ID, Facts: map[string]modelreport.CatalogField{}}
		if row.Name != nil {
			catalogFact(&m, "display_name", "get_available_models.models[].name", *row.Name)
		}
		if row.Reasoning != nil {
			catalogFact(&m, "thinking", "get_available_models.models[].reasoning", *row.Reasoning)
		}
		out = append(out, m)
	}
	if modelreport.ValidateCatalog("fresh", out) != nil {
		return nil, errCatalogProtocol
	}
	return out, nil
}

func parsePiLevels(raw []byte, m *modelreport.CatalogModel) error {
	var body struct {
		Levels []string `json:"levels"`
	}
	if len(raw) > 8192 || decodeProbeJSON(raw, &body) != nil || body.Levels == nil {
		return errCatalogProtocol
	}
	catalogFact(m, "native_levels", "get_available_thinking_levels.levels", body.Levels)
	if modelreport.ValidateCatalog("fresh", []modelreport.CatalogModel{*m}) != nil {
		return errCatalogProtocol
	}
	return nil
}
