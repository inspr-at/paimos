// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { APIError } from './api'
import { byPosition, deleteAttachment, listAttachments, patchAttachment, positionBetween, positionOf, undoEvent, uploadAttachment, type Attachment } from './attachments'
import { captureToastOwner, dismiss, toast } from './toast'

// One ticket's attachments. Uploads show at once with their progress (images with
// a local preview); a delete reaches the server at once and its toast offers Undo
// for six seconds, which undoes the removal event on the server. Reorder and
// captions use the attachment's precondition.
export interface Upload { key: string; name: string; size: number; type: string; progress: number; preview: string | null; error: string; file: File; controller: AbortController }
const UNDO_MS = 6000

export function useAttachments(nodeId: Ref<string | null>) {
  const items = ref<Attachment[]>([])
  const uploads = ref<Upload[]>([])
  const loading = ref(false)
  const error = ref('')
  // Deletes still on their way, so an early Undo waits for them.
  const deleting = new Map<string, Promise<{ eventId: string | null } | null>>()
  const restoring = new Map<string, { item: Attachment; here: () => boolean }>()
  let generation = 0
  let recordEpoch = 0
  let active = true
  function captureRecord() {
    const node = nodeId.value, epoch = recordEpoch, owner = captureToastOwner()
    return () => active && epoch === recordEpoch && nodeId.value === node && owner()
  }

  async function load() {
    const id = nodeId.value
    const current = ++generation
    const here = captureRecord()
    if (!id) { items.value = []; loading.value = false; error.value = ''; return }
    loading.value = true; error.value = ''
    try {
      const list = await listAttachments(id)
      if (current === generation && here()) {
        const pending = [...restoring.values()].filter(undo => undo.here()).map(undo => undo.item)
        const ids = new Set(pending.map(item => item.id))
        items.value = [...list.filter(item => !deleting.has(item.id) && !ids.has(item.id)), ...pending].sort(byPosition)
      }
    } catch (e) {
      if (current !== generation || !here()) return
      // A server without the attachments surface simply shows none.
      if (e instanceof APIError && (e.status === 404 || e.status === 405)) items.value = []
      else error.value = e instanceof Error ? e.message : 'Attachments could not be loaded'
    } finally { if (current === generation && here()) loading.value = false }
  }
  watch(nodeId, () => { recordEpoch++; clearUploads(); items.value = []; void load() }, { immediate: true, flush: 'sync' })

  // Resolves with the new attachment's id, or null when the upload failed.
  function upload(file: File): Promise<Attachment | null> {
    const id = nodeId.value
    if (!id || !active) return Promise.resolve(null)
    const here = captureRecord()
    const entry: Upload = {
      key: `u${Date.now()}-${Math.random().toString(36).slice(2, 7)}`, name: file.name || 'Screenshot.png', size: file.size, type: file.type, progress: 0,
      preview: file.type.startsWith('image/') ? URL.createObjectURL(file) : null, error: '', file, controller: new AbortController(),
    }
    uploads.value = [...uploads.value, entry]
    const pending = () => here() && uploads.value.some(u => u.key === entry.key)
    const update = (patch: Partial<Upload>) => { if (pending()) uploads.value = uploads.value.map(u => u.key === entry.key ? { ...u, ...patch } : u) }
    return uploadAttachment(id, file, { onProgress: progress => update({ progress }), signal: entry.controller.signal })
      .then(created => {
        const accepted = pending()
        if (accepted) items.value = [...items.value.filter(item => item.id !== created.id), created].sort(byPosition)
        finish(entry.key)
        return accepted ? created : null
      })
      .catch(e => {
        if (e instanceof DOMException && e.name === 'AbortError') { finish(entry.key); return null }
        update({ error: e instanceof Error ? e.message : 'Upload failed' })
        return null
      })
  }
  function finish(key: string) {
    const entry = uploads.value.find(u => u.key === key)
    if (entry?.preview) URL.revokeObjectURL(entry.preview)
    uploads.value = uploads.value.filter(u => u.key !== key)
  }
  // All exits take the same path. Removing the entry before a late completion
  // means its URL is revoked once and its result cannot refill another record.
  function clearUploads() { for (const entry of [...uploads.value]) cancel(entry.key) }
  function retry(key: string) {
    const entry = uploads.value.find(u => u.key === key)
    if (!entry) return
    finish(key)
    void upload(entry.file)
  }
  function cancel(key: string) { uploads.value.find(u => u.key === key)?.controller.abort(); finish(key) }
  async function add(files: File[]) { return Promise.all(files.map(upload)) }

  async function setCaption(item: Attachment, caption: string) {
    if (caption === item.caption) return true
    const before = item.caption
    items.value = items.value.map(x => x.id === item.id ? { ...x, caption } : x)
    try {
      const saved = await patchAttachment(item, { caption })
      items.value = items.value.map(x => x.id === item.id ? { ...x, ...saved } : x)
      return true
    } catch (e) {
      items.value = items.value.map(x => x.id === item.id ? { ...x, caption: before } : x)
      toast(e instanceof APIError && e.status === 412 ? `${item.name} was changed elsewhere; the newer caption is shown.` : `The caption was not saved: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
      void load()
      return false
    }
  }
  // Move `id` to index `to` among the current items.
  async function reorder(id: string, to: number) {
    const from = items.value.findIndex(x => x.id === id)
    if (from === -1 || from === to) return
    const list = items.value.filter(x => x.id !== id)
    const item = items.value[from]
    const target = Math.max(0, Math.min(list.length, to))
    const before = list[target - 1], after = list[target]
    const position = positionBetween(before ? positionOf(before) : undefined, after ? positionOf(after) : undefined)
    list.splice(target, 0, { ...item, position })
    items.value = list
    try {
      const saved = await patchAttachment(item, { position })
      items.value = items.value.map(x => x.id === id ? { ...x, ...saved } : x)
    } catch (e) {
      toast(e instanceof APIError && e.status === 412 ? 'The attachments changed elsewhere; the current order is shown.' : 'The new order was not saved.', { tone: 'error' })
      void load()
    }
  }

  function remove(item: Attachment) {
    const node = nodeId.value
    const here = captureRecord(), owner = captureToastOwner()
    const restoration = { item, here }
    function rollback() {
      if (restoring.get(item.id) !== restoration) return
      restoring.delete(item.id)
      if (here()) items.value = items.value.filter(x => x.id !== item.id)
    }
    items.value = items.value.filter(x => x.id !== item.id)
    let toastId = 0
    const done = deleteAttachment(item.id).then(eventId => ({ eventId }), e => {
      dismiss(toastId)
      rollback()
      if (!owner()) return null
      if (e instanceof APIError && e.status === 404) return null
      toast(`${item.name} could not be deleted.`, { tone: 'error' })
      if (here()) void load()
      return null
    }).finally(() => { if (deleting.get(item.id) === done) deleting.delete(item.id) })
    deleting.set(item.id, done)
    let undoStarted = false
    toastId = toast(`Deleted ${item.name}`, { timeout: UNDO_MS, action: { label: 'Undo', run: () => {
      if (undoStarted) return
      undoStarted = true
      void undo()
    } } })
    async function undo() {
      dismiss(toastId)
      if (here()) {
        restoring.set(item.id, restoration)
        items.value = [...items.value.filter(x => x.id !== item.id), item].sort(byPosition)
      }
      try {
        const deletion = await done
        if (!deletion || !owner()) return
        if (!deletion.eventId) throw new Error('The exact deletion receipt is unavailable. Reload to check the attachment.')
        const restored = await undoEvent(deletion.eventId)
        if (!owner()) return
        if (!restored || restored.id !== item.id || restored.node_id !== node) throw new Error('The restored attachment could not be confirmed.')
        if (here()) items.value = [...items.value.filter(x => x.id !== item.id), restored].sort(byPosition)
      } catch (e) {
        if (!owner()) return
        console.error('Attachment Undo failed', e)
        rollback()
        toast(`${item.name} could not be restored.`, { tone: 'error' })
        if (here()) void load()
      } finally {
        if (restoring.get(item.id) === restoration) restoring.delete(item.id)
      }
    }
  }
  onScopeDispose(() => { active = false; recordEpoch++; generation++; clearUploads() })

  const count = computed(() => items.value.length + uploads.value.length)
  const images = computed(() => items.value.filter(item => item.content_type.startsWith('image/')))
  return { items, uploads, loading, error, count, images, load, add, upload, retry, cancel, setCaption, reorder, remove }
}
