// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { BRAND_MARKS, harnessBrand, providerBrand } from '../src/components/agents/brandMarks.ts'

test('known harnesses and providers use distinct vendored silhouettes', () => {
  assert.equal(harnessBrand('codex'), 'openai')
  assert.equal(harnessBrand('claude'), 'anthropic')
  assert.equal(harnessBrand('grok'), 'grok')
  assert.equal(harnessBrand('cursor'), 'cursor')
  assert.equal(harnessBrand('pi'), 'pi')
  assert.equal(harnessBrand('gemini'), 'google')
  assert.equal(harnessBrand('opencode'), 'opencode')
  assert.equal(harnessBrand('other'), null)
  assert.equal(providerBrand('openai'), 'openai')
  assert.equal(providerBrand('xai'), 'xai')
  assert.equal(providerBrand('google'), 'google')
  assert.equal(providerBrand('unknown'), null)
  assert.ok(BRAND_MARKS.openai.paths[0]?.startsWith('M11.248'))
  assert.ok(BRAND_MARKS.anthropic.paths[0]?.includes('18.7721'))
  assert.ok(BRAND_MARKS.google.paths[0]?.startsWith('M12.48'))
  assert.ok(BRAND_MARKS.cursor.paths[0]?.startsWith('M11.503'))
  assert.equal(BRAND_MARKS.grok.paths.length, 2)
  assert.equal(BRAND_MARKS.xai.paths.length, 4)
  assert.equal(BRAND_MARKS.xai.viewBox, '0 0 834 318')
  assert.equal(BRAND_MARKS.pi.paths.length, 3)
  const shapes = Object.values(BRAND_MARKS).map(mark => mark.paths.join('|'))
  assert.equal(new Set(shapes).size, shapes.length)
})
