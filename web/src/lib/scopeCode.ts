// SPDX-License-Identifier: AGPL-3.0-only
// v1 matches internal/scopecode; shared wire vectors test both implementations.
// Codes carry explicit identifiers, never registry indices or credentials.
const PREFIX = 'aeon-scopes:v1:'
const MAX_SCOPES = 256
const MAX_BYTES = 32768
const ID = /^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$/
const invalid = () => new Error('Invalid scope code; copy the complete aeon-scopes:v1 code.')

function crc32(text: string): string {
  let crc = 0xffffffff
  for (let i = 0; i < text.length; i++) {
    crc ^= text.charCodeAt(i)
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0)
  }
  return ((crc ^ 0xffffffff) >>> 0).toString(16).padStart(8, '0')
}

export function encodeScopeCode(scopes: readonly string[]): string {
  if (scopes.length > MAX_SCOPES) throw invalid()
  const keys = scopes.map(key => key.trim().replace(/:/g, '.'))
  if (keys.some(key => key.length > 128 || !ID.test(key))) throw invalid()
  const groups: string[] = []
  let last = ''
  for (const key of [...new Set(keys)].sort()) {
    const [resource, action] = key.split('.')
    if (resource === last) groups[groups.length - 1] += `+${action}`
    else { groups.push(key); last = resource! }
  }
  const text = PREFIX + groups.join(',')
  const code = `${text}:${crc32(text)}`
  if (code.length > MAX_BYTES) throw invalid()
  return code
}

export function decodeScopeCode(input: string): string[] {
  if (input.length > MAX_BYTES) throw invalid()
  const code = input.trim()
  if (!code.startsWith(PREFIX)) throw invalid()
  const parts = code.slice(PREFIX.length).split(':')
  if (parts.length !== 2 || parts[1] !== crc32(PREFIX + parts[0])) throw invalid()
  const scopes: string[] = []
  if (parts[0]) for (const group of parts[0].split(',')) {
    const [resource, actions, ...extra] = group.split('.')
    if (!actions || extra.length) throw invalid()
    for (const action of actions.split('+')) {
      scopes.push(`${resource}.${action}`)
      if (scopes.length > MAX_SCOPES) throw invalid()
    }
  }
  if (encodeScopeCode(scopes) !== code) throw invalid()
  return scopes
}

// A proposal replaces selection exactly, subject to the existing live ceilings.
// A denied identifier stays visible in the explanation, never silently ticked.
export function scopeCodeSelection(code: string, unavailable: (key: string) => string | undefined): { selected: Set<string>; skipped: { key: string; reason: string }[] } {
  const selected = new Set<string>()
  const skipped: { key: string; reason: string }[] = []
  for (const key of decodeScopeCode(code)) {
    const reason = unavailable(key)
    if (reason) skipped.push({ key, reason })
    else selected.add(key)
  }
  return { selected, skipped }
}
