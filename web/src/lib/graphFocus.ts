// SPDX-License-Identifier: AGPL-3.0-only
// In-app focus is already visible before requesting native fullscreen. Refusal,
// missing APIs and silent browser no-ops leave that layout available.
export async function requestGraphFullscreen(element: HTMLElement, stillFocused: () => boolean): Promise<boolean> {
  const document = element.ownerDocument
  if (!stillFocused() || document.fullscreenEnabled === false || !element.requestFullscreen || document.fullscreenElement) return false
  try {
    await element.requestFullscreen()
    // Navigation, Escape or unmount can win while the browser is deciding.
    if (!stillFocused()) { await exitGraphFullscreen(element); return false }
    return document.fullscreenElement === element
  } catch { return false }
}

export async function exitGraphFullscreen(element: HTMLElement): Promise<void> {
  const document = element.ownerDocument
  // Never close fullscreen belonging to a different workspace.
  if (document.fullscreenElement !== element || !document.exitFullscreen) return
  try { await document.exitFullscreen() } catch { /* The in-app exit remains usable. */ }
}
