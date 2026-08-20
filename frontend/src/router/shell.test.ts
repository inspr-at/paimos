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

import { describe, expect, it } from 'vitest'

import { resolveLayout } from './shell'

describe('resolveLayout (PAI-805 route → shell contract)', () => {
  it('keeps the standard AppLayout for every route that does not opt in', () => {
    expect(resolveLayout(undefined)).toBe('standard')
    expect(resolveLayout({})).toBe('standard')
    expect(resolveLayout({ scrollMode: 'self' } as never)).toBe('standard')
    expect(resolveLayout({ headerSearchHidden: true } as never)).toBe('standard')
    expect(resolveLayout({ adminOnly: true } as never)).toBe('standard')
  })

  it('routes portal, public and agent shells', () => {
    expect(resolveLayout({ portal: true })).toBe('portal')
    expect(resolveLayout({ public: true })).toBe('public')
    expect(resolveLayout({ shell: 'agent' })).toBe('agent')
  })

  it('never lets the agent shell override portal / public precedence', () => {
    expect(resolveLayout({ portal: true, shell: 'agent' })).toBe('portal')
    expect(resolveLayout({ public: true, shell: 'agent' })).toBe('public')
  })
})
