// SPDX-License-Identifier: AGPL-3.0-only
// Avatars: where a person's picture lives, the colour their initials sit on when
// there is none (the server's rule, so every screen agrees), what an upload may
// be, and the crop dialog's arithmetic. Free of Vue for unit tests.

export const AVATAR_SIZES = [32, 64, 128, 256] as const
export type AvatarSize = typeof AVATAR_SIZES[number]
export const MAX_AVATAR_BYTES = 8 << 20
export const MAX_AVATAR_PIXELS = 4_194_304
export const MAX_AVATAR_DIMENSION = 4096
export const AVATAR_TYPES = ['image/png', 'image/jpeg', 'image/webp']

// The picture at one size; `version` (the variant's hash) makes it cacheable for good.
export function avatarUrl(id: string, size: AvatarSize, version?: string) {
  return `/api/people/${encodeURIComponent(id)}/avatar/${size}${version ? `?v=${version}` : ''}`
}
// The smallest variant that stays sharp at `px` CSS pixels on a 2x screen, plus a srcset.
export function avatarSources(id: string, px: number, hashes: Record<string, string> = {}) {
  const base = AVATAR_SIZES.find(size => size >= px) ?? 256
  const srcset = AVATAR_SIZES.filter(size => size >= px).map(size => `${avatarUrl(id, size, hashes[String(size)])} ${size / px}x`).join(', ')
  return { src: avatarUrl(id, base, hashes[String(base)]), srcset }
}

// ---------- Who has a picture ----------
// People payloads (list assignees, project people, activity authors, the
// directory, quote presence) say whether a person has a picture. A picture is
// asked for only when one is known to exist, so people without one never cost a
// request (or a 404 in the console); their initials show at once.
const pictures = new Map<string, boolean>()
const listeners = new Set<() => void>()
export interface PictureHint { id?: string | null; has_avatar?: boolean }
export function learnPictures(people: Iterable<PictureHint | null | undefined>) {
  let changed = false
  for (const person of people) {
    if (!person?.id || typeof person.has_avatar !== 'boolean' || pictures.get(person.id) === person.has_avatar) continue
    pictures.set(person.id, person.has_avatar)
    changed = true
  }
  if (changed) for (const listener of listeners) listener()
}
export function pictureKnown(id: string): boolean | undefined { return pictures.get(id) }
export function onPictures(listener: () => void): () => void { listeners.add(listener); return () => { listeners.delete(listener) } }
// Whether an avatar asks the server for a picture: people only, with an id, not
// already found missing; my own when my profile says so; anyone else only when
// a payload said they have one.
export function shouldRequestAvatar(p: { kind: 'person' | 'agent'; id: string | null | undefined; mine: boolean; myPicture: boolean; known: boolean | undefined; missing: boolean }): boolean {
  if (p.kind !== 'person' || !p.id || p.missing) return false
  return p.mine ? p.myPicture : p.known === true
}

// ---------- Initials and their colour ----------
// The twelve muted palette tokens, in the server's order (internal/profile).
export const AVATAR_PALETTE = ['slate', 'sage', 'moss', 'ocean', 'steel', 'denim', 'iris', 'plum', 'rose', 'clay', 'sand', 'teal'] as const
export type AvatarColor = typeof AVATAR_PALETTE[number]
const colors = new Map<string, AvatarColor>()
// palette[sha256(principal id)[0] % 12], as the server computes avatar_color.
export function avatarColor(id: string | null | undefined): AvatarColor {
  if (!id) return 'slate'
  let color = colors.get(id)
  if (!color) { color = AVATAR_PALETTE[sha256FirstByte(id) % AVATAR_PALETTE.length]; colors.set(id, color) }
  return color
}
// Initials as the server derives them: the first letter or digit of the first
// (or preferred) and last name, else of the short name, else "?".
export function deriveInitials(p: { first_name?: string; last_name?: string; preferred_name?: string; short_name?: string }) {
  const firstChar = (value = '') => [...value].find(ch => /[\p{L}\p{N}]/u.test(ch))?.toUpperCase() ?? ''
  const out = firstChar(p.first_name || p.preferred_name) + firstChar(p.last_name)
  return out || firstChar(p.short_name) || '?'
}

// ---------- Upload checks, in plain words ----------
export function uploadProblem(file: { type: string; size: number; name?: string }): string {
  if (!AVATAR_TYPES.includes(file.type)) return 'Use a PNG, JPEG or WebP image. This file is not one of those.'
  if (file.size > MAX_AVATAR_BYTES) return `This photo is ${(file.size / 1024 / 1024).toFixed(1)} MB; a photo can be up to 8 MB.`
  if (file.size === 0) return 'This file is empty.'
  return ''
}
// The server's answer to a failed upload, as people say it.
export function uploadError(status: number, message: string): string {
  if (status === 413) return 'This photo is larger than 8 MB. Choose a smaller one.'
  if (/PNG, JPEG or WebP/.test(message)) return 'Use a PNG, JPEG or WebP image.'
  if (/invalid image/.test(message)) return 'This file could not be read as an image.'
  if (/dimensions/.test(message)) return 'Use a photo with at most 4,194,304 pixels and 4096 pixels per side, and a square crop at most 2048 pixels per side.'
  if (/crop/.test(message)) return 'Choose a square inside the photo, at most 2048 pixels per side. Please adjust it and try again.'
  if (status === 0) return 'The upload did not reach the server. Check your connection and try again.'
  return 'Your photo could not be saved. Please try again.'
}

// Fit the whole oriented photo, preserving its aspect ratio. Flooring keeps both
// the pixel and side limits inclusive; the shorter side is then at most 2048.
export function avatarUploadDimensions(width: number, height: number) {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width < 1 || height < 1) {
    throw new Error('This file could not be read as an image.')
  }
  const scale = Math.min(1, MAX_AVATAR_DIMENSION / width, MAX_AVATAR_DIMENSION / height, Math.sqrt(MAX_AVATAR_PIXELS / width / height))
  return { width: Math.max(1, Math.floor(width * scale)), height: Math.max(1, Math.floor(height * scale)) }
}

// The loaded preview has already applied EXIF orientation. Drawing that same
// image bakes the orientation into the pixels; canvas encoding drops EXIF so
// the server will not rotate them again. Small originals keep their bytes.
export async function prepareAvatarUpload(file: File, image: HTMLImageElement) {
  const dimensions = avatarUploadDimensions(image.naturalWidth, image.naturalHeight)
  if (dimensions.width === image.naturalWidth && dimensions.height === image.naturalHeight) {
    return { file, ...dimensions }
  }
  const canvas = document.createElement('canvas')
  canvas.width = dimensions.width
  canvas.height = dimensions.height
  try {
    const context = canvas.getContext('2d')
    if (!context) throw new Error('This photo could not be resized. Please try another image.')
    context.drawImage(image, 0, 0, canvas.width, canvas.height)
    // WebP preserves transparency for PNG/WebP input while keeping the upload
    // small. JPEG camera photos stay JPEG.
    const resized = await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob(blob => blob ? resolve(blob) : reject(new Error('This photo could not be resized. Please try another image.')), file.type === 'image/jpeg' ? 'image/jpeg' : 'image/webp', 0.92)
    })
    const problem = uploadProblem(resized)
    if (problem) throw new Error(problem)
    return { file: resized, ...dimensions }
  } finally {
    canvas.width = canvas.height = 0
  }
}

// ---------- Crop: a square viewport over the image, which always covers it ----------
export interface CropView { view: number; width: number; height: number; zoom: number; x: number; y: number }
export const MIN_ZOOM = 1
export const MAX_ZOOM = 5
export const coverScale = (v: Pick<CropView, 'view' | 'width' | 'height'>) => v.view / Math.min(v.width, v.height)
export const scaleOf = (v: CropView) => coverScale(v) * v.zoom
// Keeps the image covering the viewport: x and y are the image's top-left corner.
export function clampView(v: CropView): CropView {
  const zoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, v.zoom))
  const s = coverScale(v) * zoom
  const minX = v.view - v.width * s, minY = v.view - v.height * s
  return { ...v, zoom, x: Math.min(0, Math.max(minX, v.x)), y: Math.min(0, Math.max(minY, v.y)) }
}
export function centred(view: number, width: number, height: number): CropView {
  const s = view / Math.min(width, height)
  return { view, width, height, zoom: 1, x: (view - width * s) / 2, y: (view - height * s) / 2 }
}
export const pan = (v: CropView, dx: number, dy: number) => clampView({ ...v, x: v.x + dx, y: v.y + dy })
// Zooms keeping the image point under (px, py) in place (the pointer, or the centre).
export function zoomAt(v: CropView, zoom: number, px = v.view / 2, py = v.view / 2): CropView {
  const s = scaleOf(v)
  const imageX = (px - v.x) / s, imageY = (py - v.y) / s
  const next = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, zoom))
  const s2 = coverScale(v) * next
  return clampView({ ...v, zoom: next, x: px - imageX * s2, y: py - imageY * s2 })
}
// The square the server crops, in the image's own pixels.
export function cropOf(v: CropView): { x: number; y: number; size: number } {
  const s = scaleOf(v)
  const size = Math.max(1, Math.min(Math.round(v.view / s), v.width, v.height))
  const x = Math.min(Math.max(0, Math.round(-v.x / s)), v.width - size)
  const y = Math.min(Math.max(0, Math.round(-v.y / s)), v.height - size)
  return { x, y, size }
}

// ---------- SHA-256, first byte only (enough for the palette index) ----------
const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
  0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
  0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
  0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])
export function sha256FirstByte(text: string): number {
  const bytes = new TextEncoder().encode(text)
  const length = Math.ceil((bytes.length + 9) / 64) * 64
  const data = new Uint8Array(length)
  data.set(bytes); data[bytes.length] = 0x80
  const bits = bytes.length * 8
  const view = new DataView(data.buffer)
  view.setUint32(length - 4, bits >>> 0); view.setUint32(length - 8, Math.floor(bits / 2 ** 32))
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const w = new Uint32Array(64)
  const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n))
  for (let offset = 0; offset < length; offset += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(offset + i * 4)
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3)
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10)
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0
    }
    let [a, b, c, d, e, f, g, hh] = h
    for (let i = 0; i < 64; i++) {
      const t1 = (hh + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i] + w[i]) >>> 0
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0
      hh = g; g = f; f = e; e = (d + t1) >>> 0; d = c; c = b; b = a; a = (t1 + t2) >>> 0
    }
    h[0] = (h[0] + a) >>> 0; h[1] = (h[1] + b) >>> 0; h[2] = (h[2] + c) >>> 0; h[3] = (h[3] + d) >>> 0
    h[4] = (h[4] + e) >>> 0; h[5] = (h[5] + f) >>> 0; h[6] = (h[6] + g) >>> 0; h[7] = (h[7] + hh) >>> 0
  }
  return h[0] >>> 24
}
