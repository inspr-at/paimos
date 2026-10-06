// SPDX-License-Identifier: AGPL-3.0-only
import { stripTypeScriptTypes } from 'node:module'
import { inputBounds } from './inputs.mjs'

export const browserCaseLimit = 300
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
      if (/^\s|^\/\//.test(token) || token.startsWith('/*')) continue
      // Skip regex literals so their character classes/braces are not syntax.
      if (token === '/' && (!tokens.length || ['(', '=', ':', ',', '=>', 'return', '[', '!'].includes(tokens.at(-1)))) {
        const regex = /(?:\\.|\[(?:\\.|[^\]\\])*\]|[^/\\\n])+\/[a-z]*/y
        regex.lastIndex = position
        const literal = regex.exec(text)
        if (!literal) return unknown
        token += literal[0]; position = regex.lastIndex
      }
      if (token.startsWith('`') && /\$\{[\s\S]*\btest\b/.test(token)) return unknown
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
    const arrays = new Map()
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
          const arrow = tokens.indexOf('=>', start + 3)
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
          count = cap(count + arrayBound(of + 1, close) * scan(close + 1, bodyEnd))
          i = bodyEnd - 1; continue
        }
        if (['while', 'do'].includes(token)) return unknown
        if (token === 'test') {
          let open = i + 1, method = ''
          while (tokens[open] === '.') { method += `${method ? '.' : ''}${tokens[open + 1]}`; open += 2 }
          if (tokens[open] !== '(') return unknown // alias/computed access
          const close = pairs.get(open)
          if (method.startsWith('describe') && !['describe.configure'].includes(method)) {
            const arrow = tokens.indexOf('=>', open + 1)
            if (arrow < 0 || arrow >= close || tokens[arrow + 1] !== '{') return unknown
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
          if (tokens.slice(body + 1, close).includes('test')) return unknown
          i = close; continue
        }
        if (token === 'const' || token === 'let' || token === 'var') {
          const name = tokens[i + 1]
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
            if (tokens[j]==='=>' && pairs.has(j+1)) { j=pairs.get(j+1); continue }
            if (tokens[j]==='(' && tokens[pairs.get(j)+1]==='=>') { j=pairs.get(j); continue }
            if (tokens[j]==='(' && /^[\p{L}_$]/u.test(tokens[j-1]??'') &&
                !['Array','String','Number','Boolean','Date','map','flatMap','join','fill','repeat','parse','cwd'].includes(tokens[j-1])) return unknown
          }
          const alias=arrays.has(tokens[equal + 1]) && finish === equal + 2
          if (alias) arrays.set(tokens[equal + 1], unknown)
          arrays.set(name, token === 'const' && !alias ? bound : unknown)
          if (tokens.slice(equal + 1, finish).includes('test')) return unknown
          i = finish - 1; continue
        }
        if (arrays.has(token) && tokens[i + 1] === '.' && tokens[i + 2] === 'push' && tokens[i + 3] === '(') {
          const close = pairs.get(i + 3)
          const extra = tokens[i + 4] === '...' ? arrayBound(i + 5, close) : unknown
          arrays.set(token, cap(arrays.get(token) + extra)); i = close; continue
        }
        if (arrays.has(token) && ['.', '[', '='].includes(tokens[i + 1])) return unknown
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
