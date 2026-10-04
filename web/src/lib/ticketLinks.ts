// SPDX-License-Identifier: AGPL-3.0-only
// Ticket keys named in text (release notes) resolved to tickets of this
// tenant: one lookup per batch of up to 100 unknown keys. A key the server does
// not answer (unknown, or in a project the caller cannot see) stays unresolved,
// so it renders as plain text rather than a dead link.
//
// What the caller may see can change, so the answers follow the permissions:
// another person, workspace or an ended session drops them at once; when the
// same person's access is asked again (focus, navigation) the keys on screen
// are asked again and the new answers replace the old ones together (keys no
// longer answered turn plain), and with nothing on screen they are dropped.
import { reactive } from 'vue'
import { lookupNodeKeys } from './api.ts'
import { onAccessChange } from './authz.ts'

export interface TicketRef { key: string; id: string; title: string; state: string; projectId: string }

const BATCH = 100
// Keep this many off-screen answers; mounted links must retain their answers.
const KEEP = 200
// upper-cased key asked for → the ticket, or null when this caller has none.
const known = reactive(new Map<string, TicketRef | null>())
const pending = new Map<string, Promise<void>>()
// Multiple links may show the same key, including across different views.
const shown = new Map<string, number>()
// Answers asked for before an access change never land.
let epoch = 0

export const normalKey = (key: string) => key.trim().toUpperCase()

export function ticketRef(key: string): TicketRef | null | undefined {
  return known.get(normalKey(key))
}

async function answer(keys: string[]): Promise<Map<string, TicketRef | null>> {
  const found = new Map<string, TicketRef | null>(keys.map(key => [key, null]))
  for (let at = 0; at < keys.length; at += BATCH) {
    const page = await lookupNodeKeys(keys.slice(at, at + BATCH))
    for (const item of page.items) {
      if (item.requested_key && item.project_id && found.has(item.requested_key)) {
        found.set(item.requested_key, { key: item.key, id: item.id, title: item.title, state: item.state, projectId: item.project_id })
      }
    }
  }
  return found
}

function remember(answers: Map<string, TicketRef | null>) {
  for (const [key, value] of answers) { known.delete(key); known.set(key, value) }
  trimOffscreenAnswers()
}

function trimOffscreenAnswers() {
  let offscreen = 0
  for (const key of known.keys()) if (!shown.has(key)) offscreen++
  for (const key of known.keys()) {
    if (offscreen <= KEEP) break
    if (!shown.has(key)) { known.delete(key); offscreen-- }
  }
}

export async function resolveTicketKeys(keys: string[]): Promise<void> {
  const wanted = [...new Set(keys.map(normalKey))].filter(key => key && !known.has(key))
  const waits = wanted.filter(key => pending.has(key)).map(key => pending.get(key)!)
  const asking = wanted.filter(key => !pending.has(key))
  const started = epoch
  for (let at = 0; at < asking.length; at += BATCH) {
    const batch = asking.slice(at, at + BATCH)
    const request = answer(batch).then(answers => { if (started === epoch) remember(answers) }, () => { /* unresolved keys stay plain; a later call asks again */ })
      .finally(() => { for (const key of batch) if (pending.get(key) === request) pending.delete(key) })
    for (const key of batch) pending.set(key, request)
    waits.push(request)
  }
  await Promise.all(waits)
}

// Keys asked for while rendering (each link asks for its own) go out together
// on the next tick: a release's keys cost one request, not one per key.
let queued: string[] = []
export function wantTicketKey(key: string) {
  const wanted = normalKey(key)
  if (!wanted || known.has(wanted) || pending.has(wanted)) return
  if (!queued.length) queueMicrotask(() => { const keys = queued; queued = []; void resolveTicketKeys(keys) })
  queued.push(wanted)
}

// Pin each mounted key until its last link leaves or changes to another key.
export function showingTicketKeys(key: string): () => void {
  const wanted = normalKey(key)
  if (!wanted) return () => {}
  shown.set(wanted, (shown.get(wanted) ?? 0) + 1)
  let released = false
  return () => {
    if (released) return
    released = true
    const remaining = shown.get(wanted)! - 1
    if (remaining) shown.set(wanted, remaining)
    else { shown.delete(wanted); trimOffscreenAnswers() }
  }
}

export function forgetTicketKeys() { epoch++; known.clear(); pending.clear(); queued = [] }

async function reaskTicketKeys() {
  const keys = [...known.keys()]
  epoch++
  pending.clear()
  const started = epoch
  if (!keys.length) return
  try {
    const answers = await answer(keys)
    if (started !== epoch) return
    // Keys answered meanwhile were asked under the new access already.
    for (const key of known.keys()) if (!answers.has(key)) answers.set(key, known.get(key)!)
    known.clear()
    remember(answers)
  } catch {
    // Without a fresh answer nothing stays: the links turn plain and ask again.
    if (started === epoch) forgetTicketKeys()
  }
}

onAccessChange(change => {
  if (change === 'reset' || !shown.size) forgetTicketKeys()
  else void reaskTicketKeys()
})
