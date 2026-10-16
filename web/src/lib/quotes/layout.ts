// SPDX-License-Identifier: AGPL-3.0-only
import type { QuotePosition } from './types'
export const PAGE_MM = { width: 210, height: 297, marginTop: 20, marginRight: 21, marginBottom: 20, marginLeft: 21 } as const
export interface PagePlan { kind: 'cover' | 'sections' | 'positions'; heading?: 'terms' | 'positions'; sectionIds: string[]; positionIds: string[]; acceptance: boolean }
export interface PaginationResult { ready: boolean; overflow: string | null; pages: PagePlan[] }
export const decimalCents = (cents: number): string => {
  if (!Number.isSafeInteger(cents)) throw new Error('Invalid minor-unit amount')
  const value = BigInt(cents)
  const sign = value < 0n ? '-' : ''
  const absolute = value < 0n ? -value : value
  return `${sign}${absolute / 100n}.${String(absolute % 100n).padStart(2, '0')}`
}
export const money = (cents: number, currency: string): string => {
  const decimal = decimalCents(cents)
  const [whole, fraction] = decimal.split('.')
  // German grouping with a dot, as the classic quote printed it (never a space).
  const sign = whole!.startsWith('-') ? '-' : ''
  const digits = whole!.replace('-', '').replace(/\B(?=(\d{3})+(?!\d))/g, '.')
  return `${sign}${digits},${fraction} ${currency}`
}
export const quantityHundredths = (quantity: string): bigint => {
  if (!/^(0|[1-9]\d*)(?:\.(\d{1,2}))?$/.test(quantity)) throw new Error('Invalid exact quantity')
  const [whole, fraction = ''] = quantity.split('.')
  return BigInt(whole!) * 100n + BigInt(fraction.padEnd(2, '0') || '0')
}
export const positionTotal = (position: QuotePosition): number => {
  if (!Number.isSafeInteger(position.unit_price_cents) || position.unit_price_cents < 0) throw new Error('Invalid minor-unit price')
  const cents = (quantityHundredths(position.quantity) * BigInt(position.unit_price_cents) + 50n) / 100n
  if (cents > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('Total is too large')
  return Number(cents)
}
export const documentTotal = (positions: readonly QuotePosition[]): number => {
  const total = positions.reduce((sum, position) => sum + BigInt(positionTotal(position)), 0n)
  if (total > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('Total is too large')
  return Number(total)
}
export function exactMM(input: string | null | undefined, min: number, max: number): string | undefined {
  if (input == null || input === '') return undefined
  if (!/^-?(?:0|[1-9]\d{0,2})(?:\.\d)?$/.test(input)) throw new Error('Millimetres need at most one decimal')
  const value = Number(input)
  if (value < min || value > max) throw new Error('Millimetres out of range')
  return value === 0 ? undefined : value.toFixed(1)
}
// `breaks`: sections that start on a new page (the Section tab's "Start on a new page").
export function fitWholeBlocks(coverPx: number, sections: { id: string; px: number }[], positions: { id: string; px: number }[], acceptancePx: number, usablePx: number, headings = { sections: 32, positions: 45, table: 32 }, breaks: ReadonlySet<string> = new Set()): PaginationResult {
  const pages: PagePlan[] = [{ kind: 'cover', sectionIds: [], positionIds: [], acceptance: false }]
  let overflow: string | null = coverPx > usablePx ? 'Cover exceeds one page' : null
  let remaining = usablePx - coverPx
  for (const section of sections) {
    if (section.px + headings.sections > usablePx) overflow ??= `Section ${section.id} exceeds one page`
    const fresh = pages.at(-1)!.kind === 'sections' && !pages.at(-1)!.sectionIds.length
    if (section.px + headings.sections > remaining || (breaks.has(section.id) && !fresh)) {
      pages.push({ kind: 'sections', sectionIds: [], positionIds: [], acceptance: false })
      remaining = usablePx
    }
    pages.at(-1)!.sectionIds.push(section.id)
    remaining -= section.px + headings.sections
  }
  pages.push({ kind: 'positions', sectionIds: [], positionIds: [], acceptance: false })
  remaining = usablePx - headings.positions - headings.table
  for (const position of positions) {
    if (position.px + headings.table > usablePx) overflow ??= `Position ${position.id} exceeds one page`
    if (position.px > remaining && pages.at(-1)!.positionIds.length) {
      pages.push({ kind: 'positions', sectionIds: [], positionIds: [], acceptance: false })
      remaining = usablePx - headings.table
    }
    pages.at(-1)!.positionIds.push(position.id)
    remaining -= position.px
  }
  if (acceptancePx > usablePx) overflow ??= 'Acceptance exceeds one page'
  if (acceptancePx > remaining) pages.push({ kind: 'positions', sectionIds: [], positionIds: [], acceptance: false })
  pages.at(-1)!.acceptance = true
  return { ready: !overflow, overflow, pages }
}

// The classic print sheet keeps one terms heading for the whole ordered block
// list, adds a small continuation inset, and reserves measured table chrome.
// Measurements are browser pixels, so pagination follows the loaded fonts.
export function fitClassicBlocks(
  coverPx: number, sections: { id: string; px: number }[], positions: { id: string; px: number }[],
  acceptancePx: number, usablePx: number,
  chrome: { terms: number; positions: number; table: number; continuation: number },
): PaginationResult {
  const pages: PagePlan[] = [{ kind: 'cover', sectionIds: [], positionIds: [], acceptance: false }]
  let overflow: string | null = coverPx > usablePx ? 'Cover exceeds one page' : null
  let remaining = usablePx - coverPx
  for (const [index, section] of sections.entries()) {
    const height = section.px + 17
    const headingHeight = index === 0 ? chrome.terms : 0
    if (height + headingHeight > usablePx - (index === 0 ? 0 : chrome.continuation)) overflow ??= `Section ${section.id} exceeds one page`
    if (height + headingHeight > remaining) {
      pages.push({ kind: 'sections', sectionIds: [], positionIds: [], acceptance: false })
      remaining = usablePx - (index === 0 ? 0 : chrome.continuation)
    }
    if (index === 0) pages.at(-1)!.heading = 'terms'
    pages.at(-1)!.sectionIds.push(section.id)
    remaining -= height + headingHeight
  }
  pages.push({ kind: 'positions', heading: 'positions', sectionIds: [], positionIds: [], acceptance: false })
  remaining = usablePx - chrome.positions - chrome.table
  for (const [index, position] of positions.entries()) {
    const height = position.px + 2
    if (height > usablePx - chrome.table - (index === 0 ? chrome.positions : chrome.continuation)) overflow ??= `Position ${position.id} exceeds one page`
    if (height > remaining && index > 0) {
      pages.push({ kind: 'positions', sectionIds: [], positionIds: [], acceptance: false })
      remaining = usablePx - chrome.continuation - chrome.table
    }
    pages.at(-1)!.positionIds.push(position.id)
    remaining -= height
  }
  const acceptanceHeight = acceptancePx + 28
  if (acceptanceHeight > usablePx - chrome.continuation) overflow ??= 'Acceptance exceeds one page'
  if (acceptanceHeight > remaining) pages.push({ kind: 'positions', sectionIds: [], positionIds: [], acceptance: false })
  pages.at(-1)!.acceptance = true
  return { ready: !overflow, overflow, pages }
}
