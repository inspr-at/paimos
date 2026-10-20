// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

func (rt *runtime) cmdRulesExport() *Command { return rt.rulesExportCommand(rt.api) }

func (rt *runtime) rulesExportCommand(connect func() (*client.Client, error)) *Command {
	var out string
	return &Command{Name: "export", Short: "Export published database rule history and its proposed git destinations", Use: "export [--out NEW-DIRECTORY]", minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&out, "out", 0, "new private directory for one JSON file per layer and manifest.json; defaults to JSON on stdout")
		},
		run: func([]string) error {
			api, err := connect()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var bundle rules.ExportBundle
			if err = api.Do(ctx, http.MethodGet, "/api/rules/export", nil, &bundle); err != nil {
				return rt.fail(err, api.Token)
			}
			if out == "" {
				return rt.printJSON(bundle)
			}
			// Validate all remote paths and checksums before creating anything locally.
			if bundle.Manifest.Format != "aeon.rules-export.v1" || !rulesUUID.MatchString(bundle.Manifest.TenantID) || len(bundle.Files) != len(bundle.Manifest.Layers) {
				return fmt.Errorf("invalid export manifest")
			}
			names := map[string]bool{"manifest.json": true}
			total := 0
			for _, f := range bundle.Files {
				total += len(f.Content)
				if total > rules.MaxExportBytes {
					return fmt.Errorf("export exceeds 16 MiB")
				}
			}
			for _, f := range bundle.Files {
				if filepath.Base(f.Path) != f.Path || !strings.HasPrefix(f.Path, "layer-") || !strings.HasSuffix(f.Path, ".json") || !rulesUUID.MatchString(strings.TrimSuffix(strings.TrimPrefix(f.Path, "layer-"), ".json")) {
					return fmt.Errorf("invalid export file name")
				}
				sum := sha256.Sum256([]byte(f.Content))
				if hex.EncodeToString(sum[:]) != f.SHA256 {
					return fmt.Errorf("export checksum mismatch")
				}
				if names[f.Path] {
					return fmt.Errorf("duplicate export file name")
				}
				names[f.Path] = true
			}
			if err = os.Mkdir(out, 0700); err != nil {
				return err
			}
			for _, f := range bundle.Files {
				if err = rules.WriteFile(filepath.Join(out, f.Path), []byte(f.Content), false); err != nil {
					return fmt.Errorf("partial local export: %w", err)
				}
			}
			raw, err := json.MarshalIndent(bundle.Manifest, "", "  ")
			if err != nil {
				return err
			}
			if err = rules.WriteFile(filepath.Join(out, "manifest.json"), append(raw, '\n'), false); err != nil {
				return fmt.Errorf("partial local export: %w", err)
			}
			return rt.printJSON(map[string]any{"directory": out, "layers": len(bundle.Files)})
		}}
}
