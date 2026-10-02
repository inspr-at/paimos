// SPDX-License-Identifier: AGPL-3.0-only
package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ClassicSource is a GET-only classic API reader. NewClassicSource accepts the
// same --source-url and --api-key-file values as the existing importer.
type ClassicSource struct {
	base   *url.URL
	key    string
	client *http.Client
}

func NewClassicSource(rawURL, keyFile string, client *http.Client) (*ClassicSource, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid source URL")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read api-key-file: %w", err)
	}
	secret := strings.TrimSpace(string(key))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("invalid api-key-file")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ClassicSource{base: u, key: secret, client: &copyClient}, nil
}
func (s *ClassicSource) get(ctx context.Context, resource string, limit int64) ([]byte, error) {
	u := *s.base
	u.Path = strings.TrimSuffix(u.Path, "/") + resource
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("classic GET failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("classic GET returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("classic response too large")
	}
	return b, nil
}
func (s *ClassicSource) users(ctx context.Context) ([]map[string]any, error) {
	b, err := s.get(ctx, "/api/users", 16<<20)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, errors.New("invalid classic users response")
	}
	return out, nil
}
func (s *ClassicSource) avatar(ctx context.Context, p string) ([]byte, error) {
	clean := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if !strings.HasPrefix(clean, "/static/") {
		clean = path.Join("/static", clean)
	}
	if strings.Contains(p, "..") || strings.ContainsAny(p, "?#") || (clean != "/static" && !strings.HasPrefix(clean, "/static/")) {
		return nil, errors.New("invalid classic avatar path")
	}
	return s.get(ctx, clean, maxAvatarBytes)
}

type ImportReport struct {
	Users      int `json:"users"`
	Matched    int `json:"matched"`
	WouldWrite int `json:"would_write"`
	Written    int `json:"written"`
	Skipped    int `json:"skipped"`
}

// ProfileImporter is the coordinator's command constructor. Run defaults to a
// read-only dry run; apply=true is required for Aeon writes. Classic sees GETs
// only. Each changed target is saved with an R1 event in db.InTenant.
type ProfileImporter struct {
	Pool   *pgxpool.Pool
	Store  attachments.Store
	Source *ClassicSource
}

func (job ProfileImporter) Run(ctx context.Context, tenantSlug string, apply bool) (ImportReport, error) {
	var report ImportReport
	if job.Pool == nil || job.Source == nil || tenantSlug == "" {
		return report, errors.New("pool, source and tenant slug required")
	}
	// Operator CLI, no principal: profiles are workspace rows (ADR-003 P2).
	ctx = db.NoProjects(ctx, "classic profile import")
	// tenants is the one global table; keep even this lookup inside InTenant.
	var tenantID string
	err := db.InTenant(ctx, job.Pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, tenantSlug).Scan(&tenantID)
	})
	if err != nil {
		return report, err
	}
	actorID := ""
	if apply {
		err = db.InTenant(ctx, job.Pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE tenant_id=$1 AND kind='agent' AND name='Classic Paimos importer' ORDER BY created_at LIMIT 1`, tenantID).Scan(&actorID)
		})
		if err != nil {
			return report, fmt.Errorf("importer principal (run paimos import paimos first): %w", err)
		}
	}
	users, err := job.Source.users(ctx)
	if err != nil {
		return report, err
	}
	report.Users = len(users)
	for _, user := range users {
		email, _ := user["email"].(string)
		email = strings.TrimSpace(email)
		if email == "" {
			report.Skipped++
			continue
		}
		var principalID string
		err = db.InTenant(ctx, job.Pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT DISTINCT coalesce(p.linked_to,p.id)::text
				FROM principals p LEFT JOIN identities i ON i.id=p.identity_id
				WHERE p.tenant_id=$1 AND p.kind='person'
				  AND translate(coalesce(i.email,p.email,''),'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz') COLLATE "C"
				      =translate($2,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz')`, tenantID, email)
			if err != nil {
				return err
			}
			defer rows.Close()
			ids := map[string]bool{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids[id] = true
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if len(ids) != 1 {
				return nil
			}
			for id := range ids {
				principalID = id
			}
			return nil
		})
		if err != nil {
			return report, err
		}
		if principalID == "" {
			report.Skipped++
			continue
		}
		report.Matched++
		patch := map[string]json.RawMessage{}
		for source, target := range map[string]string{"first_name": "first_name", "last_name": "last_name", "nickname": "preferred_name", "username": "short_name", "timezone": "timezone", "locale": "locale"} {
			if value, ok := user[source].(string); ok && value != "" {
				value = strings.TrimSpace(value)
				raw, _ := json.Marshal(value)
				if validateField(target, raw) == "" {
					patch[target] = raw
				}
			}
		}
		avatarPath, _ := user["avatar_path"].(string)
		var before State
		err = db.InTenant(ctx, job.Pool, tenantID, func(tx pgx.Tx) error {
			var err error
			before, _, err = read(ctx, tx, tenantID, principalID, false)
			return err
		})
		if err != nil {
			return report, err
		}
		after := before
		applyPatch(&after, patch)
		if same(before, after) && avatarPath == "" {
			continue
		}
		if !apply {
			report.WouldWrite++
			continue
		}
		var staged []*attachments.Staged
		closeStaged := func() {
			for _, blob := range staged {
				_ = blob.Close()
			}
		}
		if avatarPath != "" {
			body, err := job.Source.avatar(ctx, avatarPath)
			if err != nil {
				return report, err
			}
			cfg, format, err := imageConfig(body)
			if err != nil {
				return report, err
			}
			if format == "jpeg" {
				orientation := jpegOrientation(body)
				if orientation >= 5 {
					cfg.Width, cfg.Height = cfg.Height, cfg.Width
				}
			}
			size := cfg.Width
			if cfg.Height < size {
				size = cfg.Height
			}
			crop := Crop{X: (cfg.Width - size) / 2, Y: (cfg.Height - size) / 2, Size: size}
			original, variants, err := ProcessAvatar(body, crop)
			if err != nil {
				return report, err
			}
			store := job.Store
			store.MaxSize = 0
			saved, err := store.Stage(ctx, tenantID, bytes.NewReader(original))
			if err != nil {
				return report, err
			}
			staged = append(staged, saved)
			after.AvatarOriginalHash = saved.SHA256
			after.AvatarHashes = map[string]string{}
			for size, b := range variants {
				saved, err := store.Stage(ctx, tenantID, bytes.NewReader(b))
				if err != nil {
					closeStaged()
					return report, err
				}
				staged = append(staged, saved)
				after.AvatarHashes[fmt.Sprint(size)] = saved.SHA256
			}
		}
		// Serialize target writes, re-read and apply the classic snapshot only to
		// fields present in the source. A replay with equal values appends no event.
		err = db.InTenant(ctx, job.Pool, tenantID, func(tx pgx.Tx) error {
			if err := ensurePerson(ctx, tx, tenantID, principalID); err != nil {
				return err
			}
			current, had, err := read(ctx, tx, tenantID, principalID, true)
			if err != nil {
				return err
			}
			next := current
			applyPatch(&next, patch)
			if avatarPath != "" {
				next.AvatarOriginalHash = after.AvatarOriginalHash
				next.AvatarHashes = after.AvatarHashes
			}
			if err := attachments.Publish(ctx, tx, attachments.OwnerAvatar, staged...); err != nil {
				return err
			}
			if same(current, next) {
				return nil
			}
			if err := change(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Agent}, current, had, next); err != nil {
				return err
			}
			report.Written++
			return nil
		})
		closeStaged()
		if err != nil {
			return report, err
		}
	}
	return report, nil
}
func imageConfig(data []byte) (image.Config, string, error) {
	return image.DecodeConfig(bytes.NewReader(data))
}
