// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSVGEditorExportsKeepDrawing(t *testing.T) {
	for _, tc := range []struct {
		name            string
		attrs, elements int
	}{
		{"figma", 6, 0}, {"illustrator", 4, 2}, {"inkscape", 3, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := os.ReadFile("testdata/" + tc.name + ".svg")
			if err != nil {
				t.Fatal(err)
			}
			logo, err := ValidateLogo("image/svg+xml", in)
			if err != nil {
				t.Fatal(err)
			}
			c := logo.SVGCleanup
			if !logo.Cleaned || c == nil || c.RemovedAttributeCount != tc.attrs || c.RemovedElementCount != tc.elements {
				t.Fatalf("cleanup %+v, cleaned %v", c, logo.Cleaned)
			}
			if logo.Width != 160 || logo.Height != 40 {
				t.Fatalf("dimensions %+v", logo)
			}
			assertInert(t, logo.Content)
			for _, want := range []string{`d="M0 0h40v40H0z"`, `font-size="24"`, `>Acme</text>`} {
				if !bytes.Contains(logo.Content, []byte(want)) {
					t.Fatalf("drawing lost %s: %s", want, logo.Content)
				}
			}
			if tc.name == "figma" && !bytes.Contains(logo.Content, []byte(`fill="url(#ink)"`)) {
				t.Fatal("gradient lost")
			}
			for _, bad := range []string{"Editor data", "version=", "aria-", "data-", "role=", "class=", "xml:space", "enable-background", "metadata", "namedview", "pgf"} {
				if bytes.Contains(logo.Content, []byte(bad)) {
					t.Fatalf("%s survived: %s", bad, logo.Content)
				}
			}
			second, err := ValidateLogo("image/svg+xml", logo.Content)
			if err != nil || second.Cleaned || second.SVGCleanup != nil || !bytes.Equal(logo.Content, second.Content) {
				t.Fatalf("second pass %+v (%v)", second, err)
			}
		})
	}
}

func TestSVGNonDrawingAttributesAreNeverSerialized(t *testing.T) {
	in := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 40" role="img" aria-label="&lt;script onload='alert(1)'&gt;" data-name="url(https://evil.example)" class="drawing" focusable="false" xml:space="preserve" enable-background="new 0 0 160 40"><path d="M0 0h40v40z" aria-label="javascript:alert(1)"/></svg>`)
	logo, err := ValidateLogo("image/svg+xml", in)
	if err != nil {
		t.Fatal(err)
	}
	c := logo.SVGCleanup
	if !logo.Cleaned || c == nil || c.RemovedAttributeCount != 8 || !reflect.DeepEqual(c.RemovedAttributes, []string{"role", "aria-label", "data-name", "class", "focusable", "xml:space", "enable-background"}) {
		t.Fatalf("cleanup %+v", c)
	}
	assertInert(t, logo.Content)
	if bytes.Contains(logo.Content, []byte("evil")) || bytes.Contains(logo.Content, []byte("alert")) {
		t.Fatalf("metadata value survived: %s", logo.Content)
	}
	// An alias prefix resolves by URI; a familiar prefix with an unknown URI fails.
	for uri, prefix := range svgEditorNamespaces {
		doc := []byte(`<svg viewBox="0 0 160 40" xmlns:alias="` + uri + `"><path alias:label="&lt;script&gt;" d="M0 0h40v40z"/><alias:record><alias:item>metadata</alias:item></alias:record></svg>`)
		logo, err := ValidateLogo("image/svg+xml", doc)
		if err != nil || logo.SVGCleanup == nil || !reflect.DeepEqual(logo.SVGCleanup.RemovedAttributes, []string{prefix + ":label"}) {
			t.Fatalf("editor %s: %+v (%v)", prefix, logo.SVGCleanup, err)
		}
		assertInert(t, logo.Content)
	}
}

func TestSVGMetadataCannotHideActiveFeatures(t *testing.T) {
	editor := `xmlns:e="http://www.inkscape.org/namespaces/inkscape"`
	payloads := map[string]string{
		"namespaced handler":           `<path e:onload="alert(1)"/>`,
		"namespaced uppercase handler": `<path e:OnClick="alert(1)"/>`,
		"editor href":                  `<path e:href="#x"/>`,
		"xlink rebound to editor":      `<path xmlns:xlink="http://www.inkscape.org/namespaces/inkscape" xlink:href="https://evil.example"/>`,
		"editor rebound to xlink":      `<path xmlns:inkscape="http://www.w3.org/1999/xlink" inkscape:href="#x"/>`,
		"editor base":                  `<e:record xml:base="https://evil.example"/>`,
		"unknown namespace":            `<path xmlns:inkscape="urn:unknown" inkscape:label="x"/>`,
		"unknown element":              `<inkscape:record xmlns:inkscape="urn:unknown"/>`,
		"duplicate harmless attribute": `<path role="img" role="presentation"/>`,
		"duplicate editor attribute":   `<path e:label="one" e:label="two"/>`,
		"metadata outside root":        `</svg><metadata/><svg>`,
		"bad metadata XML":             `<metadata><e:record></metadata>`,
		"DTD in metadata":              `<metadata><!DOCTYPE svg></metadata>`,
		"PI in metadata":               `<metadata><?xml-stylesheet href="evil.css"?></metadata>`,
		"style on editor":              `<e:record style="fill:url(https://evil.example)"/>`,
		"style on metadata":            `<metadata style="fill:red"/>`,
		"handler on metadata":          `<metadata e:onload="alert(1)"/>`,
		"handler in metadata":          `<metadata><e:record e:onclick="alert(1)"/></metadata>`,
		"href in metadata":             `<metadata><e:record e:href="https://evil.example"/></metadata>`,
		"external paint in metadata":   `<metadata><path fill="url(https://evil.example)"/></metadata>`,
		"inherited paint in metadata":  `<metadata><path fill="inherit"/></metadata>`,
		"metadata gradient target":     `<metadata><linearGradient id="g"/></metadata><path fill="url(#g)"/>`,
		"deep metadata":                `<metadata>` + strings.Repeat("<e:record>", maxSVGDepth) + strings.Repeat("</e:record>", maxSVGDepth) + `</metadata>`,
		"many metadata":                `<metadata>` + strings.Repeat("<e:record/>", maxSVGElements) + `</metadata>`,
	}
	for _, name := range []string{"script", "foreignObject", "style", "use", "image", "animate", "animateTransform", "set", "filter", "mask"} {
		payloads["metadata "+name] = `<metadata><` + name + `/></metadata>`
		payloads["editor "+name] = `<e:` + name + `/>`
	}
	for name, body := range payloads {
		t.Run(name, func(t *testing.T) {
			out, _, err := SanitizeSVG([]byte(`<svg viewBox="0 0 160 40" ` + editor + `>` + body + `</svg>`))
			if err == nil || len(out) != 0 {
				t.Fatalf("accepted active or invalid input: %s (%v)", out, err)
			}
		})
	}
}
