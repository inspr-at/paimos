// SPDX-License-Identifier: AGPL-3.0-only
// Exact decimal and integer minor-unit formatting. Never accepts a JS number:
// binary floats are not money. Callers keep the JSON spelling via parseJSONExact.

const CURRENCY = /^[A-Z]{3}$/
const MAX_DIGITS = 40

// ISO 4217 minor-unit scales this shell is willing to apply. Unknown currencies
// throw so a display cannot invent a scale.
const MINOR_SCALE: Record<string, number> = {
  AUD: 2, BRL: 2, CAD: 2, CHF: 2, CNY: 2, DKK: 2, EUR: 2, GBP: 2, INR: 2, NOK: 2, SEK: 2, USD: 2, ZAR: 2,
  BHD: 3, JOD: 3, KWD: 3, OMR: 3, TND: 3,
  CLP: 0, ISK: 0, JPY: 0, KRW: 0, VND: 0, XAF: 0, XOF: 0,
}

export function minorScale(currency: string): number | null {
  const code = currencyCode(currency)
  return Object.prototype.hasOwnProperty.call(MINOR_SCALE, code) ? MINOR_SCALE[code]! : null
}

// Quote JSON numbers as their exact spelling before JSON.parse turns them into floats.
export function parseJSONExact(text: string): unknown {
  let quoted: string
  try {
    quoted = quoteJsonNumbers(text)
  } catch (error) {
    if (error instanceof Error && error.message === 'Exponent amounts are not exact decimals.') throw error
    throw new Error('JSON was not usable.')
  }
  try {
    return JSON.parse(quoted)
  } catch {
    throw new Error('JSON was not usable.')
  }
}

function currencyCode(currency: string): string {
  if (typeof currency !== 'string' || !CURRENCY.test(currency)) throw new Error('Currency must be a three-letter code.')
  return currency
}

function canonical(units: bigint, scale: number): string {
  const digits = units.toString().padStart(scale + 1, '0')
  if (scale === 0) return digits
  return `${digits.slice(0, -scale)}.${digits.slice(-scale)}`
}

function quoteJsonNumbers(text: string): string {
  let out = ''
  let i = 0
  while (i < text.length) {
    const c = text[i]!
    if (c === '"') {
      const start = i
      i += 1
      while (i < text.length) {
        if (text[i] === '\\') {
          i += 2
          continue
        }
        if (text[i] === '"') {
          i += 1
          break
        }
        i += 1
      }
      out += text.slice(start, i)
      continue
    }
    if (c === '-' || isDigit(c)) {
      const start = i
      if (c === '-') i += 1
      if (!isDigit(text[i] ?? '')) throw new Error('JSON was not usable.')
      if (text[i] === '0') i += 1
      else while (isDigit(text[i] ?? '')) i += 1
      if (text[i] === '.') {
        i += 1
        if (!isDigit(text[i] ?? '')) throw new Error('JSON was not usable.')
        while (isDigit(text[i] ?? '')) i += 1
      }
      const exponent = text[i]
      if (exponent === 'e' || exponent === 'E') throw new Error('Exponent amounts are not exact decimals.')
      out += JSON.stringify(text.slice(start, i))
      continue
    }
    out += c
    i += 1
  }
  return out
}

function isDigit(char: string): boolean {
  return char >= '0' && char <= '9'
}

// ---------- Fixed-scale arithmetic for quotes and hours ----------
// The server keeps amounts at four places (numeric(18,4)) and tax rates at five,
// rounding half up. These helpers repeat that arithmetic exactly with BigInt so a
// draft shows the same net, tax and total the server will freeze.

const SIGNED = /^(-?)(0|[1-9]\d*)(?:\.(\d+))?$/
export const AMOUNT_SCALE = 4

// Parse a decimal (optionally negative) into units at a fixed scale. More
// fractional digits than the scale are refused, never rounded away silently.
export function toUnits(value: string, scale: number): bigint {
  const match = SIGNED.exec(typeof value === 'string' ? value.trim() : '')
  if (!match) throw new Error('Amount must be an exact decimal string.')
  const frac = match[3] ?? ''
  if (frac.replace(/0+$/, '').length > scale) throw new Error('Amount has too many decimal places.')
  if ((match[2]!.length + frac.length) > MAX_DIGITS) throw new Error('Amount is too large.')
  const units = BigInt(match[2]! + frac.padEnd(scale, '0').slice(0, scale))
  return match[1] === '-' ? -units : units
}

export function fromUnits(units: bigint, scale: number): string {
  const negative = units < 0n
  const text = canonical(negative ? -units : units, scale)
  return negative ? `-${text}` : text
}

// a × b at their scales, rounded half up (away from zero) to `scale`.

export function sumAmounts(values: readonly string[]): string {
  return fromUnits(values.reduce((sum, value) => sum + toUnits(value, AMOUNT_SCALE), 0n), AMOUNT_SCALE)
}

// Display: en-GB grouping, at least the currency's minor digits, and any further
// non-zero digits the exact value carries (71.9640 EUR shows as 71.964).
export function formatAmount(value: string, currency: string, options: { signed?: boolean } = {}): string {
  const match = SIGNED.exec(typeof value === 'string' ? value.trim() : '')
  if (!match) throw new Error('Amount must be an exact decimal string.')
  const minor = minorScale(currency) ?? 2
  const frac = (match[3] ?? '').replace(/0+$/, '').padEnd(minor, '0')
  const whole = match[2]!.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const zero = /^0*$/.test(match[2]! + (match[3] ?? ''))
  const sign = match[1] === '-' && !zero ? '−' : options.signed && !zero ? '+' : ''
  return `${sign}${whole}${frac ? `.${frac}` : ''}`
}

// Money typed by an admin (a rate): non-negative, at most four places.
export function parseAmountInput(text: string): string | null {
  const value = text.trim().replace(/[\s,](?=\d{3}\b)/g, '').replace(',', '.')
  if (!/^\d{1,14}(\.\d{1,4})?$/.test(value)) return null
  return fromUnits(toUnits(value.replace(/^0+(?=\d)/, ''), AMOUNT_SCALE), AMOUNT_SCALE)
}
