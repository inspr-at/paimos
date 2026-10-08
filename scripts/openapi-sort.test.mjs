// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { main, sortOpenAPI } from './openapi-sort.mjs'
import '../api/generate.test.mjs'
import { generateOpenAPI } from '../api/generate.mjs'

const fixture = `openapi: 3.1.0
paths:
  # Zebra's comment travels with its path.
  /z:
    get:
      security: &auth [{session: []}, {agentKey: []}] # definition
      description: >-
        Keep this text and its formatting: exactly.
  # Alpha's comment travels too.
  '/a':
    get:
      security: *auth # alias
components:
  # Section comment stays here.
  schemas:
    Zebra: {type: object, required: [z, a], properties: {z: {}, a: {}}}
    # Schema comment
    Alpha: {type: string}
  parameters:
    z: {name: z, in: query}
    a: {name: a, in: query}
  responses:
    Z: {description: 'Z: response'}
    A: {description: 'A: response'}
  headers:
    Z: {schema: {type: string}}
    A: {schema: {type: string}}
  securitySchemes:
    z: {type: http, scheme: bearer}
    a: {type: apiKey, in: cookie, name: session}
  requestBodies:
    Z: {content: {}}
    A: {content: {}}
security: [{session: []}, {agentKey: []}]
`

test('sorts paths and every component map without changing nested content or comments', () => {
  const sorted = sortOpenAPI(fixture)
  assert.ok(sorted.indexOf("  '/a':") < sorted.indexOf('  /z:'))
  assert.ok(sorted.indexOf('    Alpha:') < sorted.indexOf('    Zebra:'))
  for (const section of ['parameters', 'responses', 'headers', 'securitySchemes', 'requestBodies']) {
    const block = sorted.split(`  ${section}:\n`)[1].split(/\n  \S/)[0]
    assert.match(block, /^    [aA]:/)
  }
  assert.match(sorted, /# Alpha's comment travels too\.\n  '\/a':/)
  assert.match(sorted, /# Zebra's comment travels with its path\.\n  \/z:/)
  assert.match(sorted, /# Section comment stays here\.\n  schemas:\n    # Schema comment\n    Alpha:/)
  for (const line of fixture.split('\n').filter(line => /description:|Keep this|Zebra:|^security:/.test(line))) assert.ok(sorted.includes(line))
  assert.equal(sortOpenAPI(sorted), sorted)
})

test('promotes a flow anchor before its first alias, preserving definitions, values and inline comments', () => {
  const sorted = sortOpenAPI(fixture)
  assert.match(sorted, /'\/a':\n    get:\n      security: &auth \[\{session: \[\]\}, \{agentKey: \[\]\}\] # alias/)
  assert.match(sorted, /\/z:\n    get:\n      security: \*auth # definition/)
  assert.equal((sorted.match(/&auth/g) ?? []).length, 1)
  assert.equal((sorted.match(/\*auth/g) ?? []).length, 1)
  // The real contract has two equal declarations of &auth.
  const repeated = fixture.replace('components:', '  /m:\n    get:\n      security: &auth [{session: []}, {agentKey: []}]\ncomponents:')
  const result = sortOpenAPI(repeated)
  assert.equal((result.match(/&auth/g) ?? []).length, 2)
  assert.equal(sortOpenAPI(result), result)
})

test('keeps CRLF and final-newline convention, handles quoted names and empty maps', () => {
  for (const newline of ['\n', '\r\n']) for (const ending of ['', newline]) {
    const input = fixture.trimEnd().replaceAll('\n', newline) + ending
    const output = sortOpenAPI(input)
    assert.equal(output.endsWith('\n'), Boolean(ending))
    if (newline === '\r\n') assert.doesNotMatch(output, /(?<!\r)\n/)
    assert.equal(sortOpenAPI(output), output)
  }
  const input = 'paths:\n  "/z": {}\n  /a: {}\ncomponents:\n  schemas:\n    "Z": {}\n    \'A\'\'a\': {}\n  responses: {}\n'
  assert.equal(sortOpenAPI(input), 'paths:\n  /a: {}\n  "/z": {}\ncomponents:\n  schemas:\n    \'A\'\'a\': {}\n    "Z": {}\n  responses: {}\n')
})

test('fails closed on duplicate keys, missing sections and unsafe anchor forms or bindings', () => {
  assert.throws(() => sortOpenAPI(fixture.replace("  '/a':", '  /z:')), /Duplicate key in paths/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha:', '    Zebra:')), /Duplicate key in components.schemas/)
  assert.throws(() => sortOpenAPI('paths: {}\ncomponents: {}\n'), /Expected block mapping paths/)
  assert.throws(() => sortOpenAPI(fixture.replace('components:', 'other:')), /Expected block mapping components/)
  assert.throws(() => sortOpenAPI(fixture.replace('  schemas:', '  schemas: {Z: {}, A: {}}')), /Expected block mapping components.schemas/)
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '&auth')), /Alias \*auth would precede its definition after sorting/)
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '&')), /Unsupported anchor form/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha: {type: string}', '    Alpha:\n      - &key name: value')), /Unsupported anchor form/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha: {type: string}', '    Alpha:\n      ? &key name\n      : value')), /Unsupported explicit key/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha: {type: string}', '    &key Alpha: {type: string}')), /Unsupported anchor form/)
  assert.throws(() => sortOpenAPI(fixture.replace('*auth # alias', '&auth [{other: []}]')), /different definitions/)
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '*missing')), /precedes its definition/)
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '!<tag:yaml.org,2002:seq> &auth [{session: []}, {agentKey: []}]')), /Unsupported verbatim tag/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha: {type: string}', '    Alpha:\n      - !<tag:yaml.org,2002:str> &key value')), /Unsupported verbatim tag/)
  assert.throws(() => sortOpenAPI(fixture.replace('    Alpha: {type: string}', '    Alpha: {type: !<tag:yaml.org,2002:str> &key string}')), /Unsupported verbatim tag/)
  // A ? glued to text or inside a plain scalar is content, never an explicit key: no definition is recorded.
  for (const scalar of ['{?&auth session : []}', '{a: what ? &auth}', '[?&auth]']) {
    assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', scalar)), /Alias \*auth precedes its definition$/, scalar)
  }
})

test('check fails on unsorted additions without writing; write fixes it and is idempotent', t => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-openapi-sort-')), path = join(directory, 'openapi.yaml')
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  writeFileSync(path, fixture)
  const logs = []
  assert.equal(main(['--check'], path, line => logs.push(line)), 1)
  assert.match(logs[0], /node scripts\/openapi-sort.mjs --write/)
  assert.equal(readFileSync(path, 'utf8'), fixture)
  assert.equal(main(['--write'], path, () => {}), 0)
  const sorted = readFileSync(path, 'utf8')
  assert.equal(main(['--check'], path, () => {}), 0)
  assert.equal(main(['--write'], path, () => {}), 0)
  assert.equal(readFileSync(path, 'utf8'), sorted)
  assert.throws(() => main([], path), /Usage:/)
  assert.throws(() => main(['--write', '--check'], path), /Usage:/)
})

test('canonical contract passes the executable lint and stays byte-identical after another sort', () => {
  const contract = generateOpenAPI()
  assert.equal(sortOpenAPI(contract), contract)
  const result = spawnSync(process.execPath, [new URL('./openapi-sort.mjs', import.meta.url).pathname, '--check'], { encoding: 'utf8' })
  assert.equal(result.status, 0, result.stderr + result.stdout)
})

test('keeps scalar hash lines and trailing blank lines with their owning entries', () => {
  for (const header of ['|+', '>+', '|2+', '>+2']) {
    // The last entry moves first: endOfMap must not trim its scalar's tail.
    const input = `paths:
  /z: {}
  /a:
    get:
      description: ${header}

        Text
        # literal hash


components:
  schemas:
    Z: {}
    A:
      description: ${header}

        Text
        # literal hash


`
    const sorted = sortOpenAPI(input)
    const scalar = `description: ${header}\n\n        Text\n        # literal hash\n\n\n`
    assert.equal(sorted.split(scalar).length, 3)
    assert.ok(sorted.indexOf('  /a:') < sorted.indexOf('  /z:'))
    assert.ok(sorted.indexOf('    A:') < sorted.indexOf('    Z:'))
    assert.deepEqual(sorted.split('\n').sort(), input.split('\n').sort())
    assert.equal(sortOpenAPI(sorted), sorted)
  }
})

test('preserves literal anchor examples while promoting real YAML anchors', () => {
  const input = `paths:
  /z:
    get:
      security: &auth [one]
      description: |-
        security: &demo [example]
  /a:
    get:
      security: *auth
      description: |-
        security: *demo
components:
  schemas: {}
`
  const sorted = sortOpenAPI(input)
  assert.match(sorted, /\/a:\n    get:\n      security: &auth \[one\]\n      description: \|-\n        security: \*demo/)
  assert.match(sorted, /\/z:\n    get:\n      security: \*auth\n      description: \|-\n        security: &demo \[example\]/)
  assert.equal(sortOpenAPI(sorted), sorted)
})

test('recognizes scalar boundaries in sequence items and tagged nodes', () => {
  for (const value of [
    'examples:\n        - |2+\n          # literal hash\n\n',
    'examples:\n        - description: |2+\n            # literal hash\n\n',
    'description: !!str |+\n        # literal hash\n\n',
    'examples:\n        - "quoted example\n          security: &demo [one]\n          other: *demo"',
  ]) {
    const input = `paths:\n  /z:\n    get:\n      ${value}\n  /a: {}\ncomponents:\n  schemas: {}\n`
    const sorted = sortOpenAPI(input)
    assert.ok(sorted.includes(`      ${value}\ncomponents:`))
    assert.ok(sorted.indexOf('  /a:') < sorted.indexOf('  /z:'))
    assert.deepEqual(sorted.split('\n').sort(), input.split('\n').sort())
    assert.equal(sortOpenAPI(sorted), sorted)
  }
})

test('preserves multiline quoted anchor examples nested in flow collections', () => {
  for (const quote of ["'", '"']) for (const wrap of [value => `{description: ${value}}`, value => `[{nested: [${value}]}]`]) {
    const example = anchor => wrap(`${quote}literal example\n        security: ${anchor}\n        end${quote}`)
    const input = `paths:\n  /z:\n    get:\n      security: &auth [one]\n      description: ${example('&demo [example]')}\n  /a:\n    get:\n      security: *auth\n      description: ${example('*demo')}\ncomponents:\n  schemas: {}\n`
    const sorted = sortOpenAPI(input)
    assert.ok(sorted.includes(`  /a:\n    get:\n      security: &auth [one]\n      description: ${example('*demo')}`))
    assert.ok(sorted.includes(`  /z:\n    get:\n      security: *auth\n      description: ${example('&demo [example]')}`))
    assert.equal(sortOpenAPI(sorted), sorted)
  }
})

test('fails closed before writing when a block scalar moves into or out of unterminated EOF', t => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-openapi-sort-')), path = join(directory, 'openapi.yaml')
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  for (const newline of ['\n', '\r\n']) for (const header of ['|', '>', '|+', '>+', '|2', '>2']) {
    for (const tail of [
      `    Z: {}\n    A:\n      description: ${header}\n        Text`,
      `    Z:\n      description: ${header}\n        Text\n    A: {}`,
    ]) {
      const input = `paths:\n  /a: {}\ncomponents:\n  schemas:\n${tail}`.replaceAll('\n', newline)
      writeFileSync(path, input)
      assert.throws(() => sortOpenAPI(input), /Cannot move a block scalar into or out of unterminated EOF/)
      assert.throws(() => main(['--write'], path, () => {}), /Cannot move a block scalar into or out of unterminated EOF/)
      assert.equal(readFileSync(path, 'utf8'), input)
      const terminated = input + newline
      assert.equal(sortOpenAPI(sortOpenAPI(terminated)), sortOpenAPI(terminated))
    }
  }
})

test('recognizes flow collection sequence items and keeps sorting after them', () => {
  const items = [
    "- {$ref: '#/components/schemas/Zebra'}",
    '- {$ref: "#/components/schemas/Alpha"}',
    '- {type: object, required: [expected_revision], properties: {expected_revision: {type: integer, minimum: 1}}}',
    "- [one, 'two: ]', \"three: }\"] # trailing comment",
    "- &item {x: 'y'}",
    "- !!map {x: 'y'}",
    '- {name: split,\n            in: query}',
    '- - {nested: [1]}',
    '- - [{deep: "}"}]',
  ]
  for (const item of items) {
    const input = `paths:
  /z:
    get:
      security: &auth [{session: []}]
      responses:
        '200':
          content:
            application/json:
              schema:
                oneOf:
                  ${item}
                  - {type: 'null'}
  /m:
    get:
      security: *auth
  /a:
    get:
      security: *auth
      description: "After the flow item: still a sortable entry"
components:
  schemas:
    Zebra: {type: string}
    Alpha: {type: string}
`
    const sorted = sortOpenAPI(input)
    assert.ok(sorted.includes(`                  ${item}\n                  - {type: 'null'}\n`), item)
    assert.ok(sorted.indexOf('  /a:') < sorted.indexOf('  /m:') && sorted.indexOf('  /m:') < sorted.indexOf('  /z:'), item)
    assert.ok(sorted.indexOf('    Alpha:') < sorted.indexOf('    Zebra:'), item)
    assert.match(sorted, /\/a:\n    get:\n      security: &auth \[\{session: \[\]\}\]\n/, item)
    assert.match(sorted, /\/z:\n    get:\n      security: \*auth\n/, item)
    assert.deepEqual(sorted.split('\n').sort(), input.split('\n').sort(), item)
    assert.equal(sortOpenAPI(sorted), sorted, item)
  }
})

test('fails closed on unbalanced flow delimiters and content after a flow node', () => {
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', 'Alpha: {type: string}}')), /Unbalanced flow delimiter/)
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', 'Alpha: {type: [string]]}')), /Unbalanced flow delimiter/)
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', "Alpha: {type: 'a'}, b")), /Unsupported content after a flow or quoted node/)
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', 'Alpha:\n      - [flow, key]: value')), /Unsupported content after a flow or quoted node/)
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', 'Alpha:\n      - [one,\n        two] three')), /Unsupported content after a flow or quoted node/)
  assert.throws(() => sortOpenAPI(fixture.replace('Alpha: {type: string}', 'Alpha:\n      - "one\n        two" three')), /Unsupported content after a flow or quoted node/)
})

test('checks maps past column-zero scalar continuations and ignores interior section headers', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-openapi-sort-')), path = join(directory, 'openapi.yaml')
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  for (const scalar of ["'first\npaths:\ncomponents:\nlast'", '"first\npaths:\ncomponents:\nlast"', '[first,\nlast]', '{first:\nlast}']) {
    for (const section of ['paths', 'schemas', 'headers']) {
      await t.test(`${section}: ${scalar}`, () => {
        const prefix = `openapi: 3.1.0\nx-example: ${scalar}\n`
        const input = prefix + (section === 'paths'
          ? `paths:\n  /z:\n    get:\n      x-example: ${scalar}\n  /a: {}\ncomponents:\n  schemas: {}\n`
          : `paths:\n  /a: {}\ncomponents:\n  ${section}:\n    Z:\n      x-example: ${scalar}\n    A: {}\n  responses: {}\n`)
        const expected = prefix + (section === 'paths'
          ? `paths:\n  /a: {}\n  /z:\n    get:\n      x-example: ${scalar}\ncomponents:\n  schemas: {}\n`
          : `paths:\n  /a: {}\ncomponents:\n  ${section}:\n    A: {}\n    Z:\n      x-example: ${scalar}\n  responses: {}\n`)
        writeFileSync(path, input)
        assert.equal(main(['--check'], path, () => {}), 1)
        assert.equal(readFileSync(path, 'utf8'), input)
        assert.equal(sortOpenAPI(input), expected)
        assert.equal(main(['--write'], path, () => {}), 0)
        assert.equal(readFileSync(path, 'utf8'), expected)
        assert.equal(main(['--check'], path, () => {}), 0)
        assert.equal(sortOpenAPI(expected), expected)
      })
    }
  }
  for (const [opening, closing] of [["'unfinished", "last'"], ['"unfinished', 'last"'], ['[first,', 'last]'], ['{first:', 'last}']]) {
    for (const tail of ['', '\n', `\n  /a: {}\ncomponents:\n  schemas: {}\nx-after: ${closing}\n`]) {
      await t.test(`unfinished ${opening}: ${JSON.stringify(tail)}`, () => {
        const input = `paths:\n  /z:\n    get:\n      x-example: ${opening}${tail}`
        writeFileSync(path, input)
        const kind = ['[', '{'].includes(opening[0]) ? 'flow collection' : 'quoted scalar'
        const failure = { message: `Unterminated ${kind} ${tail.includes('/a:') ? 'before mapping key: /a: {}' : 'at EOF'}` }
        assert.throws(() => sortOpenAPI(input), failure)
        for (const mode of ['--check', '--write']) {
          assert.throws(() => main([mode], path, () => {}), failure)
          assert.equal(readFileSync(path, 'utf8'), input)
        }
      })
    }
  }
})

// Reverse the entries of one block mapping, moving each entry's leading
// comment and blank lines with it. Resorting must restore every byte, which
// proves the sorter reaches every path and component in the real contract.
function reverseSection(text, header, depth) {
  const lines = text.split('\n')
  const start = lines.indexOf(header), pad = ' '.repeat(depth)
  let end = start + 1
  while (end < lines.length && (lines[end].startsWith(pad) || !lines[end].trim())) end++
  while (!lines[end - 1].trim()) end--
  const blocks = [], begins = []
  for (let i = start + 1; i < end; i++) if (lines[i].startsWith(pad) && /\S/.test(lines[i][depth]) && lines[i][depth] !== '#') {
    let begin = i
    while (begin > start + 1 && /^\s*(#|$)/.test(lines[begin - 1])) begin--
    begins.push(begin)
  }
  begins.forEach((begin, i) => blocks.push(lines.slice(begin, begins[i + 1] ?? end)))
  assert.ok(blocks.length > 1, `${header} has ${blocks.length} entries`)
  lines.splice(start + 1, end - start - 1, ...lines.slice(start + 1, begins[0]), ...blocks.reverse().flat())
  return lines.join('\n')
}

test('canonical contract: every path and component entry is reachable after reversal', () => {
  const contract = generateOpenAPI()
  assert.ok(contract.split('\n').some(line => /^\s*- \{\$ref: ['"]/.test(line)), 'contract has flow sequence items')
  let reversed = reverseSection(contract, 'paths:', 2)
  for (const section of ['schemas', 'parameters', 'requestBodies', 'responses']) reversed = reverseSection(reversed, `  ${section}:`, 4)
  assert.notEqual(reversed, contract)
  assert.deepEqual(reversed.split('\n').sort(), contract.split('\n').sort())
  // Reversal moves the &auth definitions behind their aliases. Keep the input
  // valid YAML by defining the anchor at its first use, as the sorter does.
  const [, value] = /security: &auth (.*)/.exec(contract)
  const demote = text => text.replace(/&auth (\[.*\])/g, (_, found) => { assert.equal(found, value); return '*auth' })
  const input = demote(reversed).replace('security: *auth', `security: &auth ${value}`)
  const sorted = sortOpenAPI(input)
  assert.equal(sortOpenAPI(sorted), sorted)
  assert.equal(demote(sorted), demote(contract))
  assert.equal(sorted.indexOf('&auth'), contract.indexOf('&auth'))
})

// Anchors in sequence items, nested flow collections, tagged nodes and block
// nodes cannot be promoted like whole-line flow anchors. Sorting must track
// them and fail closed instead of emitting an alias before its definition.
const anchorForms = [
  ['oneOf:\n        - &shared {type: string}', 'oneOf:\n        - *shared'],
  ['oneOf:\n        - - &shared {type: string}', 'oneOf:\n        - - *shared'],
  ['oneOf:\n        - !!map &shared {type: string}', 'oneOf:\n        - *shared'],
  ['security: [&shared {session: []}]', 'security: [*shared]'],
  ['security: [{session: &shared []}]', 'security: [{session: *shared}]'],
  ['security: [!!map &shared {session: []}]', 'security: [*shared]'],
  ['security: [{session: []},\n        &shared {agentKey: []}]', 'security: [{session: []},\n        *shared]'],
  ['x-shared: &shared\n        type: string', 'x-shared: *shared'],
  ['x-shared: &shared text', 'x-shared: *shared'],
  ['x-shared:\n        - &shared |\n          text\n          security: *other', 'x-shared:\n        - *shared'],
  // Explicit flow keys: the ? indicator keeps the node start open for properties.
  ['security: {? &shared session : []}', 'security: {? *shared : []}'],
  ['security: [? &shared session : []]', 'security: [? *shared : []]'],
  ['security: {? !!str &shared session : []}', 'security: {? *shared : []}'],
  ['security: {\n        ? &shared session : []}', 'security: {\n        ? *shared : []}'],
  ['security: {? session : &shared []}', 'security: {? session : *shared}'],
]

test('fails closed when sorting would move a tracked alias before its sequence, flow or block anchor', () => {
  for (const [definition, alias] of anchorForms) {
    const unsafe = `paths:\n  /z:\n    get:\n      ${definition}\n  /a:\n    get:\n      ${alias}\ncomponents:\n  schemas: {}\n`
    assert.throws(() => sortOpenAPI(unsafe), /Alias \*shared would precede its definition after sorting/, definition)
    // An alias ahead of its definition is invalid input and is rejected before any move.
    const invalid = `paths:\n  /a:\n    get:\n      ${alias}\n  /z:\n    get:\n      ${definition}\ncomponents:\n  schemas: {}\n`
    assert.throws(() => sortOpenAPI(invalid), /Alias \*shared precedes its definition$/, definition)
  }
})

test('sorts around tracked anchors whose bindings stay valid', () => {
  for (const [definition, alias] of anchorForms) {
    // /a moves before /m and /z, yet the definition in /m still precedes the alias in /z.
    const other = text => text.replaceAll('shared', 'other')
    const input = `paths:\n  /m:\n    get:\n      ${definition}\n  /a: {}\n  /z:\n    get:\n      ${alias}\ncomponents:\n  schemas:\n    Zebra:\n      ${other(definition)}\n    Alpha: {type: string}\n`
    const sorted = sortOpenAPI(input)
    assert.ok(sorted.indexOf('  /a:') < sorted.indexOf('  /m:') && sorted.indexOf('  /m:') < sorted.indexOf('  /z:'), definition)
    assert.ok(sorted.indexOf('    Alpha:') < sorted.indexOf('    Zebra:'), definition)
    assert.ok(sorted.includes(`  /m:\n    get:\n      ${definition}\n`), definition)
    assert.ok(sorted.includes(`  /z:\n    get:\n      ${alias}\n`), definition)
    assert.deepEqual(sorted.split('\n').sort(), input.split('\n').sort(), definition)
    assert.equal(sortOpenAPI(sorted), sorted, definition)
    // Repeating a tracked definition cannot be compared, so it fails closed.
    assert.throws(() => sortOpenAPI(input.replace('  /a: {}', `  /a:\n    get:\n      ${definition}`)), /Cannot reorder repeated definitions of &shared/, definition)
  }
})

test('verifies bindings nested inside a promoted whole-line anchor value', () => {
  const input = `paths:
  /z:
    get:
      x-scopes: &scopes [read]
      security: &auth [{session: *scopes}]
  /a:
    get:
      security: *auth
components:
  schemas: {}
`
  // Promoting &auth into /a would carry *scopes above its definition in /z.
  assert.throws(() => sortOpenAPI(input), /Alias \*scopes precedes its definition after sorting/)
  // With the nested definition in a path that sorts first, promotion is safe.
  const safe = input.replace('      x-scopes: &scopes [read]\n', '').replace('paths:\n', 'paths:\n  /-scopes:\n    get:\n      x-scopes: &scopes [read]\n')
  const sorted = sortOpenAPI(safe)
  assert.match(sorted, /\/-scopes:\n    get:\n      x-scopes: &scopes \[read\]\n  \/a:\n    get:\n      security: &auth \[\{session: \*scopes\}\]\n/)
  assert.match(sorted, /\/z:\n    get:\n      security: \*auth\n/)
  assert.deepEqual(sorted.split('\n').sort(), safe.split('\n').sort())
  assert.equal(sortOpenAPI(sorted), sorted)
})
