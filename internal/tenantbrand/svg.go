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
// Logos need no element references, so there are almost none: <use>,
// <symbol>, <pattern> and <marker> are dropped; href survives only on a
// gradient that inherits from one other gradient; url() is a single fragment
// of this file on fill, stroke, clip-path and mask, and a mask or clip path
// never names another (no chains); and the file's expanded size, every
// element counted once per mask or clip path that draws it, stays under a
// budget. Nothing a browser draws can multiply through references.
// Inline style declarations become presentation attributes, so the served
// file needs no style-src at all.
//
// Rewriting was chosen over server-side rasterization: it needs no renderer
// in the binary, keeps the logo sharp at every size, and the output contains
// nothing that can fetch or execute, even before the <img> context and the
// response CSP (default-src 'none'; sandbox) take their turn.

const svgNS = "http://www.w3.org/2000/svg"
const xlinkNS = "http://www.w3.org/1999/xlink"

var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true,
	"path": true, "circle": true, "rect": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "linearGradient": true, "radialGradient": true, "stop": true,
	"clipPath": true, "mask": true,
}

// hrefElements may inherit from another gradient of the same file.
var hrefElements = map[string]bool{"linearGradient": true, "radialGradient": true}

// maskElements draw what another element is clipped or masked by.
var maskElements = map[string]bool{"mask": true, "clipPath": true}

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

const fragmentID = `#[A-Za-z_][A-Za-z0-9_.:-]*`

var (
	fragmentRef = regexp.MustCompile(`^` + fragmentID + `$`)
	// localURL is one url() naming an element of this file; what follows it is
	// checked on its own, so a second url() can never ride along.
	localURL = regexp.MustCompile(`(?s)^url\(\s*(?:'(` + fragmentID + `)'|"(` + fragmentID + `)"|(` + fragmentID + `))\s*\)(.*)$`)
	// paintFallback is what may follow a paint server: a keyword, a colour name,
	// a hex colour or an rgb()/hsl() function.
	paintFallback = regexp.MustCompile(`^(?:none|currentColor|[A-Za-z]{3,32}|#[0-9A-Fa-f]{3,8}|(?:rgb|rgba|hsl|hsla)\([0-9.%\s,/+-]*\))$`)
	// safeValue is every other value: numbers, lengths, colours, keywords,
	// path data, transforms and font family names. No quotes, colons, angle
	// brackets, slashes, backslashes, semicolons or url().
	safeValue = regexp.MustCompile(`^[A-Za-z0-9 \t\r\n#%.,()+\-_]*$`)
)

const (
	maxSVGDepth    = 32
	maxSVGElements = 20000
	// maxSVGExpanded bounds the drawn elements once every mask and clip path is
	// counted for each element that uses it.
	maxSVGExpanded = 100000
)

// errNotSVG is returned for input whose root is not an <svg> element.
var errNotSVG = errors.New("not an SVG document")

// SanitizeSVG returns the rewritten document and whether anything was removed.
func SanitizeSVG(in []byte) (out []byte, cleaned bool, err error) {
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	dec.Entity = map[string]string{} // only the five predefined entities
	var buf bytes.Buffer
	var stack []svgFrame // open kept elements
	skip := 0            // depth inside a dropped subtree
	elements, kept := 0, 0
	sizes := map[string]int{}     // id -> elements in its subtree, itself included
	maskRefs := map[string]int{}  // id -> elements naming it as clip-path or mask
	inherits := map[string]bool{} // ids of gradients that inherit from another
	type edge struct{ from, to string }
	var edges []edge
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
			// A mask or clip path never names another: no chains to multiply through.
			nested := maskElements[t.Name.Local] || len(stack) > 0 && stack[len(stack)-1].nested
			id := ""
			filtered := attrs[:0]
			for _, a := range attrs {
				switch {
				case (a.name == "clip-path" || a.name == "mask") && strings.HasPrefix(a.value, "url("):
					if nested {
						dropped = true
						continue
					}
					maskRefs[fragmentOf(a.value)]++
				case a.name == "id":
					id = a.value
				}
				filtered = append(filtered, a)
			}
			attrs = filtered
			for _, a := range attrs {
				if a.name == "href" && id != "" {
					inherits[id] = true
					edges = append(edges, edge{id, strings.TrimPrefix(a.value, "#")})
				}
			}
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
			stack = append(stack, svgFrame{name: t.Name.Local, id: id, start: kept, nested: nested})
			kept++
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top.id != "" {
				sizes[top.id] = max(sizes[top.id], kept-top.start)
			}
			buf.WriteString("</" + top.name + ">")
			if len(stack) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if skip > 0 || len(stack) == 0 {
				continue
			}
			if textElements[stack[len(stack)-1].name] {
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
	// A gradient inherits from one other gradient at most: no chains, no cycles.
	for _, e := range edges {
		if inherits[e.to] {
			return nil, false, errors.New("SVG gradients inherit from each other in a chain")
		}
	}
	// Every element counts once for each mask or clip path that draws it.
	expanded := kept
	for id, n := range maskRefs {
		expanded += n * sizes[id]
		if expanded > maxSVGExpanded {
			return nil, false, errors.New("SVG is too complex")
		}
	}
	return buf.Bytes(), cleaned, nil
}

// svgFrame is an open element that is being kept.
type svgFrame struct {
	name, id string
	start    int  // kept elements before this one
	nested   bool // this element or an ancestor is a mask or clip path
}

// fragmentOf returns the id of the url(#id) that cleanURL wrote.
func fragmentOf(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, "url(#"), ")")
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

// cleanURL accepts one url() to a fragment of this file on a property that
// takes a paint server or a mask, rewritten without quotes or spaces. Only a
// paint may carry a fallback, and the fallback is validated on its own.
func cleanURL(name, v string) (string, bool) {
	paint := name == "fill" || name == "stroke"
	if !paint && name != "clip-path" && name != "mask" {
		return "", false
	}
	m := localURL.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	out := "url(" + m[1] + m[2] + m[3] + ")"
	rest := strings.TrimSpace(m[4])
	switch {
	case rest == "":
		return out, true
	case paint && paintFallback.MatchString(rest):
		return out + " " + rest, true
	}
	return "", false
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
		return cleanURL(name, v)
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
