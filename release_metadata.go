// SPDX-License-Identifier: AGPL-3.0-only
package aeon

import _ "embed"

//go:embed version.json
var releaseMetadata []byte

// ReleaseMetadata returns the immutable version source shipped in this binary.
// Adoption uses its digest/sequence only for the explicitly bound product.
func ReleaseMetadata() []byte { return append([]byte(nil), releaseMetadata...) }
