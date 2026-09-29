// SPDX-License-Identifier: AGPL-3.0-only

package deploytarget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Target records the destination and change a person reviews for deployment.
type Target struct {
	Hosts       []string `json:"hosts,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Service     string   `json:"service"`
	Image       string   `json:"image,omitempty"`
	Change      string   `json:"change"`
}

// Normalize returns the canonical target and its SHA-256 digest. The digest is
// over the complete JSON target, including the destination and change.
// Omission is supported for compatibility; the digest is informational.
func Normalize(in *Target) (*Target, string, error) {
	if in == nil {
		return nil, "", nil
	}
	out := *in
	out.Hosts = slices.Clone(in.Hosts)
	out.Environment = strings.TrimSpace(out.Environment)
	out.Service = strings.TrimSpace(out.Service)
	out.Image = strings.TrimSpace(out.Image)
	out.Change = strings.TrimSpace(out.Change)
	if len(out.Hosts) > 32 || len(out.Environment) > 128 || len(out.Service) > 128 || len(out.Image) > 512 || len(out.Change) > 1000 {
		return nil, "", errors.New("deploy target is too long")
	}
	for i := range out.Hosts {
		out.Hosts[i] = strings.TrimSpace(out.Hosts[i])
		if out.Hosts[i] == "" || len(out.Hosts[i]) > 255 {
			return nil, "", errors.New("deploy host is invalid")
		}
	}
	slices.Sort(out.Hosts)
	for i := 1; i < len(out.Hosts); i++ {
		if out.Hosts[i] == out.Hosts[i-1] {
			return nil, "", errors.New("deploy hosts must be distinct")
		}
	}
	if len(out.Hosts) == 0 && out.Environment == "" || out.Service == "" || out.Change == "" {
		return nil, "", errors.New("deploy target requires host or environment, service, and change")
	}
	for _, s := range append(append([]string{}, out.Hosts...), out.Environment, out.Service, out.Image, out.Change) {
		if strings.ContainsRune(s, 0) {
			return nil, "", errors.New("deploy target contains invalid text")
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(encoded)
	return &out, hex.EncodeToString(sum[:]), nil
}
