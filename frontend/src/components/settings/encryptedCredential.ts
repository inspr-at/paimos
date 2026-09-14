// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

export interface EncryptedCredentialDownload {
  filename: string
  ciphertext: Uint8Array
}

export function ownObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function hasExactKeys(value: Record<string, unknown>, expected: string[]): boolean {
  const actual = Object.keys(value).sort()
  const sortedExpected = expected.slice().sort()
  return actual.length === sortedExpected.length && sortedExpected.every((key, index) => key === actual[index])
}

export function decodeAgeCiphertext(value: unknown): Uint8Array | null {
  if (
    typeof value !== 'string' ||
    value.length === 0 ||
    value.length > 1024 * 1024 ||
    value.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(value)
  ) return null
  try {
    const raw = window.atob(value)
    if (window.btoa(raw) !== value) return null
    const bytes = Uint8Array.from(raw, character => character.charCodeAt(0))
    const header = 'age-encryption.org/v1\n'
    if (
      bytes.length <= header.length ||
      ![...header].every((character, index) => bytes[index] === character.charCodeAt(0))
    ) return null
    return bytes
  } catch {
    return null
  }
}

export function downloadEncryptedCredential(ready: EncryptedCredentialDownload) {
  const credentialBytes = new Uint8Array(ready.ciphertext.length)
  credentialBytes.set(ready.ciphertext)
  const blob = new Blob([credentialBytes.buffer], { type: 'application/octet-stream' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = ready.filename
  link.hidden = true
  document.body.appendChild(link)
  try {
    link.click()
  } finally {
    window.setTimeout(() => {
      link.remove()
      URL.revokeObjectURL(url)
    }, 1000)
  }
}
