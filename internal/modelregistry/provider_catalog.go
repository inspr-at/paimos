// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

// ProviderCatalog is a complete bounded source response. Missing fields are
// stored as null instead of borrowing capabilities from another model/version.
type ProviderCatalog struct {
	Facts   []CatalogFact
	Models  []providerModel
	Skipped int // Unsupported identifiers, never raw names or response bodies.
	HasMore bool
	Next    string
}
type providerModel struct {
	Provider, ID, Harness, LaunchID string
	Levels                          []string
}

func rawField(row map[string]json.RawMessage, path string) json.RawMessage {
	parts := strings.SplitN(path, ".", 2)
	raw := row[parts[0]]
	if len(parts) == 1 {
		if len(raw) == 0 {
			return json.RawMessage("null")
		}
		return raw
	}
	var child map[string]json.RawMessage
	if json.Unmarshal(raw, &child) != nil {
		return json.RawMessage("null")
	}
	return rawField(child, parts[1])
}
func rawString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func catalogLevels(raw json.RawMessage) ([]string, error) {
	if isNull(raw) {
		return nil, nil
	}
	var levels []string
	if json.Unmarshal(raw, &levels) != nil || levels == nil || len(levels) > 64 {
		return nil, errors.New("invalid native levels")
	}
	seen := map[string]bool{}
	for _, e := range levels {
		if len(e) > 32 || !effortRE.MatchString(e) || seen[e] {
			return nil, errors.New("invalid native level")
		}
		seen[e] = true
	}
	return levels, nil
}

// Decode collection limits before materializing maps or slices. A small
// JSON response can otherwise contain millions of empty model objects.
func boundedCatalogObject(raw []byte, limit int) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid catalog object")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		if len(out) >= limit {
			return nil, errors.New("catalog object limit")
		}
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid catalog key")
		}
		if _, exists := out[key]; exists {
			return nil, errors.New("duplicate catalog key")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, errors.New("invalid catalog value")
		}
		out[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid catalog object end")
	}
	return out, nil
}

func boundedCatalogRows(raw []byte) ([]map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("invalid model list")
	}
	rows := []map[string]json.RawMessage{}
	for d.More() {
		if len(rows) >= maxDiscoveryModels {
			return nil, errors.New("model list identifier limit")
		}
		var rawRow json.RawMessage
		if err := d.Decode(&rawRow); err != nil || len(rawRow) > 1<<20 {
			return nil, errors.New("model row limit")
		}
		row, err := boundedCatalogObject(rawRow, 256)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if token, err = d.Token(); err != nil || token != json.Delim(']') || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid model list end")
	}
	return rows, nil
}

// ParseProviderCatalog decodes only documented, public fields. Booleans such
// as Google's thinking support are not expanded into token budgets or levels.
func ParseProviderCatalog(source string, raw []byte, at time.Time) (ProviderCatalog, error) {
	out := ProviderCatalog{Facts: []CatalogFact{}, Models: []providerModel{}}
	if len(raw) > maxDiscoveryBytes || at.IsZero() {
		return out, errors.New("catalog response limit")
	}
	endpoint, ok := vendorURLs[source]
	if !ok {
		return out, errors.New("unsupported source")
	}
	rootLimit := 128
	if source == "modelsdev" {
		rootLimit = 512
	} else if source == "litellm" {
		rootLimit = maxDiscoveryModels + 1 // Includes sample_spec.
	}
	root, err := boundedCatalogObject(raw, rootLimit)
	if err != nil {
		return out, err
	}
	type entry struct {
		provider, id string
		row          map[string]json.RawMessage
	}
	entries := []entry{}
	count := 0
	add := func(provider, id string, row map[string]json.RawMessage) error {
		count++
		if count > maxDiscoveryModels {
			return errors.New("model list identifier limit")
		}
		if id == "" || len(id) > 128 || !modelRE.MatchString(id) || provider == "" || len(provider) > 64 || !modelRE.MatchString(provider) {
			out.Skipped++
			return nil
		}
		entries = append(entries, entry{provider, id, row})
		return nil
	}
	switch source {
	case "modelsdev":
		keys := sortedRawKeys(root)
		for _, p := range keys {
			provider, err := boundedCatalogObject(root[p], 128)
			if err != nil {
				return out, err
			}
			models, err := boundedCatalogObject(provider["models"], maxDiscoveryModels-count)
			if err != nil {
				return out, err
			}
			ids := make([]string, 0, len(models))
			for id := range models {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				if len(models[id]) > 1<<20 {
					return out, errors.New("model row limit")
				}
				row, err := boundedCatalogObject(models[id], 256)
				if err != nil {
					return out, err
				}
				if rowID := rawString(row["id"]); rowID != "" {
					id = rowID
				}
				if err := add(p, id, row); err != nil {
					return out, err
				}
			}
		}
	case "litellm":
		for _, id := range sortedRawKeys(root) {
			if id == "sample_spec" {
				continue
			}
			if len(root[id]) > 1<<20 {
				return out, errors.New("model row limit")
			}
			row, err := boundedCatalogObject(root[id], 256)
			if err != nil {
				return out, err
			}
			mode := rawString(row["mode"])
			if mode != "chat" && mode != "responses" {
				continue
			}
			p := rawString(row["litellm_provider"])
			// Provider namespaces are explicit, never inferred from marketing names.
			switch p {
			case "vertex_ai", "gemini":
				p = "google"
			case "x_ai":
				p = "xai"
			}
			if p == "" {
				continue
			}
			if err := add(p, id, row); err != nil {
				return out, err
			}
		}
	default:
		list := root["data"]
		if source == "google" {
			list = root["models"]
		}
		rows, err := boundedCatalogRows(list)
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			id := rawString(row["id"])
			p := source
			if source == "google" {
				id = strings.TrimPrefix(rawString(row["name"]), "models/")
			}
			if source == "openrouter" {
				parts := strings.SplitN(id, "/", 2)
				p = ""
				if len(parts) == 2 && parts[1] != "" {
					p = parts[0]
				}
			}
			if err := add(p, id, row); err != nil {
				return out, err
			}
		}
		_ = json.Unmarshal(root["has_more"], &out.HasMore)
		out.Next = rawString(root["last_id"])
		if source == "google" {
			out.Next = rawString(root["nextPageToken"])
			out.HasMore = out.Next != ""
		}
	}
	for _, e := range entries {
		provider := e.provider
		id := e.id
		if canonical := rawString(e.row["canonical_model_id"]); canonical != "" && source == "modelsdev" {
			parts := strings.SplitN(canonical, "/", 2)
			if len(parts) == 2 {
				provider, id = parts[0], parts[1]
			}
		}
		provider, id = CatalogIdentity(provider, "", id, "")
		if source == "litellm" && provider == "google" {
			id = strings.TrimPrefix(strings.TrimPrefix(id, "gemini/"), "vertex_ai/")
		}
		// Canonical IDs are external input too. Do not let one invalid
		// canonical override poison publication of otherwise usable facts.
		if provider == "" || len(provider) > 64 || !modelRE.MatchString(provider) || id == "" || len(id) > 128 || !modelRE.MatchString(id) {
			out.Skipped++
			continue
		}
		harness := map[string]string{"openai": "codex", "anthropic": "claude", "xai": "grok", "google": "gemini"}[provider]
		launch := id
		route := ""
		kind := "native"
		if source == "openrouter" {
			harness, launch, route, kind = "pi", e.id, "pi/"+e.id, "aggregator"
		}
		if source == "modelsdev" || source == "litellm" {
			kind = "aggregator"
			// Aggregator capabilities describe that provider's API route, not its CLI.
			route = "api/" + e.provider + "/" + e.id
		}
		fields := map[string]string{"model_id": "id", "display_name": "display_name", "native_levels": "", "default_level": "", "thinking": "", "lifecycle": "lifecycle", "deprecated_at": "deprecated_at", "retirement_at": "retires_at", "successor": "", "aliases": "aliases", "context_window": "max_input_tokens", "max_output_tokens": "max_tokens", "line": "line", "model_version": "version", "reasoning_options": ""}
		switch source {
		case "openai":
			fields["retirement_at"] = "shutdown_date"
			fields["display_name"] = ""
			fields["context_window"] = ""
			fields["max_output_tokens"] = ""
		case "xai":
			fields["native_levels"] = "capabilities.reasoning_effort"
			fields["default_level"] = "capabilities.default_reasoning_effort"
			fields["display_name"] = "name"
			fields["context_window"] = "context_length"
		case "anthropic":
			fields["thinking"] = "capabilities.thinking.supported"
		case "google":
			fields["model_id"] = "name"
			fields["display_name"] = "displayName"
			fields["thinking"] = "thinking"
			fields["context_window"] = "inputTokenLimit"
			fields["max_output_tokens"] = "outputTokenLimit"
			fields["model_version"] = "version"
		case "openrouter":
			fields["display_name"] = "name"
			fields["native_levels"] = "reasoning.supported_efforts"
			fields["default_level"] = "reasoning.default_effort"
			fields["retirement_at"] = "expiration_date"
			fields["context_window"] = "context_length"
			fields["max_output_tokens"] = "top_provider.max_completion_tokens"
		case "modelsdev":
			fields["display_name"] = "name"
			fields["thinking"] = "reasoning"
			fields["lifecycle"] = "status"
			fields["native_levels"] = "reasoning_options"
			fields["reasoning_options"] = "reasoning_options"
			fields["context_window"] = "limit.context"
			fields["max_output_tokens"] = "limit.output"
		case "litellm":
			fields["thinking"] = "supports_reasoning"
			fields["retirement_at"] = "deprecation_date"
			fields["display_name"] = ""
			fields["native_levels"] = "supports_*_reasoning_effort"
			fields["max_output_tokens"] = "max_output_tokens"
		}
		levelsRaw := rawField(e.row, fields["native_levels"])
		if len(levelsRaw) > 8192 || len(rawField(e.row, "capabilities.effort")) > 8192 {
			return out, errors.New("model capability limit")
		}
		if source == "anthropic" {
			fields["native_levels"] = "capabilities.effort.*.supported"
			var opts map[string]json.RawMessage
			if json.Unmarshal(rawField(e.row, "capabilities.effort"), &opts) == nil && opts != nil {
				levels := []string{}
				for _, name := range sortedRawKeys(opts) {
					if name == "supported" {
						continue
					}
					var v struct {
						Supported *bool `json:"supported"`
					}
					if json.Unmarshal(opts[name], &v) != nil || v.Supported == nil {
						return out, errors.New("invalid effort support")
					}
					if *v.Supported {
						levels = append(levels, name)
					}
				}
				levelsRaw, _ = json.Marshal(levels)
			}
		}
		if source == "litellm" {
			levels := []string{}
			hasFlags := false
			for _, field := range sortedRawKeys(e.row) {
				if !strings.HasPrefix(field, "supports_") || !strings.HasSuffix(field, "_reasoning_effort") {
					continue
				}
				hasFlags = true
				var supported bool
				if json.Unmarshal(e.row[field], &supported) != nil {
					return out, errors.New("invalid effort flag")
				}
				if supported {
					levels = append(levels, strings.TrimSuffix(strings.TrimPrefix(field, "supports_"), "_reasoning_effort"))
				}
			}
			if hasFlags {
				levelsRaw, _ = json.Marshal(levels)
			} else {
				levelsRaw = json.RawMessage("null")
			}
		}
		if source == "modelsdev" && !isNull(levelsRaw) {
			var opts []map[string]json.RawMessage
			if json.Unmarshal(levelsRaw, &opts) != nil {
				return out, errors.New("invalid reasoning options")
			}
			levels := []string{}
			for _, opt := range opts {
				if rawString(opt["type"]) != "effort" {
					continue
				}
				var values []json.RawMessage
				if json.Unmarshal(opt["values"], &values) != nil {
					return out, errors.New("invalid reasoning values")
				}
				for _, value := range values {
					if isNull(value) {
						continue
					}
					name := rawString(value)
					if name == "" {
						return out, errors.New("invalid reasoning value")
					}
					levels = append(levels, name)
				}
			}
			if len(levels) > 0 || len(opts) == 0 {
				levelsRaw, _ = json.Marshal(levels)
			} else {
				levelsRaw = json.RawMessage("null")
			}
		}
		levels, err := catalogLevels(levelsRaw)
		if err != nil {
			return out, err
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := rawField(e.row, fields[name])
			if name == "native_levels" {
				value = levelsRaw
			}
			if source == "litellm" && name == "model_id" {
				value, _ = json.Marshal(e.id)
			}
			if fields[name] == "" {
				value = json.RawMessage("null")
			}
			if len(value) > 8192 {
				return out, errors.New("model fact value limit")
			}
			out.Facts = append(out.Facts, CatalogFact{Provider: provider, ModelID: id, Route: route, Fact: name, Value: value, Source: endpoint, SourceKind: kind, FieldPath: fields[name], FetchedAt: at})
		}
		// Dispatch proposals require an explicit native/route level. No guessed
		// default or predecessor effort is produced when capabilities are absent.
		if kind == "native" || source == "openrouter" {
			out.Models = append(out.Models, providerModel{provider, id, harness, launch, levels})
		}
	}
	if len(out.Facts) > maxCatalogFacts {
		return out, errors.New("catalog fact limit")
	}
	return out, nil
}

func sortedRawKeys(in map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func catalogObservations(c ProviderCatalog, evidence string) []Observation {
	out := []Observation{}
	for _, m := range c.Models {
		if m.Harness == "" {
			continue
		}
		for _, e := range m.Levels {
			out = append(out, Observation{ReportID: EvidenceID(evidence + "/" + m.Harness + "/" + m.LaunchID + "/" + e), Harness: m.Harness, Model: m.LaunchID, Effort: e, Status: "advertised"})
		}
	}
	return out
}
