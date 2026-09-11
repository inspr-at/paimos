// SPDX-License-Identifier: AGPL-3.0-only
package offerpdf

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Opt-in fixture uses the built shared renderer and never sends mail.
func TestOfferPDFBundledRenderer(t *testing.T) {
	if os.Getenv("OFFER_PDF_SMOKE") != "1" {
		t.Skip("requires built frontend and Chromium")
	}
	raw, err := os.ReadFile(os.Getenv("OFFER_PDF_SMOKE_INPUT"))
	if err != nil {
		t.Fatal(err)
	}
	var offer any
	if err = json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	pdf, err := Render(context.Background(), offer, "https://example.test/offers/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("OFFER_PDF_SMOKE_OUTPUT"), pdf, 0600); err != nil {
		t.Fatal(err)
	}
}
