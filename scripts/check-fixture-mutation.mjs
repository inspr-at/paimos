// SPDX-License-Identifier: AGPL-3.0-only
// AEON-410: Playwright specs must not mutate objects they imported. A push onto
// a shared fixture array survived across files in one worker (AEON-373).
// Factories return a fresh object; mutate that, not the import. Unit tests are
// out of this scan: they reset module sentinels such as sessionEnded in afterEach
// and do not share a Playwright worker.
import { createRequire } from 'node:module'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const mutatingMethods = new Set(['push', 'pop', 'shift', 'unshift', 'splice', 'sort', 'reverse', 'fill', 'copyWithin', 'add', 'clear', 'set', 'delete'])
const iterators = new Set(['forEach', 'map', 'flatMap', 'filter', 'find', 'findIndex', 'every', 'some'])

let typescript
function loadTypeScript() {
  if (typescript) return typescript
  try {
    const require = createRequire(resolve(root, 'web/package.json'))
    typescript = require('typescript')
  } catch {
    throw new Error('fixture mutation check needs web dependencies (npm ci in web/)')
  }
  return typescript
}

function unwrap(ts, node) {
  while (node && (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isTypeAssertionExpression(node) || ts.isNonNullExpression(node) || ts.isSatisfiesExpression(node))) {
    node = node.expression
  }
  return node
}

export function findMutations(source, fileName) {
  const ts = loadTypeScript()
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const findings = []
  const scopes = [new Map()]

  const lookup = name => {
    for (let i = scopes.length - 1; i >= 0; i--) {
      if (scopes[i].has(name)) return scopes[i].get(name)
    }
    return false
  }
  const declare = (name, tainted) => { scopes[scopes.length - 1].set(name, tainted) }

  const taintedExpr = node => {
    node = unwrap(ts, node)
    if (!node) return false
    if (ts.isIdentifier(node)) return lookup(node.text)
    if (ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node)) return taintedExpr(node.expression)
    return false
  }

  const bindPattern = (pattern, tainted) => {
    if (!pattern) return
    if (ts.isIdentifier(pattern)) declare(pattern.text, tainted)
    else if (ts.isObjectBindingPattern(pattern) || ts.isArrayBindingPattern(pattern)) {
      for (const el of pattern.elements) {
        if (ts.isBindingElement(el)) bindPattern(el.name, tainted)
      }
    }
  }

  const note = (node, message) => {
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf))
    findings.push({ file: fileName, line: line + 1, message })
  }

  const isAssignment = kind => kind === ts.SyntaxKind.EqualsToken
    || kind === ts.SyntaxKind.PlusEqualsToken
    || kind === ts.SyntaxKind.MinusEqualsToken
    || kind === ts.SyntaxKind.AsteriskEqualsToken
    || kind === ts.SyntaxKind.SlashEqualsToken
    || kind === ts.SyntaxKind.PercentEqualsToken
    || kind === ts.SyntaxKind.AmpersandEqualsToken
    || kind === ts.SyntaxKind.BarEqualsToken
    || kind === ts.SyntaxKind.CaretEqualsToken
    || kind === ts.SyntaxKind.LessThanLessThanEqualsToken
    || kind === ts.SyntaxKind.GreaterThanGreaterThanEqualsToken
    || kind === ts.SyntaxKind.GreaterThanGreaterThanGreaterThanEqualsToken
    || kind === ts.SyntaxKind.AsteriskAsteriskEqualsToken
    || kind === ts.SyntaxKind.BarBarEqualsToken
    || kind === ts.SyntaxKind.AmpersandAmpersandEqualsToken
    || kind === ts.SyntaxKind.QuestionQuestionEqualsToken

  const visitIteratorCallback = fn => {
    scopes.push(new Map())
    fn.parameters.forEach((param, index) => bindPattern(param.name, index === 0))
    if (fn.body) {
      if (ts.isBlock(fn.body)) ts.forEachChild(fn.body, visit)
      else visit(fn.body)
    }
    scopes.pop()
  }

  function visit(node) {
    if (ts.isFunctionDeclaration(node) && node.name) declare(node.name.text, false)
    if (ts.isClassDeclaration(node) && node.name) declare(node.name.text, false)

    if (ts.isFunctionDeclaration(node) || ts.isFunctionExpression(node) || ts.isArrowFunction(node) || ts.isMethodDeclaration(node) || ts.isConstructorDeclaration(node) || ts.isGetAccessor(node) || ts.isSetAccessor(node)) {
      scopes.push(new Map())
      for (const param of node.parameters) bindPattern(param.name, false)
      ts.forEachChild(node, visit)
      scopes.pop()
      return
    }

    if (ts.isImportDeclaration(node)) {
      if (!node.importClause || node.importClause.isTypeOnly) return
      const spec = node.moduleSpecifier
      if (!ts.isStringLiteral(spec) || !spec.text.startsWith('.')) return
      const clause = node.importClause
      if (clause.name) declare(clause.name.text, true)
      const bindings = clause.namedBindings
      if (bindings && ts.isNamespaceImport(bindings)) declare(bindings.name.text, true)
      if (bindings && ts.isNamedImports(bindings)) {
        for (const el of bindings.elements) {
          if (!el.isTypeOnly) declare(el.name.text, true)
        }
      }
      return
    }

    if (ts.isVariableDeclaration(node)) {
      const tainted = node.initializer ? taintedExpr(node.initializer) : false
      bindPattern(node.name, tainted)
      if (node.initializer) visit(node.initializer)
      return
    }

    if (ts.isForOfStatement(node) || ts.isForInStatement(node)) {
      visit(node.expression)
      const tainted = taintedExpr(node.expression)
      scopes.push(new Map())
      if (ts.isVariableDeclarationList(node.initializer)) {
        for (const decl of node.initializer.declarations) bindPattern(decl.name, tainted)
      }
      visit(node.statement)
      scopes.pop()
      return
    }

    if (ts.isCallExpression(node)) {
      const callee = unwrap(ts, node.expression)
      if (callee && ts.isPropertyAccessExpression(callee)) {
        if (mutatingMethods.has(callee.name.text) && taintedExpr(callee.expression)) {
          note(node, `mutates an imported fixture via .${callee.name.text}()`)
        }
        const objectName = unwrap(ts, callee.expression)
        if (objectName && ts.isIdentifier(objectName) && objectName.text === 'Object' && (callee.name.text === 'assign' || callee.name.text === 'defineProperty' || callee.name.text === 'setPrototypeOf')) {
          const target = node.arguments[0]
          if (target && taintedExpr(target)) note(node, `mutates an imported fixture via Object.${callee.name.text}()`)
        }
        const callback = node.arguments[0]
        if (iterators.has(callee.name.text) && taintedExpr(callee.expression) && callback && (ts.isArrowFunction(callback) || ts.isFunctionExpression(callback))) {
          visit(node.expression)
          node.arguments.forEach((arg, index) => {
            if (index === 0) visitIteratorCallback(callback)
            else visit(arg)
          })
          return
        }
      }
    }

    if (ts.isBinaryExpression(node) && isAssignment(node.operatorToken.kind)) {
      const left = unwrap(ts, node.left)
      if (left && (ts.isPropertyAccessExpression(left) || ts.isElementAccessExpression(left)) && taintedExpr(left)) {
        note(node, 'assigns into an imported fixture')
      }
    }

    if (ts.isDeleteExpression(node) && taintedExpr(node.expression)) note(node, 'deletes a property of an imported fixture')

    if ((ts.isPrefixUnaryExpression(node) || ts.isPostfixUnaryExpression(node))
      && (node.operator === ts.SyntaxKind.PlusPlusToken || node.operator === ts.SyntaxKind.MinusMinusToken)) {
      const operand = unwrap(ts, node.operand)
      if (operand && !ts.isIdentifier(operand) && taintedExpr(operand)) note(node, 'updates a property of an imported fixture')
    }

    ts.forEachChild(node, visit)
  }

  visit(sf)
  return findings
}

function walk(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules') continue
    const path = join(dir, name)
    if (statSync(path).isDirectory()) walk(path, out)
    else if (name.endsWith('.spec.ts')) out.push(path)
  }
}

export function scanTests(repo = root) {
  const dir = join(repo, 'web/tests')
  const files = []
  walk(dir, files)
  const findings = []
  for (const file of files) {
    for (const finding of findMutations(readFileSync(file, 'utf8'), file)) {
      findings.push({ ...finding, file: relative(repo, finding.file) })
    }
  }
  return findings
}

function main() {
  const findings = scanTests()
  if (!findings.length) return
  for (const finding of findings) console.error(`${finding.file}:${finding.line}: ${finding.message}`)
  console.error(`${findings.length} imported-fixture mutation${findings.length === 1 ? '' : 's'}`)
  process.exit(1)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main()
