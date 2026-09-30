// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// SVG logos are rewritten, never passed through. The sanitizer parses the file
// and writes a new document from an allowlist of drawing elements and
// presentation attributes; everything else is dropped with its subtree
// (script, style, foreignObject, image, a, animation, filters, metadata,
// foreign namespaces), as are comments, processing instructions and DOCTYPEs.
// References may only point inside the file (href="#id", url(#id)). Inline
// style declarations become presentation attributes, so the served file needs
// no style-src at all.
//
// Rewriting was chosen over server-side rasterization: it needs no renderer
// in the binary, keeps the logo sharp at every size, and the output contains
// nothing that can fetch or execute, even before the <img> context and the
// response CSP (default-src 'none'; sandbox) take their turn.

const svgNS = "http://www.w3.org/2000/svg"
const xlinkNS = "http://www.w3.org/1999/xlink"

var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true, "title": true, "desc": true,
	"path": true, "circle": true, "rect": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "linearGradient": true, "radialGradient": true, "stop": true,
	"clipPath": true, "mask": true,
}

// hrefElements may point at another element of the same file.
var hrefElements = map[string]bool{"use": true, "linearGradient": true, "radialGradient": true}

// textElements keep their character data; everywhere else it is layout whitespace.
var textElements = map[string]bool{"text": true, "tspan": true, "title": true, "desc": true}

var svgAttributes = map[string]bool{
	"id": true, "viewBox": true, "preserveAspectRatio": true, "width": true, "height": true, "version": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"fx": true, "fy": true, "fr": true, "dx": true, "dy": true, "d": true, "points": true, "transform": true, "pathLength": true,
	"offset": true, "gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"clipPathUnits": true, "maskUnits": true, "maskContentUnits": true, "href": true,
	"textLength": true, "lengthAdjust": true,
}

// presentation attributes, allowed as attributes and as style declarations.
var presentation = map[string]bool{
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true,
	"opacity": true, "clip-path": true, "clip-rule": true, "mask": true, "color": true, "display": true, "visibility": true,
	"stop-color": true, "stop-opacity": true, "font-family": true, "font-size": true, "font-weight": true, "font-style": true,
	"font-stretch": true, "letter-spacing": true, "word-spacing": true, "text-anchor": true, "dominant-baseline": true,
	"alignment-baseline": true, "baseline-shift": true, "vector-effect": true, "paint-order": true, "shape-rendering": true,
	"isolation": true, "mix-blend-mode": true,
}

var (
	fragmentRef = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.:-]*$`)
	localURL    = regexp.MustCompile(`^url\(\s*['"]?#[A-Za-z_][A-Za-z0-9_.:-]*['"]?\s*\)(\s+[#A-Za-z0-9(),.%\s-]*)?$`)
	// safeValue is every other value: numbers, lengths, colours, keywords,
	// path data, transforms and font family names. No quotes, colons, angle
	// brackets, slashes, backslashes, semicolons or url().
	safeValue = regexp.MustCompile(`^[A-Za-z0-9 \t\r\n#%.,()+\-_]*$`)
)

const (
	maxSVGDepth    = 32
	maxSVGElements = 20000
)

// errNotSVG is returned for input whose root is not an <svg> element.
var errNotSVG = errors.New("not an SVG document")

// SanitizeSVG returns the rewritten document and whether anything was removed.
func SanitizeSVG(in []byte) (out []byte, cleaned bool, err error) {
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	dec.Entity = map[string]string{} // only the five predefined entities
	var buf bytes.Buffer
	var stack []string // open kept elements
	skip := 0          // depth inside a dropped subtree
	elements := 0
	rootSeen, rootClosed := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("invalid SVG: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			elements++
			if elements > maxSVGElements || len(stack)+skip >= maxSVGDepth {
				return nil, false, errors.New("SVG is too complex")
			}
			if skip > 0 {
				skip++
				continue
			}
			if !rootSeen {
				if t.Name.Local != "svg" || (t.Name.Space != svgNS && t.Name.Space != "") {
					return nil, false, errNotSVG
				}
				rootSeen = true
			} else if rootClosed {
				return nil, false, errors.New("SVG has more than one root element")
			}
			if (t.Name.Space != svgNS && t.Name.Space != "") || !svgElements[t.Name.Local] || (len(stack) > 0 && t.Name.Local == "svg") {
				cleaned = true
				skip = 1
				continue
			}
			attrs, dropped := cleanAttributes(t.Name.Local, t.Attr)
			cleaned = cleaned || dropped
			buf.WriteByte('<')
			buf.WriteString(t.Name.Local)
			if len(stack) == 0 {
				buf.WriteString(` xmlns="` + svgNS + `"`)
			}
			for _, a := range attrs {
				buf.WriteByte(' ')
				buf.WriteString(a.name)
				buf.WriteString(`="`)
				_ = xml.EscapeText(&buf, []byte(a.value))
				buf.WriteByte('"')
			}
			buf.WriteByte('>')
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				continue
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			buf.WriteString("</" + name + ">")
			if len(stack) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if skip > 0 || len(stack) == 0 {
				continue
			}
			if textElements[stack[len(stack)-1]] {
				_ = xml.EscapeText(&buf, t)
			}
		case xml.ProcInst:
			// The XML declaration is one too; the output does not need it.
			if t.Target != "xml" {
				cleaned = true
			}
		case xml.Comment:
			cleaned = true
		case xml.Directive:
			// A DOCTYPE may declare entities; none are honoured, and it is dropped.
			cleaned = true
		}
	}
	if !rootSeen {
		return nil, false, errNotSVG
	}
	if !rootClosed {
		return nil, false, errors.New("invalid SVG: unclosed root element")
	}
	return buf.Bytes(), cleaned, nil
}

type attribute struct{ name, value string }

// cleanAttributes keeps allowlisted attributes with safe values, in document
// order, then applies style declarations, which win as they do in CSS.
func cleanAttributes(element string, in []xml.Attr) ([]attribute, bool) {
	var out []attribute
	dropped := false
	set := func(name, value string) {
		for i := range out {
			if out[i].name == name {
				out[i].value = value
				return
			}
		}
		out = append(out, attribute{name, value})
	}
	var style string
	for _, a := range in {
		name := a.Name.Local
		switch {
		case a.Name.Space == "xmlns" || (a.Name.Space == "" && name == "xmlns"):
			continue // namespace declarations are rewritten
		case a.Name.Space == xlinkNS && name == "href":
			// xlink:href becomes the SVG 2 href.
		case a.Name.Space != "":
			dropped = true
			continue
		case name == "style":
			style = a.Value
			continue
		}
		value, ok := cleanValue(name, a.Value)
		if !ok || (!svgAttributes[name] && !presentation[name]) || (name == "href" && !hrefElements[element]) {
			dropped = true
			continue
		}
		set(name, value)
	}
	for _, decl := range strings.Split(style, ";") {
		prop, value, ok := strings.Cut(decl, ":")
		prop = strings.ToLower(strings.TrimSpace(prop))
		if !ok && prop == "" {
			continue
		}
		clean, valid := cleanValue(prop, value)
		if !ok || !presentation[prop] || !valid {
			dropped = true
			continue
		}
		set(prop, clean)
	}
	return out, dropped
}

func cleanValue(name, raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if len(v) > 100000 {
		return "", false
	}
	if name == "href" {
		return v, fragmentRef.MatchString(v)
	}
	if strings.Contains(strings.ToLower(v), "url(") {
		return v, (name == "fill" || name == "stroke" || name == "clip-path" || name == "mask") && localURL.MatchString(v)
	}
	if name == "font-family" {
		// Quoted family names lose their quotes; the list stays readable.
		v = strings.NewReplacer(`"`, "", `'`, "").Replace(v)
	}
	return v, safeValue.MatchString(v)
}

// svgSize reads the intrinsic size from the root's viewBox, else its width
// and height (unitless or px).
func svgSize(doc []byte) (float64, float64, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0, errNotSVG
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var viewBox, width, height string
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "viewBox":
				viewBox = a.Value
			case "width":
				width = a.Value
			case "height":
				height = a.Value
			}
		}
		if f := strings.FieldsFunc(viewBox, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r' }); len(f) == 4 {
			w, errW := strconv.ParseFloat(f[2], 64)
			h, errH := strconv.ParseFloat(f[3], 64)
			if errW == nil && errH == nil && positive(w) && positive(h) {
				return w, h, nil
			}
		}
		w, errW := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(width), "px"), 64)
		h, errH := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(height), "px"), 64)
		if errW == nil && errH == nil && positive(w) && positive(h) {
			return w, h, nil
		}
		return 0, 0, errors.New("SVG needs a viewBox or a width and height")
	}
}

func positive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }
