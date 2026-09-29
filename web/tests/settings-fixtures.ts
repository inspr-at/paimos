// SPDX-License-Identifier: AGPL-3.0-only
// The server side of Settings for UI tests: the personal profile (fields with the
// server's 400 and 409 answers, avatar upload and removal, people's pictures, the
// greeting), agent keys and quote settings. Registered after the work and business
// mocks, it answers only its own routes and lets the rest fall through.
import type { Page } from '@playwright/test'
import { deflateSync } from 'node:zlib'

export interface SettingsMockOptions {
  greeting?: boolean; failPatch?: boolean; noQuotes?: boolean; noKeys?: boolean
  taken?: string[]; reject?: Record<string, string>; photo?: boolean; people?: string[]; slowUpload?: number
}
const HASH = (seed: string) => seed.repeat(64).slice(0, 64)
const photoHashes = (seed: string) => ({ '32': HASH(`${seed}1`), '64': HASH(`${seed}2`), '128': HASH(`${seed}3`), '256': HASH(`${seed}4`) })
const firstChar = (value = '') => [...value].find(ch => /[\p{L}\p{N}]/u.test(ch))?.toUpperCase() ?? ''
const derive = (p: { first_name: string; last_name: string; preferred_name: string; short_name: string }) => (firstChar(p.first_name || p.preferred_name) + firstChar(p.last_name)) || firstChar(p.short_name) || '?'

// A real PNG of any size and colour (the crop dialog needs natural dimensions).
export function makePng(width: number, height: number, rgb: [number, number, number] = [14, 111, 108]) {
  const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0 })
  const crc = (buf: Buffer) => { let c = 0xffffffff; for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0 }
  const chunk = (type: string, data: Buffer) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const body = Buffer.concat([Buffer.from(type), data]); const sum = Buffer.alloc(4); sum.writeUInt32BE(crc(body)); return Buffer.concat([len, body, sum]) }
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(width, 0); ihdr.writeUInt32BE(height, 4); ihdr[8] = 8; ihdr[9] = 2
  const rows = Buffer.alloc((width * 3 + 1) * height)
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
    const at = y * (width * 3 + 1) + 1 + x * 3
    // A soft diagonal so a crop visibly differs from its neighbour.
    rows[at] = Math.min(255, rgb[0] + Math.round((x / width) * 120)); rows[at + 1] = Math.min(255, rgb[1] + Math.round((y / height) * 90)); rows[at + 2] = rgb[2]
  }
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', ihdr), chunk('IDAT', deflateSync(rows)), chunk('IEND', Buffer.alloc(0))])
}
export function settingsData(options: SettingsMockOptions = {}) {
  return {
    profile: {
      principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba',
      initials: 'MB', timezone: 'Europe/Vienna', locale: 'de-AT', greeting_enabled: options.greeting ?? true, avatar_color: 'teal',
      avatar_hashes: (options.photo ? photoHashes('a') : {}) as Record<string, string>, week_start: 1, revision: 3,
    },
    override: '',
    uploads: [] as { crop: { x: number; y: number; size: number } }[],
    removals: 0,
    greetings: [] as { timezone: string }[],
    keys: options.noKeys ? [] : [
      { id: 'k1', principal_id: 'a1', name: 'aeon-coordinator', prefix: 'c0or', scopes: [], created_at: '2026-09-20T08:00:00Z', expires_at: null, last_used_at: '2026-09-24T07:40:00Z', revoked_at: null },
      { id: 'k2', principal_id: 'a2', name: 'pharos-deployer', prefix: 'ph4r', scopes: ['nodes:read', 'journey:write'], created_at: '2026-09-12T08:00:00Z', expires_at: '2026-12-31T00:00:00Z', last_used_at: null, revoked_at: null },
      { id: 'k3', principal_id: 'a3', name: 'old-importer', prefix: 'imp0', scopes: ['imports:write'], created_at: '2026-08-01T08:00:00Z', expires_at: null, last_used_at: '2026-08-02T08:00:00Z', revoked_at: '2026-08-03T08:00:00Z' },
    ],
    quotes: { revision: 2, numbering_time_zone: 'Europe/Vienna', default_currency: 'EUR', sender: { company: 'INSPR Studio', city: 'Graz', country: 'AT', iban: 'AT00 0000 0000 0000 0000' }, defaults: {}, layout: {}, smtp_confirmation_enabled: true, smtp_configured: true },
    patches: [] as Record<string, unknown>[],
  }
}
export type SettingsData = ReturnType<typeof settingsData>

export async function mockSettings(page: Page, data: SettingsData, options: SettingsMockOptions = {}) {
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, method = request.method()
    if (path === '/api/me/profile') {
      if (method === 'PATCH') {
        const body = request.postDataJSON() as Record<string, string>
        data.patches.push(body)
        if (options.failPatch) return route.fulfill({ status: 500, json: { error: 'database operation failed' } })
        const errors: Record<string, string> = {}
        for (const [key, message] of Object.entries(options.reject ?? {})) if (key in body) errors[key] = message
        if (typeof body.short_name === 'string' && body.short_name && !/^[a-z0-9._-]{2,24}$/.test(body.short_name)) errors.short_name = 'must be lowercase [a-z0-9._-], 2–24 characters'
        if (Object.keys(errors).length) return route.fulfill({ status: 400, json: { errors } })
        if (typeof body.short_name === 'string' && (options.taken ?? []).includes(body.short_name)) return route.fulfill({ status: 409, json: { errors: { short_name: 'already used in this tenant' } } })
        const { initials, ...rest } = body
        if (initials !== undefined) data.override = initials
        Object.assign(data.profile, rest, { revision: data.profile.revision + 1 })
        data.profile.initials = data.override || derive(data.profile)
        if (body.locale) data.profile.week_start = /-(US|CA|JP)$/.test(body.locale) ? 0 : 1
      }
      return route.fulfill({ json: data.profile })
    }
    if (path === '/api/me/avatar' && method === 'POST') {
      const text = request.postDataBuffer()?.toString('latin1') ?? ''
      const crop = JSON.parse(/name="crop"\r\n\r\n(\{[^}]*\})/.exec(text)?.[1] ?? '{}')
      data.uploads.push({ crop })
      if (options.slowUpload) await new Promise(resolve => setTimeout(resolve, options.slowUpload))
      data.profile.avatar_hashes = photoHashes(String.fromCharCode(98 + data.uploads.length))
      data.profile.revision++
      return route.fulfill({ json: data.profile })
    }
    if (path === '/api/me/avatar' && method === 'DELETE') {
      data.removals++
      data.profile.avatar_hashes = {}
      data.profile.revision++
      return route.fulfill({ json: data.profile })
    }
    const picture = /^\/api\/people\/([^/]+)\/avatar\/(32|64|128|256)$/.exec(path)
    if (picture) {
      const mine = picture[1] === data.profile.principal_id && Object.keys(data.profile.avatar_hashes).length > 0
      if (mine || (options.people ?? []).includes(picture[1])) return route.fulfill({ contentType: 'image/png', body: makePng(Number(picture[2]), Number(picture[2]), mine ? [180, 90, 60] : [60, 90, 180]) })
      return route.fulfill({ status: 404, json: { error: 'not found' } })
    }
    if (path === '/api/me/greeting') {
      data.greetings.push({ timezone: request.headers()['x-timezone'] ?? '' })
      return route.fulfill({ json: { salutation: 'Good afternoon', name: data.profile.preferred_name || data.profile.first_name, message: 'Small steps, shipped, beat big plans on paper.', id: 'g-small-steps' } })
    }
    if (path === '/api/portal/settings' && method === 'GET') return route.fulfill({ json: { enabled: false, slug: 'inspr' } })
    if (path === '/api/agent-keys' && method === 'GET') return route.fulfill({ json: { keys: data.keys } })
    if (path === '/api/quotes/settings' && method === 'GET') {
      if (options.noQuotes) return route.fulfill({ status: 403, json: { code: 'forbidden', message: 'quotes plugin is not enabled' } })
      return route.fulfill({ json: data.quotes })
    }
    return route.fallback()
  })
}
