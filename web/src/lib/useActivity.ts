// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref, watch, type Ref } from 'vue'
import { createComment, deleteComment, getActivity, updateComment, type ActivityItem } from './api'
import { buildTimeline } from './activity'
import { toast } from './toast'

function message(error: unknown) { return error instanceof Error ? error.message : 'Something went wrong' }

// The ticket timeline: newest page first, older pages on request, comments
// written, edited and deleted in place.
export function useActivity(nodeId: Ref<string | null>) {
  const items = ref<ActivityItem[]>([])
  const cursor = ref<string | null>(null)
  const loading = ref(false)
  const loadingOlder = ref(false)
  const error = ref('')
  let generation = 0
  const timeline = computed(() => buildTimeline(items.value))

  async function load() {
    const id = nodeId.value
    items.value = []; cursor.value = null; error.value = ''
    if (!id) return
    const request = ++generation
    loading.value = true
    try {
      const page = await getActivity(id)
      if (request !== generation) return
      items.value = page.items
      cursor.value = page.next_cursor
    } catch (e) { if (request === generation) error.value = message(e) }
    finally { if (request === generation) loading.value = false }
  }
  async function loadOlder() {
    const id = nodeId.value
    if (!id || !cursor.value || loadingOlder.value) return
    const request = generation
    loadingOlder.value = true
    try {
      const page = await getActivity(id, cursor.value)
      if (request !== generation) return
      const seen = new Set(items.value.map(item => item.id))
      items.value = [...items.value, ...page.items.filter(item => !seen.has(item.id))]
      cursor.value = page.next_cursor
    } catch (e) { toast(`Older activity could not be loaded: ${message(e)}`, { tone: 'error' }) }
    finally { if (request === generation) loadingOlder.value = false }
  }
  watch(nodeId, load, { immediate: true })

  async function add(body: string, expectedId: string | null = nodeId.value): Promise<boolean> {
    const id = nodeId.value
    if (!id || id !== expectedId || !body.trim()) return false
    try {
      const created = await createComment(id, body)
      if (nodeId.value === id) items.value = [created, ...items.value]
      return true
    } catch (e) { toast(`Your comment was not posted: ${message(e)}. It is still in the box.`, { tone: 'error' }); return false }
  }
  async function edit(commentId: string, body: string): Promise<boolean> {
    const id = nodeId.value
    if (!id || !body.trim()) return false
    try {
      const updated = await updateComment(id, commentId, body)
      items.value = items.value.map(item => item.id === commentId ? { ...item, ...updated } : item)
      return true
    } catch (e) { toast(`The comment was not changed: ${message(e)}`, { tone: 'error' }); return false }
  }
  async function remove(commentId: string): Promise<boolean> {
    const id = nodeId.value
    if (!id) return false
    try {
      await deleteComment(id, commentId)
      items.value = items.value.filter(item => item.id !== commentId)
      toast('Comment deleted')
      return true
    } catch (e) { toast(`The comment was not deleted: ${message(e)}`, { tone: 'error' }); return false }
  }
  return { items, timeline, cursor, loading, loadingOlder, error, load, loadOlder, add, edit, remove }
}
