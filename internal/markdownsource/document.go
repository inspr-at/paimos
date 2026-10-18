// SPDX-License-Identifier: AGPL-3.0-only

// Package markdownsource preserves source-line evidence from CommonMark parsing.
package markdownsource

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Document returns the normalized source, its AST, and zero-based code-line
// flags. The flags include fence delimiters (even empty/unclosed fences), not
// just the code content stored in AST line segments. Goldmark alone decides
// whether a fence opens/closes and whether indentation is code or a list.
func Document(source string) ([]byte, ast.Node, []bool) {
	source = strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
	raw := []byte(source)
	code := make([]bool, strings.Count(source, "\n")+1)
	blocks := parser.DefaultBlockParsers()
	for i := range blocks {
		blocks[i].Value = &codeTracker{BlockParser: blocks[i].Value.(parser.BlockParser), lines: code}
	}
	p := parser.NewParser(parser.WithBlockParsers(blocks...), parser.WithInlineParsers(parser.DefaultInlineParsers()...))
	return raw, p.Parse(text.NewReader(raw)), code
}

func CodeLines(source string) []bool {
	_, _, lines := Document(source)
	return lines
}

type codeTracker struct {
	parser.BlockParser
	lines []bool
}

func isCode(n ast.Node) bool {
	return n != nil && (n.Kind() == ast.KindCodeBlock || n.Kind() == ast.KindFencedCodeBlock)
}

func (p *codeTracker) Open(parent ast.Node, r text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := r.Position()
	n, state := p.BlockParser.Open(parent, r, pc)
	if isCode(n) && line < len(p.lines) {
		p.lines[line] = true
	}
	return n, state
}

func (p *codeTracker) Continue(n ast.Node, r text.Reader, pc parser.Context) parser.State {
	line, before := r.Position()
	state := p.BlockParser.Continue(n, r, pc)
	afterLine, after := r.Position()
	// A valid fenced closer is consumed before Close. An indented block's
	// first following prose line is not consumed and must remain visible.
	if isCode(n) && line < len(p.lines) && (state&parser.Continue != 0 || afterLine != line || after.Start != before.Start) {
		p.lines[line] = true
	}
	return state
}
