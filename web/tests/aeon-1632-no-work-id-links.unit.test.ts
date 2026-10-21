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
  expect(readFileSync(new URL('../src/components/agents/DecisionDeskMemo.vue', import.meta.url), 'utf8')).toContain('<TicketPeekLink')
  expect(hits).toEqual([])
})
