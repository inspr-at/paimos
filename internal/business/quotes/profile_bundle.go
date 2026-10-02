// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ProfileBundle contains the POST /api/quote-profiles payload and its files.
// In profile.json, asset_id fields hold relative bundle paths; ApplyProfileBundle
// replaces those paths with tenant asset IDs before using the API validator.
type ProfileBundle struct {
	Profile json.RawMessage
	Files   map[string][]byte
}

const maxProfileBundle = 64 << 20

var profileSourceInstance = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func safeBundlePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

// ReadProfileBundleDir rejects symlinks and files outside the bundle root.
func ReadProfileBundleDir(root string) (ProfileBundle, error) {
	files := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = filepath.ToSlash(name)
		if !safeBundlePath(name) || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid bundle path %q", name)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("nonregular bundle file %q", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 10<<20 || total+info.Size() > maxProfileBundle {
			return errors.New("profile bundle exceeds size limit")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		total += int64(len(data))
		files[name] = data
		return nil
	})
	if err != nil {
		return ProfileBundle{}, err
	}
	return makeProfileBundle(files)
}

// ReadProfileBundleTar accepts regular files and directories only, with no
// absolute paths, traversal, links, duplicate entries or oversized payloads.
func ReadProfileBundleTar(src io.Reader) (ProfileBundle, error) {
	files := map[string][]byte{}
	reader := tar.NewReader(io.LimitReader(src, maxProfileBundle+1))
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ProfileBundle{}, err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !safeBundlePath(name) {
			return ProfileBundle{}, fmt.Errorf("invalid bundle path %q", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return ProfileBundle{}, fmt.Errorf("nonregular bundle file %q", name)
		}
		if _, exists := files[name]; exists || header.Size < 0 || header.Size > 10<<20 || total+header.Size > maxProfileBundle {
			return ProfileBundle{}, fmt.Errorf("duplicate or oversized bundle file %q", name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return ProfileBundle{}, fmt.Errorf("read bundle file %q: %w", name, err)
		}
		total += int64(len(data))
		files[name] = data
	}
	return makeProfileBundle(files)
}

func makeProfileBundle(files map[string][]byte) (ProfileBundle, error) {
	profile := files["profile.json"]
	if len(profile) == 0 || len(profile) > 1<<20 {
		return ProfileBundle{}, errors.New("bundle requires profile.json (at most 1 MiB)")
	}
	delete(files, "profile.json")
	return ProfileBundle{Profile: profile, Files: files}, nil
}

type profileBundleReport struct {
	Applied        bool   `json:"applied"`
	Name           string `json:"name"`
	Action         string `json:"action"`
	ProfileID      string `json:"profile_id,omitempty"`
	Revision       int    `json:"revision"`
	AssetsCreated  int    `json:"assets_created"`
	DefaultChange  bool   `json:"default_change"`
	DraftsChanged  int    `json:"drafts_changed"`
	SourceInstance string `json:"source_instance,omitempty"`
	RefreshDrafts  bool   `json:"refresh_drafts,omitempty"`
}

type bundleAsset struct {
	path, kind, hash string
	raw              []byte
	id               string
}

// ApplyProfileBundle plans or applies a tenant profile. Every read and write
// runs in db.InTenant. Apply is one database transaction; immutable content
// files may remain unreferenced if the transaction fails.
func ApplyProfileBundle(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, filesDir, sourceInstance string, bundle ProfileBundle, makeDefault, apply bool) (profileBundleReport, error) {
	return applyProfileBundle(ctx, pool, tenantID, actorID, filesDir, sourceInstance, bundle, makeDefault, false, apply)
}

// ApplyProfileBundleRefreshingDrafts applies the bundle like ApplyProfileBundle
// and then points every live draft quote that already uses this profile at the
// current revision, with one quote.profile_selected event each. Drafts already
// on the current revision are left alone, so a second run changes nothing.
// It repairs drafts that hold a stale or malformed snapshot (AEON-274).
func ApplyProfileBundleRefreshingDrafts(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, filesDir, sourceInstance string, bundle ProfileBundle, makeDefault, apply bool) (profileBundleReport, error) {
	return applyProfileBundle(ctx, pool, tenantID, actorID, filesDir, sourceInstance, bundle, makeDefault, true, apply)
}

// prepareProfileBundle validates and addresses every referenced asset without
// writing files or taking locks. It is also used by composite transactions.
func prepareProfileBundle(bundle ProfileBundle) (in profileWrite, assets map[string]*bundleAsset, paths []string, err error) {
	decoder := json.NewDecoder(bytes.NewReader(bundle.Profile))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, nil, nil, fmt.Errorf("profile.json: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || in.ExpectedRevision != 0 {
		return in, nil, nil, errors.New("profile.json must contain one creation payload without expected_revision")
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 100 {
		return in, nil, nil, errors.New("invalid profile name")
	}
	assets = map[string]*bundleAsset{}
	use := func(ref *string) error {
		if *ref == "" {
			return nil
		}
		if !safeBundlePath(*ref) || *ref == "profile.json" {
			return fmt.Errorf("invalid asset path %q", *ref)
		}
		asset := assets[*ref]
		if asset == nil {
			raw, exists := bundle.Files[*ref]
			if !exists || len(raw) == 0 || len(raw) > 10<<20 {
				return fmt.Errorf("missing or oversized asset %q", *ref)
			}
			kind := profileAssetType(raw)
			if kind == "" {
				return fmt.Errorf("invalid profile asset %q", *ref)
			}
			hash := sha256.Sum256(raw)
			asset = &bundleAsset{path: *ref, raw: raw, kind: kind, hash: hex.EncodeToString(hash[:])}
			assets[*ref] = asset
		}
		return nil
	}
	for i := range in.Definition.Fonts {
		if err := use(&in.Definition.Fonts[i].AssetID); err != nil {
			return in, nil, nil, err
		}
	}
	for _, ref := range []string{in.Definition.Cover["brand_asset_id"], in.Definition.Footer.AssetID, in.Definition.Footer.DotsAssetID} {
		if err := use(&ref); err != nil {
			return in, nil, nil, err
		}
	}
	paths = make([]string, 0, len(assets))
	for p := range assets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return in, assets, paths, nil
}

// LockProfileBundles reserves every profile's domain and blob locks before a
// composite apply. Call inside db.InTransaction, before the first profile apply
// or event append, and pass the same transaction context to subsequent applies.
func LockProfileBundles(ctx context.Context, pool *pgxpool.Pool, tenantID string, bundles ...ProfileBundle) error {
	var names, hashes []string
	for _, bundle := range bundles {
		in, assets, _, err := prepareProfileBundle(bundle)
		if err != nil {
			return err
		}
		names = append(names, in.Name)
		for _, asset := range assets {
			hashes = append(hashes, asset.hash)
		}
	}
	sort.Strings(names)
	return db.InTenant(db.AllProjects(ctx, "quote profile bundle locks"), pool, tenantID, func(tx pgx.Tx) error {
		// Every apply takes the tenant lock before profile and blob locks.
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, tenantID).Scan(&locked); err != nil {
			return err
		}
		for _, name := range names {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":quote-profile:"+name); err != nil {
				return err
			}
		}
		return attachments.LockBlobs(ctx, tx, tenantID, hashes...)
	})
}

func applyProfileBundle(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, filesDir, sourceInstance string, bundle ProfileBundle, makeDefault, refreshDrafts, apply bool) (profileBundleReport, error) {
	report := profileBundleReport{Applied: apply, SourceInstance: sourceInstance, RefreshDrafts: refreshDrafts}
	if pool == nil || !uuidRe.MatchString(tenantID) || (actorID != "" && !uuidRe.MatchString(actorID)) || (sourceInstance != "" && !profileSourceInstance.MatchString(sourceInstance)) {
		return report, errors.New("invalid tenant, actor or source instance")
	}
	in, assets, paths, err := prepareProfileBundle(bundle)
	if err != nil {
		return report, err
	}
	report.Name = in.Name
	err = db.InTenant(db.AllProjects(ctx, "quote profile bundle"), pool, tenantID, func(tx pgx.Tx) error {
		if apply {
			// Settings PATCH takes this same tenant lock. It also serializes
			// this command's default change with concurrent profile applies.
			var locked string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, tenantID).Scan(&locked); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":quote-profile:"+in.Name); err != nil {
				return err
			}
			if actorID == "" {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND 'operator'=ANY(roles) ORDER BY created_at,id LIMIT 1`).Scan(&actorID); err != nil {
					return fmt.Errorf("operator actor unavailable; pass --actor-principal-id: %w", err)
				}
			}
			var kind, status string
			var operator bool
			if err := tx.QueryRow(ctx, `SELECT kind,status,kind='agent' AND 'operator'=ANY(roles) FROM principals WHERE id=$1::uuid`, actorID).Scan(&kind, &status, &operator); err != nil {
				return err
			}
			if status != "active" {
				return errors.New("actor is inactive")
			}
			if !operator {
				if kind != "person" || authz.RequireTx(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Person}, "quotes.manage", authz.Scope{}) != nil {
					return errors.New("actor requires quote management permission")
				}
			}
		}
		actor := tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Person}
		if actorID != "" {
			var kind string
			if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1::uuid`, actorID).Scan(&kind); err != nil {
				return err
			}
			if kind == "agent" {
				actor.Kind = tenant.Agent
			}
		}
		if apply {
			hashes := make([]string, 0, len(assets))
			for _, asset := range assets {
				hashes = append(hashes, asset.hash)
			}
			if err := attachments.LockBlobs(ctx, tx, tenantID, hashes...); err != nil {
				return err
			}
		}
		for _, p := range paths {
			a := assets[p]
			err := tx.QueryRow(ctx, `SELECT id::text FROM quote_document_profile_assets WHERE sha256=$1 AND content_type=$2 ORDER BY created_at,id LIMIT 1`, a.hash, a.kind).Scan(&a.id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				report.AssetsCreated++
				if apply {
					prepared, err := putProfileAsset(ctx, tx, tenantID, a.raw, a.kind, filesDir)
					if err != nil {
						return fmt.Errorf("store asset %q: %w", p, err)
					}
					if prepared.SHA256 != a.hash {
						return fmt.Errorf("stored asset %q has wrong digest", p)
					}
					if err := tx.QueryRow(ctx, `INSERT INTO quote_document_profile_assets(tenant_id,sha256,content_type,size,created_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5::uuid) RETURNING id::text`, tenantID, a.hash, a.kind, len(a.raw), actorID).Scan(&a.id); err != nil {
						return err
					}
					if err := appendEvent(ctx, tx, actor, "", "quote.profile_asset_uploaded", nil, map[string]any{"asset_id": a.id, "sha256": a.hash, "content_type": a.kind}); err != nil {
						return err
					}
				} else {
					a.id = syntheticAssetID(a.hash)
				}
			} else if apply {
				if file, err := (attachments.Store{FilesDir: filesDir}).Open(tenantID, a.hash, "original"); err != nil {
					if _, err = putProfileAsset(ctx, tx, tenantID, a.raw, a.kind, filesDir); err != nil {
						return err
					}
				} else if err := file.Close(); err != nil {
					return err
				}
			}
		}
		definition := in.Definition.normalized()
		// Never nil: a nil slice is stored as null and crashed the quote page (AEON-274).
		definition.Fonts = append([]profileFont{}, in.Definition.Fonts...)
		definition.Cover = make(map[string]string, len(in.Definition.Cover))
		for k, v := range in.Definition.Cover {
			definition.Cover[k] = v
		}
		for i := range definition.Fonts {
			definition.Fonts[i].AssetID = assets[definition.Fonts[i].AssetID].id
		}
		if ref := definition.Cover["brand_asset_id"]; ref != "" {
			definition.Cover["brand_asset_id"] = assets[ref].id
		}
		if definition.Footer.AssetID != "" {
			definition.Footer.AssetID = assets[definition.Footer.AssetID].id
		}
		if definition.Footer.DotsAssetID != "" {
			definition.Footer.DotsAssetID = assets[definition.Footer.DotsAssetID].id
		}
		assetType := func(id string) (string, error) {
			for _, a := range assets {
				if a.id == id {
					return a.kind, nil
				}
			}
			return "", pgx.ErrNoRows
		}
		if err := validateProfileWithAssets(definition, assetType); err != nil {
			return err
		}
		var currentID string
		var currentRev int
		var currentRaw []byte
		var archived bool
		profileQuery := `SELECT p.id::text,p.current_revision,r.definition,p.archived_at IS NOT NULL FROM quote_document_profiles p JOIN quote_document_profile_revisions r ON r.profile_id=p.id AND r.revision=p.current_revision WHERE p.name=$1 ORDER BY p.id`
		if apply {
			profileQuery += ` FOR UPDATE OF p`
		}
		rows, err := tx.Query(ctx, profileQuery, in.Name)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			count++
			if err := rows.Scan(&currentID, &currentRev, &currentRaw, &archived); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count > 1 {
			return errors.New("multiple profiles have this name")
		}
		if archived {
			return errors.New("profile with this name is archived")
		}
		newRaw, err := marshalProfile(definition)
		if err != nil {
			return err
		}
		report.ProfileID = currentID
		report.Revision = currentRev
		report.Action = "unchanged"
		if count == 0 {
			report.Action, report.Revision = "create", 1
		} else {
			var prior any
			var next any
			if err := json.Unmarshal(currentRaw, &prior); err != nil {
				return err
			}
			if err := json.Unmarshal(newRaw, &next); err != nil {
				return err
			}
			priorBytes, _ := json.Marshal(prior)
			nextBytes, _ := json.Marshal(next)
			if !bytes.Equal(priorBytes, nextBytes) {
				report.Action, report.Revision = "update", currentRev+1
			}
		}
		if apply && report.Action != "unchanged" {
			if report.Action == "create" {
				if err := tx.QueryRow(ctx, `INSERT INTO quote_document_profiles(tenant_id,name,current_revision) VALUES($1::uuid,$2,1) RETURNING id::text`, tenantID, in.Name).Scan(&report.ProfileID); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(ctx, `UPDATE quote_document_profiles SET current_revision=$2,updated_at=clock_timestamp() WHERE id=$1::uuid`, report.ProfileID, report.Revision); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO quote_document_profile_revisions(tenant_id,profile_id,revision,name,definition,created_by_principal_id) VALUES($1::uuid,$2::uuid,$3,$4,$5::jsonb,$6::uuid)`, tenantID, report.ProfileID, report.Revision, in.Name, string(newRaw), actorID); err != nil {
				return err
			}
			if err := appendEvent(ctx, tx, actor, "", "quote.profile_saved", map[string]any{"profile_id": report.ProfileID, "revision": currentRev}, map[string]any{"profile_id": report.ProfileID, "revision": report.Revision}); err != nil {
				return err
			}
		}
		if makeDefault {
			settings, err := readSettings(ctx, tx)
			if err != nil {
				return err
			}
			if settings.Revision == 0 {
				return errors.New("quote settings must be configured before --default")
			}
			report.DefaultChange = settings.DefaultProfileID != report.ProfileID || report.ProfileID == ""
			if apply && report.DefaultChange {
				_, err = tx.Exec(ctx, `UPDATE quote_settings SET default_profile_id=$2::uuid,revision=revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$3::uuid WHERE tenant_id=$1::uuid`, tenantID, report.ProfileID, actorID)
				if err != nil {
					return err
				}
				if err := appendEvent(ctx, tx, actor, "", "quote.settings_updated", map[string]any{"default_profile_id": settings.DefaultProfileID, "revision": settings.Revision}, map[string]any{"default_profile_id": report.ProfileID, "revision": settings.Revision + 1}); err != nil {
					return err
				}
			}
		}
		if sourceInstance != "" || (refreshDrafts && report.ProfileID != "") {
			// Imported drafts from one source instance, and with refreshDrafts
			// every live draft whose snapshot already names this profile.
			rows, err := tx.Query(ctx, `SELECT d.quote_node_id::text FROM quote_drafts d JOIN business_quotes q ON q.quote_node_id=d.quote_node_id WHERE q.state='draft' AND q.deleted_at IS NULL AND q.archived_at IS NULL AND (
				($1<>'' AND EXISTS (SELECT 1 FROM paimos_offer_imports i WHERE i.node_id=d.quote_node_id AND i.source_instance=$1 AND i.source_kind='offer'))
				OR ($2 AND d.document->'profile'->>'id'=$3)) ORDER BY d.quote_node_id`, sourceInstance, refreshDrafts, report.ProfileID)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range ids {
				if apply {
					quote, err := readQuote(ctx, tx, id, true)
					if err != nil {
						return err
					}
					if quote.State != "draft" || quote.Archived {
						continue
					}
				}
				draft, err := readDraft(ctx, tx, id, apply)
				if err != nil {
					return err
				}
				doc, err := decodeDocument(draft.Document)
				if err != nil {
					return err
				}
				if doc.Profile != nil && doc.Profile.ID == report.ProfileID && doc.Profile.Revision == report.Revision && report.ProfileID != "" {
					continue
				}
				report.DraftsChanged++
				if !apply {
					continue
				}
				before := doc.Profile
				doc.Profile = &documentProfileSnapshot{ID: report.ProfileID, Revision: report.Revision, Definition: definition}
				raw, err := marshalDraft(doc)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE quote_drafts SET document=$1::jsonb,draft_revision=draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$2::uuid WHERE quote_node_id=$3::uuid`, string(raw), actorID, id); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE business_quotes SET revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
					return err
				}
				if err := appendEvent(ctx, tx, actor, id, "quote.profile_selected", map[string]any{"profile": before, "draft_revision": draft.DraftRevision}, map[string]any{"profile_id": report.ProfileID, "profile_revision": report.Revision, "draft_revision": draft.DraftRevision + 1}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return report, err
}

func syntheticAssetID(hash string) string {
	return hash[:8] + "-" + hash[8:12] + "-4" + hash[13:16] + "-8" + hash[17:20] + "-" + hash[20:32]
}
