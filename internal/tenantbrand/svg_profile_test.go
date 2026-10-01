// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func logoSVG(body string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 40">` + body + `</svg>`)
}

// The second review bypassed explicit-reference bounds by inheriting a mask
// from the enclosing group, doubling the work at every level.
func inheritedMasks(levels int, property string, style bool) []byte {
	var b strings.Builder
	b.WriteString(`<defs>`)
	element := "mask"
	if property == "clip-path" {
		element = "clipPath"
	}
	fmt.Fprintf(&b, `<%s id="m0"><rect width="40" height="40"/></%s>`, element, element)
	for i := 1; i <= levels; i++ {
		inherited := property + `="inherit"`
		if style {
			inherited = `style="` + property + `:inherit"`
		}
		fmt.Fprintf(&b, `<g %s="url(#m%d)"><%s id="m%d"><rect width="20" height="40" %s/><rect x="20" width="20" height="40" %s/></%s></g>`, property, i-1, element, i, inherited, inherited, element)
	}
	fmt.Fprintf(&b, `</defs><rect width="40" height="40" %s="url(#m%d)"/>`, property, levels)
	return logoSVG(b.String())
}

func TestStaticSVGRejectsReferenceCapabilities(t *testing.T) {
	payloads := map[string][]byte{
		"round 1 use":            []byte(doubledUse(46)),
		"round 1 mask":           []byte(doubledMask(46)),
		"round 2 inherited mask": inheritedMasks(30, "mask", false),
		"round 2 styled mask":    inheritedMasks(30, "mask", true),
		"round 2 inherited clip": inheritedMasks(30, "clip-path", false),
		"round 2 styled clip":    inheritedMasks(30, "clip-path", true),
	}
	for _, element := range []string{"mask", "clipPath", "filter", "pattern", "marker", "use", "symbol", "image", "foreignObject", "style", "animate", "animateTransform", "set", "script", "a", "metadata"} {
		payloads[element] = logoSVG("<" + element + "/>")
	}
	for _, property := range []string{"mask", "clip-path", "filter", "marker-start", "marker-mid", "marker-end", "fill", "stroke"} {
		for _, value := range []string{"inherit", "unset", "revert", "revert-layer"} {
			payloads[property+" attribute "+value] = logoSVG(`<rect width="40" height="40" ` + property + `="` + value + `"/>`)
			payloads[property+" style "+value] = logoSVG(`<rect width="40" height="40" style="` + property + `:` + value + `"/>`)
		}
	}
	for name, body := range map[string]string{
		"gradient href":       `<linearGradient id="g" href="#a"/>`,
		"gradient xlink href": `<linearGradient xmlns:xlink="http://www.w3.org/1999/xlink" id="g" xlink:href="#a"/>`,
		"shape href":          `<path href="#a" d="M0 0h40v40z"/>`,
		"gradient cycle":      `<linearGradient id="a" href="#b"/><linearGradient id="b" href="#a"/>`,
		"wrong target":        `<rect id="a" width="40" height="40"/><path d="M0 0h40v40z" fill="url(#a)"/>`,
		"missing target":      `<path d="M0 0h40v40z" fill="url(#missing)"/>`,
		"overridden target":   `<path fill="url(#missing)" style="fill:red"/>`,
		"overridden style":    `<path style="fill:url(#missing);fill:red"/>`,
		"duplicate id":        `<linearGradient id="a"/><rect id="a" width="40" height="40"/><path fill="url(#a)"/>`,
		"url on opacity":      `<path opacity="url(#g)"/>`,
		"external paint":      `<path fill="url(https://example.org/g.svg#g)"/>`,
		"second paint":        `<linearGradient id="g"/><path fill="url(#g) url(other.svg)"/>`,
		"inherited fallback":  `<linearGradient id="g"/><path fill="url(#g) inherit"/>`,
		"escaped css":         `<path style="fill:u\72l(other.svg)"/>`,
		"unknown property":    `<path style="background:red"/>`,
		"malformed style":     `<path style="fill"/>`,
		"handler":             `<path onload="alert(1)"/>`,
		"foreign attribute":   `<path xmlns:e="urn:editor" e:fill="red"/>`,
		"base url":            `<path xml:base="https://example.org/"/>`,
		"doctype":             `<!DOCTYPE svg><path/>`,
		"stylesheet":          `<?xml-stylesheet href="other.css"?><path/>`,
	} {
		payloads[name] = logoSVG(body)
	}
	for name, in := range payloads {
		t.Run(name, func(t *testing.T) {
			out, _, err := SanitizeSVG(in)
			if err == nil || len(out) != 0 {
				t.Fatalf("unsafe upload was rewritten instead of rejected: %.200s (%v)", out, err)
			}
			if !strings.Contains(err.Error(), "SVG") {
				t.Fatalf("missing SVG rejection reason: %v", err)
			}
		})
	}
}

func TestStaticSVGOrdinaryLogoCorpusIsStable(t *testing.T) {
	corpus := map[string]string{
		"paths":                   `<g transform="translate(4 4)"><path fill="#0b6e6e" fill-rule="evenodd" d="M0 0h32v32H0z M8 8v16h16V8z"/><path fill="#fff" d="M8 8h16v16H8z"/></g>`,
		"shapes":                  `<rect width="40" height="40" rx="6" fill="#0b6e6e"/><circle cx="20" cy="20" r="12" fill="none" stroke="white" stroke-width="2"/><ellipse cx="20" cy="20" rx="8" ry="6"/><line x1="5" y1="5" x2="35" y2="35"/><polyline points="0,0 20,40 40,0"/><polygon points="0,0 40,0 20,40"/>`,
		"linear gradient":         `<defs><linearGradient id="ink" x1="0" y1="0" x2="1" y2="0" gradientTransform="rotate(15)"><stop offset="0" stop-color="#0b6e6e"/><stop offset="100%" stop-color="#e30613" stop-opacity="0.9"/></linearGradient></defs><path fill="url(#ink)" d="M0 0h40v40H0z"/>`,
		"forward radial gradient": `<circle cx="20" cy="20" r="18" stroke="url('#glow') #fff" fill="url(#glow)"/><defs><radialGradient id="glow" cx="0.5" cy="0.5" r="0.5" fx="0.3" fy="0.3" gradientUnits="objectBoundingBox" spreadMethod="pad"><stop offset="0" stop-color="white"/><stop offset="1" stop-color="teal"/></radialGradient></defs>`,
		"text":                    `<title>Acme &amp; Co</title><desc>Company logo</desc><text x="44" y="28" font-family="'Inter', sans-serif" font-size="24" font-weight="600">Acme <tspan fill="#0b6e6e" dx="2">&amp; Co</tspan></text>`,
		"inline style":            `<g opacity="0.8" style="fill:#0b6e6e;stroke:none"><rect width="40" height="40" style="fill:#fff;stroke:#000;stroke-width:2"/></g>`,
	}
	for name, body := range corpus {
		t.Run(name, func(t *testing.T) {
			first, _, err := SanitizeSVG(logoSVG(body))
			if err != nil {
				t.Fatal(err)
			}
			assertInert(t, first)
			second, cleaned, err := SanitizeSVG(first)
			if err != nil || cleaned || !bytes.Equal(first, second) {
				t.Fatalf("logo bytes changed on a second pass: %s / %s (cleaned=%v, %v)", first, second, cleaned, err)
			}
			logo, err := ValidateLogo("image/svg+xml", first)
			if err != nil || logo.Cleaned || !bytes.Equal(first, logo.Content) {
				t.Fatalf("validated logo bytes changed: %+v (%v)", logo, err)
			}
		})
	}
}

func TestStaticSVGAcceptsUTF8Export(t *testing.T) {
	doc := logoSVG(`<path d="M0 0h40v40z"/>`)
	plain, _, err := SanitizeSVG(doc)
	if err != nil {
		t.Fatal(err)
	}
	export := append([]byte("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"), doc...)
	logo, err := ValidateLogo("image/svg+xml", export)
	if err != nil || !bytes.Equal(plain, logo.Content) {
		t.Fatalf("UTF-8 export changed the drawing: %s (%v)", logo.Content, err)
	}
}

func TestStaticSVGComplexityBackstops(t *testing.T) {
	for name, in := range map[string][]byte{
		"elements": logoSVG(strings.Repeat(`<g/>`, maxSVGElements)),
		"depth":    logoSVG(strings.Repeat(`<g>`, maxSVGDepth) + strings.Repeat(`</g>`, maxSVGDepth)),
		"bytes":    logoSVG(strings.Repeat(" ", MaxLogoBytes)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := SanitizeSVG(in); err == nil {
				t.Fatal("over-budget SVG accepted")
			}
		})
	}
}
