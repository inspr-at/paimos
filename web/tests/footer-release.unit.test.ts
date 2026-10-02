// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { footerReleaseContent } from '../src/lib/footerRelease'
import type { Release } from '../src/lib/releases'

const value = '261002004317.0.0'
const now = Date.parse('2026-10-02T09:43:17Z')
const content = (count: number | null = 0, release?: Release) => footerReleaseContent(value, 'inspr-calendar-v2', 'Rugged Ratio', count, release, now)

it('names the running release, full coordinate, UTC release time and history action', () => {
  expect(content()).toEqual({ heading: 'Running release', name: 'Rugged Ratio', version: value, released: 'Released 2 Oct 2026, 00:43 UTC · 9h ago', action: 'Click for all releases', fresh: '' })
})

it.each([[0, ''], [null, ''], [3, '3 new']])('only shows a known positive new count (%s)', (count, fresh) => {
  expect(content(count as number | null).fresh).toBe(fresh)
})

it('uses the running release publication time, then tag time, then reservation', () => {
  const release = { published_at: '2026-10-02T01:43:17Z', tagged_at: '2026-10-02T01:13:17Z', reserved_at: '2026-10-02T00:43:17Z' } as Release
  expect(content(3, release).released).toBe('Released 2 Oct 2026, 01:43 UTC · 8h ago')
  expect(content(0, { ...release, published_at: null }).released).toContain('01:13 UTC')
  expect(content(0, { ...release, published_at: null, tagged_at: null }).released).toContain('00:43 UTC')
})

it('keeps UTC across day boundaries', () => {
  expect(content(0, { published_at: '2026-10-01T23:43:17Z' } as Release).released).toBe('Released 1 Oct 2026, 23:43 UTC · 10h ago')
})

it('handles nameless, loading, failed and legacy releases without inventing dates', () => {
  expect(footerReleaseContent('', undefined, '', null, undefined, now)).toMatchObject({ name: '', version: 'Loading release…', released: '', fresh: '' })
  expect(footerReleaseContent('', undefined, '', 0, undefined, now, true).version).toBe('Version unavailable')
  expect(footerReleaseContent('5.21.0', 'legacy', '', 0, undefined, now)).toMatchObject({ version: '5.21.0', released: '' })
  expect(footerReleaseContent('260229004317.0.0', 'inspr-calendar-v2', '', 0, undefined, now).released).toBe('')
  expect(footerReleaseContent(value, 'unknown', '', 0, undefined, now).released).toBe('')
})
