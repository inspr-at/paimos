// SPDX-License-Identifier: AGPL-3.0-only

package scopecode

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestSharedV1Vectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name          string
		Input, Scopes []string
		Code          string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			code, err := Encode(v.Input)
			if err != nil || code != v.Code {
				t.Fatalf("encode = %q, %v; want %q", code, err, v.Code)
			}
			keys, err := Decode(" \n" + code + "\t")
			if err != nil || !slices.Equal(keys, v.Scopes) {
				t.Fatalf("decode = %v, %v", keys, err)
			}
		})
	}
}

func TestInvalidV1Codes(t *testing.T) {
	valid, _ := Encode([]string{"events.read"})
	checksummed := func(payload string) string {
		text := Prefix + payload
		return fmt.Sprintf("%s:%08x", text, crc32.ChecksumIEEE([]byte(text)))
	}
	for _, code := range []string{
		"", "aeon-scopes:v2:events.read:00000000", strings.Replace(valid, "events", "nodes", 1), valid[:len(valid)-1], valid + "x", strings.ToUpper(valid),
		checksummed("nodes.write+read"), checksummed("nodes.read,events.read"), checksummed("nodes.read+read"), checksummed("nodes.read,nodes.write"),
		checksummed("nodes.read+"), checksummed("nodes..read"), checksummed("nodes.read,keys.*"), checksummed("évents.read"), checksummed("nodes.read\n"),
		strings.Repeat("a", MaxCodeBytes+1), checksummed("nodes." + strings.Repeat("a", 128)), checksummed("nodes." + strings.Repeat("read+", MaxScopes) + "write"),
	} {
		if _, err := Decode(code); err == nil {
			t.Errorf("accepted invalid code %q", code[:min(100, len(code))])
		}
	}
	for _, keys := range [][]string{{""}, {"*"}, {"nodes.*"}, {"nodes.read.extra"}, {"node s.read"}, make([]string, MaxScopes+1)} {
		if _, err := Encode(keys); err == nil {
			t.Fatal("accepted invalid scopes")
		}
	}
}

func FuzzCodeRoundTrip(f *testing.F) {
	f.Add("nodes.read,events.read")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		code, err := Encode(strings.Split(raw, ","))
		if err != nil {
			return
		}
		keys, err := Decode(code)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Encode(keys)
		if err != nil || again != code {
			t.Fatal("noncanonical round trip")
		}
	})
}
