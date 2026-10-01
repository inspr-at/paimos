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
// A raster logo also has at most 4 megapixels (4096×1024, or 2048×2048). That
// bounds the pixel buffer a decode allocates (4 bytes a pixel, 16 MiB at most),
// not the decoder's own tables: a lossless WebP sizes its Huffman groups from
// indexes in the file, so 96 bytes at 32×32 made golang.org/x/image v0.36.0
// allocate 167 MiB. Those tables are bounded by the decoder, x/image v0.45.0 or
// later (fewer than 2600 groups, and no more than the image has tiles beyond
// 1000); TestVP8LHuffmanBomb measures it, so a downgrade fails. Module admits
// one decode at a time on top of that.
const (
	MaxLogoBytes  = 256 << 10
	minLogoSide   = 32
	maxLogoSide   = 4096
	maxLogoPixels = 4 << 20
	maxAspect     = 8.0
	minAspect     = 0.5
)

// Logo is a validated upload, ready to store.
type Logo struct {
	ContentType string
	Content     []byte
	Width       int
	Height      int
	// Cleaned reports harmless SVG comments removed. Unsupported features reject.
	Cleaned bool
}

var errLogoType = errors.New("expected a PNG, WebP or SVG image")

// TooLargeError is a logo over MaxLogoBytes, as uploaded or once an SVG is
// rewritten; the API answers it with 413.
type TooLargeError struct{ msg string }

func (e TooLargeError) Error() string { return e.msg }

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
		return Logo{}, TooLargeError{"the logo exceeds 256 KB"}
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
		bounds, decode := pngBounds, png.Decode
		if kind == "image/webp" {
			bounds, decode = webpBounds, webp.Decode
		}
		w, h, err := bounds(b)
		if err != nil {
			return Logo{}, err
		}
		if err := checkSize(float64(w), float64(h), true); err != nil {
			return Logo{}, err
		}
		// Only now, within the bounds, a full decode proves the file is intact
		// and that the decoder saw the size the headers declare.
		img, err := decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			return Logo{}, errUnreadable
		}
		logo.Content, logo.Width, logo.Height = b, w, h
	case "image/svg+xml":
		clean, cleaned, err := SanitizeSVG(b)
		if err != nil {
			return Logo{}, err
		}
		// Rewriting can grow a file (<circle/> becomes <circle></circle>), so the
		// stored bytes are held to the same limit.
		if len(clean) > MaxLogoBytes {
			return Logo{}, TooLargeError{"the cleaned SVG exceeds 256 KB"}
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
		if w*h > maxLogoPixels {
			return fmt.Errorf("the logo is %.0f×%.0f px; it may have at most 4 megapixels, such as 4096×1024 or 2048×2048", w, h)
		}
	}
	if r := w / h; r > maxAspect || r < minAspect {
		return errors.New("the logo is too narrow or too tall; use one between 1:2 and 8:1")
	}
	return nil
}
