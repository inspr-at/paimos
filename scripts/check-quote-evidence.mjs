// SPDX-License-Identifier: AGPL-3.0-only
// AEON-98: keep the parity inventory and the four new runtime notices tied to
// exact dependency pins. This runs during web prebuild and in the image build.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const read = path => readFileSync(join(root, path), 'utf8')
const fail = message => { throw new Error(`quote evidence: ${message}`) }

export function checkQuoteEvidence() {
  const matrix = JSON.parse(read('web/tests/quotes/parity-matrix.json'))
  if (matrix.schema !== 'aeon.quote-parity.v1' || matrix.ticket !== 'AEON-98') fail('wrong parity matrix schema or ticket')
  const rows = [...matrix.features, ...matrix.backlog_criteria]
  if (!Array.isArray(matrix.release_checks) || matrix.release_checks.length < 6) fail('release checks missing')
  if (matrix.features.length < 68 || matrix.backlog_criteria.length !== 53) fail('a feature or backlog criterion is missing')
  const ids = new Set()
  for (const row of rows) {
    if (!row.id || ids.has(row.id) || !row.claim || !['pass', 'gap', 'deferred'].includes(row.status) || !row.owner || !Array.isArray(row.evidence) || !row.evidence.length || !row.reason) fail(`invalid parity row ${row.id}`)
    ids.add(row.id)
  }
  for (const row of matrix.release_checks) {
    if (!row.id || ids.has(row.id) || !['pass', 'gap', 'deferred'].includes(row.status) || !row.owner || !Array.isArray(row.evidence) || !row.evidence.length || !row.reason) fail(`invalid release check ${row.id}`)
    ids.add(row.id)
  }
  for (const ticket of ['PAI-1067', 'PAI-1071', 'PAI-1064', 'PAI-1065', 'PAI-1066', 'PAI-1068', 'PAI-1070', 'PAI-991']) {
    if (![...ids].some(id => id.startsWith(`${ticket}.AC`))) fail(`missing ${ticket} criteria`)
  }
  const counts = Object.fromEntries(['pass', 'gap', 'deferred'].map(status => [status, rows.filter(row => row.status === status).length]))
  if (JSON.stringify(matrix.counts) !== JSON.stringify(counts)) fail('parity counts are stale')

  const fixtures = join(root, 'web/tests/quotes/fixtures')
  for (const name of readdirSync(fixtures)) {
    if (!name.endsWith('.json')) fail(`unexpected quote fixture ${name}`)
    const bytes = readFileSync(join(fixtures, name))
    const value = JSON.parse(bytes.toString('utf8'))
    const scan = { ...value }
    delete scan.forbidden_text // the PDF golden names strings that must be absent
    if (bytes.length > 200_000 || /augmentoring|paimos[.]agm[.]ng|pm[.]barta[.]cm/i.test(JSON.stringify(scan))) fail(`fixture ${name} is not public-safe`)
  }
  if (readdirSync(join(root, 'web/tests/quotes')).some(name => name.endsWith('.md'))) fail('quote evidence must not add Markdown files')

  const docker = read('Dockerfile'), notice = read('NOTICE'), gomod = read('go.mod')
  const lock = JSON.parse(read('web/package-lock.json'))
  const qr = lock.packages?.['node_modules/qrcode-generator']
  if (qr?.version !== '1.5.2' || qr.license !== 'MIT') fail('qrcode-generator pin/license changed')
  if (!/chromium=152\.0\.7977\.82-r0/.test(docker) || !/tini=0\.19\.0-r3/.test(docker)) fail('runtime APK pins changed')
  if (!/golang\.org\/x\/image v0\.45\.0/.test(gomod)) fail('x/image pin changed')
  if (!docker.includes('COPY NOTICE /usr/share/doc/aeon/NOTICE')) fail('runtime image omits NOTICE')
  for (const entry of [
    'Chromium 152.0.7977.82-r0', 'License: BSD-3-Clause', 'Copyright 2015 The Chromium Authors',
    'qrcode-generator 1.5.2', 'License: MIT', 'Copyright (c) 2009 Kazuhiko Arase',
    'tini 0.19.0-r3', 'Copyright (c) 2015 Thomas Orozco',
    'golang.org/x/image v0.45.0', 'Copyright 2009 The Go Authors.',
    'Permission is hereby granted', 'Redistribution and use in source and binary forms',
  ]) if (!notice.includes(entry)) fail(`NOTICE missing ${entry}`)
  return { features: matrix.features.length, criteria: matrix.backlog_criteria.length, counts }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try { console.log(JSON.stringify(checkQuoteEvidence())) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
