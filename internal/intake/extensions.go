// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	extensionKeyPattern     = regexp.MustCompile(`^x-[a-z0-9]+(\.[a-z0-9-]+)+@(0|[1-9][0-9]*)$`)
	extensionVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// Registry/schema and RFC 8785 size validation stay in Aithema. This native
// boundary validates the envelope without interpreting or rewriting its data.
// The existing 1 MiB request cap leaves room for a 64 KiB canonical map, JSON
// escaping and native projection fields; no 65,536-character cap applies here.
func validateExtensions(in *draftWrite) error {
	if in.DocumentBytes != nil {
		raw, err := snapshotExtensions(*in.DocumentBytes, in.Kind)
		if err != nil {
			return err
		}
		if len(in.Extensions) > 0 && !bytes.Equal(in.Extensions, raw) {
			return fail(http.StatusBadRequest, "extensions do not match the snapshot bytes")
		}
		in.Extensions = raw
	}
	if len(in.Extensions) == 0 {
		return nil
	}
	var instances map[string]json.RawMessage
	if !utf8.Valid(in.Extensions) || json.Unmarshal(in.Extensions, &instances) != nil || instances == nil || len(instances) > 8 {
		return fail(http.StatusBadRequest, "invalid extension map")
	}
	for key, raw := range instances {
		namespace, major, _ := strings.Cut(key, "@")
		if len(namespace) > 128 || !extensionKeyPattern.MatchString(key) {
			return fail(http.StatusBadRequest, "invalid extension namespace")
		}
		var instance struct {
			Version string          `json:"version"`
			Data    json.RawMessage `json:"data"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&instance) != nil || len(instance.Version) > 33 || !extensionVersionPattern.MatchString(instance.Version) ||
			!strings.HasPrefix(instance.Version, major+".") || len(instance.Data) == 0 {
			return fail(http.StatusBadRequest, "invalid extension instance")
		}
	}
	return nil
}

// The native projection accompanies the unchanged document, rather than
// replacing the Aithema adapter protocol. Only its one submission candidate
// supplies extensions; historical versions never overwrite that candidate.
func snapshotExtensions(document, draftKind string) (json.RawMessage, error) {
	var snapshot struct {
		Contract  string `json:"contract"`
		Major     int    `json:"major"`
		Minor     *int   `json:"minor"`
		MinReader *int   `json:"min_reader"`
		HostMode  string `json:"host_mode"`
		Spec      struct {
			Items []struct {
				Kind       string          `json:"kind"`
				State      string          `json:"state"`
				Host       json.RawMessage `json:"host"`
				Extensions json.RawMessage `json:"extensions"`
			} `json:"items"`
		} `json:"spec"`
	}
	if !utf8.ValidString(document) || json.Unmarshal([]byte(document), &snapshot) != nil || snapshot.Contract != "aithema.spec.snapshot" ||
		snapshot.Major != 1 || snapshot.Minor == nil || snapshot.MinReader == nil || *snapshot.Minor < 0 ||
		*snapshot.MinReader < 0 || *snapshot.MinReader > *snapshot.Minor || *snapshot.MinReader > 1 || snapshot.HostMode != "review" {
		return nil, fail(http.StatusBadRequest, "invalid Aithema review snapshot")
	}
	var extensions json.RawMessage
	count := 0
	for _, item := range snapshot.Spec.Items {
		if item.State != "confirmed" || !bytes.Equal(bytes.TrimSpace(item.Host), []byte("null")) {
			continue
		}
		kind := "brief"
		if item.Kind == "requirement" {
			kind = "requirement"
		} else if item.Kind != "constraint" {
			return nil, fail(http.StatusBadRequest, "invalid snapshot item kind")
		}
		if kind != draftKind {
			return nil, fail(http.StatusBadRequest, "snapshot item kind does not match the draft")
		}
		count++
		extensions = item.Extensions
	}
	if count != 1 {
		return nil, fail(http.StatusBadRequest, "snapshot needs one confirmed unbound item")
	}
	return extensions, nil
}

func extensionText(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
