// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package contracts

import (
	"crypto/sha256"
	"encoding/hex"
)

// ExternalStageV1FixtureDigestHex is the immutable lowercase SHA-256 of
// the canonical owner and dependency fixture set. The digest input is:
//
//	"paimos.external-stage.fixtures.v1\x00" followed, in lexical filename
//	order, by filename + "\x00" + exact fixture bytes + "\x00".
//
// The manifest is deliberately excluded so release-pin metadata can change
// without changing the adapter contract. Tests recompute this value from the
// exact committed bytes and fail on inventory, byte, or digest drift.
const ExternalStageV1FixtureDigestHex = "0318f4025902c9d5dd790384950cc9daebb16e02e79a4a90ce7dddc673e68bed"

// ExternalStageV2FixtureDigestHex is the immutable lowercase SHA-256 of the
// additive scheme-aware Pharos owner fixture set. Its domain and inventory are
// pinned independently from v1 so publishing v2 cannot alter v1 bytes.
const ExternalStageV2FixtureDigestHex = "a1d5575ffe9e84f984c212f47cf88d48fe3cf65383f34c2a6bc0dff897c5ae66"

// ExternalStageV1FixtureDigest returns a fresh fixed-size digest suitable for
// externalstage.Options. It cannot expose mutable package-level digest state.
func ExternalStageV1FixtureDigest() [sha256.Size]byte {
	return externalStageFixtureDigest(ExternalStageV1FixtureDigestHex)
}

func ExternalStageV2FixtureDigest() [sha256.Size]byte {
	return externalStageFixtureDigest(ExternalStageV2FixtureDigestHex)
}

func externalStageFixtureDigest(value string) [sha256.Size]byte {
	var digest [sha256.Size]byte
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(digest) {
		// The compile-time literal is covered by tests. Returning zero here keeps
		// a malformed future edit fail-closed in service option validation.
		return digest
	}
	copy(digest[:], raw)
	return digest
}
