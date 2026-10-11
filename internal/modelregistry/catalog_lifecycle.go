// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CatalogLifecycle keeps absence of evidence distinct from an active model.
// Source identifies the evidence; inferred successors name the shared SQL rule.
type CatalogLifecycle struct {
	State     *string    `json:"state"`
	Date      *time.Time `json:"date"`
	Successor *string    `json:"successor"`
	Reason    string     `json:"reason"`
	Source    string     `json:"source"`
}

type lifecycleModel struct {
	Identity  string
	Provider  string
	Model     string
	Line      string
	Version   string
	Sourced   bool
	Facts     []CatalogFact
	Profiles  []Profile
	Lifecycle CatalogLifecycle
}

func lifecycleFacts(facts []CatalogFact) map[string]CatalogFact {
	// Resolve each route separately first. A route's evidence must never lend
	// capabilities to another route; this projection is lifecycle evidence only.
	chosen := map[string]CatalogFact{}
	for _, f := range facts {
		if f.Fact != "lifecycle" && f.Fact != "retirement_at" && f.Fact != "deprecated_at" && f.Fact != "successor" && f.Fact != "present" {
			continue
		}
		f.Route = ""
		for name, winner := range ResolveCatalogFacts([]CatalogFact{chosen[f.Fact], f}, "") {
			if name != "" {
				chosen[name] = winner
			}
		}
	}
	return chosen
}

func factDate(f CatalogFact) *time.Time {
	raw := rawString(f.Value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			return &at
		}
	}
	return nil
}

func resolveLifecycle(facts []CatalogFact, identity string, now time.Time) CatalogLifecycle {
	out := CatalogLifecycle{}
	chosen := lifecycleFacts(facts)
	set := func(state, reason string, f CatalogFact) {
		out.State, out.Reason, out.Source = &state, reason, f.Source
	}
	if f := chosen["successor"]; !isNull(f.Value) {
		successor := rawString(f.Value)
		provider, _, _ := strings.Cut(identity, ":")
		if successor != "" && len(successor) <= 128 && modelRE.MatchString(successor) {
			successor = strings.TrimPrefix(successor, provider+":")
			_, successor = CatalogIdentity(provider, "", successor, "")
			successor = provider + ":" + successor
			if successor != identity {
				out.Successor = &successor
				set("superseded", "Source reports a successor", f)
			}
		}
	}
	if f := chosen["lifecycle"]; !isNull(f.Value) {
		state := rawString(f.Value)
		switch state {
		case "active", "preview":
			if out.State == nil {
				set(state, "Source reports "+state, f)
			}
		case "deprecated", "retiring", "vanished", "superseded":
			set(state, "Source reports "+state, f)
		}
	}
	if f := chosen["deprecated_at"]; factDate(f) != nil && !factDate(f).After(now) {
		out.Date = factDate(f)
		set("deprecated", "Source deprecation date reached", f)
	}
	if f := chosen["retirement_at"]; factDate(f) != nil {
		out.Date = factDate(f)
		set("retiring", "Source reports a retirement date", f)
	}
	if f := chosen["present"]; string(f.Value) == "false" {
		set("vanished", "Absent from a complete successful source listing", f)
		at := f.FetchedAt
		out.Date = &at
	}
	return out
}

// Only a complete successful provider response can assert disappearance. A
// failed, revoked, partial or bounded-out response never reaches this writer.
func recordCatalogPresence(ctx context.Context, tx pgx.Tx, tenantID, source string, at time.Time, catalog ProviderCatalog) error {
	endpoint := vendorURLs[source]
	rows, err := tx.Query(ctx, `SELECT provider,model_id,route,source_kind FROM model_catalog_facts WHERE source=$1 AND fact='model_id' ORDER BY provider,model_id,route LIMIT 20001`, endpoint)
	if err != nil {
		return err
	}
	facts := []CatalogFact{}
	seen := map[string]bool{}
	for _, f := range catalog.Facts {
		if f.Fact == "model_id" {
			seen[f.Provider+"/"+f.ModelID+"/"+f.Route] = true
		}
	}
	for rows.Next() {
		f := CatalogFact{Fact: "present", Source: endpoint, FieldPath: "complete_listing", FetchedAt: at}
		if err := rows.Scan(&f.Provider, &f.ModelID, &f.Route, &f.SourceKind); err != nil {
			rows.Close()
			return err
		}
		f.Value = json.RawMessage("false")
		if seen[f.Provider+"/"+f.ModelID+"/"+f.Route] {
			f.Value = json.RawMessage("true")
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(facts) > 20000 {
		return fail(413, "catalog presence limit")
	}
	return RecordCatalogFacts(ctx, tx, tenantID, facts)
}

func loadLifecycleCatalog(ctx context.Context, tx pgx.Tx, now time.Time, page ...[2]string) ([]lifecycleModel, error) {
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return nil, err
	}
	profiles := []Profile{}
	for _, ps := range c.profiles {
		profiles = append(profiles, ps...)
	}
	return loadLifecycleProfiles(ctx, tx, now, profiles, page...)
}

// Registry reads pass their already-listed profiles, without the board's cap
// or a second profile read. Candidate evidence is restricted to their lines.
func loadLifecycleProfiles(ctx context.Context, tx pgx.Tx, now time.Time, profiles []Profile, page ...[2]string) ([]lifecycleModel, error) {
	focused := map[string]bool{}
	for _, id := range page {
		focused[id[0]+":"+id[1]] = true
	}
	for _, p := range profiles {
		provider, id := CatalogIdentity(p.Family, p.Harness, p.Model, p.Effort)
		focused[provider+":"+id] = true
		if len(focused) > 20000 {
			return nil, fail(413, "catalog identity limit")
		}
	}
	if len(focused) == 0 {
		return []lifecycleModel{}, nil
	}
	keys := []map[string]string{}
	for identity := range focused {
		provider, id, _ := strings.Cut(identity, ":")
		keys = append(keys, map[string]string{"provider": provider, "model_id": id})
	}
	keyJSON, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	// Derive the SQL line once per identity and apply the identity budget only
	// to relevant lines. Unrelated vendor models cannot exhaust this projection.
	rows, err := tx.Query(ctx, `WITH focus AS MATERIALIZED (
		SELECT provider,model_id FROM jsonb_to_recordset($1::jsonb) AS k(provider text,model_id text)
	), focus_lines AS MATERIALIZED (
		SELECT provider,model_id,(aeon_model_line(CASE provider WHEN 'openai' THEN 'codex' WHEN 'anthropic' THEN 'claude' WHEN 'google' THEN 'gemini' WHEN 'xai' THEN 'grok' ELSE '' END,model_id))[1] AS line FROM focus
	), identities AS MATERIALIZED (
		SELECT provider,model_id,bool_or(source_kind IN ('native','harness','aggregator')) AS sourced
		FROM model_catalog_facts WHERE provider IN (SELECT provider FROM focus) GROUP BY provider,model_id
	), model_lines AS MATERIALIZED (
		SELECT i.*,aeon_model_line(CASE provider WHEN 'openai' THEN 'codex' WHEN 'anthropic' THEN 'claude' WHEN 'google' THEN 'gemini' WHEN 'xai' THEN 'grok' ELSE '' END,model_id) AS parts FROM identities i
	) SELECT provider,model_id,parts[1],parts[2],sourced FROM model_lines m
	WHERE EXISTS(SELECT 1 FROM focus_lines f WHERE f.provider=m.provider AND (f.model_id=m.model_id OR f.line=m.parts[1]))
	ORDER BY provider,model_id LIMIT 20001`, string(keyJSON))
	if err != nil {
		return nil, err
	}
	models := []lifecycleModel{}
	indexes := map[string]int{}
	for rows.Next() {
		var model lifecycleModel
		if err := rows.Scan(&model.Provider, &model.Model, &model.Line, &model.Version, &model.Sourced); err != nil {
			rows.Close()
			return nil, err
		}
		model.Identity = model.Provider + ":" + model.Model
		indexes[model.Identity] = len(models)
		models = append(models, model)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(models) > 20000 {
		return nil, fail(413, "catalog identity limit")
	}
	keys = []map[string]string{}
	for _, model := range models {
		keys = append(keys, map[string]string{"provider": model.Provider, "model_id": model.Model})
	}
	keyJSON, err = json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT f.provider,f.model_id,route,fact,value,source,source_kind,field_path,fetched_at FROM model_catalog_facts f JOIN jsonb_to_recordset($1::jsonb) AS k(provider text,model_id text) USING(provider,model_id) WHERE fact IN ('lifecycle','deprecated_at','retirement_at','successor','present') AND value<>'null'::jsonb ORDER BY f.provider,f.model_id,route,fact,source LIMIT 32001`, string(keyJSON))
	if err != nil {
		return nil, err
	}
	count, bytes := 0, 0
	for rows.Next() {
		var f CatalogFact
		if err := rows.Scan(&f.Provider, &f.ModelID, &f.Route, &f.Fact, &f.Value, &f.Source, &f.SourceKind, &f.FieldPath, &f.FetchedAt); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		bytes += len(f.Value) + len(f.Source) + 256
		if count > 32000 || bytes > 4<<20 {
			rows.Close()
			return nil, fail(413, "catalog lifecycle evidence limit")
		}
		if i, ok := indexes[f.Provider+":"+f.ModelID]; ok {
			models[i].Facts = append(models[i].Facts, f)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		provider, id := CatalogIdentity(p.Family, p.Harness, p.Model, p.Effort)
		key := provider + ":" + id
		i, ok := indexes[key]
		if !ok {
			_, line, version := ProfileLine(p)
			i = len(models)
			indexes[key] = i
			models = append(models, lifecycleModel{Identity: key, Provider: provider, Model: id, Line: line, Version: version})
		}
		models[i].Profiles = append(models[i].Profiles, p)
	}
	for i := range models {
		sort.Slice(models[i].Profiles, func(a, b int) bool { return models[i].Profiles[a].ID < models[i].Profiles[b].ID })
		models[i].Lifecycle = resolveLifecycle(models[i].Facts, models[i].Identity, now)
	}
	// The SQL rule remains the authority for concrete numeric comparisons.
	// Floating aliases never prove which numbered version succeeds a pin.
	candidateLines := map[string][]int{}
	for i, model := range models {
		if !numericCatalogVersion(model.Version) || !model.Sourced {
			continue
		}
		if model.Lifecycle.State != nil && (*model.Lifecycle.State == "vanished" || *model.Lifecycle.State == "deprecated" || *model.Lifecycle.State == "retiring") {
			continue
		}
		key := model.Provider + ":" + model.Line
		candidateLines[key] = append(candidateLines[key], i)
	}
	for key, indices := range candidateLines {
		sort.Slice(indices, func(i, j int) bool {
			cmp := compareCatalogVersions(models[indices[i]].Version, models[indices[j]].Version)
			if cmp == 0 {
				return models[indices[i]].Identity < models[indices[j]].Identity
			}
			return cmp > 0
		})
		candidateLines[key] = indices
	}
	comparisons := []map[string]string{}
	for _, model := range models {
		if !focused[model.Identity] || model.Lifecycle.Successor != nil || !numericCatalogVersion(model.Version) {
			continue
		}
		candidates := candidateLines[model.Provider+":"+model.Line]
		if len(candidates) == 0 || models[candidates[0]].Identity == model.Identity {
			continue
		}
		next := models[candidates[0]]
		comparisons = append(comparisons, map[string]string{"identity": model.Identity, "successor": next.Identity, "version": model.Version, "next_version": next.Version})
	}
	if len(comparisons) > 0 {
		comparisonJSON, err := json.Marshal(comparisons)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT identity,successor FROM jsonb_to_recordset($1::jsonb)
			AS c(identity text,successor text,version text,next_version text)
			WHERE aeon_model_version_newer(next_version,version)`, string(comparisonJSON))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var identity, successor string
			if err := rows.Scan(&identity, &successor); err != nil {
				return nil, err
			}
			model := &models[indexes[identity]]
			model.Lifecycle.Successor = &successor
			if model.Lifecycle.State == nil || *model.Lifecycle.State == "active" || *model.Lifecycle.State == "preview" {
				state := "superseded"
				model.Lifecycle.State = &state
				model.Lifecycle.Reason = "Same model line has a newer sourced version"
				model.Lifecycle.Source = "aeon_model_line/aeon_model_version_newer"
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Identity < models[j].Identity })
	return models, nil
}

func numericCatalogVersion(version string) bool {
	for _, part := range strings.Split(version, ".") {
		if part == "" {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

// Source identifiers are bounded to 128 bytes. Arbitrary precision preserves
// the SQL numeric-segment order even for versions outside the platform int.
func compareCatalogVersions(a, b string) int {
	if a == b {
		return 0
	}
	if a == "alias" {
		return 1
	}
	if b == "alias" {
		return -1
	}
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aa) || i < len(bb); i++ {
		av, bv := new(big.Int), new(big.Int)
		if i < len(aa) {
			av.SetString(aa[i], 10)
		}
		if i < len(bb) {
			bv.SetString(bb[i], 10)
		}
		if cmp := av.Cmp(bv); cmp != 0 {
			return cmp
		}
	}
	return 0
}
