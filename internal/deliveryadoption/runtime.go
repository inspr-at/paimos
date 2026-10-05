// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	paimos "github.com/inspr-at/paimos"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimeFile is non-secret release evidence provisioned locally by deployment.
// It contains no keys, credentials, submitted mappings or manual apply flag.
// Authority entries cover their own tenants, never another instance's identity.
type RuntimeFile struct {
	Rollout          Config `json:"rollout"`
	ProviderCommand  string `json:"provider_command"`
	ReportsDirectory string `json:"reports_directory"`
}

// FromEnvironment never invents an operator or backup provider. Without local
// release evidence the automatic runner exposes an unmet rollout dependency;
// it cannot create authorized jobs or move projects. Configured activation starts
// discovery immediately in the same binary, after its live reporting is mounted.
func FromEnvironment(ctx context.Context, pool *pgxpool.Pool, instance string) (*Service, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	path := os.Getenv("AEON_DELIVERY_ADOPTION_FILE")
	if path == "" {
		return New(pool, Config{Instance: instance, Artifact: "unconfigured"}, nil, nil)
	}
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".json" || strings.Contains(path, "/secrets/") || strings.Contains(path, "/Secrets/") {
		return nil, errors.New("adoption evidence must be an absolute non-secret JSON file")
	}
	raw, err := readPrivate(path, 1<<20)
	if err != nil {
		return nil, errors.New("adoption release evidence is unavailable or not private")
	}
	var file RuntimeFile
	if err = strictJSON(raw, &file); err != nil {
		return nil, errors.New("adoption release evidence failed validation")
	}
	if file.Rollout.Instance != instance || !filepath.IsAbs(file.ProviderCommand) || len(file.ProviderCommand) > 512 || !filepath.IsAbs(file.ReportsDirectory) || len(file.ReportsDirectory) > 512 {
		return nil, ErrPrerequisite
	}
	// Product history/version data always comes from this immutable binary,
	// never a mutable file path or caller-supplied counter. Resolve AEON's exact
	// project through the same configured key as history and recurrences.
	history, err := releasehistory.Embedded()
	if err != nil {
		return nil, err
	}
	historyRaw, err := json.Marshal(history)
	if err != nil {
		return nil, err
	}
	versionRaw := paimos.ReleaseMetadata()
	var version struct {
		Product  string `json:"product"`
		Sequence int    `json:"release_sequence"`
	}
	if err = json.Unmarshal(versionRaw, &version); err != nil || version.Product != "PAIMOS AEON" || version.Sequence < 1 || history.Product != "PAIMOS AEON" || history.Repository != "inspr-at/paimos" {
		return nil, ErrPrerequisite
	}
	product := Product{Repository: history.Repository, HistoryDigest: sum(historyRaw), VersionDigest: sum(versionRaw), VersionSequence: version.Sequence}
	for _, r := range history.Releases {
		product.HistorySequences = append(product.HistorySequences, r.ReleaseSequence)
	}
	service, err := New(pool, file.Rollout, CommandProvider{file.ProviderCommand}, FileReports{file.ReportsDirectory})
	if err != nil {
		return nil, err
	}
	bindings := 0
	for _, a := range service.cfg.Authorities {
		err = service.with(ctx, a.Executor, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1))='AEON' ORDER BY n.id LIMIT 2`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if err = rows.Scan(&product.ProjectID); err != nil {
					return err
				}
				product.TenantID = a.Executor.TenantID
				bindings++
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}
	// Missing/ambiguous binding does not seed anybody with product history.
	// Would-be AEON projects get an explicit refusal from their dry-run report.
	if bindings != 1 {
		product.TenantID = ""
		product.ProjectID = ""
	}
	service.cfg.Product = product
	return service, nil
}
