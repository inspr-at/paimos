// SPDX-License-Identifier: AGPL-3.0-only
import { reactive } from 'vue'

export interface ToastAction { label: string; run: () => void }
// A release named inside a toast's sentence: `message` is the whole sentence in
// plain text, `release` says where the release stands in it so the toast can draw
// the name with its version on hover or focus (AEON-430).
export interface ToastRelease { version: string; name?: string; before: string; after?: string }
export interface Toast { id: number; message: string; tone: 'info' | 'error'; actions: ToastAction[]; sticky: boolean; key?: string; release?: ToastRelease }
export const toasts = reactive<Toast[]>([])
let next = 1
let ownerEpoch = 0
export function resetToasts() { ownerEpoch++; toasts.splice(0) }
export function captureToastOwner() { const started = ownerEpoch; return () => started === ownerEpoch }

// Short, dismissible confirmations. Errors stay a little longer than info. A sticky
// toast stays until it is acted on or dismissed; a keyed one replaces its earlier self.
export function toast(message: string, options: { tone?: Toast['tone']; action?: ToastAction; actions?: ToastAction[]; timeout?: number; sticky?: boolean; key?: string; release?: ToastRelease } = {}) {
  if (options.key) { const earlier = toasts.find(item => item.key === options.key); if (earlier) dismiss(earlier.id) }
  const current = captureToastOwner()
  const item: Toast = {
    id: next++, message, tone: options.tone ?? 'info', sticky: options.sticky ?? false, key: options.key, release: options.release,
    actions: [...(options.action ? [options.action] : []), ...(options.actions ?? [])].map(action => ({ ...action, run: () => { if (current()) action.run() } })),
  }
  toasts.push(item)
  // Sticky toasts are kept when the stack is full; the oldest passing one goes.
  while (toasts.length > 3) { const drop = toasts.findIndex(t => !t.sticky); toasts.splice(drop === -1 ? 0 : drop, 1) }
  if (!item.sticky) setTimeout(() => dismiss(item.id), options.timeout ?? (item.tone === 'error' ? 7000 : 4500))
  return item.id
}
export function dismiss(id: number) {
  const index = toasts.findIndex(item => item.id === id)
  if (index !== -1) toasts.splice(index, 1)
}
