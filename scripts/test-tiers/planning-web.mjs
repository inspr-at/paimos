// SPDX-License-Identifier: AGPL-3.0-only
import { stripTypeScriptTypes } from 'node:module'
import { inputBounds } from './inputs.mjs'

export const browserCaseLimit = 300
export const planningTokenLimit = 20_000
const unknown = browserCaseLimit + 1
const cap = n => Math.min(unknown, n)

// A dependency-free upper bound, never a native inventory. Recognise direct
// Playwright registrations, describe blocks and finite literal-array loops.
// Test/hook callbacks execute after collection and are excluded. Unsupported
// registration factories, iteration or syntax exceed the lane limit.
export function planningWebCases(text) {
  if (typeof text !== 'string' || Buffer.byteLength(text) >= inputBounds.fileBytes) return unknown
  try {
    text = stripTypeScriptTypes(text)
    const tokens = [], starts = [], ends = []
    const lex = /\s+|\/\/[^\n]*|\/\*[\s\S]*?\*\/|'(?:\\[\s\S]|[^'\\])*'|"(?:\\[\s\S]|[^"\\])*"|`(?:\\[\s\S]|[^`\\])*`|[\p{L}_$][\p{L}\p{N}_$]*|\d+(?:\.\d+)?|=>|\.\.\.|\?\.|[^\s]/uy
    let position = 0
    while (position < text.length) {
      lex.lastIndex = position
      const match = lex.exec(text)
      if (!match) return unknown
      let token = match[0]
      position = lex.lastIndex
      if ((token.startsWith("'") || token.startsWith('"')) && /[\r\n]/.test(token)) return unknown
      if (token === "'" || token === '"' || token === '`') return unknown
      if (/^\s|^\/\//.test(token) || token.startsWith('/*')) continue
      // Skip regex literals so their character classes/braces are not syntax.
      if (token === '/' && (!tokens.length || ['(', '=', ':', ',', '=>', 'return', '[', '!'].includes(tokens.at(-1)))) {
        const regex = /(?:\\.|\[(?:\\.|[^\]\\])*\]|[^/\\\n])+\/[a-z]*/y
        regex.lastIndex = position
        const literal = regex.exec(text)
        if (!literal) return unknown
        token += literal[0]; position = regex.lastIndex
      }
      // Slash outside a proven regex context is ambiguous (division or regex).
      if (token === '/') return unknown
      if (token.startsWith('`') && token.includes('${') && /\btest\b/.test(token)) return unknown
      if (tokens.length >= planningTokenLimit) return unknown
      starts.push(match.index); ends.push(position); tokens.push(token)
    }
    const pairs = new Map(), stack = []
    for (let i = 0; i < tokens.length; i++) {
      const token = tokens[i]
      if (['(', '[', '{'].includes(token)) stack.push(i)
      if ([')', ']', '}'].includes(token)) {
        const start = stack.pop()
        if (tokens[start] !== { ')': '(', ']': '[', '}': '{' }[token]) return unknown
        pairs.set(start, i)
      }
    }
    if (stack.length) return unknown
    const arrays = new Map(), declarations = new Set()
    // Constant-time range queries keep repeated scans from copying token slices.
    const testPrefix = [0], nextArrow = new Array(tokens.length + 1).fill(-1)
    for (const token of tokens) testPrefix.push(testPrefix.at(-1) + Number(token === 'test'))
    for (let i = tokens.length - 1; i >= 0; i--) nextArrow[i] = tokens[i] === '=>' ? i : nextArrow[i + 1]
    const hasTest = (start, end) => testPrefix[end] > testPrefix[start]
    const indirectCall = i => (tokens[i] === '?.' && tokens[i + 1] === '(') ||
      (tokens[i] === ')' && tokens[i + 1] === '(') ||
      (/^[\p{L}_$]/u.test(tokens[i]) && tokens[i + 1]?.startsWith('`'))
    const lastArgument = (open, close) => {
      let last = open + 1
      for (let i = last; i < close; i++) {
        if (tokens[i] === ',') last = i + 1
        if (pairs.has(i)) i = pairs.get(i)
      }
      return last
    }
    // Count a literal array including nested objects/tuples as single entries.
    const literalCount = start => {
      const end = pairs.get(start)
      if (tokens[start] !== '[' || end === undefined) return unknown
      let n = 0, entry = false
      for (let i = start + 1; i < end; i++) {
        if (tokens[i] === '...') return unknown
        if (tokens[i] === ',') { n++; entry = false; continue }
        entry = true
        if (pairs.has(i)) i = pairs.get(i)
      }
      return cap(n + Number(entry))
    }
    const arrayBound = (start, end) => {
      if (tokens[start] === '(' && pairs.get(start) === end - 1) return arrayBound(start + 1, end - 1)
      // A finite conditional iterator takes at most the larger branch.
      let question = -1, colon = -1
      for (let i = start; i < end; i++) {
        if (tokens[i] === '?' && question < 0) question = i
        else if (tokens[i] === ':' && question >= 0) { colon = i; break }
        if (pairs.has(i)) i = pairs.get(i)
      }
      if (question >= 0 && colon >= 0) return Math.max(arrayBound(question + 1, colon), arrayBound(colon + 1, end))
      let n
      if (tokens[start] === '[') { n = literalCount(start); start = pairs.get(start) + 1 }
      else { n = arrays.get(tokens[start]) ?? unknown; start++ }
      if (tokens[start] === 'as' && tokens[start + 1] === 'const') start += 2
      while (start < end) {
        if (tokens[start] !== '.' || !['map', 'flatMap'].includes(tokens[start + 1]) || tokens[start + 2] !== '(') return unknown
        const method = tokens[start + 1], close = pairs.get(start + 2)
        if (close === undefined || close >= end) return unknown
        if (method === 'flatMap') {
          // Only a literal array returned directly by the arrow is bounded.
          const arrow = nextArrow[start + 3]
          if (arrow < 0 || arrow >= close) return unknown
          n = cap(n * arrayBound(arrow + 1, close))
        }
        start = close + 1
      }
      return n
    }
    const statementEnd = start => {
      if (tokens[start] === '{') return pairs.get(start) + 1
      if (tokens[start] === 'for' && tokens[start + 1] === '(') return statementEnd(pairs.get(start + 1) + 1)
      if (tokens[start] === 'test') {
        let open = start + 1
        while (tokens[open] === '.' && open < tokens.length) open += 2
        if (tokens[open] === '(') return pairs.get(open) + 1
      }
      return undefined
    }
    const scan = (start, end) => {
      let count = 0
      for (let i = start; i < end; i++) {
        const token = tokens[i]
        if (token === 'import') {
          // Static imports bind the fixture; aliased/computed test access is
          // rejected when used below. Dynamic imports are unsupported.
          if (tokens[i + 1] === '(') return unknown
          while (++i < end && !/^['"]/.test(tokens[i])) {}
          continue
        }
        if (token === 'for') {
          if (tokens[i + 1] !== '(') return unknown
          const close = pairs.get(i + 1), bodyEnd = statementEnd(close + 1)
          let of = -1
          for (let j = i + 2; j < close; j++) {
            if (tokens[j] === 'of') { of = j; break }
            if (pairs.has(j)) j = pairs.get(j)
          }
          if (of < 0 || bodyEnd === undefined) return unknown
          if (scan(of + 1, close) !== 0) return unknown
          count = cap(count + arrayBound(of + 1, close) * scan(close + 1, bodyEnd))
          i = bodyEnd - 1; continue
        }
        if (['while', 'do'].includes(token)) return unknown
        if (token === 'test') {
          let open = i + 1, method = ''
          while (tokens[open] === '.') { method += `${method ? '.' : ''}${tokens[open + 1]}`; open += 2 }
          if (tokens[open] !== '(') return unknown // alias/computed access
          const close = pairs.get(open), callback = lastArgument(open, close)
          // Titles/options and iterators run during collection too.
          const arrow = nextArrow[callback]
          const callbackStart = tokens[callback] === 'async' ? callback + 1 : callback
          const isCallback = arrow >= callback && arrow < close &&
            (arrow === callbackStart + 1 || (tokens[callbackStart] === '(' && pairs.get(callbackStart) === arrow - 1))
          const argsEnd = isCallback ? callback - (callback > open + 1 ? 1 : 0) : close
          if (scan(open + 1, argsEnd) !== 0) return unknown
          if (method.startsWith('describe') && !['describe.configure'].includes(method)) {
            if (arrow < 0 || arrow >= close || tokens[arrow + 1] !== '{' || pairs.get(arrow + 1) !== close - 1) return unknown
            count = cap(count + scan(arrow + 2, pairs.get(arrow + 1)))
          } else if (['', 'only', 'skip', 'fixme', 'fail'].includes(method)) count = cap(count + 1)
          else if (!['beforeEach', 'afterEach', 'beforeAll', 'afterAll', 'use', 'setTimeout', 'describe.configure'].includes(method)) return unknown
          i = close; continue
        }
        if (token === 'function') {
          // Helper functions containing registrations are factories: no bound.
          let body = i + 1
          while (body < end && tokens[body] !== '{') {
            if (pairs.has(body)) body = pairs.get(body)
            body++
          }
          if (body >= end) return unknown
          const close = pairs.get(body)
          if (hasTest(body + 1, close)) return unknown
          i = close; continue
        }
        if (token === 'const' || token === 'let' || token === 'var') {
          const name = tokens[i + 1]
          if (declarations.has(name)) return unknown
          declarations.add(name)
          let equal = i + 2
          while (equal < end && !['=', ';', 'const', 'let', 'test', 'for', 'function'].includes(tokens[equal])) equal++
          if (tokens[equal] !== '=') continue
          let finish = equal + 1
          while (finish < end && ![';', 'const', 'let', 'test', 'for', 'function', 'async'].includes(tokens[finish])) {
            if (pairs.has(finish)) finish = pairs.get(finish)
            finish++
            if (finish < end && /[\r\n]/.test(text.slice(ends[finish - 1], starts[finish])) && tokens[finish] !== '.' && !['=', '=>', ',', '.'].includes(tokens[finish - 1])) break
          }
          const init=equal + 1, first=tokens[init]
          const inertArrow=tokens[init + 1]==='=>' || (first==='(' && tokens[pairs.get(init) + 1]==='=>')
          const bound=arrayBound(init,finish)
          if (!inertArrow) for (let j=init;j<finish;j++) {
            if (indirectCall(j)) return unknown
            if (tokens[j]==='=>' && pairs.has(j+1)) { j=pairs.get(j+1); continue }
            if (tokens[j]==='(' && tokens[pairs.get(j)+1]==='=>') { j=pairs.get(j); continue }
            if (tokens[j]==='(' && /^[\p{L}_$]/u.test(tokens[j-1]??'') &&
                !['Array','String','Number','Boolean','Date','map','flatMap','join','fill','repeat','parse','cwd'].includes(tokens[j-1])) return unknown
          }
          const alias=arrays.has(tokens[equal + 1]) && finish === equal + 2
          if (alias) arrays.set(tokens[equal + 1], unknown)
          arrays.set(name, token === 'const' && !alias ? bound : unknown)
          if (hasTest(equal + 1, finish)) return unknown
          i = finish - 1; continue
        }
        if (arrays.has(token) && tokens[i + 1] === '.' && tokens[i + 2] === 'push' && tokens[i + 3] === '(') {
          const close = pairs.get(i + 3)
          const extra = tokens[i + 4] === '...' ? arrayBound(i + 5, close) : unknown
          arrays.set(token, cap(arrays.get(token) + extra)); i = close; continue
        }
        if (arrays.has(token) && ['.', '[', '='].includes(tokens[i + 1])) return unknown
        if (indirectCall(i)) return unknown
        // Calling a registration factory or mutating an iterator cannot be
        // inferred. Calls inside test/hook/helper bodies were handled above.
        if (tokens[i + 1] === '(' && /^[\p{L}_$]/u.test(token) && !['if', 'switch', 'catch', 'import'].includes(token)) return unknown
        if (token === '=>') {
          // An initializer callback could register when called repeatedly.
          const bodyEnd = statementEnd(i + 1)
          if (bodyEnd !== undefined) {
            if (scan(i + 1, bodyEnd)) return unknown
            i = bodyEnd - 1
          }
        }
      }
      return count
    }
    return scan(0, tokens.length)
  } catch { return unknown }
}

// Reuse the already-collected native catalogue; no additional collection/jobs.
export function assertPlanningWebBounds(rows, readFile) {
  const counts = new Map()
  for (const row of rows) if (row.kind === 'browser') counts.set(row.file, (counts.get(row.file) ?? 0) + 1)
  for (const [file, native] of counts) {
    const estimate = planningWebCases(readFile(file))
    if (estimate <= browserCaseLimit && estimate < native) {
      throw new Error(`Browser planner undercount: ${file}: planner ${estimate}, native ${native}`)
    }
  }
  return counts.size
}
