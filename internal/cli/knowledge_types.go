// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"net/http"
	"strings"
)

// Knowledge CLI types use the classic kebab-case URL segment. Node kind
// slugs are snake_case: node_kinds.slug rejects hyphens, and the importer
// already stores external_system and related_project. The nine starter
// kinds stay unchanged. Creation ensures the requested kind through
// POST /api/kinds, which records kind.created. Reads never create kinds. Node writes
// still go through the mounted nodes module and record node.created.
// This package exports no httpapi.Module and no plugins.Plugin.

func knowledgeKindSlug(typ string) (string, bool) {
	switch strings.TrimSpace(typ) {
	case "memory", "runbook", "guideline", "decision":
		return typ, true
	case "external-system", "external_system":
		return "external_system", true
	case "related-project", "related_project":
		return "related_project", true
	default:
		return "", false
	}
}

func knowledgeCLIType(slug string) string {
	switch slug {
	case "external_system":
		return "external-system"
	case "related_project":
		return "related-project"
	default:
		return slug
	}
}

// ensuredKnowledgeKinds are not part of the nine starter kinds. They are
// installed on use so a tenant that never stores them keeps the R1 set.
var ensuredKnowledgeKinds = []apiKind{
	{Slug: "external_system", Label: "External system", ShortPrefix: "EXT"},
	{Slug: "related_project", Label: "Related project", ShortPrefix: "RPR"},
}

func (rt *runtime) ensureKnowledgeKind(slug string) error {
	table, err := rt.loadKinds()
	if err != nil {
		return err
	}
	for _, spec := range ensuredKnowledgeKinds {
		if spec.Slug != slug {
			continue
		}
		if _, ok := table.bySlug[spec.Slug]; ok {
			continue
		}
		var created apiKind
		req := map[string]any{
			"slug":         spec.Slug,
			"label":        spec.Label,
			"short_prefix": spec.ShortPrefix,
			"icon":         spec.Slug,
			"field_schema": map[string]any{},
		}
		if err := rt.do(http.MethodPost, "/api/kinds", req, &created); err != nil {
			rt.kinds = nil
			reloaded, loadErr := rt.loadKinds()
			if loadErr != nil {
				return loadErr
			}
			table = reloaded
			if _, ok := table.bySlug[spec.Slug]; ok {
				continue
			}
			return err
		}
		if created.Slug == "" {
			created.Slug = spec.Slug
		}
		table.bySlug[created.Slug] = created
		if created.ID != "" {
			table.byID[created.ID] = created
		}
	}
	return nil
}
