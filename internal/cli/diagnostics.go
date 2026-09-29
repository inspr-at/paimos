// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type cliVersion struct {
	Version string `json:"version"`
	Scheme  string `json:"scheme"`
	Brand   struct {
		Wordmark string `json:"wordmark"`
		Product  string `json:"product"`
	} `json:"brand"`
}

type cliSchema struct {
	Version     string                         `json:"version"`
	Enums       map[string][]string            `json:"enums"`
	Transitions map[string]map[string][]string `json:"transitions"`
	Entities    map[string]map[string]any      `json:"entities"`
	EnumFields  map[string]string              `json:"enum_fields"`
	Conventions map[string]string              `json:"conventions"`
}

func (rt *runtime) nodeSchema() (cliSchema, error) {
	var v cliVersion
	if err := rt.do(http.MethodGet, "/api/version", nil, &v); err != nil {
		return cliSchema{}, err
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := rt.do(http.MethodGet, "/api/kinds", nil, &page); err != nil {
		return cliSchema{}, err
	}
	entities := map[string]map[string]any{}
	for _, item := range page.Items {
		slug, _ := item["slug"].(string)
		if slug != "" {
			entities[slug] = item
		}
	}
	return cliSchema{Version: v.Version, Enums: map[string][]string{"relation": {"blocks", "relates", "implements", "cites", "duplicates", "customer_of", "contact_for"}}, Transitions: map[string]map[string][]string{}, Entities: entities, EnumFields: map[string]string{}, Conventions: map[string]string{"node": "tenant-configured kind schema", "version_scheme": v.Scheme}}, nil
}

func (rt *runtime) cmdSchema() *Command {
	var refresh bool
	return &Command{Name: "schema", Short: "Print live node schema", Use: "schema [--refresh]", addFlags: func(fs *flagSet) { fs.bool(&refresh, "refresh", 0, "fetch fresh from server") }, run: func([]string) error {
		_ = refresh // Aeon reads live schema on each invocation.
		s, err := rt.nodeSchema()
		if err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(s)
		}
		inst, err := rt.resolve()
		if err != nil {
			return err
		}
		fmt.Fprintf(rt.stdout, "instance: %s (%s)\nversion:  %s\n", inst.Name, inst.URL, s.Version)
		keys := make([]string, 0, len(s.Entities))
		for slug := range s.Entities {
			keys = append(keys, slug)
		}
		sort.Strings(keys)
		for _, slug := range keys {
			fmt.Fprintf(rt.stdout, "kind %s\n", slug)
		}
		return nil
	}}
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (rt *runtime) cmdDoctor() *Command {
	return &Command{Name: "doctor", Short: "Run read-only preflight checks", Use: "doctor", run: func([]string) error {
		checks := []doctorCheck{}
		inst, err := rt.resolve()
		if err != nil {
			checks = append(checks, doctorCheck{Name: "config", Status: "fail", Detail: err.Error()})
			return rt.renderDoctor(checks)
		}
		checks = append(checks, doctorCheck{Name: "config", Status: "ok", Detail: "instance=" + inst.Name + " url=" + inst.URL})
		var health struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		}
		if err := rt.do(http.MethodGet, "/api/health", nil, &health); err != nil {
			checks = append(checks, doctorCheck{Name: "health", Status: "fail", Detail: err.Error()})
			return rt.renderDoctor(checks)
		}
		if health.Status != "ok" || health.DB != "ok" {
			checks = append(checks, doctorCheck{Name: "health", Status: "fail", Detail: "status=" + health.Status + " db=" + health.DB})
			return rt.renderDoctor(checks)
		}
		var v cliVersion
		if err := rt.do(http.MethodGet, "/api/version", nil, &v); err != nil {
			checks = append(checks, doctorCheck{Name: "health", Status: "fail", Detail: err.Error()})
			return rt.renderDoctor(checks)
		}
		brand := strings.TrimSpace(v.Brand.Wordmark)
		if brand == "" {
			brand = v.Brand.Product
		}
		checks = append(checks, doctorCheck{Name: "health", Status: "ok", Detail: "service=" + brand + " version=" + v.Version})
		var me struct {
			Principal struct {
				Name string `json:"name"`
			} `json:"principal"`
		}
		if err := rt.do(http.MethodGet, "/api/me", nil, &me); err != nil {
			checks = append(checks, doctorCheck{Name: "auth", Status: "fail", Detail: "API key rejected or auth unavailable"})
			return rt.renderDoctor(checks)
		}
		checks = append(checks, doctorCheck{Name: "auth", Status: "ok", Detail: "user=" + me.Principal.Name})
		s, err := rt.nodeSchema()
		if err != nil {
			checks = append(checks, doctorCheck{Name: "schema", Status: "fail", Detail: err.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: "schema", Status: "ok", Detail: fmt.Sprintf("version=%s kinds=%d", s.Version, len(s.Entities))})
		}
		checks = append(checks, rt.rulesChannelCheck())
		return rt.renderDoctor(checks)
	}}
}

func (rt *runtime) renderDoctor(checks []doctorCheck) error {
	if rt.jsonOut {
		if err := rt.printJSON(checks); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(rt.stdout, "%s doctor — read-only preflight\n", rt.program)
		for _, c := range checks {
			fmt.Fprintf(rt.stdout, "  %-12s %-5s — %s\n", c.Name, c.Status, c.Detail)
		}
	}
	for _, c := range checks {
		if c.Status == "fail" {
			return &exitError{code: 2}
		}
		if c.Status == "warn" {
			return &exitError{code: 1}
		}
	}
	return nil
}
