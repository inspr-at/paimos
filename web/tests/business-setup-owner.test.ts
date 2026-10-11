// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'

// AEON-1189: Settings › Business is the only writer for the business parts. The
// switches live in SetupCard, which only the settings section mounts; the overview
// and the closed Business pages read the parts and link to Settings › Business.
const src = new URL('../src/', import.meta.url)
const read = (path: string) => readFileSync(new URL(path, src), 'utf8')
const sources = (readdirSync(src, { recursive: true }) as string[]).filter(path => /\.(vue|ts)$/.test(path)).map(path => path.split('\\').join('/'))

test('only Settings › Business mounts the business parts switches', () => {
  const importers = sources.filter(path => /['"][^'"]*\/SetupCard\.vue['"]/.test(read(path)))
  assert.deepEqual(importers, ['components/settings/BusinessSection.vue'])
})

test('the overview and its parts status only read the parts', () => {
  for (const path of ['views/business/BusinessHome.vue', 'components/business/PartsStatus.vue']) {
    const text = read(path)
    for (const writer of [/business\.(?:enable|disable)\(/, /configurePlugin\(/, /installationWrite\(/, /type="checkbox"/, /SetupCard/]) {
      assert.doesNotMatch(text, writer, `${path} writes the parts: ${writer}`)
    }
  }
  assert.doesNotMatch(read('components/business/PartsStatus.vue'), /@click|<button/, 'the parts status has no controls')
})

test('every setup link on the Business pages ends in Settings › Business', () => {
  assert.match(read('components/business/areas.ts'), /export const SETUP_PLACE = 'Settings › Business'/)
  for (const path of ['views/business/BusinessHome.vue', 'components/business/PartsStatus.vue', 'components/business/BusinessPage.vue']) {
    const links = read(path).match(/<RouterLink[^>]*>Set up in \{\{ SETUP_PLACE \}\}/g) ?? []
    assert.ok(links.length > 0, `${path} links to Settings › Business`)
    for (const link of links) assert.match(link, /:to="settingsLink\('business', 'parts'\)"/, `${path}: ${link}`)
  }
  assert.doesNotMatch(read('components/business/BusinessPage.vue'), /Open Business setup|on the Business overview/)
})
