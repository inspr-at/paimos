// SPDX-License-Identifier: AGPL-3.0-only
// Exact decimal and integer minor-unit formatting. Never accepts a JS number:
// binary floats are not money. Callers keep the JSON spelling via parseJSONExact.

const DECIMAL = /^(0|[1-9]\d*)(?:\.(\d+))?$/
const MINOR = /^(0|[1-9]\d*)$/
const CURRENCY = /^[A-Z]{3}$/
const MAX_DIGITS = 40

// ISO 4217 minor-unit scales this shell is willing to apply. Unknown currencies
// throw so a display cannot invent a scale.
const MINOR_SCALE: Record<string, number> = {
  AUD: 2, BRL: 2, CAD: 2, CHF: 2, CNY: 2, DKK: 2, EUR: 2, GBP: 2, INR: 2, NOK: 2, SEK: 2, USD: 2, ZAR: 2,
  BHD: 3, JOD: 3, KWD: 3, OMR: 3, TND: 3,
  CLP: 0, ISK: 0, JPY: 0, KRW: 0, VND: 0, XAF: 0, XOF: 0,
}

export interface ExactDecimal { scale: number; units: bigint }

export function minorScale(currency: string): number | null {
  const code = currencyCode(currency)
  return Object.prototype.hasOwnProperty.call(MINOR_SCALE, code) ? MINOR_SCALE[code]! : null
}

export function parseDecimal(value: string): ExactDecimal {
  if (typeof value !== 'string') throw new Error('Amount must be an exact decimal string.')
  const text = value.trim()
  const match = DECIMAL.exec(text)
  if (!match) throw new Error('Amount must be an exact decimal string.')
  const whole = match[1]!
  const frac = match[2] ?? ''
  if (whole.length + frac.length > MAX_DIGITS) throw new Error('Amount is too large.')
  const digits = `${whole}${frac}`.replace(/^0+(?=\d)/, '')
  return { scale: frac.length, units: BigInt(digits) }
}

export function formatDecimal(amount: string, currency: string): string {
  const parsed = parseDecimal(amount)
  return `${renderUnits(parsed.units, parsed.scale)} ${currencyCode(currency)}`
}

export function formatMinor(minor: string, currency: string, scale?: number | null): string {
  if (typeof minor !== 'string') throw new Error('Minor units must be an integer string.')
  const text = minor.trim()
  if (!MINOR.test(text) || text.length > MAX_DIGITS) throw new Error('Minor units must be an integer string.')
  const code = currencyCode(currency)
  const resolved = scale === undefined ? minorScale(code) : scale
  if (resolved == null || !Number.isSafeInteger(resolved) || resolved < 0 || resolved > 8) {
    throw new Error('Unknown currency scale.')
  }
  return `${renderUnits(BigInt(text), resolved)} ${code}`
}

export function addDecimal(left: string, right: string): string {
  const a = parseDecimal(left)
  const b = parseDecimal(right)
  const scale = Math.max(a.scale, b.scale)
  return canonical(rescale(a, scale) + rescale(b, scale), scale)
}

export function multiplyDecimal(left: string, right: string): string {
  const a = parseDecimal(left)
  const b = parseDecimal(right)
  if (a.scale + b.scale > MAX_DIGITS) throw new Error('Amount is too large.')
  return canonical(a.units * b.units, a.scale + b.scale)
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

export async function readExactJSON(response: Response): Promise<unknown> {
  return parseJSONExact(await response.text())
}

function currencyCode(currency: string): string {
  if (typeof currency !== 'string' || !CURRENCY.test(currency)) throw new Error('Currency must be a three-letter code.')
  return currency
}

function rescale(value: ExactDecimal, scale: number): bigint {
  return value.units * 10n ** BigInt(scale - value.scale)
}

function canonical(units: bigint, scale: number): string {
  const digits = units.toString().padStart(scale + 1, '0')
  if (scale === 0) return digits
  return `${digits.slice(0, -scale)}.${digits.slice(-scale)}`
}

function renderUnits(units: bigint, scale: number): string {
  return group(canonical(units, scale))
}

function group(canonicalDecimal: string): string {
  const [whole, frac] = canonicalDecimal.split('.')
  const grouped = whole!.replace(/\B(?=(\d{3})+(?!\d))/g, '\u202f')
  return frac === undefined ? grouped : `${grouped}.${frac}`
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
export const TAX_SCALE = 5

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
function productRounded(a: bigint, b: bigint, fromScale: number, scale: number): bigint {
  const product = a * b
  const divisor = 10n ** BigInt(fromScale - scale)
  const half = divisor / 2n
  return product >= 0n ? (product + half) / divisor : -((-product + half) / divisor)
}

// Net line amount: bill rate × quantity, four places.
export function lineNet(rate: string, quantity: string): string {
  return fromUnits(productRounded(toUnits(rate, AMOUNT_SCALE), toUnits(quantity, AMOUNT_SCALE), AMOUNT_SCALE * 2, AMOUNT_SCALE), AMOUNT_SCALE)
}

// Line tax: net × tax rate (five places), four places.
export function lineTax(net: string, taxRate: string): string {
  return fromUnits(productRounded(toUnits(net, AMOUNT_SCALE), toUnits(taxRate, TAX_SCALE), AMOUNT_SCALE + TAX_SCALE, AMOUNT_SCALE), AMOUNT_SCALE)
}

export function sumAmounts(values: readonly string[]): string {
  return fromUnits(values.reduce((sum, value) => sum + toUnits(value, AMOUNT_SCALE), 0n), AMOUNT_SCALE)
}

export function diffAmounts(after: string, before: string): string {
  return fromUnits(toUnits(after, AMOUNT_SCALE) - toUnits(before, AMOUNT_SCALE), AMOUNT_SCALE)
}

export function compareAmounts(a: string, b: string): number {
  const x = toUnits(a, AMOUNT_SCALE), y = toUnits(b, AMOUNT_SCALE)
  return x === y ? 0 : x < y ? -1 : 1
}

export function isZero(value: string): boolean {
  try { return toUnits(value, AMOUNT_SCALE) === 0n } catch { return false }
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

// Quantity typed by a person: "1,5" or "1.5"; positive, at most four places.
export function parseQuantityInput(text: string): string | null {
  const value = text.trim().replace(',', '.')
  if (!/^\d{1,14}(\.\d{1,4})?$/.test(value)) return null
  const units = toUnits(value.replace(/^0+(?=\d)/, ''), AMOUNT_SCALE)
  if (units <= 0n) return null
  return fromUnits(units, AMOUNT_SCALE).replace(/\.?0+$/, '')
}

// Tax typed as a percentage ("20", "20 %", "7.7"): a rate 0..1 with five places.
export function parsePercentInput(text: string): string | null {
  const value = text.trim().replace(/\s*%$/, '').replace(',', '.')
  if (!/^\d{1,3}(\.\d{1,3})?$/.test(value)) return null
  const percent = toUnits(value.replace(/^0+(?=\d)/, ''), 3)
  if (percent > 100_000n) return null
  return fromUnits(percent, TAX_SCALE)
}

// A stored rate ("0.20000") as a percentage for display ("20").
export function ratePercent(rate: string): string {
  const units = toUnits(rate, TAX_SCALE)
  return fromUnits(units, 3).replace(/\.?0+$/, '') || '0'
}

// Money typed by an admin (a rate): non-negative, at most four places.
export function parseAmountInput(text: string): string | null {
  const value = text.trim().replace(/[\s,](?=\d{3}\b)/g, '').replace(',', '.')
  if (!/^\d{1,14}(\.\d{1,4})?$/.test(value)) return null
  return fromUnits(toUnits(value.replace(/^0+(?=\d)/, ''), AMOUNT_SCALE), AMOUNT_SCALE)
}
