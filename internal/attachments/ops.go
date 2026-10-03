// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

type VerifyIssue struct {
	SHA256  string `json:"sha256"`
	Variant string `json:"variant"`
	Problem string `json:"problem"`
}
type VerifyReport struct {
	Checked int           `json:"checked"`
	Issues  []VerifyIssue `json:"issues"`
}

// Verify checks the complete tenant owner inventory, including undo/history.
func Verify(ctx context.Context, pool *pgxpool.Pool, store Store, tenantID string) (VerifyReport, error) {
	report := VerifyReport{Issues: []VerifyIssue{}}
	refs, err := references(ctx, pool, tenantID)
	if err != nil {
		return report, err
	}
	for hash, isImage := range refs {
		report.Checked++
		expected := map[string]string{"original": hash}
		if isImage {
			original, err := store.Open(tenantID, hash, "original")
			if err == nil {
				decoded, _, decodeErr := image.Decode(original)
				_ = original.Close()
				if decodeErr == nil {
					for _, spec := range []struct {
						name string
						max  int
					}{{"thumb", 320}, {"preview", 1600}} {
						var body bytes.Buffer
						if png.Encode(&body, resize(decoded, spec.max)) == nil {
							sum := sha256.Sum256(body.Bytes())
							expected[spec.name] = hex.EncodeToString(sum[:])
						}
					}
				} else {
					report.Issues = append(report.Issues, VerifyIssue{hash, "original", "invalid image"})
				}
			}
		}
		for _, variant := range []string{"original", "preview", "thumb"} {
			if variant != "original" && !isImage {
				continue
			}
			f, err := store.Open(tenantID, hash, variant)
			if err != nil {
				problem := "unreadable"
				if errors.Is(err, os.ErrNotExist) {
					problem = "missing"
				}
				report.Issues = append(report.Issues, VerifyIssue{hash, variant, problem})
				continue
			}
			h := sha256.New()
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil {
				report.Issues = append(report.Issues, VerifyIssue{hash, variant, "unreadable"})
				continue
			}
			if want := expected[variant]; want != "" && hex.EncodeToString(h.Sum(nil)) != want {
				report.Issues = append(report.Issues, VerifyIssue{hash, variant, "corrupt"})
			}
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}
	return report, nil
}
func references(ctx context.Context, pool *pgxpool.Pool, tenantID string) (map[string]bool, error) {
	if !validTenant(tenantID) {
		return nil, errors.New("invalid tenant")
	}
	refs := map[string]bool{}
	err := db.InTenant(db.AllProjects(ctx, "attachment files verify and gc"), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		refs, err = referencesTx(ctx, tx, tenantID, "")
		return err
	})
	return refs, err
}

type GCReport struct {
	Candidates int   `json:"candidates"`
	Removed    int   `json:"removed"`
	Bytes      int64 `json:"bytes"`
}

// GC examines only the selected tenant. apply=false is the dry-run default.
// A seven-day minimum age preserves the orphan grace period. Shared lifetime
// locks, rather than age, protect concurrent writers reusing an old digest.
func GC(ctx context.Context, pool *pgxpool.Pool, store Store, tenantID string, apply bool) (GCReport, error) {
	return gc(ctx, pool, store, tenantID, apply, os.Remove)
}

func gc(ctx context.Context, pool *pgxpool.Pool, store Store, tenantID string, apply bool, unlink func(string) error) (GCReport, error) {
	report := GCReport{}
	refs, err := references(ctx, pool, tenantID)
	if err != nil {
		return report, err
	}
	base := filepath.Join(store.root(), tenantID)
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		hash := strings.TrimSuffix(strings.TrimSuffix(name, ".thumb.png"), ".preview.png")
		spool := filepath.Dir(path) == filepath.Join(base, "incoming") && strings.HasPrefix(name, "upload-")
		_, referenced := refs[hash]
		if !spool && (!validHash(hash) || referenced) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) < 7*24*time.Hour {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		report.Candidates++
		report.Bytes += info.Size()
		if apply {
			if !spool {
				removed := false
				if err := db.InTenant(db.AllProjects(ctx, "attachment files verify and gc"), pool, tenantID, func(tx pgx.Tx) error {
					if err := lockBlob(ctx, tx, tenantID, hash); err != nil {
						return err
					}
					current, err := referencesTx(ctx, tx, tenantID, hash)
					if err != nil {
						return err
					}
					if _, live := current[hash]; live {
						return nil
					}
					// A writer may have recreated the file since discovery. Inspect
					// age again under the lock, and hold it THROUGH the unlink.
					latest, err := os.Lstat(path)
					if errors.Is(err, os.ErrNotExist) {
						return nil
					}
					if err != nil {
						return err
					}
					if !latest.Mode().IsRegular() || time.Since(latest.ModTime()) < 7*24*time.Hour {
						return nil
					}
					if err := unlink(path); err != nil {
						return err
					}
					removed = true
					return nil
				}); err != nil {
					return err
				}
				if !removed {
					report.Candidates--
					report.Bytes -= info.Size()
					return nil
				}
			} else if err := unlink(path); err != nil {
				return err
			}
			report.Removed++
		}
		return nil
	})
	return report, err
}
