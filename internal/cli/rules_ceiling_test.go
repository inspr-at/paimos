// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

func TestRulesClientReceivesAndRendersByteCeiling(t *testing.T) {
	c := rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex"}
	floor := "- [safety] Preserve safety.\n"
	for _, size := range []int{12000, 12001, 64000, 64001} {
		body := rules.SessionHeader + floor
		body += strings.Repeat("<", size-len(body)-1) + "\n"
		sum := sha256.Sum256([]byte(body))
		m := rules.Merged{Context: c, Body: body, Floor: floor, ByteSize: len(body), SHA256: hex.EncodeToString(sum[:]), Version: "260929120000.0.0", Versions: []rules.VersionRef{{SetID: "10000000-0000-4000-8000-000000000005", Version: "260929120000.0.0", SHA256: strings.Repeat("a", 64)}}, Rules: []rules.Rule{}}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(rules.ClientMaximumHeader) != strconv.Itoa(rules.SessionFileLimit(c.Harness)) {
				t.Error("receive omitted the harness read limit")
			}
			json.NewEncoder(w).Encode(m)
		}))
		api := client.New(srv.URL, "fixture")
		o := rulesOptions{Cache: filepath.Join(t.TempDir(), "rules.json")}
		got, _, _, err := rulesReceiveFetch(api, c, o, floor)
		srv.Close()
		if (err == nil) != (size <= rules.MaxBytes) {
			t.Fatalf("receive at %d: %v", size, err)
		}
		if err != nil {
			continue
		}
		if got.Body != m.Body {
			t.Fatal("receive changed bytes")
		}
		cached, err := rules.ReadFile(o.Cache, rules.MaxCacheBytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rules.DecodeCache(cached, api.BaseURL, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, h := range rules.Harnesses {
			got.Context.Harness = h
			rendered, err := renderRulesThroughHarness(got)
			if err != nil || rendered.Body != body {
				t.Fatal("renderer changed bytes", h, err)
			}
		}
	}
}
