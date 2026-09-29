// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	mdparser "github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// documentMarkdown parses a doctrine file. Heading paths come from its AST.
// inlineMarkdown parses one rule sentence. Only a paragraph block parser is
// installed, so a sentence that starts with a hash or a backtick run stays
// inline text instead of becoming a heading or a fence.
var (
	documentMarkdown = mdparser.NewParser(
		mdparser.WithBlockParsers(mdparser.DefaultBlockParsers()...),
		mdparser.WithInlineParsers(mdparser.DefaultInlineParsers()...),
	)
	inlineMarkdown = mdparser.NewParser(
		mdparser.WithBlockParsers(util.Prioritized(mdparser.NewParagraphParser(), 1000)),
		mdparser.WithInlineParsers(mdparser.DefaultInlineParsers()...),
	)
)

type mdHeading struct {
	level int
	title string
}

// indexATXHeadings maps a 1-based line number to a column-0 ATX heading.
// Setext headings and headings indented off column 0 are omitted so the line
// scanner keeps its existing section boundaries.
func indexATXHeadings(source string) map[int]mdHeading {
	raw := []byte(source)
	if len(raw) == 0 {
		return nil
	}
	doc := documentMarkdown.Parse(text.NewReader(raw))
	out := make(map[int]mdHeading)
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok {
			continue
		}
		lineStart, ok := atxLineStart(raw, h)
		if !ok {
			continue
		}
		out[bytes.Count(raw[:lineStart], []byte("\n"))+1] = mdHeading{
			level: h.Level,
			title: strings.TrimSpace(markdownInlineText(raw, h)),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renderMarkdownInline removes emphasis by node and keeps code-span text.
// Delimiter backticks are not part of the span, at any run length.
func renderMarkdownInline(s string) string {
	if s == "" {
		return ""
	}
	raw := []byte(s)
	return markdownInlineText(raw, inlineMarkdown.Parse(text.NewReader(raw)))
}

func markdownInlineText(source []byte, n ast.Node) string {
	var b strings.Builder
	b.Grow(len(source))
	ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.CodeSpan:
			writeCodeSpan(&b, source, node)
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			b.Write(node.Value(source))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(node.Value)
		case *ast.AutoLink:
			b.Write(node.Label(source))
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			for i := 0; i < node.Segments.Len(); i++ {
				seg := node.Segments.At(i)
				b.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// writeCodeSpan joins the span's lines with spaces. CommonMark turns a line
// ending inside a code span into a space; the backticks themselves are not text.
func writeCodeSpan(b *strings.Builder, source []byte, n ast.Node) {
	for i, c := 0, n.FirstChild(); c != nil; i, c = i+1, c.NextSibling() {
		if i > 0 {
			b.WriteByte(' ')
		}
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Value(source))
		}
	}
}

func atxLineStart(raw []byte, h *ast.Heading) (int, bool) {
	if h.Lines().Len() > 0 {
		start := beginningOfLine(raw, h.Lines().At(0).Start)
		if start < len(raw) && raw[start] == '#' {
			return start, true
		}
		return 0, false
	}
	gapFrom := 0
	if prev := h.PreviousSibling(); prev != nil {
		gapFrom = blockEnd(raw, prev)
	}
	gapTo := len(raw)
	if next := h.NextSibling(); next != nil {
		if start := nodeStart(raw, next); start >= gapFrom && start <= len(raw) {
			gapTo = start
		}
	}
	if gapFrom > gapTo || gapFrom > len(raw) {
		return 0, false
	}
	rel := indexEmptyATX(raw[gapFrom:gapTo])
	if rel < 0 {
		return 0, false
	}
	return gapFrom + rel, true
}

func beginningOfLine(raw []byte, offset int) int {
	if offset > len(raw) {
		offset = len(raw)
	}
	for offset > 0 && raw[offset-1] != '\n' {
		offset--
	}
	return offset
}

func blockEnd(raw []byte, n ast.Node) int {
	end := nodeEnd(raw, n)
	if end < len(raw) && raw[end] == '\n' {
		end++
	}
	return end
}

func nodeStart(raw []byte, n ast.Node) int {
	if n == nil {
		return len(raw)
	}
	if lines := n.Lines(); lines != nil && lines.Len() > 0 {
		return beginningOfLine(raw, lines.At(0).Start)
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if start := nodeStart(raw, c); start < len(raw) {
			return start
		}
	}
	return len(raw)
}

func nodeEnd(raw []byte, n ast.Node) int {
	if n == nil {
		return 0
	}
	if lines := n.Lines(); lines != nil && lines.Len() > 0 {
		return lines.At(lines.Len() - 1).Stop
	}
	end := 0
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if stop := nodeEnd(raw, c); stop > end {
			end = stop
		}
	}
	return end
}

func indexEmptyATX(gap []byte) int {
	for i := 0; i < len(gap); {
		line := gap[i:]
		next := len(gap)
		if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
			next = i + nl + 1
		}
		if isEmptyATX(line) {
			return i
		}
		if next == i {
			break
		}
		i = next
	}
	return -1
}

// isEmptyATX reports a column-0 ATX heading with no inline text.
// Goldmark stores no line segment for these, including a closing hash run
// ("## ##"), so the scanner would otherwise keep the previous section.
func isEmptyATX(line []byte) bool {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
		if i > 6 {
			return false
		}
	}
	if i == 0 {
		return false
	}
	rest := line[i:]
	if len(rest) == 0 {
		return true
	}
	if !isATXSpace(rest[0]) {
		return false
	}
	for len(rest) > 0 && isATXSpace(rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}
	for len(rest) > 0 && isATXSpace(rest[0]) {
		rest = rest[1:]
	}
	for _, c := range rest {
		if c != '#' {
			return false
		}
	}
	return true
}

func isATXSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r'
}
