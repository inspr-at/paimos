// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PairingNixGuide is public display data set only by the deployment admin.
// The pairing command is deliberately not configurable.
type PairingNixGuide struct {
	ModuleURL     string   `json:"module_url"`
	ServiceOption string   `json:"service_option"`
	Platforms     []string `json:"platforms"`
	ServiceNote   string   `json:"service_note"`
}

func parsePairingNixGuide(raw string) (*PairingNixGuide, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := errors.New("AEON_PAIRING_NIX_GUIDE_JSON must contain a public HTTPS module_url, service_option, platforms (darwin and/or linux), and service_note; commands and extra fields are not allowed")
	if len(raw) > 8192 {
		return nil, invalid
	}
	var guide PairingNixGuide
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&guide) != nil || d.Decode(new(any)) != io.EOF || guide.Validate() != nil {
		return nil, invalid
	}
	return &guide, nil
}

// Validate also protects callers that construct configuration directly.
func (g PairingNixGuide) Validate() error {
	invalid := errors.New("invalid public Nix pairing guide")
	text := func(s string, max int) bool {
		return s != "" && len(s) <= max && strings.TrimSpace(s) == s && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
	}
	u, err := url.Parse(g.ModuleURL)
	if !text(g.ModuleURL, 2000) || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return invalid
	}
	if !text(g.ServiceOption, 200) || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)+$`).MatchString(g.ServiceOption) || !text(g.ServiceNote, 1000) {
		return invalid
	}
	if len(g.Platforms) < 1 || len(g.Platforms) > 2 {
		return invalid
	}
	seen := map[string]bool{}
	for _, platform := range g.Platforms {
		if (platform != "darwin" && platform != "linux") || seen[platform] {
			return invalid
		}
		seen[platform] = true
	}
	return nil
}
