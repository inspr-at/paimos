// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Instances names this deployment and its configured siblings. Connections
// belong to people; browser-held credentials never cross server boundaries.
type Instances struct {
	// Here is this instance's short tag (AEON_INSTANCE_TAG, e.g. "ppm").
	Here string
	// Siblings are the other instances (AEON_SIBLING_INSTANCES,
	// "pma=https://pm.example.com"), in the configured order.
	Siblings []Sibling
}

// Sibling is one other instance: its tag and exact web origin.
type Sibling struct {
	Tag    string `json:"tag"`
	Origin string `json:"origin"`
}

// MaxSiblings bounds the browser's reads per Goals view.
const MaxSiblings = 4

var instanceTag = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// Origins lists the sibling origins, the only cross-site callers a sibling
// summary answers.
func (i Instances) Origins() []string {
	origins := make([]string, 0, len(i.Siblings))
	for _, sibling := range i.Siblings {
		origins = append(origins, sibling.Origin)
	}
	return origins
}

// ParseInstances validates the tags and origins. Siblings need a tag for this
// instance; prod origins must be HTTPS. An origin is scheme://host[:port] only.
func ParseInstances(here, siblings string, dev bool) (Instances, error) {
	out := Instances{Here: strings.TrimSpace(here)}
	if out.Here != "" && !instanceTag.MatchString(out.Here) {
		return Instances{}, fmt.Errorf("AEON_INSTANCE_TAG must match %s", instanceTag)
	}
	raw := strings.TrimSpace(siblings)
	if raw == "" {
		return out, nil
	}
	if out.Here == "" {
		return Instances{}, fmt.Errorf("AEON_SIBLING_INSTANCES needs AEON_INSTANCE_TAG")
	}
	seen := map[string]bool{out.Here: true}
	for _, entry := range strings.Split(raw, ",") {
		tag, origin, ok := strings.Cut(strings.TrimSpace(entry), "=")
		tag, origin = strings.TrimSpace(tag), strings.TrimSpace(origin)
		if !ok || !instanceTag.MatchString(tag) {
			return Instances{}, fmt.Errorf("AEON_SIBLING_INSTANCES entries are tag=origin with a tag matching %s", instanceTag)
		}
		if seen[tag] || seen[origin] {
			return Instances{}, fmt.Errorf("AEON_SIBLING_INSTANCES repeats %q", tag)
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
			(u.Scheme != "https" && !(dev && u.Scheme == "http")) || origin != u.Scheme+"://"+u.Host {
			return Instances{}, fmt.Errorf("AEON_SIBLING_INSTANCES origin for %q must be an exact https://host[:port] origin", tag)
		}
		seen[tag], seen[origin] = true, true
		out.Siblings = append(out.Siblings, Sibling{Tag: tag, Origin: origin})
	}
	if len(out.Siblings) > MaxSiblings {
		return Instances{}, fmt.Errorf("AEON_SIBLING_INSTANCES names at most %d siblings", MaxSiblings)
	}
	return out, nil
}
