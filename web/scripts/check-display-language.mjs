// SPDX-License-Identifier: AGPL-3.0-only
import ts from 'typescript'
import { parse } from '@vue/compiler-sfc'
import { parse as parseTemplate, NodeTypes } from '@vue/compiler-dom'

// Inspect syntax, not comments or formatting preferences. Literal translations
// may select from an already decided language; profile/document/query locale
// detectors belong only in displayLanguage.ts. Document output has its own
// explicitly authored language and is independent of app UI copy.
export function displayLanguageViolations(source, filename) {
  if (filename === 'lib/displayLanguage.ts') return []
  const script = filename.endsWith('.vue') ? (() => {
    const { descriptor } = parse(source)
    const expressions = []
    const visit = node => {
      if (node.type === NodeTypes.INTERPOLATION) expressions.push(node.content.content)
      if (node.type === NodeTypes.ELEMENT) for (const prop of node.props) {
        if (prop.type === NodeTypes.DIRECTIVE && prop.exp) expressions.push(prop.exp.content)
      }
      for (const child of node.children ?? []) visit(child)
    }
    if (descriptor.template) visit(parseTemplate(descriptor.template.content))
    return [descriptor.script?.content, descriptor.scriptSetup?.content, ...expressions.map(value => `;(${value});`)].filter(Boolean).join('\n')
  })() : source
  const tree = ts.createSourceFile(filename, script, ts.ScriptTarget.Latest, true)
  const violations = []
  const localeNames = new Set()
  const text = node => node.getText(tree)
  const locale = node => {
    const raw = text(node).replace(/(?:displayLanguage|deliveryLanguage|noteLocale|releaseLang)\([^)]*\)/g, '')
    return /\bprofile\s*\??\.\s*locale\b/.test(raw) || /documentElement\s*\.\s*lang|(?:query|searchParams)\s*\??\.\s*lang/.test(raw)
      || [...localeNames].some(name => new RegExp(`\\b${name}\\b`).test(raw))
  }
  const walk = node => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer && !text(node.initializer).includes('displayLanguage(') && locale(node.initializer)) localeNames.add(node.name.text)
    ts.forEachChild(node, walk)
  }
  walk(tree)
  const inspect = node => {
    const raw = text(node)
    const comparison = ts.isBinaryExpression(node) && [ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken].includes(node.operatorToken.kind)
    const germanDetector = ts.isCallExpression(node) && (/\.(?:startsWith|includes|indexOf)\s*\(\s*['"]de(?:[-_][^'"]*)?['"]/.test(raw) || /\/\^de[^/]*\/[a-z]*\.test\(/.test(raw))
    const profileDetector = ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && ['test', 'match', 'search', 'startsWith', 'includes', 'indexOf'].includes(node.expression.name.text) && locale(node)
    if (germanDetector || profileDetector || comparison && locale(node) && /['"](?:de|en)(?:[-_][^'"]*)?['"]/.test(raw)) {
      const { line } = tree.getLineAndCharacterOfPosition(node.getStart(tree))
      violations.push({ file: filename, line: line + 1, expression: raw })
      return
    }
    ts.forEachChild(node, inspect)
  }
  inspect(tree)
  return violations
}
