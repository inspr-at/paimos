// SPDX-License-Identifier: AGPL-3.0-only
import { shallowRef, ref } from 'vue'
import { api } from './api.ts'
import { createScope } from './identityScope.ts'

export class PolicyFailure extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) { super(message); this.status = status; this.code = code }
}
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
const writing = new Map<string, Promise<void>>()
async function serializedWrite<T>(key: string, signal: AbortSignal, work: () => Promise<T>) {
  const previous = writing.get(key) ?? Promise.resolve()
  let release!: () => void
  const current = new Promise<void>(resolve => { release = resolve })
  const tail = previous.then(() => current)
  writing.set(key, tail)
  try {
    await previous
    if (signal.aborted) throw new Error('Editor context changed before submission')
    return await work()
  } finally { release(); if (writing.get(key) === tail) writing.delete(key) }
}
export async function policyRequest(path: string, init: RequestInit = {}) {
  const response = await api(path, init, 30_000)
  if (!response.ok) {
    const body: { code?: string; error?: string } = await policyJSON<{ code?: string; error?: string }>(response, 64 * 1024).catch(() => ({}))
    throw new PolicyFailure(response.status, body.code ?? '', body.error ?? `Request refused (${response.status}).`)
  }
  return response
}
export function policyError(error: unknown, undo = false) {
  if (!(error instanceof PolicyFailure)) return 'Could not confirm the save. Reload to see the current value.'
  if (error.code === 'preference_person_changed') return 'Your linked person changed. The old draft and Undo were discarded; make a new choice.'
  if (error.code === 'unknown_kind') return undo ? 'This work kind is no longer available; the change could not be restored.' : 'This work kind is no longer available; the change was refused.'
  if (error.status === 403) return 'Permission was refused. The change was not saved.'
  if (error.status === 409) return undo ? 'A newer change prevents Undo. The current setting was kept.' : 'This setting changed elsewhere. Review the current value before saving again.'
  if (error.code === 'locked_above') return 'A broader setting locks this choice. See the source below.'
  return error.message
}
export interface EditorWire<T, D> {
  read: (signal: AbortSignal) => Promise<T>
  write: (snapshot: T, draft: D, undo: boolean, signal: AbortSignal) => Promise<T>
  compensate: (before: T, draft: D) => D
  identity: (snapshot: T) => string
  writeKey?: (snapshot: T) => string
  saved?: (result: T, undo: boolean, wanted: D) => string
}
/** One open editor, one write at a time. Context includes tenant/principal and
 * role or level/project; reset on navigation invalidates even aborted responses.
 * A compensating write always uses its confirmation, never a refreshed revision. */
export function createPolicyEditor<T, D>(owner: () => string, wire: EditorWire<T, D>) {
  const scope = createScope(owner), reads = scope.lane()
  const snapshot = shallowRef<T | null>(null), draft = shallowRef<D | null>(null)
  const busy = ref(false), needsReload = ref(false), loading = ref(false)
  const message = ref(''), phase = ref<'idle' | 'saving' | 'saved' | 'refused' | 'unknown'>('idle')
  const undo = shallowRef<{ before: T; confirmed: T; desired: D; owner: string } | null>(null)
  let baseline: T | null = null
  function reset() {
    scope.reset(); snapshot.value = null; draft.value = null; undo.value = null; baseline = null
    busy.value = false; loading.value = false; needsReload.value = false; message.value = ''; phase.value = 'idle'
  }
  async function load() {
    if (busy.value) return
    loading.value = true
    await reads.run(({ after, signal }) => after(wire.read(signal), result => {
      if (snapshot.value && wire.identity(snapshot.value) !== wire.identity(result)) { draft.value = null; baseline = null; undo.value = null }
      snapshot.value = result; needsReload.value = false
    }), { failed: error => { if (error instanceof PolicyFailure && [401,403].includes(error.status)) { snapshot.value = null; undo.value = null }; needsReload.value = true; message.value = error instanceof PolicyFailure ? policyError(error) : 'Could not load the current setting. Reload before editing.' }, settled: () => { loading.value = false } })
  }
  function edit(value: D) {
    if (busy.value || loading.value || needsReload.value || !snapshot.value) return
    baseline = snapshot.value; draft.value = structuredClone(value); undo.value = null; phase.value = 'idle'; message.value = ''
  }
  function cancel() { if (!busy.value) { draft.value = null; baseline = null } }
  async function submit(restoring = false) {
    if (busy.value || loading.value || needsReload.value) return
    const history = undo.value
    if (restoring && (!history || history.owner !== owner())) return
    const before = restoring ? history!.confirmed : baseline
    const desired = restoring ? wire.compensate(history!.before, history!.desired) : draft.value
    if (!before || !desired || !owner()) return
    const prior = restoring ? history!.before : before
    reads.cancel(); busy.value = true; phase.value = 'saving'; message.value = 'Saving…'
    // Provisional display is distinct from the last confirmed server snapshot.
    draft.value = structuredClone(desired); undo.value = null
    const capturedOwner = owner()
    await scope.run(async ({ after, signal }) => {
      await after(serializedWrite(wire.writeKey?.(before) ?? capturedOwner, signal, () => wire.write(before, desired, restoring, signal)), result => {
        if (wire.identity(result) !== wire.identity(before)) throw new Error('Invalid confirmation identity')
        snapshot.value = result; draft.value = null; baseline = null; phase.value = 'saved'
        undo.value = restoring ? null : { before: prior, confirmed: result, desired: structuredClone(desired), owner: capturedOwner }
        message.value = wire.saved?.(result, restoring, desired) ?? (restoring ? 'Setting restored.' : 'Saved.')
      })
      // Derived data reads cannot certify a lost write response and cannot
      // replace another generation or overwrite a new draft.
      await after(wire.read(signal), current => {
        if (wire.identity(current) !== wire.identity(before)) {
          draft.value = null; undo.value = null; baseline = null; phase.value = 'refused'
          message.value = 'Your linked person changed. Make a new choice.'
        }
        snapshot.value = current
      }).catch(error => after(Promise.resolve(error), () => { if (error instanceof PolicyFailure && [401,403].includes(error.status)) { snapshot.value = null; undo.value = null }; needsReload.value = true; message.value += ' Current preview could not be refreshed; reload before editing.' }))
    }, {
      failed: error => {
        const identityChanged = error instanceof PolicyFailure && error.code === 'preference_person_changed'
        phase.value = error instanceof PolicyFailure ? 'refused' : 'unknown'; message.value = policyError(error, restoring)
        needsReload.value = true; undo.value = null
        if (identityChanged || restoring) { draft.value = null; baseline = null }
        // Reconciliation preserves refusal/unknown feedback and retained drafts;
        // only a new user action may capture a newer revision.
        void reads.run(({ after, signal }) => after(wire.read(signal), current => {
          if (wire.identity(current) !== wire.identity(before)) { draft.value = null; baseline = null }
          snapshot.value = current; needsReload.value = false
        }), { failed: error => { if (error instanceof PolicyFailure && [401,403].includes(error.status)) snapshot.value = null; message.value += ' Current value could not be loaded; reload before editing.' } })
      }, settled: () => { busy.value = false },
    })
  }
  return { snapshot, draft, busy, loading, needsReload, message, phase, undo, reset, load, edit, cancel, submit, dispose: scope.dispose }
}
