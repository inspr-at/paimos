// SPDX-License-Identifier: AGPL-3.0-only
// Move complete source blocks; never re-serialize descriptions, examples or schemas.
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const contract = fileURLToPath(new URL('../api/openapi.yaml', import.meta.url))
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const trivia = line => /^\s*(?:#.*)?$/.test(line)
const indent = line => line.match(/^ */)[0].length

function entry(line, depth) {
  const match = new RegExp(`^ {${depth}}("(?:[^"\\\\]|\\\\.)*"|'(?:[^']|'')*'|[^:'"\\s][^:]*):(?=\\s|$)`).exec(line)
  if (!match) throw new Error(`Unsupported mapping key: ${line.trim()}`)
  const raw = match[1].trim()
  const key = raw.startsWith('"') ? JSON.parse(raw) : raw.startsWith("'") ? raw.slice(1, -1).replaceAll("''", "'") : raw
  return { key, value: line.slice(match[0].length).trim() }
}

// Node properties (tags, anchors) and aliases precede a node's content. Strip
// them from the text and record every anchor or alias as a token, so that the
// ordering checks see anchors in sequence items, flow collections and block
// nodes, not only in whole-line mapping values.
const anchorName = /^[^\s,\[\]{}]*/
function properties(text, lines, i, column, tokens) {
  for (;;) {
    const tag = /^![^\s,\[\]{}]*\s+/.exec(text)
    if (tag) { text = text.slice(tag[0].length); column += tag[0].length; continue }
    if (!/^[&*]/.test(text)) return { text, column }
    const name = anchorName.exec(text.slice(1))[0]
    if (!name) throw new Error(`Unsupported anchor form: ${lines[i].trim()}`)
    tokens.push({ line: i, column, kind: text[0], name })
    const consumed = 1 + name.length + /^\s*/.exec(text.slice(1 + name.length))[0].length
    text = text.slice(consumed); column += consumed
  }
}

// A hash or blank line inside a scalar is data, not movable map trivia.
// Track scalar spans before interpreting mapping keys or anchor tokens. Infer
// block indentation from its first nonblank line, or use the explicit digit.
// Keep trailing blank lines too: the + chomping indicator owns those bytes.
// Tokens collects every anchor and alias outside scalar content, in order.
function scalarLines(lines, blockContent = new Set(), tokens = []) {
  const content = new Set()
  let block, quote, flowDepth = 0, prev, inlineParent
  // Flow collections can contain quoted scalars at any nesting depth. Keep
  // scanning after a closing quote: another scalar may open on the same line.
  // Delimiters must balance: a stray closer, or anything but a comment after a
  // closed top-level node, is unsupported source and must fail closed rather
  // than desynchronise the scanner for the rest of the document.
  // prev is the last significant character of the node being scanned; a node
  // starts after an opener, a separator or a key, where & and * are anchors.
  const scanInline = (text, i, offset) => {
    const line = lines[i]
    let closed = false
    for (let j = 0; j < text.length; j++) {
      const char = text[j]
      if (quote) {
        if (quote === '"' && char === '\\') { j++; continue }
        if (char !== quote) continue
        if (quote === "'" && text[j + 1] === "'") { j++; continue }
        quote = undefined
        closed = flowDepth === 0
        prev = char
        continue
      }
      if (char === '#' && (j === 0 || /\s/.test(text[j - 1]))) break
      if (/\s/.test(char)) continue
      if (char === ']' || char === '}') {
        if (--flowDepth < 0) throw new Error(`Unbalanced flow delimiter: ${line.trim()}`)
        closed = flowDepth === 0
        prev = char
        continue
      }
      if (closed) throw new Error(`Unsupported content after a flow or quoted node: ${line.trim()}`)
      const start = prev === undefined || /[[{,:]/.test(prev)
      // An explicit-key indicator (? followed by space) keeps the node start
      // open, so the key's properties are seen: {? &key name : value}. A ?
      // glued to other text is plain scalar content.
      if (start && char === '?' && (j + 1 === text.length || /\s/.test(text[j + 1]))) continue
      if (char === '[' || char === '{') flowDepth++
      else if ((char === '"' || char === "'") && (j === 0 || /[\s[{,:?]/.test(text[j - 1]))) quote = char
      else if (start && /[&*!]/.test(char)) {
        const name = anchorName.exec(text.slice(j + 1))[0]
        if (char !== '!') {
          if (!name) throw new Error(`Unsupported anchor form: ${line.trim()}`)
          tokens.push({ line: i, column: offset + j, kind: char, name })
        }
        j += name.length
        // A tag leaves the node start open for the anchor or content after it.
        if (char === '!') continue
      }
      prev = char
    }
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (quote || flowDepth) {
      const kind = quote ? 'quoted scalar' : 'flow collection'
      content.add(i)
      scanInline(line, i, 0)
      // An indented mapping key at or above the opening node's level is
      // ambiguous with a sibling. Reject it instead of hiding sortable keys
      // inside an unfinished node. Column-zero continuations remain content.
      if ((quote || flowDepth) && indent(line) > 0 && indent(line) <= inlineParent && !trivia(line)) {
        let sibling
        try { sibling = entry(line, indent(line)) } catch {}
        if (sibling) throw new Error(`Unterminated ${kind} before mapping key: ${line.trim()}`)
      }
      continue
    }
    if (block) {
      if (!line.trim()) { content.add(i); blockContent.add(i); continue }
      const depth = indent(line)
      if (depth > block.parent && (block.depth === undefined || depth >= block.depth)) {
        block.depth ??= depth
        content.add(i)
        blockContent.add(i)
        continue
      }
      block = undefined
    }
    if (trivia(line)) continue
    // Sequence scalars use the dash's indentation. A mapping inside a
    // sequence instead uses the key's column as its indentation base.
    let node = line, parent = indent(line), sequence = false
    while (node.slice(indent(node)).startsWith('- ')) {
      parent = indent(node)
      node = node.slice(0, parent) + '  ' + node.slice(parent + 2)
      sequence = true
    }
    const head = node.trim()
    // Explicit keys could carry anchors the entry scanner never sees.
    if (head === '?' || head.startsWith('? ')) throw new Error(`Unsupported explicit key: ${line.trim()}`)
    // A sequence item or a node with leading properties is a complete node
    // (- {$ref: '#/x'}, - &item {x: 1}, - *item, - |), not a mapping entry
    // keyed by its first word. Anchored mapping entries would hide the key's
    // anchor or the value's scalar span, so they fail closed.
    let value, column
    if (sequence || /^[&*!]/.test(head)) {
      ;({ text: value, column } = properties(head, lines, i, line.trimEnd().length - head.length, tokens))
      if (value !== head && /^[^\s'"\[{][^:]*:(?:\s|$)/.test(value)) throw new Error(`Unsupported anchor form: ${line.trim()}`)
      // A plain or quoted sequence item may still be a mapping entry (- "$ref": x).
      if (value === head && !/^[\[{|>]/.test(value)) value = undefined
    }
    if (value === undefined) {
      try { ({ value } = entry(node, indent(node))); parent = indent(node); column = line.trimEnd().length - value.length }
      catch { if (!sequence) continue; value = head; column = line.trimEnd().length - head.length }
      ;({ text: value, column } = properties(value, lines, i, column, tokens))
    }
    const header = /^[|>]([1-9][+-]?|[+-][1-9]?|)(?:\s+#.*)?$/.exec(value)
    if (header) {
      const digit = /[1-9]/.exec(header[1])
      block = { parent, depth: digit ? parent + Number(digit[0]) : undefined }
    } else if (/^[\[{'"]/.test(value)) {
      prev = undefined
      inlineParent = parent
      scanInline(value, i, column)
    }
  }
  if (quote || flowDepth) throw new Error(`Unterminated ${quote ? 'quoted scalar' : 'flow collection'} at EOF`)
  return content
}

function endOfMap(lines, start, depth) {
  const content = scalarLines(lines)
  let end = start + 1
  while (end < lines.length && (content.has(end) || trivia(lines[end]) || indent(lines[end]) > depth)) end++
  // Whitespace and parent-level comments belong to the surrounding section.
  while (end > start + 1 && !content.has(end - 1) && trivia(lines[end - 1])) end--
  return end
}

function sortMap(lines, start, end, depth, label) {
  const content = scalarLines(lines)
  const entries = [], names = new Set()
  for (let i = start + 1; i < end; i++) {
    if (content.has(i) || trivia(lines[i]) || indent(lines[i]) !== depth) continue
    const { key } = entry(lines[i], depth)
    if (names.has(key)) throw new Error(`Duplicate key in ${label}: ${key}`)
    names.add(key)
    let begin = i
    while (begin > start + 1 && !content.has(begin - 1) && trivia(lines[begin - 1]) &&
      (!lines[begin - 1].trim() || indent(lines[begin - 1]) >= depth)) begin--
    entries.push({ key, begin })
  }
  if (!entries.length) return
  const prefix = lines.slice(start + 1, entries[0].begin)
  const blocks = entries.map((item, i) => ({ ...item, lines: lines.slice(item.begin, entries[i + 1]?.begin ?? end) }))
  blocks.sort((a, b) => compare(a.key, b.key))
  lines.splice(start + 1, end - start - 1, ...prefix, ...blocks.flatMap(item => item.lines))
}

// The contract uses whole-line flow anchors (notably security: &auth [...]).
// Sorting can put an alias before its definition. Only that form is promoted:
// swap the definition with its first alias, retaining every anchor/alias and
// its exact value. Anchors in sequence items, nested flow collections or block
// nodes are tracked, so a sort that would move such an alias before its
// definition fails closed. Different definitions of the same name are unsafe
// to reorder and fail closed too.
const wholeLine = /^(\s*[^#\s][^:]*:\s+)(?:&([^\s,\[\]{}]+)\s+(\[.*\]|\{.*\})|\*([^\s,\[\]{}]+))(\s*(?:#.*)?)$/
function anchorTokens(lines) {
  const tokens = []
  scalarLines(lines, undefined, tokens)
  for (const token of tokens) {
    const match = wholeLine.exec(lines[token.line])
    if (match && token.column === match[1].length) Object.assign(token, { whole: true, prefix: match[1], value: match[3], suffix: match[5] })
  }
  return tokens
}

function checkAnchors(tokens, phase) {
  const definitions = new Map()
  for (const token of tokens) {
    if (token.kind === '*') {
      if (!definitions.has(token.name)) throw new Error(`Alias *${token.name} precedes its definition${phase}`)
      continue
    }
    const previous = definitions.get(token.name)
    if (previous) {
      if (!previous.whole || !token.whole) throw new Error(`Cannot reorder repeated definitions of &${token.name}; only equal whole-line flow anchors may repeat`)
      if (previous.value !== token.value) throw new Error(`Cannot reorder different definitions of &${token.name}`)
    }
    definitions.set(token.name, token)
  }
}

export function sortOpenAPI(source) {
  const newline = source.includes('\r\n') ? '\r\n' : '\n'
  const finalNewline = source.endsWith('\n')
  const lines = source.split(/\r?\n/)
  if (finalNewline) lines.pop()
  const originalBlocks = new Set()
  let content = scalarLines(lines, originalBlocks)
  // Check the original bindings before moving any blocks.
  checkAnchors(anchorTokens(lines), '')
  const paths = lines.findIndex((line, i) => line === 'paths:' && !content.has(i))
  if (paths < 0) throw new Error('Expected block mapping paths:')
  sortMap(lines, paths, endOfMap(lines, paths, 0), 2, 'paths')
  content = scalarLines(lines)
  const components = lines.findIndex((line, i) => line === 'components:' && !content.has(i))
  if (components < 0) throw new Error('Expected block mapping components:')
  const end = endOfMap(lines, components, 0)
  for (let i = components + 1; i < end; i++) {
    if (trivia(lines[i]) || indent(lines[i]) !== 2) continue
    const { key, value } = entry(lines[i], 2)
    if (value === '{}' || value.startsWith('{} #')) continue
    if (value && !value.startsWith('#')) throw new Error(`Expected block mapping components.${key}`)
    const sectionEnd = endOfMap(lines, i, 2)
    sortMap(lines, i, sectionEnd, 4, `components.${key}`)
    i = sectionEnd - 1
  }
  const tokens = anchorTokens(lines), declared = new Set()
  for (const token of tokens) {
    if (token.kind === '&') { declared.add(token.name); continue }
    if (declared.has(token.name)) continue
    const definition = tokens.find(item => item.kind === '&' && item.name === token.name)
    if (!definition) throw new Error(`Missing definition for *${token.name}`)
    if (!token.whole || !definition.whole) {
      throw new Error(`Alias *${token.name} would precede its definition after sorting; only whole-line flow anchors (key: &name [...]) can be promoted`)
    }
    lines[token.line] = `${token.prefix}&${token.name} ${definition.value}${token.suffix}`
    lines[definition.line] = `${definition.prefix}*${token.name}${definition.suffix}`
    definition.kind = '*'
    declared.add(token.name)
  }
  // Promotion moves whole lines, including any anchors nested in a promoted
  // value. Prove the final bindings instead of trusting the bookkeeping.
  checkAnchors(anchorTokens(lines), ' after sorting')
  const result = lines.join(newline) + (finalNewline ? newline : '')
  if (!finalNewline && result !== source) {
    const sortedBlocks = new Set()
    scalarLines(lines, sortedBlocks)
    // A block scalar owns its final line break. Moving it across an EOF with
    // no newline can add or remove that break from its parsed value. Reject
    // before write mode touches the file, retaining source bytes exactly.
    if (originalBlocks.has(lines.length - 1) || sortedBlocks.has(lines.length - 1)) {
      throw new Error('Cannot move a block scalar into or out of unterminated EOF; add a final newline before sorting')
    }
  }
  return result
}

export function main(args, path = contract, log = console.log) {
  if (args.length !== 1 || !['--check', '--write'].includes(args[0])) throw new Error('Usage: node scripts/openapi-sort.mjs --check|--write')
  const source = readFileSync(path, 'utf8'), sorted = sortOpenAPI(source)
  if (source === sorted) { log('OpenAPI paths and components are sorted'); return 0 }
  if (args[0] === '--check') { log('OpenAPI is unsorted; run node scripts/openapi-sort.mjs --write'); return 1 }
  writeFileSync(path, sorted)
  log('Sorted OpenAPI paths and components')
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 2 }
}
