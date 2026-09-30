// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/webp"
)

// Logo limits. The header draws a logo at most 32 px high and 160 px wide, so
// 32 px on the short side is the least that stays crisp, and a logo may be
// square, wide (up to 8:1) or a little tall (down to 1:2).
const (
	MaxLogoBytes = 256 << 10
	minLogoSide  = 32
	maxLogoSide  = 4096
	maxAspect    = 8.0
	minAspect    = 0.5
)

// Logo is a validated upload, ready to store.
type Logo struct {
	ContentType string
	Content     []byte
	Width       int
	Height      int
	// Cleaned reports that the SVG sanitizer removed something.
	Cleaned bool
}

var errLogoType = errors.New("expected a PNG, WebP or SVG image")

// sniff decides the type from the bytes; the declared type must agree.
func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	head := bytes.TrimLeft(b, "\xef\xbb\xbf \t\r\n")
	if bytes.HasPrefix(head, []byte("<")) && bytes.Contains(b[:min(len(b), 4096)], []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// ValidateLogo checks a raw upload. declared is the request's Content-Type.
func ValidateLogo(declared string, b []byte) (Logo, error) {
	if len(b) == 0 {
		return Logo{}, errors.New("the logo is empty")
	}
	if len(b) > MaxLogoBytes {
		return Logo{}, errors.New("the logo exceeds 256 KB")
	}
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	kind := sniff(b)
	if kind == "" {
		return Logo{}, errLogoType
	}
	if declared != kind {
		return Logo{}, fmt.Errorf("the file is %s, not %s", kind, declared)
	}
	logo := Logo{ContentType: kind}
	switch kind {
	case "image/png", "image/webp":
		decodeConfig, decode := png.DecodeConfig, png.Decode
		if kind == "image/webp" {
			decodeConfig, decode = webp.DecodeConfig, webp.Decode
		}
		cfg, err := decodeConfig(bytes.NewReader(b))
		if err != nil {
			return Logo{}, errors.New("the image cannot be read")
		}
		if err := checkSize(float64(cfg.Width), float64(cfg.Height), true); err != nil {
			return Logo{}, err
		}
		// A full decode proves the file is intact before anyone's browser sees it.
		if _, err := decode(bytes.NewReader(b)); err != nil {
			return Logo{}, errors.New("the image cannot be read")
		}
		logo.Content, logo.Width, logo.Height = b, cfg.Width, cfg.Height
	case "image/svg+xml":
		clean, cleaned, err := SanitizeSVG(b)
		if err != nil {
			return Logo{}, err
		}
		w, h, err := svgSize(clean)
		if err != nil {
			return Logo{}, err
		}
		if err := checkSize(w, h, false); err != nil {
			return Logo{}, err
		}
		// A vector has no pixels; its box is kept at its proportions, within the
		// stored bounds.
		scale := math.Min(1, maxLogoSide/math.Max(w, h))
		logo.Content, logo.Cleaned = clean, cleaned
		logo.Width, logo.Height = max(1, int(math.Round(w*scale))), max(1, int(math.Round(h*scale)))
	}
	return logo, nil
}

func checkSize(w, h float64, pixels bool) error {
	if pixels {
		if w < minLogoSide || h < minLogoSide {
			return fmt.Errorf("the logo is %.0f×%.0f px; each side needs at least %d px", w, h, minLogoSide)
		}
		if w > maxLogoSide || h > maxLogoSide {
			return fmt.Errorf("the logo is %.0f×%.0f px; each side may be at most %d px", w, h, maxLogoSide)
		}
	}
	if r := w / h; r > maxAspect || r < minAspect {
		return errors.New("the logo is too narrow or too tall; use one between 1:2 and 8:1")
	}
	return nil
}
