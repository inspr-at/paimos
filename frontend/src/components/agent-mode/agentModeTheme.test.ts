/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// PAI-805 — theme contract. Agent Mode colors follow the HOST theme. A
// component-local `prefers-color-scheme` override once produced ~2:1
// pastel accents on the light host; this guards against it returning and
// checks the fixed semantic accents meet WCAG on the host surfaces.

import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// vitest runs with the frontend package as cwd; resolve sources from there
// (happy-dom swaps the URL global, so import.meta.url is not usable here).
const AGENT_MODE_DIR = resolve(process.cwd(), 'src/components/agent-mode')

/** Source with CSS / HTML comments stripped, so prose about the rule does
 * not trip the rule. */
function read(rel: string): string {
  return readFileSync(resolve(AGENT_MODE_DIR, rel), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
}

const agentModeSfcs = readdirSync(AGENT_MODE_DIR).filter((f) => f.endsWith('.vue')).map((f) => `./${f}`)
const SOURCES = [...agentModeSfcs, '../../views/AgentModeView.vue', '../AgentModeLayout.vue']

function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16)
  const c = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
}

function contrast(a: string, b: string): number {
  const la = luminance(a)
  const lb = luminance(b)
  const [hi, lo] = la > lb ? [la, lb] : [lb, la]
  return (hi + 0.05) / (lo + 0.05)
}

function cssVar(source: string, name: string): string {
  const m = source.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})`))
  if (!m) throw new Error(`missing ${name}`)
  return m[1].toLowerCase()
}

describe('Agent Mode theme contract (PAI-805)', () => {
  it('has no component-local OS color-scheme override anywhere in Agent Mode', () => {
    for (const rel of SOURCES) {
      expect(read(rel), `${rel} must not bind to prefers-color-scheme`).not.toMatch(/prefers-color-scheme/)
    }
    // Motion preferences are still honoured (they are not a theme).
    expect(read('./AgentModeActivityGlyph.vue')).toMatch(/prefers-reduced-motion/)
  })

  it('derives base surfaces from the host tokens, and its semantic accents meet WCAG on the host surfaces', () => {
    const view = read('../../views/AgentModeView.vue')
    const app = read('../../App.vue')
    const hostCard = cssVar(app, '--bg-card')
    const hostBg = cssVar(app, '--bg')
    // Base tokens follow the host.
    for (const pair of [
      ['--am-ink', 'var(--text)'],
      ['--am-muted', 'var(--text-muted)'],
      ['--am-line', 'var(--border)'],
      ['--am-surface', 'var(--bg-card)'],
      ['--am-shell', 'var(--bg)'],
      ['--am-focus', 'var(--text)'],
    ]) {
      expect(view).toContain(`${pair[0]}: ${pair[1]};`)
    }
    // Semantic accents are used both as text (≥ 4.5:1) and as non-text
    // selection / status indicators (≥ 3:1) on card (#fff) and shell (#f2f5f8).
    const accents = ['--am-green', '--am-amber', '--am-red', '--am-blue', '--am-purple', '--am-select']
    for (const name of accents) {
      const hex = cssVar(view, name)
      for (const surface of [hostCard, hostBg]) {
        const ratio = contrast(hex, surface)
        expect(ratio, `${name} ${hex} on ${surface} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
      }
    }
    // Hard-coded text colors (the live chip lives in the shell header, on --bg-card).
    const literalColors = [...view.matchAll(/color:\s*(#[0-9a-fA-F]{6})/g)].map((m) => m[1].toLowerCase())
    expect(literalColors.length).toBeGreaterThan(0)
    for (const hex of literalColors) {
      expect(contrast(hex, hostCard), `${hex} on ${hostCard}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('keeps the complete project CountSet bounded and reflows it at both responsive breakpoints', () => {
    const overview = read('./AgentModePortfolioOverview.vue')
    expect(overview).toMatch(/\.am-aggregate-project-countset\s*\{[^}]*display:\s*grid/)
    expect(overview).toMatch(/@media\s*\(max-width:\s*860px\)[\s\S]*?\.am-aggregate-project-countset\s*\{[^}]*grid-template-columns:\s*1fr/)
    expect(overview).toMatch(/@media\s*\(max-width:\s*620px\)[\s\S]*?\.am-aggregate-project-countset\s*\{[^}]*overflow-x:\s*auto/)
  })
})
