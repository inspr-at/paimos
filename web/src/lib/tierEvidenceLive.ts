// SPDX-License-Identifier: AGPL-3.0-only
export interface AgentEventIdentity { session_id?: string; run_id?: string }
export interface EvidenceSession { id: string; run_id?: string | null }
// Live data is only a wake hint; never retain the event's resource snapshot.
export function agentEventIdentity(data: unknown): AgentEventIdentity | undefined {
  if (typeof data !== 'string' || data.length > 65_536) return
  try {
    const { before, after } = JSON.parse(data)
    const session = after?.session_id ?? before?.session_id ?? (after?.harness ? after.id : before?.harness ? before.id : undefined)
    const run = after?.run_id ?? after?.run?.id
    const identity: AgentEventIdentity = {}
    if (typeof session === 'string') identity.session_id = session
    if (typeof run === 'string') identity.run_id = run
    return Object.keys(identity).length ? identity : undefined
  } catch { return }
}
const EVIDENCE_EVENTS = new Set(['run.telemetry', 'harness.usage_reported'])
const TIER_EVENTS = new Set(['harness.control_completed', 'harness.tier_changed', 'harness.tier_cancelled', 'harness.tier_requested', 'harness.tier_declined', 'harness.tier_reported'])
// Retain one trailing wake for the selected record, with a five-second floor
// between telemetry/usage reads. Decisions bypass that floor for confirmation.
export function tierEvidenceRefresh<S extends EvidenceSession>(selected: () => S | undefined, load: (session: S) => void, visible: () => boolean, interval = 5000) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let pending: S | undefined
  let lastID = '', lastRun: string | null | undefined, lastAt = -Infinity
  function flush() {
    timer = undefined
    const s = pending; pending = undefined
    if (!s || !visible() || selected()?.id !== s.id || selected()?.run_id !== s.run_id) return
    lastID = s.id; lastRun = s.run_id; lastAt = Date.now()
    load(s)
  }
  function notify(event?: string, identity?: AgentEventIdentity) {
    const s = selected()
    if (!s || !visible()) return
    const evidence = !!event && EVIDENCE_EVENTS.has(event)
    if (event && !evidence && !TIER_EVENTS.has(event)) return
    if (event && !(identity?.session_id === s.id || (identity?.run_id && identity.run_id === s.run_id))) return
    pending = s
    const delay = evidence && lastID === s.id && lastRun === s.run_id ? Math.max(0, lastAt + interval - Date.now()) : 0
    // A decision replaces a throttled telemetry wake with an immediate read.
    if (!delay) { clearTimeout(timer); flush() }
    else if (!timer) timer = setTimeout(flush, delay)
  }
  function stop() { clearTimeout(timer); timer = undefined; pending = undefined }
  return { notify, stop }
}
