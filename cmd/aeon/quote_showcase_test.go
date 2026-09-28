// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestQuoteShowcaseUsage(t *testing.T) {
	err := quoteShowcaseApply(context.Background(), nil, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || err.Error() != "usage: aeon quote-showcase apply --tenant SLUG --bundle DIR|- [--archive-quote UUID ...] [--actor-principal-id UUID] [--apply]" {
		t.Fatalf("usage: %v", err)
	}
}

func TestQuoteShowcaseOperatorCommand(t *testing.T) {
	database := dbtest.Open(t)
	ctx := context.Background()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "synthetic", "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	sender := []byte(`{"company":"INSPR GmbH","street":"Musterstraße 1","postal_code":"8010","city":"Graz","country":"Österreich","email":"quotes@inspr.example"}`)
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		for slug, prefix := range map[string]string{"organisation": "ORG", "contact": "CON", "quote": "QUO"} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,$3,$2)`, tenantID, slug, prefix); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id) SELECT $1::uuid,1,'Europe/Vienna','EUR',$2::jsonb,'{}'::jsonb,'{}'::jsonb,id FROM principals WHERE kind='agent' AND name='Tenant bootstrap'`, tenantID, sender)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	profile := []byte(`{"name":"Synthetic print","definition":{"schema":"inspr.document-profile.v1","layout_variant":"classic-v1","locale":"de-AT","fonts":[],"colors":{"ink":"#253335","muted":"#637477","soft":"#91a1a3","accent":"#287f78","rule":"#d5dfdf","paper":"#ffffff"},"typography":{"body_pt":"10"},"page":{"width_mm":"210","height_mm":"297","top_mm":"18","right_mm":"20","bottom_mm":"16","left_mm":"22"},"cover":{"top_mm":"11"},"sections":{"numbering":"upper-roman"},"positions_table":{"columns":[{"key":"position","width_mm":"9"},{"key":"description","width_mm":"71"},{"key":"quantity","width_mm":"15"},{"key":"unit","width_mm":"22"},{"key":"unit_price","width_mm":"24"},{"key":"total","width_mm":"27"}],"separator":"rule","repeat_header":true},"totals":{"vat":"note","discount":"hidden","net_label":"Net"},"payment_terms":{"position":"sections","heading":"Payment"},"acceptance":{"signature_columns":2,"gap_mm":"14","lead_mm":"28"},"footer":{"width_mm":"33","offset_mm":"0","page_number_format":"PAGE {page} OF {total}"},"labels":{"quote":"QUOTE"}}}`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), profile, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_DATABASE_URL", database.URL)
	t.Setenv("AEON_FILES_DIR", t.TempDir())
	t.Setenv("AEON_ENV", "dev")
	keyFile := filepath.Join(t.TempDir(), "link-key")
	if err := os.WriteFile(keyFile, []byte("showcase-link-key-material-32ch!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_LINK_KEY_FILE", keyFile)
	var out bytes.Buffer
	if err := quoteProfileApply(ctx, []string{"--tenant", "synthetic", "--bundle", dir, "--default", "--apply"}, bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	bundleDir := filepath.Join(filepath.Dir(file), "..", "..", "internal", "business", "quotes", "showcase", "acme-labs")
	out.Reset()
	if err := quoteShowcaseApply(ctx, []string{"--tenant", "synthetic", "--bundle", bundleDir}, bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	var planned map[string]any
	if err := json.Unmarshal(out.Bytes(), &planned); err != nil || planned["applied"] != false {
		t.Fatalf("dry-run: %s: %v", out.String(), err)
	}
	quotes, _ := planned["quotes"].([]any)
	organisations, _ := planned["organisations"].([]any)
	if len(quotes) != 5 || len(organisations) != 2 || !reportHasKey(quotes, "steinwender-belegleser") || !reportHasKey(organisations, "steinwender-metallbau") {
		t.Fatalf("dry-run quotes: %s", out.String())
	}
	var stream bytes.Buffer
	if err := writeBundleTar(bundleDir, &stream); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := quoteShowcaseApply(ctx, []string{"--tenant", "synthetic", "--bundle", "-", "--apply"}, &stream, &out); err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	if err := json.Unmarshal(out.Bytes(), &applied); err != nil || applied["applied"] != true {
		t.Fatalf("apply: %s: %v", out.String(), err)
	}
}

func reportHasKey(items []any, key string) bool {
	for _, item := range items {
		row, _ := item.(map[string]any)
		if row["key"] == key {
			return true
		}
	}
	return false
}

func writeBundleTar(root string, dest *bytes.Buffer) error {
	writer := tar.NewWriter(dest)
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}
