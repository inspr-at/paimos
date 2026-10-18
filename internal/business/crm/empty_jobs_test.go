// SPDX-License-Identifier: AGPL-3.0-only

package crm

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/plugins"
)

func TestCRMBackgroundJobsJSONIsEmptyArray(t *testing.T) {
	plug, err := Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	stored, ok := reg.Lookup(ID)
	if !ok {
		t.Fatal("missing plugin")
	}
	if stored.Manifest.DigestSHA256 != plug.Manifest.DigestSHA256 {
		t.Fatalf("lookup changed digest %q", stored.Manifest.DigestSHA256)
	}
	raw, err := json.Marshal(stored.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"background_jobs":[]`)) || bytes.Contains(raw, []byte(`"background_jobs":null`)) {
		t.Fatalf("background_jobs: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"allowed_child_kinds":[]`)) || bytes.Contains(raw, []byte(`"allowed_child_kinds":null`)) {
		t.Fatalf("allowed_child_kinds: %s", raw)
	}
}
