// SPDX-License-Identifier: AGPL-3.0-only
// A workspace's own brand in the header (AEON-431): a logo, an optional logo for
// dark mode, and a short name, from the session payload (GET /api/me,
// tenant.brand). The product's names stay in lib/brand.ts and the footer.
// Logos are only ever drawn through <img>, and only from the server's own
// logo route.

export interface TenantBrandLogo { url: string; width: number; height: number }
export interface TenantBrand { short_name?: string; logo?: TenantBrandLogo; logo_dark?: TenantBrandLogo }
export interface BrandLogoInfo extends TenantBrandLogo { content_type: string; size: number; sha256: string; uploaded_at: string }
export interface BrandSettings { short_name: string; logo: BrandLogoInfo | null; logo_dark: BrandLogoInfo | null; updated_at?: string; cleaned?: boolean }

export const MAX_SHORT_NAME = 32
export const MAX_LOGO_BYTES = 256 * 1024
export const LOGO_TYPES = ['image/png', 'image/webp', 'image/svg+xml'] as const

// The header's logo box: as high as the product mark, and never wider than a
// short word, so a wide logo stays calm.
export const LOGO_HEIGHT = 28
export const LOGO_MAX_WIDTH = 132

const LOGO_URL = /^\/api\/brand\/logo\/(light|dark)(\?v=[0-9a-f]{1,64})?$/

export function safeLogo(value: unknown): TenantBrandLogo | null {
  if (!value || typeof value !== 'object') return null
  const l = value as Record<string, unknown>
  if (typeof l.url !== 'string' || !LOGO_URL.test(l.url)) return null
  if (!Number.isInteger(l.width) || !Number.isInteger(l.height) || (l.width as number) < 1 || (l.height as number) < 1) return null
  return { url: l.url, width: l.width as number, height: l.height as number }
}

export function shortName(value: unknown) {
  return typeof value === 'string' ? value.replace(/\s+/g, ' ').trim().slice(0, MAX_SHORT_NAME) : ''
}

// The box a logo is drawn in: its own proportions, fitted into the header slot.
export function fitLogo(logo: TenantBrandLogo, height = LOGO_HEIGHT, maxWidth = LOGO_MAX_WIDTH) {
  const ratio = logo.width / logo.height
  const width = Math.min(maxWidth, height * ratio)
  return { width: Math.round(width), height: Math.round(Math.min(height, width / ratio)) }
}

export interface HeaderBrand {
  name: string
  logo: TenantBrandLogo | null
  // No dark logo in dark mode: the light logo sits on a light plate, so dark
  // lettering stays legible ("auto").
  plate: boolean
}

// What the header shows instead of the product mark, or null for the product mark.
export function headerBrand(brand: unknown, dark: boolean): HeaderBrand | null {
  if (!brand || typeof brand !== 'object') return null
  const b = brand as Record<string, unknown>
  const name = shortName(b.short_name)
  const light = safeLogo(b.logo)
  const darkLogo = safeLogo(b.logo_dark)
  const logo = dark ? darkLogo ?? light : light ?? darkLogo
  if (!logo && !name) return null
  return { name, logo, plate: dark && !!logo && logo === light && !darkLogo }
}

// The session's brand after a settings change, so the header follows at once.
export function publicBrand(s: BrandSettings): TenantBrand | undefined {
  const out: TenantBrand = {}
  const name = shortName(s.short_name)
  if (name) out.short_name = name
  if (s.logo) out.logo = { url: s.logo.url, width: s.logo.width, height: s.logo.height }
  if (s.logo_dark) out.logo_dark = { url: s.logo_dark.url, width: s.logo_dark.width, height: s.logo_dark.height }
  return Object.keys(out).length ? out : undefined
}

// The browser's type for a picked file; some systems leave SVG untyped.
export function logoType(file: { name: string; type: string }) {
  const type = file.type.toLowerCase()
  if ((LOGO_TYPES as readonly string[]).includes(type)) return type
  const ext = file.name.toLowerCase().split('.').pop()
  if (!type || type === 'application/octet-stream') {
    if (ext === 'svg') return 'image/svg+xml'
    if (ext === 'png') return 'image/png'
    if (ext === 'webp') return 'image/webp'
  }
  return ''
}

// A check before upload; the server decides again.
export function logoProblem(file: { name: string; type: string; size: number }) {
  if (!logoType(file)) return 'Use a PNG, WebP or SVG file.'
  if (file.size > MAX_LOGO_BYTES) return `The file is ${Math.ceil(file.size / 1024)} KB; the limit is 256 KB.`
  if (file.size === 0) return 'The file is empty.'
  return ''
}
