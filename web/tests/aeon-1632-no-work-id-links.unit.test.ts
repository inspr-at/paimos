// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1632: /work/<id> is not a route; such a link lands on the 404 page. Ticket links use the
// peek (TicketPeekLink) with the canonical /p/<project>/<KEY> instead.
import { readFileSync, readdirSync } from 'node:fs'
import { expect, it } from 'vitest'

it('AEON-1632: no web source builds a /work/<id> link', () => {
  const hits: string[] = []
  let scanned = 0
  function scan(dir: URL) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) { scan(new URL(`${entry.name}/`, dir)); continue }
      if (!/\.(ts|vue)$/.test(entry.name)) continue
      scanned++
      readFileSync(new URL(entry.name, dir), 'utf8').split('\n').forEach((line, at) => {
        if (line.includes('/work/${')) hits.push(`${new URL(entry.name, dir).pathname.replace(/^.*\/web\/src\//, 'web/src/')}:${at + 1}`)
      })
    }
  }
  scan(new URL('../src/', import.meta.url))
  // The scan reached the sources it guards.
  expect(scanned).toBeGreaterThan(100)
  const memo = readFileSync(new URL('../src/components/agents/DecisionDeskMemo.vue', import.meta.url), 'utf8')
  expect(memo).toContain('<TicketPeekLink')
  expect(hits).toEqual([])
  // The Open tip names the new-tab modifier in words. A command glyph there
  // fails text-glyphs (CI on d1c71583, DecisionDeskMemo.vue:930).
  const open = memo.split('\n').find(line => line.includes('data-testid="desk-ticket-open"'))
  expect(open).toContain("${mac ? 'Command' : 'Ctrl'}-click for a new tab")
  expect(open).not.toMatch(/\u2318/u)
})
