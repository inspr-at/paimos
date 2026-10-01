// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
)

// Unsupported active features reject the whole upload, without saving a rewrite.
func TestSanitizeSVGNeutralizesXSS(t *testing.T) {
	payloads := map[string]string{
		"script element":      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><script>alert(1)</script><rect width="10" height="10"/></svg>`,
		"cdata script":        `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><script><![CDATA[alert(1)]]></script></svg>`,
		"namespaced script":   `<svg xmlns="http://www.w3.org/2000/svg" xmlns:h="http://www.w3.org/1999/xhtml" viewBox="0 0 10 10"><h:script>alert(1)</h:script></svg>`,
		"onload":              `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" onload="alert(1)"><rect onclick="alert(1)" width="10" height="10"/></svg>`,
		"foreignObject":       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><foreignObject><iframe xmlns="http://www.w3.org/1999/xhtml" src="javascript:alert(1)"/></foreignObject></svg>`,
		"external use":        `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10"><use xlink:href="https://evil.example/x.svg#a"/><use href="//evil.example/x.svg#a"/></svg>`,
		"javascript href":     `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10"><a xlink:href="javascript:alert(1)"><rect width="10" height="10"/></a><use href="javascript:alert(1)"/></svg>`,
		"data uri image":      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><image href="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+"/></svg>`,
		"data uri use":        `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><use href="data:image/svg+xml,&lt;svg onload='alert(1)'&gt;"/></svg>`,
		"data uri fill":       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect fill="url(data:image/svg+xml,&lt;script&gt;)" width="10" height="10"/></svg>`,
		"external fill":       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect fill="url(https://evil.example/p.svg#g)" width="10" height="10"/></svg>`,
		"style element":       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><style>@import url(https://evil.example/x.css); rect{fill:url(javascript:alert(1))}</style></svg>`,
		"style attribute":     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect style="fill:url(javascript:alert(1));background:url(https://evil.example)" width="10" height="10"/></svg>`,
		"animate href":        `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><a href="#x"><animate attributeName="href" to="javascript:alert(1)"/><set attributeName="onload" to="alert(1)"/></a></svg>`,
		"stylesheet pi":       `<?xml-stylesheet href="https://evil.example/x.css"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`,
		"comment smuggle":     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><!--<script>alert(1)</script>--></svg>`,
		"xml:base":            `<svg xmlns="http://www.w3.org/2000/svg" xml:base="https://evil.example/" viewBox="0 0 10 10"/>`,
		"event on text":       `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><text onmouseover="alert(1)">&lt;script&gt;evil</text></svg>`,
		"entity-free doctype": `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`,
	}
	for name, in := range payloads {
		t.Run(name, func(t *testing.T) {
			out, cleaned, err := SanitizeSVG([]byte(in))
			if name != "comment smuggle" {
				if err == nil || len(out) != 0 {
					t.Fatalf("active SVG was accepted: %s (%v)", out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertInert(t, out)
			if !cleaned {
				t.Fatalf("payload was not reported as cleaned: %s", out)
			}
		})
	}
}

// assertInert re-parses the output: only allowlisted elements, no handlers,
// and no reference that leaves the file.
func assertInert(t *testing.T, out []byte) {
	t.Helper()
	if bytes.Contains(out, []byte("<!")) || bytes.Contains(out, []byte("<?")) {
		t.Fatalf("comment, CDATA, DOCTYPE or PI survived: %s", out)
	}
	dec := xml.NewDecoder(bytes.NewReader(out))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("output is not XML: %v: %s", err, out)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !svgElements[start.Name.Local] || start.Name.Space != svgNS {
			t.Fatalf("element %v survived: %s", start.Name, out)
		}
		for _, a := range start.Attr {
			v := strings.ToLower(a.Value)
			if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") || a.Name.Space != "" && !(a.Name.Space == "" && a.Name.Local == "xmlns") {
				t.Fatalf("attribute %v survived: %s", a.Name, out)
			}
			if a.Name.Local == "xmlns" {
				continue
			}
			for _, bad := range []string{"javascript", "data:", "http", "//", "@import", "expression"} {
				if strings.Contains(v, bad) {
					t.Fatalf("%s=%q survived: %s", a.Name.Local, a.Value, out)
				}
			}
			if strings.Contains(v, "url(") && !strings.HasPrefix(v, "url(#") {
				t.Fatalf("%s=%q survived: %s", a.Name.Local, a.Value, out)
			}
			if a.Name.Local == "href" {
				t.Fatalf("href %q survived: %s", a.Value, out)
			}
		}
	}
}

// Entities are never expanded: a DOCTYPE may not smuggle markup or a billion laughs.
func TestSanitizeSVGRejectsEntities(t *testing.T) {
	for _, in := range []string{
		`<!DOCTYPE svg [<!ENTITY x "<script>alert(1)</script>">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1">&x;</svg>`,
		`<!DOCTYPE svg [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><text>&b;</text></svg>`,
		`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><text>&x;</text></svg>`,
	} {
		if out, _, err := SanitizeSVG([]byte(in)); err == nil {
			t.Fatalf("entity accepted: %s", out)
		}
	}
	for _, in := range []string{`<html><script>alert(1)</script></html>`, `<svg xmlns="http://www.w3.org/2000/svg">`, `<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`, strings.Repeat("<g>", 40)} {
		if _, _, err := SanitizeSVG([]byte(in)); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
}

// A static exported logo keeps its look: gradients, local paint references,
// style declarations as attributes, and its text.
func TestSanitizeSVGKeepsARealLogo(t *testing.T) {
	in := `<?xml version="1.0" encoding="UTF-8"?>
<!-- Generator: Illustrator -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 240 60">
  <defs>
    <linearGradient id="a" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="#0b6e6e"/><stop offset="1" style="stop-color:#e30613;stop-opacity:0.9"/></linearGradient>
    <linearGradient id="b" gradientTransform="rotate(45)"><stop offset="0" stop-color="#0b6e6e"/><stop offset="1" stop-color="#e30613"/></linearGradient>
  </defs>
  <g transform="translate(4 4)"><path d="M0 0h52v52H0z" fill="url(#b)"/><circle cx="26" cy="26" r="12" style="fill:#fff; stroke: none"/></g>
  <text x="64" y="40" font-family="'Inter', sans-serif" font-size="28" font-weight="600">Acme &amp; Co</text>
</svg>`
	out, cleaned, err := SanitizeSVG([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 60">`, `fill="url(#b)"`, `stop-color="#e30613"`, `fill="#fff"`, `stroke="none"`, `font-family="Inter, sans-serif"`, `Acme &amp; Co`, `gradientTransform="rotate(45)"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "inkscape") || strings.Contains(s, "Illustrator") || strings.Contains(s, "style=") {
		t.Fatalf("editor residue kept: %s", s)
	}
	if !cleaned {
		t.Fatal("dropping the harmless editor comment counts as cleaning")
	}
	w, h, err := svgSize(out)
	if err != nil || w != 240 || h != 60 {
		t.Fatalf("size %v×%v %v", w, h, err)
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	img.Set(0, 0, color.NRGBA{11, 110, 110, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateLogo(t *testing.T) {
	if l, err := ValidateLogo("image/png", pngOf(t, 200, 50)); err != nil || l.Width != 200 || l.Height != 50 || l.ContentType != "image/png" {
		t.Fatalf("wide png: %+v %v", l, err)
	}
	if l, err := ValidateLogo("image/png", pngOf(t, 64, 64)); err != nil || l.Width != 64 {
		t.Fatalf("square png: %+v %v", l, err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="120px" height="40px"><rect width="120" height="40"/></svg>`)
	if l, err := ValidateLogo("image/svg+xml; charset=utf-8", svg); err != nil || l.Width != 120 || l.Height != 40 || l.Cleaned {
		t.Fatalf("svg: %+v %v", l, err)
	}
	huge := append(append([]byte{}, svg[:len(svg)-6]...), append(bytes.Repeat([]byte(" "), MaxLogoBytes), []byte("</svg>")...)...)
	for name, tc := range map[string]struct {
		declared string
		body     []byte
	}{
		"small":       {"image/png", pngOf(t, 31, 64)},
		"too wide":    {"image/png", pngOf(t, 900, 100)},
		"too tall":    {"image/png", pngOf(t, 32, 80)},
		"mismatch":    {"image/png", svg},
		"spoofed svg": {"image/svg+xml", pngOf(t, 64, 64)},
		"gif":         {"image/gif", []byte("GIF89a\x01\x00\x01\x00")},
		"html":        {"image/svg+xml", []byte("<html><body onload=alert(1)></body></html>")},
		"truncated":   {"image/png", pngOf(t, 64, 64)[:60]},
		"empty":       {"image/png", nil},
		"over 256 KB": {"image/svg+xml", huge},
		"no box":      {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`)},
		"svg ratio":   {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 10"/>`)},
	} {
		if _, err := ValidateLogo(tc.declared, tc.body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// doubledUse is the review's amplification payload: every level names the one
// below it twice, so 94 source elements describe over a billion instances.
func doubledUse(levels int) string {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><g id="l0"><rect width="10" height="10"/></g>`)
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&b, `<g id="l%d"><use href="#l%d"/><use href="#l%d"/></g>`, i, i-1, i-1)
	}
	fmt.Fprintf(&b, `</defs><use href="#l%d"/></svg>`, levels)
	return b.String()
}

// doubledMask amplifies through masks instead: each mask draws two elements
// that are masked by the mask below.
func doubledMask(levels int) string {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><mask id="m0"><rect width="10" height="10" fill="#fff"/></mask>`)
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&b, `<mask id="m%d"><rect width="5" height="10" mask="url(#m%d)"/><rect x="5" width="5" height="10" mask="url(#m%d)"/></mask>`, i, i-1, i-1)
	}
	fmt.Fprintf(&b, `</defs><rect width="10" height="10" mask="url(#m%d)"/></svg>`, levels)
	return b.String()
}

// Gradient inheritance is rejected even at one level or with a missing target.
func TestSanitizeSVGGradientChains(t *testing.T) {
	wrap := func(body string) []byte {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs>` + body + `</defs></svg>`)
	}
	for name, body := range map[string]string{
		"chain": `<linearGradient id="a"><stop offset="0"/></linearGradient><linearGradient id="b" href="#a"/><linearGradient id="c" href="#b"/>`,
		"cycle": `<linearGradient id="a" href="#b"/><linearGradient id="b" href="#a"/>`,
		"self":  `<linearGradient id="a" href="#a"/>`,
	} {
		if out, _, err := SanitizeSVG(wrap(body)); err == nil {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
	if _, _, err := SanitizeSVG(wrap(`<linearGradient id="a"><stop offset="0"/></linearGradient><linearGradient id="b" href="#a"/>`)); err == nil {
		t.Error("single-level inheritance accepted")
	}
	if _, _, err := SanitizeSVG(wrap(`<linearGradient id="a" href="#missing"/>`)); err == nil {
		t.Error("missing inherited gradient accepted")
	}
}

// Every url() in an attribute or style value must be a fragment of this file;
// a paint fallback is validated on its own and never carries another url().
func TestSanitizeSVGURLsAreFragmentsOnly(t *testing.T) {
	rect := func(attr string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="g"><stop offset="0"/></linearGradient></defs><rect width="10" height="10" ` + attr + `/></svg>`
	}
	for name, attr := range map[string]string{
		"review payload":     `mask="url(#m) , url(other.svg)"`,
		"second url":         `mask="url(#m) url(https://evil.example/x.svg#a)"`,
		"fill second url":    `fill="url(#g) url(https://evil.example/p.svg#g)"`,
		"fill comma url":     `fill="url(#g), url(other.svg)"`,
		"fill fallback url":  `fill="url(#g) url(data:image/svg+xml,x)"`,
		"clip-path trailing": `clip-path="url(#m) , red"`,
		"style second url":   `style="mask:url(#m) , url(other.svg)"`,
		"mixed quotes":       `fill="url('#g&quot;)"`,
		"uppercase":          `fill="URL(https://evil.example/p.svg#g)"`,
		"spaced":             `fill="url( https://evil.example/p.svg#g )"`,
	} {
		out, _, err := SanitizeSVG([]byte(rect(attr)))
		if err == nil || len(out) != 0 {
			t.Errorf("%s: invalid URL was rewritten instead of rejected: %s (%v)", name, out, err)
		}
	}
	for name, tc := range map[string]struct{ attr, want string }{
		"plain":            {`fill="url(#g)"`, `fill="url(#g)"`},
		"quoted":           {`fill="url('#g')"`, `fill="url(#g)"`},
		"fallback keyword": {`fill="url(#g) none"`, `fill="url(#g) none"`},
		"fallback colour":  {`stroke="url(#g)  #0b6e6e"`, `stroke="url(#g) #0b6e6e"`},
		"fallback rgb":     {`fill="url(#g) rgb(1, 2, 3)"`, `fill="url(#g) rgb(1, 2, 3)"`},
		"style":            {`style="fill: url(#g) currentColor"`, `fill="url(#g) currentColor"`},
	} {
		out, _, err := SanitizeSVG([]byte(rect(tc.attr)))
		if err != nil || !strings.Contains(string(out), tc.want) {
			t.Errorf("%s: want %s in %s (%v)", name, tc.want, out, err)
		}
	}
}
