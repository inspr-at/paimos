// SPDX-License-Identifier: AGPL-3.0-only

// The cookie is HttpOnly. Share only a random change marker, never a cookie,
// identity or attach code. Publish BEFORE an authentication operation can change
// that cookie, so other tabs drop their old identity and outstanding answers.
export const AUTH_GENERATION_KEY = 'aeon.auth.generation'
export const OIDC_PENDING_KEY = 'aeon.auth.pending'

export function followAuthentication(changed: () => void) {
  const browser = typeof window === 'undefined' ? null : window
  let storageAvailable = !!browser
  const read = (): string | null => {
    if (!storageAvailable) return null
    try { return browser!.localStorage.getItem(AUTH_GENERATION_KEY) ?? '' } catch { storageAvailable = false; return null }
  }
  let generation = read() ?? ''
  let channel: BroadcastChannel | null = null
  try { if (browser && typeof BroadcastChannel !== 'undefined') channel = new BroadcastChannel(AUTH_GENERATION_KEY) } catch { /* storage events still work */ }
  function receive(value: unknown, stored = true) {
    if (typeof value !== 'string') return
    // Reading the latest marker also prevents a delayed event from rolling back
    // a more recent local sign-in. The channel is the fallback without storage.
    if (!stored) storageAvailable = false
    const latest = read() ?? value
    if (latest === generation) return
    generation = latest
    changed()
  }
  const storage = (event: StorageEvent) => {
    if (event.key === AUTH_GENERATION_KEY || event.key === null) receive(event.newValue ?? '')
  }
  browser?.addEventListener('storage', storage)
  if (channel) channel.onmessage = event => receive(event.data?.generation, event.data?.stored !== false)
  return {
    // An attach callback can resume before the queued storage event. Reading
    // here closes that gap synchronously, just like the scope's owner check.
    current: () => { const latest = read(); return latest === null || latest === generation },
    owns: (value: string | null) => !!value && value === generation,
    publish() {
      generation = crypto.randomUUID()
      try { browser?.localStorage.setItem(AUTH_GENERATION_KEY, generation) } catch { storageAvailable = false }
      try { channel?.postMessage({ generation, stored: storageAvailable }) } catch { /* storage events still work */ }
      return generation
    },
    stop() {
      browser?.removeEventListener('storage', storage)
      channel?.close()
    },
  }
}
