// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

export const BACKGROUND_PATTERN_VALUES = [
  'triangle',
  'square',
  'hex',
  'lines',
  'none',
] as const

export type BackgroundPattern = (typeof BACKGROUND_PATTERN_VALUES)[number]

export const BACKGROUND_PATTERN_OPTIONS: ReadonlyArray<{
  value: BackgroundPattern
  label: string
}> = [
  { value: 'triangle', label: 'Triangle' },
  { value: 'square', label: 'Square' },
  { value: 'hex', label: 'Hex' },
  { value: 'lines', label: 'Lines' },
  { value: 'none', label: 'None' },
]

export function isBackgroundPattern(value: unknown): value is BackgroundPattern {
  return typeof value === 'string'
    && BACKGROUND_PATTERN_VALUES.includes(value as BackgroundPattern)
}

const patternSpecs: Record<Exclude<BackgroundPattern, 'none'>, {
  width: number
  height: number
  path: string
}> = {
  triangle: {
    width: 32,
    height: 28,
    path: 'M0 28L16 0L32 28ZM-16 0L0 28L16 0ZM16 0L32 28L48 0Z',
  },
  square: {
    width: 24,
    height: 24,
    path: 'M.5.5H24V24H.5Z',
  },
  hex: {
    width: 28,
    height: 49,
    path: 'M13.99 9.25l13 7.5v15l-13 7.5L1 31.75v-15l12.99-7.5zM3 17.9v12.7l10.99 6.34 11-6.35V17.9l-11-6.34L3 17.9zM0 15l12.98-7.5V0h-2v6.35L0 12.69v2.3zm0 18.5L12.98 41v8h-2v-6.85L0 35.81v-2.3zM15 0v7.5L27.99 15H28v-2.31h-.01L17 6.35V0h-2zm0 49v-8l12.99-7.5H28v2.31h-.01L17 42.15V49h-2z',
  },
  lines: {
    width: 24,
    height: 24,
    path: 'M-6 24L24-6M0 30L30 0',
  },
}

/** One safe renderer for the login and sidebar background surfaces. */
export function backgroundPatternImage(pattern: BackgroundPattern, patternColor: string): string {
  if (pattern === 'none') return 'none'

  const color = /^#[0-9a-f]{6}$/i.test(patternColor) ? patternColor : '#27272a'
  const spec = patternSpecs[pattern]
  const svg = [
    `<svg xmlns='http://www.w3.org/2000/svg' data-pattern='${pattern}'`,
    ` width='${spec.width}' height='${spec.height}' viewBox='0 0 ${spec.width} ${spec.height}'>`,
    `<path d='${spec.path}' fill='none' stroke='${color}' stroke-opacity='.42' stroke-width='1'/>`,
    '</svg>',
  ].join('')
  return `url("data:image/svg+xml,${encodeURIComponent(svg)}")`
}
