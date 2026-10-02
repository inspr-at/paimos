// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// providerForms are the DigitalOcean and Shopify shapes the round-3 review
// found stored in nomination excerpts: documented prefixes, lower-case hex.
func providerForms() map[string]string {
	sum := sha256.Sum256([]byte("AEON-288 round-3 synthetic noncredential fixture"))
	h := hex.EncodeToString(sum[:])
	return map[string]string{
		"digitalocean-pat":                 "dop" + "_v1_" + h,
		"digitalocean-access-token":        "doo" + "_v1_" + h,
		"shopify-access-token":             "shp" + "at_" + h[:32],
		"shopify-private-app-access-token": "shp" + "pa_" + h[:32],
	}
}

// The tagger never stores a provider token in a nomination excerpt, while
// the same incident without one is nominated.
func TestSensitiveProviderFormsNotStored(t *testing.T) {
	f := setup(t)
	control := commentLearningID(f.ticket, addComment(t, f, f.ticket, "Incident: the deploy failed because the provider token expired"))
	forms := providerForms()
	keys := map[string]string{}
	for id, token := range forms {
		keys[id] = commentLearningID(f.ticket, addComment(t, f, f.ticket, "Incident: "+token))
	}
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := nominationOfKey(t, f, control); !ok {
		t.Fatal("the control incident was not nominated")
	}
	for id, key := range keys {
		if _, excerpt, ok := nominationOfKey(t, f, key); ok {
			t.Errorf("%s stored (excerpt contains token: %v)", id, strings.Contains(excerpt, forms[id]))
		}
	}
	w := call(t, f, f.b, "GET", "/api/knowledge/learnings?project_id="+f.project, nil)
	expect(t, w, 200)
	for _, item := range decode[LearningPage](t, w).Items {
		for id, token := range forms {
			if strings.Contains(item.Text, token) {
				t.Errorf("%s visible in the learning inbox", id)
			}
		}
	}
}
