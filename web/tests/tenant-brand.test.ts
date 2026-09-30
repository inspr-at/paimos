// SPDX-License-Identifier: AGPL-3.0-only
// AEON-431: the workspace brand helpers. Only the server's own logo route is
// ever drawn; dark mode without a dark logo uses the light one on a plate.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { codenameOf, fitLogo, headerBrand, logoProblem, logoType, publicBrand, safeLogo, shortName } from '../src/lib/tenantBrand.ts'

const LIGHT = { url: '/api/brand/logo/light?v=0123456789abcdef', width: 240, height: 60 }
const DARK = { url: '/api/brand/logo/dark?v=fedcba9876543210', width: 240, height: 60 }

test('only same-origin logo URLs with sane sizes are accepted', () => {
  assert.deepEqual(safeLogo(LIGHT), LIGHT)
  for (const url of ['https://evil.example/logo.svg', '//evil.example/x', 'javascript:alert(1)', 'data:image/svg+xml,<svg onload=alert(1)>', '/api/brand/logo/light?v=x"onerror=', '/api/attachments/1/content', '/api/brand/logo/sepia']) {
    assert.equal(safeLogo({ ...LIGHT, url }), null, url)
  }
  assert.equal(safeLogo({ ...LIGHT, width: 0 }), null)
  assert.equal(safeLogo({ ...LIGHT, height: 1.5 }), null)
  assert.equal(safeLogo(null), null)
})

test('the header draws the dark logo in dark mode, else the light one on a plate', () => {
  assert.equal(headerBrand(undefined, false), null)
  assert.equal(headerBrand({}, true), null)
  assert.deepEqual(headerBrand({ short_name: '  Northwind  ', logo: LIGHT }, false), { name: 'Northwind', logo: LIGHT, plate: false })
  assert.deepEqual(headerBrand({ logo: LIGHT }, true), { name: '', logo: LIGHT, plate: true })
  assert.deepEqual(headerBrand({ logo: LIGHT, logo_dark: DARK }, true), { name: '', logo: DARK, plate: false })
  assert.deepEqual(headerBrand({ logo_dark: DARK }, false), { name: '', logo: DARK, plate: false })
  assert.deepEqual(headerBrand({ short_name: 'Northwind' }, true), { name: 'Northwind', logo: null, plate: false })
  assert.deepEqual(headerBrand({ short_name: 'x', logo: { ...LIGHT, url: 'https://evil.example/a.svg' } }, false), { name: 'x', logo: null, plate: false })
})

test('logos are fitted into the header slot at their own proportions', () => {
  assert.deepEqual(fitLogo({ ...LIGHT, width: 64, height: 64 }), { width: 28, height: 28 })
  assert.deepEqual(fitLogo(LIGHT), { width: 112, height: 28 })
  assert.deepEqual(fitLogo({ ...LIGHT, width: 800, height: 100 }), { width: 132, height: 17 })
  assert.deepEqual(fitLogo({ ...LIGHT, width: 32, height: 64 }), { width: 14, height: 28 })
})

test('short names are trimmed and capped; the session brand drops what is unset', () => {
  assert.equal(shortName('  North   wind '), 'North wind')
  assert.equal(shortName('x'.repeat(40)).length, 32)
  assert.equal(shortName(7), '')
  assert.equal(publicBrand({ short_name: '', logo: null, logo_dark: null }), undefined)
  assert.deepEqual(publicBrand({ short_name: 'N', logo: { ...LIGHT, content_type: 'image/svg+xml', size: 10, sha256: 'a', uploaded_at: '' }, logo_dark: null }), { short_name: 'N', logo: LIGHT })
})

test('picked files are typed by their extension when the browser leaves them untyped', () => {
  assert.equal(logoType({ name: 'logo.SVG', type: '' }), 'image/svg+xml')
  assert.equal(logoType({ name: 'logo.png', type: 'application/octet-stream' }), 'image/png')
  assert.equal(logoType({ name: 'logo.webp', type: 'image/webp' }), 'image/webp')
  assert.equal(logoType({ name: 'logo.svg', type: 'text/html' }), '')
  assert.equal(logoType({ name: 'logo.gif', type: 'image/gif' }), '')
  assert.equal(logoProblem({ name: 'a.png', type: 'image/png', size: 300 * 1024 }), 'The file is 300 KB; the limit is 256 KB.')
  assert.equal(logoProblem({ name: 'a.png', type: 'image/png', size: 1000 }), '')
})

test('the codename is optional and plain', () => {
  assert.equal(codenameOf({ version: '1', codename: ' Amber   Aurora ' }), 'Amber Aurora')
  assert.equal(codenameOf({ version: '1' }), '')
  assert.equal(codenameOf({ codename: 42 }), '')
  assert.equal(codenameOf(null), '')
})
