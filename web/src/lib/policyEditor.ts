// SPDX-License-Identifier: AGPL-3.0-only

export async function policyJSON<T = unknown>(response: Response, maxBytes = 2 * 1024 * 1024): Promise<T> {
  const reader = response.body?.getReader()
  if (!reader) throw new Error('Missing response body')
  const chunks: Uint8Array[] = []; let size = 0
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      size += next.value.byteLength
      if (size > maxBytes) throw new Error('Response exceeds the editor limit')
      chunks.push(next.value)
    }
  } finally { await reader.cancel().catch(() => {}) }
  const bytes = new Uint8Array(size); let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  return JSON.parse(new TextDecoder().decode(bytes)) as T
}
