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

// SVG logos use a static drawing profile. Unsupported elements, attributes and
// style properties refuse the upload rather than silently changing the logo.
// There are no element references or inherited gradients: only fill and stroke
// may name a same-document gradient. defs is an inert container for definitions.
// Inline styles become allowlisted presentation attributes. Comments and the XML
// declaration may be removed; output remains sharp without a server renderer.
const svgNS = "http://www.w3.org/2000/svg"

var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true,
	"path": true, "circle": true, "rect": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "linearGradient": true, "radialGradient": true, "stop": true,
}

var gradientElements = map[string]bool{"linearGradient": true, "radialGradient": true}
var textElements = map[string]bool{"text": true, "tspan": true, "title": true, "desc": true}

var svgAttributes = map[string]bool{
	"id": true, "viewBox": true, "preserveAspectRatio": true, "width": true, "height": true, "version": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"fx": true, "fy": true, "fr": true, "dx": true, "dy": true, "d": true, "points": true, "transform": true, "pathLength": true,
	"offset": true, "gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"textLength": true, "lengthAdjust": true,
}

// presentation is also the allowlist for inline CSS; no resource properties.
var presentation = map[string]bool{
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true,
	"opacity": true, "color": true, "display": true, "visibility": true,
	"stop-color": true, "stop-opacity": true, "font-family": true, "font-size": true, "font-weight": true, "font-style": true,
	"font-stretch": true, "letter-spacing": true, "word-spacing": true, "text-anchor": true, "dominant-baseline": true,
	"alignment-baseline": true, "baseline-shift": true, "vector-effect": true, "paint-order": true, "shape-rendering": true,
	"transform": true,
}

const fragmentID = `#[A-Za-z_][A-Za-z0-9_.:-]*`

var (
	fragmentRef   = regexp.MustCompile(`^` + fragmentID + `$`)
	localURL      = regexp.MustCompile(`(?s)^url\(\s*(?:'(` + fragmentID + `)'|"(` + fragmentID + `)"|(` + fragmentID + `))\s*\)(.*)$`)
	paintFallback = regexp.MustCompile(`^(?:none|currentColor|[A-Za-z]{3,32}|#[0-9A-Fa-f]{3,8}|(?:rgb|rgba|hsl|hsla)\([0-9.%\s,/+-]*\))$`)
	// Numbers, paths, transforms, lengths, colours and static typography only.
	safeValue = regexp.MustCompile(`^[A-Za-z0-9 \t\r\n#%.,()+\-_]*$`)
)

const (
	maxSVGDepth    = 32
	maxSVGElements = 20000
)

var errNotSVG = errors.New("not an SVG document")

// SanitizeSVG validates the static profile and returns canonical bytes. cleaned
// reports harmless comments removed, not unsupported features: those are errors.
func SanitizeSVG(in []byte) (out []byte, cleaned bool, err error) {
	if len(in) > MaxLogoBytes {
		return nil, false, TooLargeError{"the SVG exceeds 256 KB"}
	}
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(in, []byte("\xef\xbb\xbf"))))
	dec.Strict = true
	dec.Entity = map[string]string{} // only the five predefined entities
	var buf bytes.Buffer
	var stack []string
	elements := 0
	ids := map[string]string{} // unique id -> element type
	var paints []string
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
			if elements > maxSVGElements || len(stack) >= maxSVGDepth {
				return nil, false, errors.New("SVG is too complex")
			}
			if !rootSeen {
				if t.Name.Local != "svg" || (t.Name.Space != svgNS && t.Name.Space != "") {
					return nil, false, errNotSVG
				}
				rootSeen = true
			} else if rootClosed {
				return nil, false, errors.New("SVG has more than one root element")
			}
			if (t.Name.Space != svgNS && t.Name.Space != "") || !svgElements[t.Name.Local] {
				return nil, false, fmt.Errorf("SVG element <%s> is not supported; use a static logo with paths, shapes, text or gradients", t.Name.Local)
			}
			if len(stack) > 0 && !svgChildAllowed(stack[len(stack)-1], t.Name.Local) {
				return nil, false, fmt.Errorf("SVG element <%s> is not supported inside <%s>", t.Name.Local, stack[len(stack)-1])
			}
			attrs, refs, err := cleanAttributes(t.Attr)
			if err != nil {
				return nil, false, err
			}
			paints = append(paints, refs...)
			for _, a := range attrs {
				if a.name == "id" {
					if _, exists := ids[a.value]; exists {
						return nil, false, errors.New("SVG ids must be unique")
					}
					ids[a.value] = t.Name.Local
				}
			}
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
			if len(stack) == 0 {
				return nil, false, errors.New("invalid SVG: unexpected closing element")
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			buf.WriteString("</" + top + ">")
			if len(stack) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if len(stack) > 0 && textElements[stack[len(stack)-1]] {
				_ = xml.EscapeText(&buf, t)
			} else if len(bytes.TrimSpace(t)) != 0 {
				return nil, false, errors.New("SVG character data is only supported in text, title or desc")
			}
		case xml.ProcInst:
			if t.Target != "xml" || rootSeen {
				return nil, false, errors.New("SVG processing instructions are not supported")
			}
		case xml.Comment:
			cleaned = true
		case xml.Directive:
			return nil, false, errors.New("SVG directives and DOCTYPEs are not supported")
		}
		if buf.Len() > MaxLogoBytes {
			return nil, false, TooLargeError{"the rewritten SVG exceeds 256 KB"}
		}
	}
	if !rootSeen {
		return nil, false, errNotSVG
	}
	if !rootClosed {
		return nil, false, errors.New("invalid SVG: unclosed root element")
	}
	for _, id := range paints {
		if !gradientElements[ids[id]] {
			return nil, false, errors.New("SVG fill and stroke url() must name a gradient in this file")
		}
	}
	return buf.Bytes(), cleaned, nil
}

func svgChildAllowed(parent, child string) bool {
	if child == "svg" {
		return false
	}
	switch parent {
	case "svg", "g", "defs":
		return child != "stop" && child != "tspan"
	case "linearGradient", "radialGradient":
		return child == "stop" || child == "title" || child == "desc"
	case "text", "tspan":
		return child == "tspan" || child == "title" || child == "desc"
	case "title", "desc":
		return false
	default:
		return child == "title" || child == "desc"
	}
}

type attribute struct{ name, value string }

// cleanAttributes rejects every non-allowlisted attribute or declaration, even
// if a later declaration would override it. Inline CSS wins over attributes.
func cleanAttributes(in []xml.Attr) ([]attribute, []string, error) {
	var out []attribute
	var paints []string
	set := func(name, value string) {
		// Validate even a reference that a later style declaration overrides.
		if (name == "fill" || name == "stroke") && strings.HasPrefix(value, "url(") {
			paints = append(paints, value[len("url(#"):strings.IndexByte(value, ')')])
		}
		for i := range out {
			if out[i].name == name {
				out[i].value = value
				return
			}
		}
		out = append(out, attribute{name, value})
	}
	var style string
	seen := map[xml.Name]bool{}
	for _, a := range in {
		if seen[a.Name] {
			return nil, nil, fmt.Errorf("SVG attribute %s is repeated", a.Name.Local)
		}
		seen[a.Name] = true
		name := a.Name.Local
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && name == "xmlns") {
			continue // namespace declarations are rewritten
		}
		if a.Name.Space != "" || name == "href" || (!svgAttributes[name] && !presentation[name] && name != "style") {
			return nil, nil, fmt.Errorf("SVG attribute %s is not supported in static logos", name)
		}
		if name == "style" {
			style = a.Value
			continue
		}
		value, ok := cleanValue(name, a.Value)
		if !ok {
			return nil, nil, fmt.Errorf("SVG attribute %s has an unsupported value; fill and stroke may reference only a local gradient, without inheritance", name)
		}
		set(name, value)
	}
	for _, decl := range strings.Split(style, ";") {
		if strings.TrimSpace(decl) == "" {
			continue
		}
		prop, value, ok := strings.Cut(decl, ":")
		prop = strings.ToLower(strings.TrimSpace(prop))
		if !ok || !presentation[prop] {
			return nil, nil, fmt.Errorf("SVG style property %s is not supported in static logos", prop)
		}
		clean, valid := cleanValue(prop, value)
		if !valid {
			return nil, nil, fmt.Errorf("SVG style property %s has an unsupported value; fill and stroke may reference only a local gradient, without inheritance", prop)
		}
		set(prop, clean)
	}
	return out, paints, nil
}

func inheritedPaint(v string) bool {
	switch strings.ToLower(v) {
	case "inherit", "initial", "unset", "revert", "revert-layer":
		return true
	}
	return false
}

// cleanURL permits one local gradient on fill/stroke and a static colour fallback.
func cleanURL(name, v string) (string, bool) {
	if name != "fill" && name != "stroke" {
		return "", false
	}
	m := localURL.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	out := "url(" + m[1] + m[2] + m[3] + ")"
	rest := strings.TrimSpace(m[4])
	if rest == "" {
		return out, true
	}
	if paintFallback.MatchString(rest) && !inheritedPaint(rest) {
		return out + " " + rest, true
	}
	return "", false
}

func cleanValue(name, raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if len(v) > 100000 {
		return "", false
	}
	if name == "id" {
		return v, fragmentRef.MatchString("#" + v)
	}
	if strings.Contains(strings.ToLower(v), "url(") {
		return cleanURL(name, v)
	}
	if (name == "fill" || name == "stroke") && inheritedPaint(v) {
		return "", false
	}
	if name == "font-family" {
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
