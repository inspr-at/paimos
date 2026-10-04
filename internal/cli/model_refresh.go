// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

func (rt *runtime) cmdModelRefresh() *Command {
	return &Command{Name: "refresh", Short: "Refresh model sightings with workspace settings", Use: "model refresh", maxArgs: 0, run: func([]string) error {
		var out modelregistry.RefreshResult
		if err := rt.do(http.MethodPost, "/api/models/refresh", nil, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		fmt.Fprintf(rt.stdout, "Model refresh: %d profiles added, %d proposed; ladders preserved\n", out.Added, out.Proposed)
		for _, source := range out.Sources {
			fmt.Fprintf(rt.stdout, "%s: %s (%d models)\n", source.Vendor, source.State, source.Seen)
		}
		return nil
	}}
}

func (rt *runtime) cmdModelReport() *Command {
	var path string
	return &Command{Name: "report", Short: "Report content-free CLI model evidence", Use: "model report --file observations.json", maxArgs: 0, addFlags: func(fs *flagSet) { fs.string(&path, "file", 0, "JSON observations file; - reads stdin") }, run: func([]string) error {
		if path == "" {
			return usagef("--file is required")
		}
		var reader io.Reader = rt.stdin
		if path != "-" {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		var in []modelregistry.Observation
		dec := json.NewDecoder(io.LimitReader(reader, 1<<20+1))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return usagef("invalid observations JSON")
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return usagef("expected one observations array")
		}
		var out modelregistry.ReportResult
		if err := rt.do(http.MethodPost, "/api/models/reports", in, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		fmt.Fprintf(rt.stdout, "Model evidence: %d recorded, %d profiles added, %d proposed\n", out.Recorded, out.Added, out.Proposed)
		return nil
	}}
}
