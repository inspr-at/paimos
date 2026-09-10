import { describe, expect, it } from 'vitest'
import { money, parseAmount, total, type OfferPosition } from './types'
describe('offer amounts', () => {
  it('rounds fractional quantities half-up in cents', () => {
    for (const [quantity, price, want] of [
      [2, 145000, 290000],
      [16, 16500, 264000],
      [1.5, 9999, 14999],
    ])
      expect(total({ quantity, unit_price_cents: price } as OfferPosition)).toBe(want)
    expect(money(14999)).toBe('€ 149,99')
    expect(money(290000)).toBe('€ 2.900,00')
  })
  it('accepts Austrian accounting values and decimal keyboards without silently multiplying prices', () => {
    expect(parseAmount('€ 1.450,00')).toBe(1450)
    expect(parseAmount('99.99')).toBe(99.99)
    expect(parseAmount('99,99')).toBe(99.99)
    expect(parseAmount('1.450')).toBe(1450)
    expect(parseAmount('-1')).toBeNull()
    expect(parseAmount('1,234')).toBeNull()
    expect(parseAmount('x')).toBeNull()
  })
})
