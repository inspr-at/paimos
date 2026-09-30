// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// A valid PG2 corpus using AEON-319's normalization: these NFKC lowercase
// accented words remain accented. The new normalizer strips their accents.
// Encode the old layout independently; never manufacture it with new marshal.
func oldNormalizerCorpus(key []byte) []byte {
	id := guardFingerprint(key)
	raw := append([]byte{'P', 'G', 2}, id[:]...)
	raw = appendU32(raw, 1)
	h := hashWords(key, []string{"café", "notebooks", "remain", "under", "the", "staircase"})
	raw = append(raw, h[:]...)
	raw = appendU32(raw, 0)  // whole entries
	return appendU32(raw, 0) // shingle entries
}

func TestGuardNormalizerAndPolicyVersion(t *testing.T) {
	key := testGuardMaster()
	if _, err := unmarshalGuard(oldNormalizerCorpus(key), key); err == nil {
		t.Fatal("old normalizer accepted")
	}
	raw, err := quoteCorpus(guardRule).marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{11, 43} {
		changed := bytes.Clone(raw)
		changed[offset] ^= 1
		if _, err := unmarshalGuard(changed, key); err == nil {
			t.Fatalf("version at %d ignored", offset)
		}
	}
	policy := map[string]string{"assets/logo.png": strings.Repeat("a", 64)}
	if _, err := unmarshalGuard(raw, key, policy); err == nil {
		t.Fatal("changed policy did not invalidate guard")
	}
}

func TestOldNormalizerBlocksUntilStartupRebuild(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	private := seedPrivateGuard(t, f, m, owner)
	for _, kind := range []string{"PG2", "different table or code", "different allowlist"} {
		t.Run(kind, func(t *testing.T) {
			var raw []byte
			if kind == "PG2" {
				raw = oldNormalizerCorpus(m.guardKey(owner.TenantID))
			} else {
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT corpus FROM doctrine_private_guard WHERE source_id=$1`, private.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				offset := 11
				if kind == "different allowlist" {
					offset = 43
				}
				raw[offset] ^= 1
			}
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_private_guard SET corpus=$2 WHERE source_id=$1`, private.ID, raw); err != nil {
				t.Fatal(err)
			}
			calls, minted := forge.calls, forge.minted
			got := f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 422)
			if !bytes.Contains(got, []byte("private_index_unavailable")) || forge.calls != calls || forge.minted != minted {
				t.Fatal("old corpus accepted or performed network I/O")
			}
			m.EnsurePrivateGuards(t.Context())
			if forge.calls == calls {
				t.Fatal("startup failed to rebuild old corpus")
			}
			calls, minted = forge.calls, forge.minted
			quoted := in
			quoted.Explanation = guardRule
			got = f.call(owner, "POST", "/api/rules/doctrine/proposals", quoted, 422)
			if !bytes.Contains(got, []byte("private_doctrine")) || forge.calls != calls || forge.minted != minted {
				t.Fatal("rebuilt guard missed quote or used network")
			}
			m.EnsurePrivateGuards(t.Context())
			if forge.calls != calls {
				t.Fatal("current corpus rebuilt unnecessarily")
			}
		})
	}
	f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
}

func TestCachedMainExemptionFailsClosedWithoutNetwork(t *testing.T) {
	for _, cache := range []string{"missing", "stale", "future", "wrong pin", "malformed"} {
		t.Run(cache, func(t *testing.T) {
			f, forge, m, owner, in := publicProposalFixture(t)
			seedPrivateGuard(t, f, m, owner)
			query := `DELETE FROM doctrine_public_main_cache WHERE source_id=$1`
			switch cache {
			case "stale":
				query = `UPDATE doctrine_public_main_cache SET observed_at=clock_timestamp()-interval '6 minutes' WHERE source_id=$1`
			case "future":
				query = `UPDATE doctrine_public_main_cache SET observed_at=clock_timestamp()+interval '6 minutes' WHERE source_id=$1`
			case "wrong pin":
				query = `UPDATE doctrine_public_main_cache SET pin_commit=repeat('f',40) WHERE source_id=$1`
			case "malformed":
				query = `UPDATE doctrine_public_main_cache SET tree='[1]' WHERE source_id=$1`
			}
			if _, err := f.d.Admin.Exec(t.Context(), query, in.SourceID); err != nil {
				t.Fatal(err)
			}
			calls, minted := forge.calls, forge.minted
			bad := in
			bad.Explanation = guardRule
			got := f.call(owner, "POST", "/api/rules/doctrine/proposals", bad, 422)
			if !bytes.Contains(got, []byte("public_main_unavailable")) || forge.calls != calls || forge.minted != minted {
				t.Fatal("cache miss fetched main or minted token")
			}
			// No private match needs no exemption cache. It reaches ordinary PR
			// preparation and succeeds even when no guard main read is possible.
			f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
		})
	}
}

func TestVerifiedMainSharedTextCanBeProposed(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	seedPrivateGuard(t, f, m, owner)
	files := fixtureFiles()
	files["docs/AGENTS-DOMAIN-DEV.md"] += "\n- " + guardRule + "\n"
	f.fake.commit(publicRepository, fixtureCommit, files, "main")
	f.call(owner, "POST", "/api/rules/doctrine/sources/"+in.SourceID+"/index", nil, 200)
	in.Explanation = guardRule
	f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
	if forge.minted != 1 || len(forge.pulls) != 1 {
		t.Fatal("verified public main exemption failed")
	}
}

func TestPublicMainCacheTenantIsolation(t *testing.T) {
	f, _, _, owner, in := publicProposalFixture(t)
	other := f.tenant("cache-other")
	for _, tid := range []string{owner.TenantID, other} {
		var count int
		err := db.InTenant(t.Context(), f.d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_public_main_cache WHERE source_id=$1`, in.SourceID).Scan(&count)
		})
		want := 0
		if tid == owner.TenantID {
			want = 1
		}
		if err != nil || count != want {
			t.Fatal("public main cache crossed tenant boundaries")
		}
	}
}

func TestNonTextExceptionsBindExactBytesAndPath(t *testing.T) {
	for _, raw := range [][]byte{
		{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		append([]byte{0x89, 'P', 'N', 'G', 0}, []byte("café\nnotebooks\nstay\nhidden\ntoday")...),
		append([]byte{0x1f, 0x8b, 0}, []byte("short wrapped\nprivate\ntext")...),
		utf16BEString(guardRule), {0xff, 0xfe}, {'h', 0, 'i'}, {1}, {0x7f},
	} {
		policy := map[string]string{"assets/reviewed.bin": fmt.Sprintf("%x", sha256.Sum256(raw))}
		if classifyPrivateBlob("assets/reviewed.bin", raw) != blobReject {
			t.Fatal("non-text accepted by default")
		}
		if classifyPrivateBlob("assets/reviewed.bin", raw, policy) != blobSkip {
			t.Fatal("reviewed bytes rejected")
		}
		if classifyPrivateBlob("assets/other.bin", raw, policy) != blobReject {
			t.Fatal("path exception widened")
		}
		if classifyPrivateBlob("assets/reviewed.bin", append(bytes.Clone(raw), 'x'), policy) != blobReject {
			t.Fatal("digest exception widened")
		}
		docs := map[string][]byte{"docs/OK.md": []byte(guardRule), "assets/reviewed.bin": raw}
		_, err := readPrivateCorpus(t.Context(), memReader{files: docs}, privateRepository, fixtureCommit, testGuardMaster())
		if err == nil || !strings.Contains(err.Error(), "assets/reviewed.bin") || strings.Contains(err.Error(), "notebooks") {
			t.Fatal("non-text refusal did not report only the path")
		}
	}
}
