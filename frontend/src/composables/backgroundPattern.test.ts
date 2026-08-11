// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { describe, expect, it } from 'vitest'
import {
  BACKGROUND_PATTERN_OPTIONS,
  backgroundPatternImage,
} from './backgroundPattern'

describe('backgroundPatternImage (PAI-738)', () => {
  it('offers exactly the supported settings choices', () => {
    expect(BACKGROUND_PATTERN_OPTIONS.map((option) => option.value)).toEqual([
      'triangle',
      'square',
      'hex',
      'lines',
      'none',
    ])
  })

  it.each(['triangle', 'square', 'hex', 'lines'] as const)(
    'renders %s through the shared SVG renderer',
    (pattern) => {
      const image = backgroundPatternImage(pattern, '#123456')
      expect(image).toContain('data:image/svg+xml')
      expect(decodeURIComponent(image)).toContain(`data-pattern='${pattern}'`)
      expect(decodeURIComponent(image)).toContain('#123456')
    },
  )

  it('turns the background image off for none', () => {
    expect(backgroundPatternImage('none', '#123456')).toBe('none')
  })

  it('does not inject an invalid pattern color into SVG', () => {
    const image = decodeURIComponent(backgroundPatternImage('triangle', "red' onload='alert(1)"))
    expect(image).not.toContain('onload')
    expect(image).toContain('#27272a')
  })
})
