// SPDX-License-Identifier: AGPL-3.0-only
// Timers can resume after display sleep without an EventSource error or a
// visibility transition. Check the wall clock as well as browser wake signals.
export const STREAM_CHECK_MS = 5_000
export const STREAM_SILENCE_MS = 45_000

export function watchStreamHealth(recover: () => void) {
  let checkedAt = Date.now()
  let heardAt = checkedAt
  const heard = () => { heardAt = Date.now() }
  const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  const online = () => typeof navigator === 'undefined' || navigator.onLine !== false
  function wake() {
    if (!visible() || !online()) return
    checkedAt = Date.now()
    heard()
    recover()
  }
  const timer = setInterval(() => {
    const at = Date.now()
    const jumped = at - checkedAt > 2 * STREAM_CHECK_MS || at < checkedAt
    checkedAt = at
    if (visible() && online() && (jumped || at - heardAt >= STREAM_SILENCE_MS)) wake()
  }, STREAM_CHECK_MS)
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', wake)
  if (typeof window !== 'undefined') {
    window.addEventListener('pageshow', wake)
    window.addEventListener('online', wake)
  }
  return {
    heard,
    stop() {
      clearInterval(timer)
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', wake)
      if (typeof window !== 'undefined') {
        window.removeEventListener('pageshow', wake)
        window.removeEventListener('online', wake)
      }
    },
  }
}
