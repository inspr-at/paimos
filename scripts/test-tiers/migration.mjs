// SPDX-License-Identifier: AGPL-3.0-only
import { inputBounds } from './inputs.mjs'

// One lexical pass: comments are recognised only outside strings/identifiers.
// Dollar bodies are tokens, never SQL we can safely analyse as top-level DDL.
function tokenize(sql) {
  const tokens = []
  for (let i = 0; i < sql.length;) {
    if (tokens.length >= 100_000) throw new Error('SQL token bound reached')
    const c = sql[i], next = sql[i + 1]
    if (/\s/.test(c)) { i++; continue }
    if (c === '-' && next === '-') { while (i < sql.length && sql[i] !== '\n' && sql[i] !== '\r') i++; continue }
    if (c === '/' && next === '*') {
      let depth = 1; i += 2
      while (i < sql.length && depth) {
        if (sql.slice(i, i + 2) === '/*') { depth++; i += 2 }
        else if (sql.slice(i, i + 2) === '*/') { depth--; i += 2 }
        else i++
      }
      if (depth) throw new Error('unbalanced comment')
      continue
    }
    if (c === "'" || c === '"') {
      const quote = c; let value = '', closed = false; i++
      while (i < sql.length) {
        if (sql[i] === quote) {
          if (sql[i + 1] === quote) { value += quote; i += 2 }
          else { i++; closed = true; break }
        } else {
          // Backslash string semantics depend on settings/prefixes. Decline.
          if (sql[i] === '\\') throw new Error('ambiguous escape')
          value += sql[i++]
        }
      }
      if (!closed) throw new Error('unbalanced quote')
      tokens.push({ kind: quote === '"' ? 'identifier' : 'literal', value })
      continue
    }
    if (c === '$') {
      const tag = /^(?:\$\$|\$[A-Za-z_][A-Za-z0-9_]*\$)/.exec(sql.slice(i))?.[0]
      if (!tag) throw new Error('unknown dollar token')
      const end = sql.indexOf(tag, i + tag.length)
      if (end < 0) throw new Error('unbalanced dollar quote')
      tokens.push({ kind: 'body', value: sql.slice(i, end + tag.length) }); i = end + tag.length
      continue
    }
    const word = /^[A-Za-z_][A-Za-z0-9_]*/.exec(sql.slice(i))?.[0]
    if (word) { tokens.push({ kind: 'word', value: word.toLowerCase() }); i += word.length; continue }
    const number = /^\d+(?:\.\d+)?/.exec(sql.slice(i))?.[0]
    if (number) { tokens.push({ kind: 'number', value: number }); i += number.length; continue }
    if ('(),;.=<>+-'.includes(c)) { tokens.push({ kind: 'symbol', value: c }); i++; continue }
    throw new Error('unknown SQL token')
  }
  return tokens
}

const types = new Set(['uuid', 'text', 'boolean', 'bool', 'smallint', 'integer', 'int', 'bigint', 'real', 'serial', 'bigserial', 'json', 'jsonb', 'bytea', 'date', 'timestamp', 'timestamptz', 'numeric', 'decimal', 'varchar'])

// A deliberately small grammar. Every token must be consumed; adding support
// requires a complete production, never a regex matching only its prefix.
function statement(tokens, objects) {
  let i = 0
  const take = value => tokens[i]?.kind !== 'literal' && tokens[i]?.kind !== 'identifier' && tokens[i]?.value === value && (++i > 0)
  const need = value => { if (!take(value)) throw new Error(`expected ${value}`) }
  const identifier = () => {
    const token = tokens[i++]
    if (!token || !['word', 'identifier'].includes(token.kind) || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(token.value)) throw new Error('expected plain identifier')
    return token.value
  }
  const table = () => {
    let name = identifier()
    if (take('.')) {
      if (name !== 'public') throw new Error('unsupported schema')
      name = identifier()
    }
    objects.add(name)
  }
  const literal = () => {
    if (take('-') || take('+')) { if (tokens[i]?.kind !== 'number') throw new Error('expected number') }
    const token = tokens[i++]
    if (!token || !(token.kind === 'literal' || token.kind === 'number' || token.kind === 'word' && ['true', 'false', 'null'].includes(token.value))) throw new Error('expected constant')
  }
  const identifiers = () => {
    need('('); identifier()
    while (take(',')) identifier()
    need(')')
  }
  const column = () => {
    identifier()
    const type = tokens[i++]
    if (type?.kind !== 'word' || !types.has(type.value)) throw new Error('unsupported type')
    if (['varchar', 'numeric', 'decimal'].includes(type.value) && take('(')) {
      if (tokens[i++]?.kind !== 'number') throw new Error('expected type size')
      if (take(',') && tokens[i++]?.kind !== 'number') throw new Error('expected type scale')
      need(')')
    }
    if (take('not')) need('null')
    else take('null')
    if (take('default')) literal()
    if (take('primary')) need('key')
    else take('unique')
  }
  const predicate = () => {
    identifier()
    if (take('is')) { take('not'); need('null') }
    else {
      if (!(take('=') || take('<') || take('>'))) throw new Error('unsupported predicate')
      literal()
    }
    if (take('and') || take('or')) predicate()
  }
  if (take('create')) {
    const unique = take('unique')
    if (!unique && take('table')) {
      if (take('if')) { need('not'); need('exists') }
      table(); need('(')
      const entry = () => {
        if (take('primary')) { need('key'); identifiers() }
        else if (take('unique')) identifiers()
        else column()
      }
      entry(); while (take(',')) entry(); need(')')
    } else {
      need('index')
      if (take('if')) { need('not'); need('exists') }
      identifier(); need('on'); table(); identifiers()
    }
  } else if (take('alter')) {
    need('table'); take('only'); table(); need('add'); need('column')
    if (take('if')) { need('not'); need('exists') }
    column()
  } else if (take('insert')) {
    need('into'); table(); identifiers(); need('values')
    const values = () => { need('('); literal(); while (take(',')) literal(); need(')') }
    values(); while (take(',')) values()
  } else if (take('update')) {
    table(); need('set')
    const assignment = () => { identifier(); need('='); literal() }
    assignment(); while (take(',')) assignment()
    if (take('where')) predicate()
  } else if (take('delete')) {
    need('from'); table(); if (take('where')) predicate()
  } else throw new Error('unsupported statement')
  if (i !== tokens.length) throw new Error('partially understood statement')
}

// Undefined means FULL for the *whole* migration, including any earlier DDL.
export function migrationObjects(sql) {
  if (typeof sql !== 'string' || Buffer.byteLength(sql) >= inputBounds.fileBytes) return undefined
  try {
    const tokens = tokenize(sql), objects = new Set()
    let start = 0
    for (let i = 0; i <= tokens.length; i++) {
      if (i !== tokens.length && !(tokens[i].kind === 'symbol' && tokens[i].value === ';')) continue
      if (i === start) {
        if (i < tokens.length) throw new Error('empty SQL statement')
      } else statement(tokens.slice(start, i), objects)
      start = i + 1
    }
    return objects
  } catch { return undefined }
}
