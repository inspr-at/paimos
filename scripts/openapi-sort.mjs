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

// A hash or blank line inside a scalar is data, not movable map trivia.
// Track scalar spans before interpreting mapping keys or anchor tokens. Infer
// block indentation from its first nonblank line, or use the explicit digit.
// Keep trailing blank lines too: the + chomping indicator owns those bytes.
function scalarLines(lines, blockContent = new Set()) {
  const content = new Set()
  let block, quote, flowDepth = 0
  // Flow collections can contain quoted scalars at any nesting depth. Keep
  // scanning after a closing quote: another scalar may open on the same line.
  const scanInline = text => {
    for (let j = 0; j < text.length; j++) {
      const char = text[j]
      if (quote) {
        if (quote === '"' && char === '\\') { j++; continue }
        if (char !== quote) continue
        if (quote === "'" && text[j + 1] === "'") { j++; continue }
        quote = undefined
      } else {
        if (char === '#' && (j === 0 || /\s/.test(text[j - 1]))) break
        if (char === '[' || char === '{') flowDepth++
        else if (char === ']' || char === '}') flowDepth--
        else if ((char === '"' || char === "'") && (j === 0 || /[\s[{,:?]/.test(text[j - 1]))) quote = char
      }
    }
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (quote || flowDepth) {
      content.add(i)
      scanInline(line)
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
    let value
    try { ({ value } = entry(node, indent(node))); parent = indent(node) }
    catch {
      if (!sequence) continue
      value = node.trim()
    }
    // Tags modify a scalar's type, not its source boundaries.
    value = value.replace(/^(?:!\S+\s+)+/, '')
    const header = /^[|>]([1-9][+-]?|[+-][1-9]?|)(?:\s+#.*)?$/.exec(value)
    if (header) {
      const digit = /[1-9]/.exec(header[1])
      block = { parent, depth: digit ? parent + Number(digit[0]) : undefined }
    } else {
      const inline = value.replace(/^&[\w-]+\s+/, '')
      if (/^[\[{'"]/.test(inline)) scanInline(inline)
    }
  }
  return content
}

function endOfMap(lines, start, depth) {
  const content = scalarLines(lines)
  let end = start + 1
  while (end < lines.length && (trivia(lines[end]) || indent(lines[end]) > depth)) end++
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
// Sorting can put an alias before its definition. Swap that definition with
// the first alias, retaining every anchor/alias and its exact value. Different
// definitions of the same name are unsafe to reorder and must fail closed.
function anchorLines(lines) {
  const content = scalarLines(lines)
  const definitions = new Map(), uses = []
  for (let i = 0; i < lines.length; i++) {
    if (content.has(i)) continue
    const match = /^(\s*[^#\s][^:]*:\s+)(?:&([\w-]+)\s+(\[.*\]|\{.*\})|\*([\w-]+))(\s*(?:#.*)?)$/.exec(lines[i])
    if (!match) {
      if (/^\s*[^#\s][^:]*:\s+[&*][\w-]+/.test(lines[i])) throw new Error(`Unsupported anchor form: ${lines[i].trim()}`)
      continue
    }
    const [, prefix, name, value, alias, suffix] = match
    if (name) {
      if (definitions.has(name) && definitions.get(name) !== value) throw new Error(`Cannot reorder different definitions of &${name}`)
      definitions.set(name, value)
    }
    uses.push({ i, prefix, name, value, alias, suffix })
  }
  return { definitions, uses }
}

export function sortOpenAPI(source) {
  const newline = source.includes('\r\n') ? '\r\n' : '\n'
  const finalNewline = source.endsWith('\n')
  const lines = source.split(/\r?\n/)
  if (finalNewline) lines.pop()
  const originalBlocks = new Set()
  scalarLines(lines, originalBlocks)
  // Check the original bindings before moving any blocks.
  const original = anchorLines(lines)
  const seen = new Set()
  for (const use of original.uses) {
    if (use.name) seen.add(use.name)
    else if (!seen.has(use.alias)) throw new Error(`Alias *${use.alias} precedes its definition`)
  }
  const paths = lines.indexOf('paths:')
  if (paths < 0) throw new Error('Expected block mapping paths:')
  sortMap(lines, paths, endOfMap(lines, paths, 0), 2, 'paths')
  const components = lines.indexOf('components:')
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
  const sorted = anchorLines(lines), declared = new Set()
  for (const use of sorted.uses) {
    if (use.name) { declared.add(use.name); continue }
    if (declared.has(use.alias)) continue
    const definition = sorted.uses.find(item => item.name === use.alias)
    if (!definition) throw new Error(`Missing definition for *${use.alias}`)
    lines[use.i] = `${use.prefix}&${use.alias} ${definition.value}${use.suffix}`
    lines[definition.i] = `${definition.prefix}*${use.alias}${definition.suffix}`
    definition.name = undefined
    definition.alias = use.alias
    declared.add(use.alias)
  }
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
