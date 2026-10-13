// SPDX-License-Identifier: AGPL-3.0-only
import { nativeSessionBounds as bounds, type NativeStreamEvent, type NativeTerminalFrame } from '../nativeSessionContract'

const maxUint64 = '18446744073709551615'
export const sequence = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d{0,19})$/.test(value) &&
  (value.length < 20 || value <= maxUint64)
export const gridSize = (cols: unknown, rows: unknown) => Number.isInteger(cols) && Number.isInteger(rows) &&
  Number(cols) >= 40 && Number(cols) <= 320 && Number(rows) >= 12 && Number(rows) <= 100

export function validEnvelope(event: NativeStreamEvent): boolean {
  if (!event || typeof event !== 'object' || !event.binding || typeof event.binding !== 'object' ||
      !sequence(event.seq) || !sequence(event.sequence) || !sequence(event.dropped_frames) ||
      !Number.isSafeInteger(event.generation) || event.generation < 1 || !event.payload || typeof event.payload !== 'object') return false
  const binding = event.binding
  for (const key of ['tenant_id', 'project_id', 'owned_session_id', 'harness_session_id', 'runtime_epoch', 'daemon_id', 'daemon_generation'] as const) {
    if (typeof binding[key] !== 'string' || !binding[key] || binding[key].length > 128) return false
  }
  return Number.isSafeInteger(binding.binding_revision) && binding.binding_revision >= 1 &&
    (binding.run_id === null || typeof binding.run_id === 'string' && binding.run_id.length <= 128)
}

/** Check encoded/decoded byte counts before allocation, then canonical base64.
 * The C0 exported-envelope ceiling is tighter than its local PTY chunk limit. */
export function terminalBytes(event: NativeStreamEvent): { frame: NativeTerminalFrame; bytes: Uint8Array } | null {
  if (event.kind !== 'terminal' || !event.payload || typeof event.payload !== 'object') return null
  const p = event.payload
  if (typeof p.terminal_epoch !== 'string' || !p.terminal_epoch || p.terminal_epoch.length > 128 ||
      !sequence(p.sequence) || !gridSize(p.cols, p.rows) || typeof p.reset !== 'boolean' || typeof p.data_base64 !== 'string' ||
      p.data_base64.length > bounds.exported_frame_bytes - 2048 || p.data_base64.length % 4 !== 0 ||
      !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(p.data_base64)) return null
  const size = p.data_base64.length / 4 * 3 - (p.data_base64.endsWith('==') ? 2 : p.data_base64.endsWith('=') ? 1 : 0)
  if (size > bounds.pty_chunk_bytes) return null
  try {
    const raw = atob(p.data_base64)
    if (raw.length !== size || btoa(raw) !== p.data_base64) return null
    return { frame: { ...p }, bytes: Uint8Array.from(raw, char => char.charCodeAt(0)) }
  } catch { return null }
}
