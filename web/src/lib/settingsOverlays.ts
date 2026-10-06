// SPDX-License-Identifier: AGPL-3.0-only
// Shared popovers are mutually exclusive, including those in a docked pane.
let current: (() => void) | undefined
export function claimSettingsPopover(close: () => void): () => void {
  current?.()
  current = close
  return () => { if (current === close) current = undefined }
}
export function isSettingsField(target: EventTarget | null): target is HTMLElement {
  return target instanceof HTMLElement && !!target.closest('input, textarea, select, [contenteditable="true"], [contenteditable=""]')
}
export function settingsSubmitKey(event: KeyboardEvent): boolean {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  return event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey && !event.isComposing
}
