// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import * as API from '../src/lib/api'
import * as Receipts from '../src/lib/crm'
import * as Toast from '../src/lib/toast'
import type { Attachment } from '../src/lib/attachments'
import type { TimeEntry } from '../src/lib/business'
import { deferred, flush, sourceModule } from './record-source'

const Attachments = sourceModule<typeof import('../src/lib/attachments')>('lib/attachments.ts', { './api.ts': API, './crm.ts': Receipts })
const Business = sourceModule<typeof import('../src/lib/business')>('lib/business.ts', {
  './api': API, './avatar': { learnPictures() {} }, './crm': Receipts,
  '../components/business/money': { parseJSONExact: JSON.parse },
})
const attachment = (node = 'A'): Attachment => ({ id: 'image', node_id: node, name: 'image.png', content_type: 'image/png', size: 4, sha256: 'hash', width: 1, height: 1, caption: '', position: '1', created_by: 'person', created_at: '2026-10-01T00:00:00Z' })
const scopes: ReturnType<typeof effectScope>[] = []
function setup(uploadAttachment = vi.fn()) {
  const { useAttachments } = sourceModule<typeof import('../src/lib/useAttachments')>('lib/useAttachments.ts', {
    './api': API, './attachments': { ...Attachments, uploadAttachment }, './toast': Toast,
  })
  const scope = effectScope(), node = ref<string | null>('A')
  scopes.push(scope)
  const model = scope.run(() => useAttachments(node))!
  return { model, node, stop: () => scope.stop() }
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); Toast.resetToasts(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

it('AEON-1416: attachment Undo keeps its own receipt across two deletion cycles', async () => {
  let listed = [attachment()], deletion = 0
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
    const path = String(input); calls.push(`${init?.method ?? 'GET'} ${path}`)
    if (init?.method === 'DELETE') {
      listed = []; deletion++
      return new Response(null, { status: 204, headers: { 'X-Aeon-Event-Ids': deletion === 1 ? '10' : '12' } })
    }
    if (path.endsWith('/events/10/undo')) return new Response('{"error":"already undone"}', { status: 409 })
    if (path.endsWith('/events/12/undo')) { listed = [attachment()]; return Response.json({ after: listed[0] }) }
    // An external restore followed by another deletion must not change the
    // receipt retained by the first toast. This also exposes the old scan.
    if (path.includes('/events?')) return Response.json({ items: [10, 12].map(id => ({ id, type: 'attachment.removed', after: attachment(), undo_of: null })), next_after: null })
    return Response.json({ items: listed })
  }))
  const { model } = setup()
  await model.load()
  model.remove(model.items.value[0]); await flush()
  const firstUndo = Toast.toasts.at(-1)!.actions[0]
  listed = [attachment()]; await model.load() // restored by someone else
  model.remove(model.items.value[0]); await flush()
  const secondUndo = Toast.toasts.at(-1)!.actions[0]
  firstUndo.run(); await flush()
  expect(calls.filter(call => call.includes('/undo'))).toEqual(['POST /api/events/10/undo'])
  expect(model.items.value).toEqual([])
  secondUndo.run(); secondUndo.run(); await flush()
  expect(calls.filter(call => call.includes('/undo'))).toEqual(['POST /api/events/10/undo', 'POST /api/events/12/undo'])
  expect(calls.filter(call => call.includes('/events?'))).toEqual([])
  expect(model.items.value.map(item => item.id)).toEqual(['image'])
})

it('AEON-1416: deletion receipts keep exact IDs and refuse missing or ambiguous receipts', async () => {
  let header: string | null = '9007199254740993'
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
    calls.push(String(input))
    if (init?.method === 'DELETE') return new Response(null, { status: 204, headers: header === null ? {} : { 'X-Aeon-Event-Ids': header } })
    return Response.json({ items: [] })
  }))
  const entry = { id: 'entry', updated_at: '2026-10-01T00:00:00Z' } as TimeEntry
  expect(await Attachments.deleteAttachment('image')).toBe(header)
  expect(await Business.deleteEntry(entry)).toBe(header)
  const { model } = setup()
  for (header of [null, '', '10,12', '0', 'not-an-id']) {
    expect(await Business.deleteEntry(entry)).toBeNull()
    model.items.value = [attachment()]
    model.remove(model.items.value[0])
    Toast.toasts.at(-1)!.actions[0].run() // Undo before DELETE has answered
    await flush()
    expect(model.items.value).toEqual([])
    expect(Toast.toasts.at(-1)!.message).toBe('image.png could not be restored.')
  }
  expect(calls.some(path => path.includes('/events'))).toBe(false)
})

it('AEON-1416: attachment Undo restores immediately through reloads and rolls back failures', async () => {
  const logged = vi.spyOn(console, 'error').mockImplementation(() => {})
  for (const outcome of ['success', 'failure', 'unconfirmed', 'unknown']) {
    const before = { ...attachment(), id: 'before', position: '0' }
    const after = { ...attachment(), id: 'after', position: '2' }
    let listed = [before, attachment(), after]
    const deletion = deferred<Response>(), undo = deferred<Response>()
    const requests: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input); requests.push(path)
      if (init?.method === 'DELETE') {
        const response = await deletion.promise
        listed = [before, after]
        return response
      }
      if (path.endsWith('/undo')) return undo.promise
      return Response.json({ items: listed })
    }))
    const { model, stop } = setup()
    await model.load()
    model.remove(model.items.value[1])
    const action = Toast.toasts.at(-1)!.actions[0]
    action.run(); action.run()
    expect(model.items.value.map(item => item.id)).toEqual(['before', 'image', 'after'])
    expect(requests.some(path => path.endsWith('/undo'))).toBe(false)
    await model.load() // DELETE is pending and the server still lists the row.
    expect(model.items.value.map(item => item.id)).toEqual(['before', 'image', 'after'])
    deletion.resolve(new Response(null, { status: 204, headers: { 'X-Aeon-Event-Ids': '10' } }))
    await flush()
    expect(requests.filter(path => path.endsWith('/undo'))).toEqual(['/api/events/10/undo'])
    await model.load() // DELETE finished; /undo is pending and the row is absent on the server.
    expect(model.items.value.map(item => item.id)).toEqual(['before', 'image', 'after'])
    if (outcome === 'success') undo.resolve(Response.json({ after: { ...attachment(), caption: 'server restored' } }))
    else if (outcome === 'unconfirmed') undo.resolve(Response.json({ after: { ...attachment('B') } }))
    else if (outcome === 'unknown') undo.reject('internal diagnostic')
    else undo.resolve(Response.json({ error: 'internal receipt diagnostic' }, { status: 500 }))
    await flush()
    if (outcome === 'success') expect(model.items.value[1].caption).toBe('server restored')
    else {
      expect(model.items.value.map(item => item.id)).toEqual(['before', 'after'])
      expect(Toast.toasts.at(-1)!.message).toBe('image.png could not be restored.')
      expect(Toast.toasts.at(-1)!.tone).toBe('error')
      expect(logged).toHaveBeenCalled()
    }
    stop(); Toast.resetToasts()
  }
})

it('AEON-1416: upload cleanup revokes each preview once and rejects late completions', async () => {
  const created: string[] = [], revoked: string[] = []
  vi.spyOn(URL, 'createObjectURL').mockImplementation(() => { const url = `blob:preview-${created.length}`; created.push(url); return url })
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(url => { revoked.push(url) })
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ items: [] })))
  const transfers: (ReturnType<typeof deferred<Attachment>> & { signal: AbortSignal })[] = []
  const upload = vi.fn((_id: string, _file: File, options: { signal: AbortSignal }) => {
    const transfer = { ...deferred<Attachment>(), signal: options.signal }; transfers.push(transfer); return transfer.promise
  })
  const { model, node, stop } = setup(upload)
  await model.load()
  const file = new File(['data'], 'image.png', { type: 'image/png' })
  const success = model.upload(file)
  transfers[0].resolve(attachment()); await success
  expect(revoked).toEqual([created[0]])

  const cancelled = model.upload(file)
  model.cancel(model.uploads.value[0].key)
  expect(transfers[1].signal.aborted).toBe(true)
  transfers[1].resolve({ ...attachment(), id: 'cancelled' })
  expect(await cancelled).toBeNull()

  const failed = model.upload(file)
  transfers[2].reject(new Error('retry me')); await failed
  model.retry(model.uploads.value[0].key)
  expect(revoked).toEqual(created.slice(0, 3))
  node.value = 'B'
  expect(transfers[3].signal.aborted).toBe(true)
  await model.load()
  node.value = 'A'; await model.load() // even navigating back cannot revive it
  transfers[3].resolve({ ...attachment(), id: 'from-old-visit' }); await flush()
  expect(model.items.value).toEqual([])
  expect(model.uploads.value).toEqual([])

  const abandoned = model.upload(file)
  stop()
  expect(transfers[4].signal.aborted).toBe(true)
  transfers[4].resolve({ ...attachment(), id: 'after-unmount' })
  expect(await abandoned).toBeNull()
  expect(model.items.value).toEqual([])
  expect(revoked).toEqual(created)
  expect(new Set(revoked).size).toBe(created.length)
})
