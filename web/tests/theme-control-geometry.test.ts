// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const tokens = readFileSync(new URL('../src/styles/tokens.css', import.meta.url), 'utf8')
const base = readFileSync(new URL('../src/styles/base.css', import.meta.url), 'utf8')
const lightbox = readFileSync(new URL('../src/components/work/AttachmentLightbox.vue', import.meta.url), 'utf8')
const agents = readFileSync(new URL('../src/components/settings/ThemeAgentsCard.vue', import.meta.url), 'utf8')

function canvasBlocks(css: string) {
  return css.split('}').filter(block => /(?:^|[^\w-])--canvas\s*:/.test(block))
}

test('every canvas token has a darker canvas-lo beside it and the stage gradient falls back to canvas', () => {
  const blocks = canvasBlocks(tokens)
  assert.equal(blocks.length, 3, 'light, dark attribute, and prefers-color-scheme each define --canvas')
  for (const [index, block] of blocks.entries()) {
    const head = block.slice(block.indexOf('--canvas'), block.indexOf('--canvas') + 80).replaceAll('\n', ' ')
    assert.ok(/--canvas-lo\s*:/.test(block), `canvas block ${index + 1} defines --canvas without --canvas-lo (${head})`)
  }
  const rule = lightbox.match(/\.lightbox\s*\{([^}]*)\}/)
  assert.ok(rule, 'lightbox rule')
  assert.match(rule[1], /var\(--canvas-lo,\s*var\(--canvas\)\)/)
  assert.doesNotMatch(rule[1], /var\(--canvas-lo\)(?!,)/)
})

test('the shared switch sizes its knob from the track and the agents label keeps the 44px hit target', () => {
  const shared = base.slice(base.indexOf('/* Switch */'), base.indexOf('/* Segmented control */'))
  assert.match(shared, /\.switch input \{[^}]*container-type:\s*inline-size/)
  assert.match(shared, /\.switch input::after \{[^}]*height:\s*calc\(100% - 4px\);[^}]*aspect-ratio:\s*1/)
  assert.match(shared, /\.switch input:checked::after \{[^}]*transform:\s*translateX\(calc\(100cqw - 100% - 4px\)\)/)
  assert.match(shared, /\.switch input:indeterminate::after \{[^}]*transform:\s*translateX\(calc\(\(100cqw - 100% - 4px\) \/ 2\)\)/)
  const style = agents.match(/<style scoped>([\s\S]*?)<\/style>/)
  assert.ok(style, 'agents card style')
  assert.doesNotMatch(style[1], /\.switch input\s*\{[^}]*(?:width|height)\s*:/)
  assert.match(style[1], /\.switch\s*\{[^}]*min-width:\s*44px;[^}]*min-height:\s*44px/)
})
