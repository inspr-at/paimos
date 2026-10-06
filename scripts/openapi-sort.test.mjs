// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { main, sortOpenAPI } from './openapi-sort.mjs'

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
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '&auth')), /Unsupported anchor form/)
  assert.throws(() => sortOpenAPI(fixture.replace('*auth # alias', '&auth [{other: []}]')), /different definitions/)
  assert.throws(() => sortOpenAPI(fixture.replace('&auth [{session: []}, {agentKey: []}]', '*missing')), /precedes its definition/)
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
  const contract = readFileSync(new URL('../api/openapi.yaml', import.meta.url), 'utf8')
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
