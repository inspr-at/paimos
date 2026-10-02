// SPDX-License-Identifier: AGPL-3.0-only
package quoteshowcase

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type blobLockTraceKey struct{}

// Stop just after apply obtains its first blob lock, without a production hook.
// The competing publisher must be waiting on a database lock before we resume.
type firstBlobLockBarrier struct {
	once           sync.Once
	locked, resume chan struct{}
}

func (b *firstBlobLockBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, blobLockTraceKey{}, strings.Contains(data.SQL, "pg_advisory_xact_lock(hashtextextended($1,557))"))
}

func (b *firstBlobLockBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if matched, _ := ctx.Value(blobLockTraceKey{}).(bool); !matched || data.Err != nil {
		return
	}
	b.once.Do(func() {
		close(b.locked)
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	})
}

func TestProfileAndShowcaseBlobLockInterleaving(t *testing.T) {
	for _, composite := range []bool{false, true} {
		name := "profile"
		if composite {
			name = "showcase"
		}
		t.Run(name, func(t *testing.T) {
			database := dbtest.Open(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			tenantID, err := tenantbootstrap.Create(ctx, database.App, "lock-order", "Lock order")
			if err != nil {
				t.Fatal(err)
			}
			var actorID string
			if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND 'operator'=ANY(roles) LIMIT 1`).Scan(&actorID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id)
				 VALUES($1,1,'Europe/Vienna','EUR','{"company":"Synthetic","street":"Street 1","postal_code":"8010","city":"Graz","country":"AT","email":"test@example.invalid"}','{}','{}',$2)`, tenantID, actorID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			store := attachments.Store{FilesDir: t.TempDir()}
			var staged []*attachments.Staged
			bodies := map[string][]byte{}
			for _, shade := range []uint8{32, 128} {
				img := image.NewRGBA(image.Rect(0, 0, 2, 2))
				img.SetRGBA(0, 0, color.RGBA{R: shade, A: 255})
				var body bytes.Buffer
				if err := png.Encode(&body, img); err != nil {
					t.Fatal(err)
				}
				raw := append([]byte(nil), body.Bytes()...)
				blob, err := store.Stage(ctx, tenantID, &body)
				if err != nil {
					t.Fatal(err)
				}
				defer blob.Close()
				staged = append(staged, blob)
				bodies[blob.SHA256] = raw
			}
			if staged[0].SHA256 > staged[1].SHA256 {
				staged[0], staged[1] = staged[1], staged[0]
			}
			fixture, err := os.ReadFile(filepath.Join(acmeDir(t), "profiles", "acme-english.json"))
			if err != nil {
				t.Fatal(err)
			}
			makeProfile := func(name string, files map[string][]byte) ProfileSpec {
				var payload map[string]any
				if err := json.Unmarshal(fixture, &payload); err != nil {
					t.Fatal(err)
				}
				payload["name"] = name
				definition := payload["definition"].(map[string]any)
				if _, ok := files["a.png"]; ok {
					definition["cover"].(map[string]any)["brand_asset_id"] = "a.png"
				}
				if _, ok := files["z.png"]; ok {
					definition["footer"].(map[string]any)["asset_id"] = "z.png"
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				return ProfileSpec{Name: name, Raw: raw, Files: files}
			}
			high, low := bodies[staged[1].SHA256], bodies[staged[0].SHA256]
			profiles := []ProfileSpec{makeProfile("First profile", map[string][]byte{"a.png": high, "z.png": low})}
			if composite {
				profiles = []ProfileSpec{makeProfile("First profile", map[string][]byte{"a.png": high}), makeProfile("Second profile", map[string][]byte{"z.png": low})}
			}
			barrier := &firstBlobLockBarrier{locked: make(chan struct{}), resume: make(chan struct{})}
			cfg := database.App.Config()
			cfg.ConnConfig.Tracer = barrier
			applyPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer applyPool.Close()
			// Cancel before pool.Close on every failure, releasing the tracer barrier.
			defer cancel()
			applyDone := make(chan error, 1)
			go func() {
				if composite {
					_, err := Apply(ctx, applyPool, tenantID, actorID, store.FilesDir, Bundle{Profiles: profiles}, nil, nil, true)
					applyDone <- err
				} else {
					_, err := quotes.ApplyProfileBundle(ctx, applyPool, tenantID, actorID, store.FilesDir, "", quotes.ProfileBundle{Profile: profiles[0].Raw, Files: profiles[0].Files}, false, true)
					applyDone <- err
				}
			}()
			select {
			case <-barrier.locked:
			case err := <-applyDone:
				t.Fatalf("apply before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			publishDone := make(chan error, 1)
			publisherPID := make(chan uint32, 1)
			go func() {
				publishDone <- db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
					publisherPID <- tx.Conn().PgConn().PID()
					if err := attachments.Publish(ctx, tx, attachments.OwnerAvatar, staged...); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,avatar_original_hash,avatar_hashes) VALUES($1,$2,$3,jsonb_build_object('32',$4::text))`, tenantID, actorID, staged[0].SHA256, staged[1].SHA256); err != nil {
						return err
					}
					_, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Agent}, events.Change{Type: "profile.updated", After: map[string]any{"avatar_original_hash": staged[0].SHA256, "avatar_hashes": map[string]string{"32": staged[1].SHA256}}})
					return err
				})
			}()
			var pid uint32
			select {
			case pid = <-publisherPID:
			case err := <-publishDone:
				t.Fatalf("publish before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for {
				var waiting bool
				if err := database.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-publishDone:
					t.Fatalf("publish did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
				}
			}
			// Old path-order locking holds high here while the avatar holds low.
			// Resuming then deadlocks (40P01). Sorted transaction-wide locking lets
			// apply finish while the avatar waits on low, then the avatar commits.
			close(barrier.resume)
			if err := <-applyDone; err != nil {
				t.Fatalf("apply: %v", err)
			}
			if err := <-publishDone; err != nil {
				t.Fatalf("publish: %v", err)
			}
			for _, blob := range staged {
				file, err := store.Open(tenantID, blob.SHA256, "original")
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(file)
				file.Close()
				if err != nil || !bytes.Equal(got, bodies[blob.SHA256]) {
					t.Fatalf("blob changed: %v", err)
				}
			}
			verified, err := attachments.Verify(ctx, database.App, store, tenantID)
			if err != nil || verified.Checked != 2 || len(verified.Issues) != 0 {
				t.Fatalf("verify: %+v %v", verified, err)
			}
		})
	}
}
