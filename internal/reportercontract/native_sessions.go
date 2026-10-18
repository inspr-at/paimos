// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"gopkg.in/yaml.v3"
)

const NativeSession = nativecontract.ContractVersion

// NativeSessionPin includes the stream variants as well as JSON and disabled
// responses, including denials before the handler. Current includes this pin
// alongside the legacy surfaces without changing their versions or shapes.
func NativeSessionPin(openAPI []byte) (Pin, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(openAPI, &doc); err != nil {
		return Pin{}, err
	}
	schemas, err := child(doc, "components", "schemas")
	if err != nil {
		return Pin{}, err
	}
	version, err := child(doc, "components", "headers", "NativeSessionContract", "schema")
	if err != nil {
		return Pin{}, err
	}
	values, ok := version["enum"].([]any)
	if !ok || len(values) != 1 || values[0] != NativeSession {
		return Pin{}, fmt.Errorf("native-session: invalid contract header")
	}
	requests, err := requestComponents(doc)
	if err != nil {
		return Pin{}, err
	}
	shape, requestPaths := map[string]any{}, map[string]bool{}
	for _, route := range nativecontract.PublicRoutes() {
		path, method := strings.TrimPrefix(route.Path, "/api"), strings.ToLower(route.Method)
		responses, err := child(doc, "paths", path, method, "responses")
		if err != nil {
			return Pin{}, err
		}
		for _, status := range []string{"200", "202", "401", "403", "503"} {
			if _, ok := responses[status]; !ok {
				continue
			}
			response, err := child(responses, status)
			if err != nil {
				return Pin{}, err
			}
			header, err := child(response, "headers", Header)
			if err != nil {
				return Pin{}, err
			}
			if header["$ref"] != "#/components/headers/NativeSessionContract" {
				return Pin{}, fmt.Errorf("native-session: missing header on %s %s", method, path)
			}
			content, err := child(response, "content")
			if err != nil {
				return Pin{}, err
			}
			for mime := range content {
				body, err := child(content, mime, "schema")
				if err != nil {
					return Pin{}, err
				}
				label := route.Method + " " + path + " " + status
				if mime != "application/json" {
					label += " " + mime
				}
				expander := schemaExpander{schemas: schemas, seen: map[string]bool{}, requests: requests, requestPaths: requestPaths}
				expanded, err := expander.expand(body, pointerChild("", label))
				if err != nil {
					return Pin{}, err
				}
				shape[label] = expanded
			}
		}
	}
	encoded, err := json.Marshal(shape)
	if err != nil {
		return Pin{}, err
	}
	sum := sha256.Sum256(encoded)
	pin := Pin{Version: NativeSession, SHA256: hex.EncodeToString(sum[:]), Shape: encoded}
	for path := range requestPaths {
		pin.RequestPaths = append(pin.RequestPaths, path)
	}
	sort.Strings(pin.RequestPaths)
	return pin, nil
}
