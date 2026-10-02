// SPDX-License-Identifier: AGPL-3.0-only
// Ticket attachments (B8, internal/attachments): list in order, upload files or a
// pasted image with progress, caption and reorder with a precondition, delete, and
// the three content sizes (thumb for strips, preview for the viewer, original only
// at 100%). Positions are decimal strings; created_by is the principal id.
import { api, APIError, sessionEnded } from './api.ts'

export interface Attachment {
  id: string; node_id: string; name: string; content_type: string; size: number; sha256: string
  width: number | null; height: number | null; caption: string; position: string | number
  created_by: string | { id: string; name: string }; created_at: string; updated_at?: string
  thumbnail_kind?: 'html-text'
}
export const positionOf = (item: Pick<Attachment, 'position'>) => Number(item.position) || 0
export const byPosition = (a: Attachment, b: Attachment) => positionOf(a) - positionOf(b) || a.id.localeCompare(b.id)
export const creatorId = (item: Pick<Attachment, 'created_by'>) => typeof item.created_by === 'string' ? item.created_by : item.created_by?.id ?? ''
export type Variant = 'thumb' | 'preview' | 'original'

export const contentUrl = (id: string, variant: Variant = 'preview') => `/api/attachments/${encodeURIComponent(id)}/content?variant=${variant}`
export const isImage = (item: Pick<Attachment, 'content_type'>) => item.content_type.startsWith('image/')
export const isHTML = (item: Pick<Attachment, 'content_type'>) => /^text\/html(?:;\s*charset=utf-8)?$/i.test(item.content_type)
export const hasThumbnail = (item: Attachment) => isImage(item) || item.thumbnail_kind === 'html-text'

export interface HTMLPreview { available: boolean; url?: string; expires_at?: string }
export async function createHTMLPreview(id: string, signal?: AbortSignal): Promise<HTMLPreview> {
  const preview = await json<HTMLPreview>(await api(`/attachments/${encodeURIComponent(id)}/preview`, { method: 'POST', signal }))
  if (!preview.available) return preview
  // Defense in depth: never let an API fixture/misconfiguration point an
  // executable frame back into the authenticated app. The server enforces
  // the stricter registrable-domain boundary.
  const url = new URL(preview.url ?? '')
  const expires = Date.parse(preview.expires_at ?? '')
  if (url.protocol !== 'https:' || url.hostname === location.hostname || url.username || url.password || url.port || url.search || url.hash || !/^\/preview\/[A-Za-z0-9_-]{43}$/.test(url.pathname) || !Number.isFinite(expires) || expires <= Date.now()) {
    throw new Error('HTML preview unavailable')
  }
  return preview
}
export function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
export function fileKind(item: Pick<Attachment, 'name' | 'content_type'>) {
  const ext = item.name.includes('.') ? item.name.split('.').pop()!.toUpperCase().slice(0, 5) : ''
  if (ext) return ext
  return item.content_type.split('/')[1]?.toUpperCase().slice(0, 5) ?? 'FILE'
}
// ![caption](attachment:<id>) in Markdown shows the attachment inline.
export const ATTACHMENT_REF = /^attachment:([A-Za-z0-9_-]{1,64})$/
export const markdownRef = (item: Pick<Attachment, 'id' | 'caption' | 'name'>) => `![${(item.caption || item.name).replace(/[[\]]/g, '')}](attachment:${item.id})`

async function json<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  return response.status === 204 ? (undefined as T) : response.json()
}
// Uploads answer with an array; PATCH with one attachment.
function one(body: unknown): Attachment {
  if (Array.isArray(body)) return body[0]
  const value = body as { items?: Attachment[] } & Attachment
  return Array.isArray(value?.items) ? value.items[0] : value
}

export async function listAttachments(nodeId: string): Promise<Attachment[]> {
  const body = await json<{ items?: Attachment[] } | Attachment[]>(await api(`/nodes/${encodeURIComponent(nodeId)}/attachments`))
  const items = Array.isArray(body) ? body : body.items ?? []
  return [...items].sort(byPosition)
}

// Multipart upload with progress (fetch cannot report upload progress).
export function uploadAttachment(nodeId: string, file: File, options: { caption?: string; onProgress?: (fraction: number) => void; signal?: AbortSignal } = {}): Promise<Attachment> {
  if (sessionEnded.blocked) return Promise.reject(new APIError(401, 'Your session has ended.'))
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/nodes/${encodeURIComponent(nodeId)}/attachments`)
    xhr.withCredentials = true
    xhr.setRequestHeader('Accept', 'application/json')
    xhr.upload.onprogress = event => { if (event.lengthComputable) options.onProgress?.(event.loaded / event.total) }
    xhr.onload = () => {
      if (xhr.status === 401) { sessionEnded.blocked = true; sessionEnded.handler?.(`/nodes/${nodeId}/attachments`) }
      let body: unknown = {}
      try { body = JSON.parse(xhr.responseText || '{}') } catch { /* keep empty */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(one(body))
      else reject(new APIError(xhr.status, typeof (body as { error?: unknown }).error === 'string' ? (body as { error: string }).error : `Upload failed (${xhr.status})`))
    }
    xhr.onerror = () => reject(new APIError(0, 'The upload was interrupted. Check your connection and try again.'))
    xhr.onabort = () => reject(new DOMException('Upload cancelled', 'AbortError'))
    options.signal?.addEventListener('abort', () => xhr.abort())
    const form = new FormData()
    if (options.caption) form.append('caption', options.caption)
    form.append('file', file, file.name || 'Screenshot.png')
    xhr.send(form)
  })
}

export async function patchAttachment(item: Attachment, patch: { caption?: string; position?: string }): Promise<Attachment> {
  const response = await api(`/attachments/${encodeURIComponent(item.id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'If-Unmodified-Since': item.updated_at ?? item.created_at },
    body: JSON.stringify(patch),
  })
  return one(await json(response))
}
export async function deleteAttachment(id: string, keepalive = false): Promise<void> {
  await json(await api(`/attachments/${encodeURIComponent(id)}`, { method: 'DELETE', keepalive }))
}

// Server undo: the removal is an event; its undo restores the attachment. The
// DELETE answers without the event, so the ticket's events are read (oldest
// first, in pages) for the newest removal of this attachment.
interface EventRow { id: number; type: string; after: { id?: string } | null; undo_of: number | null }
export async function findRemovalEvent(nodeId: string, attachmentId: string): Promise<number | null> {
  let after = 0, found: number | null = null
  for (let page = 0; page < 50; page++) {
    const body = await json<{ items: EventRow[]; next_after: number | null }>(await api(`/events?node_id=${encodeURIComponent(nodeId)}&limit=200${after ? `&after=${after}` : ''}`))
    for (const event of body.items) if (event.type === 'attachment.removed' && event.after?.id === attachmentId && event.undo_of == null) found = event.id
    if (!body.next_after) break
    after = body.next_after
  }
  return found
}
export async function undoEvent(eventId: number): Promise<Attachment | null> {
  const event = await json<{ after: Attachment | null }>(await api(`/events/${eventId}/undo`, { method: 'POST' }))
  return event?.after ?? null
}

// The position between two neighbours, so one PATCH moves an item; a decimal
// string the server accepts (at most 15 places, no exponent).
export function positionBetween(before: number | undefined, after: number | undefined): string {
  const value = before === undefined && after === undefined ? 1024 : before === undefined ? after! - 1024 : after === undefined ? before + 1024 : (before + after) / 2
  return value.toFixed(12).replace(/\.?0+$/, '') || '0'
}
