// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/doctrinerepo"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const maxExportNodes = 10000
const MaxExportBytes = 16 << 20

type ExportTarget struct {
	Target     string `json:"target"`
	Repository string `json:"repository,omitempty"`
	Path       string `json:"path,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}
type ExportText struct {
	Identity      string       `json:"identity"`
	NodeID        string       `json:"node_id,omitempty"`
	SHA256        string       `json:"sha256"`
	WhySHA256     string       `json:"why_sha256"`
	DetailsSHA256 string       `json:"details_sha256"`
	Target        ExportTarget `json:"proposed_git_target"`
}
type ExportVersion struct {
	ID       string       `json:"id"`
	SetID    string       `json:"set_id"`
	Revision int64        `json:"revision"`
	Authors  []string     `json:"authors"`
	Snapshot Snapshot     `json:"snapshot"`
	Texts    []ExportText `json:"texts"`
}
type ExportSet struct {
	ID               string          `json:"id"`
	Revision         int64           `json:"revision"`
	Authors          []string        `json:"authors"`
	PublishedVersion string          `json:"published_version"`
	Versions         []ExportVersion `json:"versions"`
}
type ExportLayer struct {
	ID      string       `json:"id"`
	Scope   Scope        `json:"scope"`
	Authors []string     `json:"authors"`
	Target  ExportTarget `json:"proposed_git_target"`
	Sets    []ExportSet  `json:"sets"`
}
type ExportFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}
type ExportManifest struct {
	Format   string        `json:"format"`
	TenantID string        `json:"tenant_id"`
	Coverage string        `json:"coverage"`
	Layers   []ExportLayer `json:"layers"`
}
type ExportBundle struct {
	Manifest ExportManifest `json:"manifest"`
	Files    []ExportFile   `json:"files"`
}

func exportSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func exportTarget(scope Scope, rule *Rule, repos doctrinerepo.Pair) ExportTarget {
	switch scope.Layer {
	case "company":
		repository := repos.Public()
		if rule != nil {
			source := strings.ToLower(rule.Source.Reference + "\n" + rule.Details)
			if strings.Contains(source, "doctrine-private/") || strings.Contains(source, "agents-kernel-private") || strings.Contains(source, strings.ToLower(repos.Private())) {
				repository = repos.Private()
			}
		}
		if repository == "" {
			return ExportTarget{Target: "needs decision"}
		}
		return ExportTarget{Target: "doctrine repository", Repository: repository, Path: "docs/AGENTS-KERNEL.md"}
	case "project":
		return ExportTarget{Target: "project repository", Path: "AGENTS.md", ProjectID: scope.ProjectID}
	default:
		return ExportTarget{Target: "needs decision"}
	}
}

// export never broadens rule ownership or project visibility. A bundle covers
// all published history visible to this administrator, including disabled rules.
// It contains no drafts and has no clock-dependent fields. The audit contains
// counts and a digest only. Oversized exports fail whole, never truncate.
func (m *Module) export(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	if len(r.URL.Query()) != 0 {
		return nil, fail(400, "invalid_request", "The rules export takes no selectors.")
	}
	if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,coalesce(n.parent_id::text,''),n.rule_resource,
 CASE WHEN octet_length(n.fields::text)<=$1 THEN n.fields ELSE NULL END,
 ARRAY(SELECT DISTINCT e.actor_principal_id::text FROM events e
 WHERE e.tenant_id=n.tenant_id AND e.node_id=CASE WHEN n.rule_resource='version' THEN n.parent_id ELSE n.id END
 AND (n.rule_resource<>'version' OR (e.type='rules.published' AND e.after->>'version'=n.fields->>'version'))
 ORDER BY e.actor_principal_id::text LIMIT 101)
 FROM nodes n WHERE n.tenant_id=$2 AND n.rule_resource IS NOT NULL AND n.deleted_at IS NULL
 ORDER BY n.id LIMIT $3`, maxBody, p.TenantID, maxExportNodes+1)
	if err != nil {
		return nil, err
	}
	type record struct {
		id, parent, resource string
		f                    fields
		authors              []string
	}
	records := []record{}
	total := 0
	for rows.Next() {
		var n record
		var raw []byte
		if err = rows.Scan(&n.id, &n.parent, &n.resource, &raw, &n.authors); err != nil {
			rows.Close()
			return nil, err
		}
		total += len(raw)
		if raw == nil || total > MaxExportBytes || len(records) >= maxExportNodes || len(n.authors) > 100 {
			rows.Close()
			return nil, fail(413, "rules_export_too_large", "The complete rules export exceeds its bounded export limits; nothing was exported.")
		}
		n.f, err = decodeFields(raw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	layers := map[string]*ExportLayer{}
	sets := map[string]*ExportSet{}
	setLayers := map[string]string{}
	ruleIDs := map[string]string{}
	for _, n := range records {
		if n.resource == "layer" {
			if err = permission(ctx, tx, p, n.f.Scope, "rules.read"); err != nil {
				return nil, err
			}
			layers[n.id] = &ExportLayer{ID: n.id, Scope: n.f.Scope, Authors: n.authors, Target: exportTarget(n.f.Scope, nil, m.repositories), Sets: []ExportSet{}}
		}
		if n.resource == "set" {
			sets[n.id] = &ExportSet{ID: n.id, Revision: n.f.Revision, Authors: n.authors, PublishedVersion: n.f.PublishedVersion, Versions: []ExportVersion{}}
			setLayers[n.id] = n.parent
		}
		if n.resource == "rule" && n.f.Rule != nil {
			ruleIDs[n.parent+"/"+n.f.Rule.Identity] = n.id
		}
	}
	for _, n := range records {
		if n.resource != "version" {
			continue
		}
		s := sets[n.parent]
		l := layers[setLayers[n.parent]]
		if s == nil || l == nil || n.f.Snapshot == nil {
			return nil, fail(409, "snapshot_integrity", "A published rule version is missing its layer or set.")
		}
		snap := *n.f.Snapshot
		if snap.SHA256 != SnapshotDigest(snap) || snap.SetID != s.ID || snap.Scope != l.Scope {
			return nil, fail(409, "snapshot_integrity", "A published rule version failed its digest or scope check.")
		}
		v := ExportVersion{ID: n.id, SetID: s.ID, Revision: snap.Revision, Authors: n.authors, Snapshot: snap, Texts: []ExportText{}}
		for _, rule := range snap.Rules {
			v.Texts = append(v.Texts, ExportText{Identity: rule.Identity, NodeID: ruleIDs[s.ID+"/"+rule.Identity], SHA256: exportSHA(rule.Text), WhySHA256: exportSHA(rule.Why), DetailsSHA256: exportSHA(rule.Details), Target: exportTarget(l.Scope, &rule, m.repositories)})
		}
		s.Versions = append(s.Versions, v)
	}
	for id, s := range sets {
		if len(s.Versions) == 0 {
			continue
		}
		sort.Slice(s.Versions, func(i, j int) bool { return s.Versions[i].Snapshot.Version < s.Versions[j].Snapshot.Version })
		l := layers[setLayers[id]]
		if l == nil {
			return nil, fail(409, "snapshot_integrity", "A published set is missing its layer.")
		}
		l.Sets = append(l.Sets, *s)
	}
	out := ExportBundle{Manifest: ExportManifest{Format: "aeon.rules-export.v1", TenantID: p.TenantID, Coverage: "caller-visible published history; private ownership and project access preserved", Layers: []ExportLayer{}}, Files: []ExportFile{}}
	ids := []string{}
	for id, l := range layers {
		if len(l.Sets) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		l := layers[id]
		sort.Slice(l.Sets, func(i, j int) bool { return l.Sets[i].ID < l.Sets[j].ID })
		raw, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			return nil, err
		}
		content := string(raw) + "\n"
		out.Manifest.Layers = append(out.Manifest.Layers, *l)
		out.Files = append(out.Files, ExportFile{Path: fmt.Sprintf("layer-%s.json", id), SHA256: exportSHA(content), Content: content})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxExportBytes {
		return nil, fail(413, "rules_export_too_large", "The complete bundle exceeds 16 MiB; nothing was exported.")
	}
	// Event counter is last: no resource locks or writes follow it.
	_, err = events.Append(ctx, tx, p, events.Change{Type: "settings.rules_exported", After: map[string]any{"layers": len(out.Files), "sha256": exportSHA(string(raw))}})
	return out, err
}
