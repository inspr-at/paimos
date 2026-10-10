// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/jackc/pgx/v5"
)

const maxCatalogFacts = 200000

// CatalogFact is public, field-level evidence. Null means unknown; an empty
// array is a known empty list. Route is empty for provider-wide facts, otherwise
// harness/launch-id. Native names are retained verbatim, with no shared scale.
type CatalogFact struct {
	Provider   string          `json:"provider"`
	ModelID    string          `json:"model_id"`
	Route      string          `json:"route"`
	Fact       string          `json:"fact"`
	Value      json.RawMessage `json:"value"`
	Source     string          `json:"source"`
	SourceKind string          `json:"source_kind"`
	FieldPath  string          `json:"field_path"`
	FetchedAt  time.Time       `json:"fetched_at"`
}

// CatalogIdentity folds only an effort explicitly advertised for this exact
// Cursor launch ID. No suffix vocabulary is guessed. Namespace prefixes denote
// routing, not a second provider identity.
func CatalogIdentity(provider, harness, model, effort string) (string, string) {
	if provider == "" {
		provider = harnesslaunch.ModelFamily(harness, model)
	}
	if provider == "x-ai" {
		provider = "xai"
	}
	if harness == "gemini" {
		model = strings.TrimPrefix(model, "models/")
	}
	for _, prefix := range []string{provider + "/", "x-ai/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	if harness == "cursor" && effort != "" && effort != "default" {
		tail := "-" + effort
		if strings.HasSuffix(model, tail) {
			model = strings.TrimSuffix(model, tail)
		} else if strings.HasSuffix(model, tail+"-fast") {
			model = strings.TrimSuffix(model, tail+"-fast")
		}
	}
	if provider == "" {
		provider = "unknown"
	}
	return provider, model
}

func factRank(kind string) int {
	switch kind {
	case "harness":
		return 3
	case "native":
		return 2
	case "aggregator":
		return 1
	default:
		return 0
	}
}

// ResolveCatalogFacts chooses independently per field. Harness evidence is
// specific to its exact route. Native provider facts apply across routes;
// aggregator route facts cannot override them. A missing native field permits
// sourced fallback, never an inferred value.
func ResolveCatalogFacts(in []CatalogFact, route string) map[string]CatalogFact {
	out := map[string]CatalogFact{}
	for _, f := range in {
		if f.Route != "" && f.Route != route {
			continue
		}
		old, ok := out[f.Fact]
		known := len(f.Value) > 0 && string(f.Value) != "null"
		oldKnown := len(old.Value) > 0 && string(old.Value) != "null"
		if !ok || known && !oldKnown || known == oldKnown && (factRank(f.SourceKind) > factRank(old.SourceKind) || factRank(f.SourceKind) == factRank(old.SourceKind) && (f.FetchedAt.After(old.FetchedAt) || f.FetchedAt.Equal(old.FetchedAt) && f.Source < old.Source)) {
			out[f.Fact] = f
		}
	}
	return out
}

// RecordCatalogFacts is for refresh/probe writers already holding the tenant
// and catalog fences and their final authority check. It appends no event and
// takes no locks after an event. Values are public allowlisted fields only.
func RecordCatalogFacts(ctx context.Context, tx pgx.Tx, tenantID string, facts []CatalogFact) error {
	if len(facts) > maxCatalogFacts {
		return errors.New("catalog fact limit")
	}
	for _, f := range facts {
		if f.Provider == "" || len(f.Provider) > 64 || !modelRE.MatchString(f.Provider) || len(f.ModelID) > 128 || !modelRE.MatchString(f.ModelID) || len(f.Route) > 256 || len(f.Fact) > 64 || !effortRE.MatchString(f.Fact) || len(f.Value) > 8192 || !json.Valid(f.Value) || len(f.Source) > 256 || f.Source == "" || len(f.FieldPath) > 128 || f.FetchedAt.IsZero() || (f.SourceKind != "native" && f.SourceKind != "harness" && f.SourceKind != "aggregator" && f.SourceKind != "bootstrap") {
			return errors.New("invalid catalog fact")
		}
	}
	// Sort and batch before SQL to keep writes deterministic and bounded.
	facts = append([]CatalogFact(nil), facts...)
	sort.Slice(facts, func(i, j int) bool {
		a, b := facts[i], facts[j]
		return a.Provider+"/"+a.ModelID+"/"+a.Route+"/"+a.Fact+"/"+a.Source < b.Provider+"/"+b.ModelID+"/"+b.Route+"/"+b.Fact+"/"+b.Source
	})
	for start := 0; start < len(facts); start += 500 {
		raw, err := json.Marshal(facts[start:min(start+500, len(facts))])
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO model_catalog_facts(tenant_id,provider,model_id,route,fact,value,source,source_kind,field_path,fetched_at)
 SELECT $1::uuid,provider,model_id,route,fact,value,source,source_kind,field_path,fetched_at FROM jsonb_to_recordset($2::jsonb)
 AS f(provider text,model_id text,route text,fact text,value jsonb,source text,source_kind text,field_path text,fetched_at timestamptz)
 ON CONFLICT(tenant_id,provider,model_id,route,fact,source) DO UPDATE SET value=EXCLUDED.value,source_kind=EXCLUDED.source_kind,field_path=EXCLUDED.field_path,fetched_at=EXCLUDED.fetched_at
 WHERE model_catalog_facts.fetched_at<=EXCLUDED.fetched_at`, tenantID, string(raw))
		if err != nil {
			return err
		}
	}
	return nil
}

func bootstrapCatalogFacts(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&at); err != nil {
		return err
	}
	facts := []CatalogFact{}
	seen := map[string]bool{}
	for _, s := range seedModels {
		provider, model := CatalogIdentity("", s.Harness, s.ID, "")
		key := provider + "/" + model + "/" + s.Harness + "/" + s.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		value, _ := json.Marshal(s.ID)
		facts = append(facts, CatalogFact{Provider: provider, ModelID: model, Route: s.Harness + "/" + s.ID, Fact: "model_id", Value: value, Source: "offline-bootstrap", SourceKind: "bootstrap", FieldPath: "id", FetchedAt: at})
	}
	return RecordCatalogFacts(ctx, tx, tenantID, facts)
}
