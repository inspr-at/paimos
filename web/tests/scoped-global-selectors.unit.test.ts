// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1618 risk: in a scoped style, Vue replaces a whole selector with the inside of its
// :global(). `:global(.floating.bottom) .row-menu-keys { display: none }` shipped as
// `.floating.bottom { display: none }` and hid every phone bottom sheet in the app.
import { globSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileStyle, parse } from '@vue/compiler-sfc'
import { expect, it } from 'vitest'

const root = fileURLToPath(new URL('../src/', import.meta.url))

// Shipped before this guard; the compiled rule (`.display-menu:not(.as-sheet) { display: contents }`)
// is what its specs pin, so it changes only in its own ticket.
const allowed = [
  'components/work/HeaderNavMenu.vue: :global(.display-menu:not(.as-sheet)) .nav-views',
  'components/work/HeaderNavMenu.vue: :global(.display-menu:not(.as-sheet)) .rows',
]

// True when :global() wraps the whole selector, so Vue drops nothing.
function wholeGlobal(selector: string) {
  const open = selector.match(/^:{1,2}(?:v-)?global\(/)
  if (!open) return false
  let depth = 0
  for (let at = open[0].length - 1; at < selector.length; at++) {
    if (selector[at] === '(') depth++
    else if (selector[at] === ')' && --depth === 0) return at === selector.length - 1
  }
  return false
}

// Selectors in a scoped style that keep anything outside their :global(), read before
// Vue's scoped plugin rewrites them (custom postcss plugins run first).
function partialGlobals(source: string, filename: string) {
  const found: string[] = []
  const guard = {
    postcssPlugin: 'aeon-partial-global',
    Once(css: { walkRules(visit: (rule: { selectors: string[] }) => void): void }) {
      css.walkRules(rule => {
        for (const selector of rule.selectors.map(s => s.trim())) {
          if (/:{1,2}(?:v-)?global\(/.test(selector) && !wholeGlobal(selector)) found.push(selector)
        }
      })
    },
  }
  const { code, errors } = compileStyle({ source, filename, id: 'data-v-guard', scoped: true, postcssPlugins: [guard] })
  return { found, code, errors }
}

it('Vue keeps only the inside of :global(), so the guard flags anything beside it', () => {
  const partial = partialGlobals(':global(.floating.bottom) .row-menu-keys { display: none; }', 'Partial.vue')
  expect(partial.errors).toEqual([])
  expect(partial.code.replace(/\s+/g, ' ').trim()).toBe('.floating.bottom { display: none; }')
  expect(partial.found).toEqual([':global(.floating.bottom) .row-menu-keys'])
  expect(partialGlobals('.menu :global(.floating) { color: red; }', 'Before.vue').found).toEqual(['.menu :global(.floating)'])

  const whole = partialGlobals(':global(.floating.bottom .row-menu-keys) { display: none; }\n@media (max-width: 600px) { :global(.floating.sheet) { height: 100dvh; } }', 'Whole.vue')
  expect(whole.code.replace(/\s+/g, ' ')).toContain('.floating.bottom .row-menu-keys { display: none; }')
  expect(whole.found).toEqual([])
})

it('no scoped style in web/src keeps a selector part outside its :global()', () => {
  const hits: string[] = []
  const scanned = new Set<string>()
  for (const path of globSync('**/*.vue', { cwd: root })) {
    const { descriptor } = parse(readFileSync(`${root}${path}`, 'utf8'), { filename: path })
    for (const style of descriptor.styles) {
      if (!style.scoped || style.lang) continue
      scanned.add(path)
      for (const selector of partialGlobals(style.content, path).found) hits.push(`${path}: ${selector}`)
    }
  }
  // The scan reached the styles it guards, including the bottom-sheet menu.
  expect(scanned.size).toBeGreaterThan(300)
  expect(scanned).toContain('components/business/RowMenu.vue')
  expect(hits.filter(hit => !allowed.includes(hit))).toEqual([])
})
