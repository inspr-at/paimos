// SPDX-License-Identifier: AGPL-3.0-only
import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { APIError, createNode, createRelation, deleteNode, deleteRelation, getKinds, getNode, getRelations, listNodes, lookupNodes, moveNode, updateNode, type Kind, type ListItem, type ListParent, type NodePatch, type Relation, type WorkNode } from './api'
import { liveNodes, type LiveNodeStore, type LiveView, type NodeChange } from './liveNodes'
import { compareRevision, describeFields } from './liveUpdates'
import { linkBody, linkedSentence, relationLabel, unlinkedSentence, type RelationChoice } from './relations'
import { benefitGateError } from './doneGate'
import { toast } from './toast'
import { statusMeta } from './work'

function message(error: unknown) { return error instanceof Error ? error.message : 'Something went wrong' }

// Kinds rarely change: one request per session.
let kindsRequest: Promise<Kind[]> | undefined
export function kinds(): Promise<Kind[]> {
  kindsRequest ??= getKinds().then(result => result.items).catch(error => { kindsRequest = undefined; throw error })
  return kindsRequest
}
export function keyPrefix(routeKey: string | undefined): string | undefined {
  return routeKey && /^[A-Z][A-Z0-9]{1,9}$/.test(routeKey) ? routeKey : undefined
}
// A freshly created node in list-item shape, so lists and the panel can show it at once.
export function asListItem(node: WorkNode, kind: Kind, parent: ListParent | null, project: ListItem['project']): ListItem {
  const priority = typeof node.fields.priority === 'string' ? node.fields.priority : null
  return { ...node, kind_slug: kind.slug, kind_label: kind.label, priority, assignee: null, parent, children_count: 0, project }
}

export type SaveResult = 'ok' | 'conflict' | 'error'

// Move a ticket under another parent (an epic, or the project for "No epic").
// The move carries If-Unmodified-Since; the server checks it under the row lock
// and answers 412 with the current node, which the row then shows.
export async function guardedMove(item: ListItem, parent: ListParent, after?: (item: ListItem, fromParentId: string | null) => void): Promise<SaveResult> {
  const from = item.parent
  const fromParentId = item.parent_id
  const conflict = (latest: WorkNode) => {
    Object.assign(item, { title: latest.title, body: latest.body, fields: latest.fields, state: latest.state, updated_at: latest.updated_at, parent_id: latest.parent_id })
    toast(`${item.key} was changed elsewhere, so it was not moved. The newer version is shown.`, { tone: 'error' })
    return 'conflict' as const
  }
  try {
    let node: WorkNode
    try { node = await moveNode(item.id, parent.id, null, { ifUnmodifiedSince: item.updated_at }) }
    catch (e) {
      if (!(e instanceof APIError) || e.status !== 412) throw e
      const current = e.body.node as WorkNode | undefined
      return conflict(current && typeof current.updated_at === 'string' ? current : await getNode(item.id))
    }
    Object.assign(item, { parent_id: node.parent_id, updated_at: node.updated_at, parent })
    after?.(item, fromParentId)
    const where = parent.kind_slug === 'project' ? 'out of its epic' : `to ${parent.key} ${parent.title}`
    toast(`${item.key} moved ${where}`, from ? { action: { label: 'Undo', run: () => void guardedMove(item, from, after) } } : {})
    return 'ok'
  } catch (e) {
    toast(`${item.key} could not be moved: ${message(e)}`, { tone: 'error' })
    return 'error'
  }
}
export interface RelatedTicket { id: string; key: string; title: string; state: string }
export interface RelatedNode { relation: Relation; label: string; node: RelatedTicket | null }

// State and writes for one open ticket. Every write sends the ticket's
// updated_at as a precondition; a 412 loads the newer version and reports a
// conflict so editors can keep their drafts.
export function useTicket(item: Ref<ListItem | null>, context: {
  names: Map<string, string>
  onRemoved: (item: ListItem) => void
  onCreated: (item: ListItem) => void
  onMoved: (item: ListItem, fromParent: string | null) => void
  // Live updates (AEON-326): while busy (an editor open, a save running)
  // changes by others wait; me tells the viewer's own changes apart.
  live?: { busy: Ref<boolean>; me: () => string | null; store?: LiveNodeStore }
}) {
  const loading = ref(false)
  const error = ref('')
  const gone = ref(false)
  const readOnly = ref(false)
  const children = ref<ListItem[]>([])
  const childrenLoading = ref(false)
  const related = ref<RelatedNode[]>([])
  // Whether this ticket's relations have been read (or failed): the blocks below them
  // wait for it, so relations never push activity down after it shows.
  const relationsReady = ref(false)
  let generation = 0

  function merge(target: ListItem, node: WorkNode) {
    const assigneeId = typeof node.fields.assignee === 'string' ? node.fields.assignee : null
    Object.assign(target, {
      title: node.title, body: node.body, fields: node.fields, state: node.state, updated_at: node.updated_at, parent_id: node.parent_id, estimate: node.estimate,
      priority: typeof node.fields.priority === 'string' && node.fields.priority ? node.fields.priority : null,
      assignee: assigneeId ? (target.assignee?.id === assigneeId ? target.assignee : { id: assigneeId, name: context.names.get(assigneeId) ?? 'Someone' }) : null,
    })
  }

  async function refresh() {
    const target = item.value
    if (!target) return
    const request = ++generation
    loading.value = true; error.value = ''
    try {
      const node = await getNode(target.id)
      if (request !== generation) return
      merge(target, node)
      gone.value = false
    } catch (e) {
      if (request !== generation) return
      if (e instanceof APIError && (e.status === 404 || e.status === 410)) gone.value = true
      else error.value = message(e)
    } finally {
      if (request === generation) loading.value = false
    }
  }

  async function loadChildren() {
    const target = item.value
    children.value = []
    if (!target || (target.kind_slug !== 'epic' && !target.children_count)) return
    childrenLoading.value = true
    try {
      const page = await listNodes({ parent_id: target.id, limit: 200, sort: 'position' })
      if (item.value?.id === target.id) children.value = page.items
    } catch { /* the section shows nothing rather than a broken list */ }
    finally { childrenLoading.value = false }
  }

  async function loadRelations() {
    const target = item.value
    related.value = []
    relationsReady.value = false
    if (!target) return
    try {
      const page = await getRelations(target.id)
      const items = page.items.filter(relation => relation.type !== ('parent' as never))
      const ids = [...new Set(items.map(relation => relation.source_node_id === target.id ? relation.target_node_id : relation.source_node_id))]
      let previews = new Map<string, { id: string; key: string; title: string; state: string }>()
      if (ids.length) {
        try { previews = new Map((await lookupNodes(ids)).items.map(node => [node.id, node])) }
        catch { /* Preserve unavailable chips when previews cannot be loaded. */ }
      }
      const resolved = items.map(relation => {
        const otherId = relation.source_node_id === target.id ? relation.target_node_id : relation.source_node_id
        return { relation, label: relationLabel(relation, target.id), node: previews.get(otherId) ?? null }
      })
      if (item.value?.id === target.id) related.value = resolved
    } catch { /* relations are optional context */ }
    finally { if (item.value?.id === target.id) relationsReady.value = true }
  }

  watch(() => item.value?.id, id => {
    gone.value = false; error.value = ''
    if (!id) return
    void refresh(); void loadChildren(); void loadRelations()
  }, { immediate: true })

  async function patch(body: NodePatch, onOk?: (node: WorkNode) => void): Promise<SaveResult> {
    const target = item.value
    if (!target) return 'error'
    try {
      const node = await updateNode(target.id, body, { ifUnmodifiedSince: target.updated_at })
      merge(target, node)
      onOk?.(node)
      return 'ok'
    } catch (e) {
      if (e instanceof APIError && e.status === 412) {
        try { merge(target, await getNode(target.id)) } catch { /* keep what we have */ }
        toast(`${target.key} was changed elsewhere. The newer version is shown; your draft is kept.`, { tone: 'error' })
        return 'conflict'
      }
      if (e instanceof APIError && e.status === 403) {
        readOnly.value = true
        toast(`You can read ${target.key} but not change it.`, { tone: 'error' })
        return 'error'
      }
      if (e instanceof APIError && (e.status === 404 || e.status === 410)) { gone.value = true; return 'error' }
      if (benefitGateError(e)) {
        toast(`${target.key} needs a 2–4 word pill and a benefit in both languages.`, { tone: 'error' })
        return 'error'
      }
      toast(`${target.key} was not saved: ${message(e)}`, { tone: 'error' })
      return 'error'
    }
  }

  function withField(name: string, value: unknown): Record<string, unknown> {
    const fields = { ...(item.value?.fields ?? {}) }
    if (value === null || value === undefined || value === '') delete fields[name]
    else fields[name] = value
    return fields
  }
  const setEstimate = (hours: number | null) => {
    const fields = { ...(item.value?.fields ?? {}), estimate_hours: hours } as Record<string, unknown>
    for (const key of ['estimate_source', 'estimate_by', 'estimate_at', 'estimate_confirmed']) delete fields[key]
    return patch({ fields })
  }
  const setTitle = (title: string) => patch({ title })
  const setBody = (body: string) => patch({ body })
  const setField = (name: string, value: string) => patch({ fields: withField(name, value) })
  async function setPriority(priority: string | null) {
    const target = item.value
    if (!target || (target.priority ?? null) === priority) return
    const result = await patch({ fields: withField('priority', priority) })
    if (result === 'ok') toast(`${target.key} priority is now ${priority ? priority[0].toUpperCase() + priority.slice(1) : 'not set'}`)
  }
  async function setAssignee(person: { id: string; name: string } | null) {
    const target = item.value
    if (!target || (target.assignee?.id ?? null) === (person?.id ?? null)) return
    if (person) context.names.set(person.id, person.name)
    const result = await patch({ fields: withField('assignee', person?.id ?? null) })
    if (result === 'ok') { target.assignee = person; toast(person ? `${target.key} is assigned to ${person.name}` : `${target.key} is unassigned`) }
  }

  async function moveTo(epic: { id: string; key: string; title: string }) {
    const target = item.value
    if (!target || target.parent?.id === epic.id) return
    await guardedMove(target, { id: epic.id, key: epic.key, title: epic.title, kind_slug: 'epic' }, context.onMoved)
  }

  async function remove(): Promise<boolean> {
    const target = item.value
    if (!target) return false
    try {
      await deleteNode(target.id)
      toast(`${target.key} was deleted`)
      context.onRemoved(target)
      return true
    } catch (e) {
      const text = e instanceof APIError && e.status === 409 ? 'it still has children. Move or delete them first.' : message(e)
      toast(`${target.key} was not deleted: ${text}`, { tone: 'error' })
      return false
    }
  }

  // ---------- Links to other tickets ----------
  // Link this ticket to another. A refusal comes back as the server's reason in
  // words (an existing link, or the loop it would make) for the picker to show.
  async function link(choice: RelationChoice, other: RelatedTicket): Promise<string | null> {
    const target = item.value
    if (!target) return 'This ticket is no longer open.'
    try {
      const relation = await createRelation(linkBody(choice, target.id, other.id))
      const entry: RelatedNode = { relation, label: choice.label, node: other }
      if (item.value?.id === target.id) related.value = [...related.value, entry]
      const [source, dest] = choice.outgoing ? [target.key, other.key] : [other.key, target.key]
      toast(linkedSentence(choice.type, source, dest), { action: { label: 'Undo', run: () => void unlink(entry, target) } })
      return null
    } catch (e) {
      if (e instanceof APIError && e.status === 403) { readOnly.value = true; return `You can read ${target.key} but not change its links.` }
      if (e instanceof APIError && e.status === 404) return `${other.key} is no longer available.`
      return message(e)
    }
  }

  // Remove one link (the caller confirms first). Undo links the same two
  // tickets the same way again; the server checks it like any new link.
  async function unlink(entry: RelatedNode, owner: ListItem | null = item.value): Promise<boolean> {
    const target = owner
    if (!target) return false
    const { relation } = entry
    const otherKey = entry.node?.key ?? 'the other ticket'
    const [source, dest] = relation.type === 'relates' || relation.source_node_id === target.id ? [target.key, otherKey] : [otherKey, target.key]
    const drop = () => { if (item.value?.id === target.id) related.value = related.value.filter(other => other.relation.id !== relation.id) }
    try {
      await deleteRelation(relation.id)
      drop()
    } catch (e) {
      // Already gone elsewhere: the list catches up, nothing to undo.
      if (e instanceof APIError && e.status === 404) { drop(); return true }
      if (e instanceof APIError && e.status === 403) readOnly.value = true
      toast(`The link was not removed: ${message(e)}`, { tone: 'error' })
      return false
    }
    toast(unlinkedSentence(relation.type, source, dest), {
      action: {
        label: 'Undo',
        run: () => void createRelation({ source_node_id: relation.source_node_id, target_node_id: relation.target_node_id, type: relation.type })
          .then(restored => {
            if (item.value?.id === target.id) related.value = [...related.value, { ...entry, relation: restored }]
            toast(linkedSentence(relation.type, source, dest))
          })
          .catch(e => toast(`The link was not restored: ${message(e)}`, { tone: 'error' })),
      },
    })
    return true
  }

  // Inline child creation: an epic gets tickets, a ticket gets tasks.
  async function addChild(title: string, routeKey: string | undefined): Promise<ListItem | null> {
    const target = item.value
    if (!target || !title.trim()) return null
    try {
      const all = await kinds()
      const kind = all.find(k => k.slug === (target.kind_slug === 'epic' ? 'ticket' : 'task'))
      if (!kind) throw new Error('this workspace has no such kind')
      const node = await createNode({ kind_id: kind.id, title: title.trim(), parent_id: target.id, state: 'new', key_prefix: keyPrefix(routeKey) })
      const created = asListItem(node, kind, { id: target.id, key: target.key, title: target.title, kind_slug: target.kind_slug }, target.project)
      children.value = [...children.value, created]
      target.children_count = (target.children_count ?? 0) + 1
      context.onCreated(created)
      return created
    } catch (e) {
      toast(`The ${target.kind_slug === 'epic' ? 'ticket' : 'task'} was not created: ${message(e)}`, { tone: 'error' })
      return null
    }
  }

  // ---------- Live: someone else's change patches the ticket in place (AEON-326) ----------
  // The store refetches the node through the normal API. While the viewer
  // edits, the change waits: nothing moves under them, and a save still sends
  // the revision they started from, so it meets the change as a conflict.
  const liveFields = ref<string[]>([]) // what someone else just changed, for a brief highlight
  const liveMessage = ref('') // said politely to screen readers
  const liveHeld = ref<'changed' | 'deleted' | null>(null) // a change waiting while the viewer edits
  let held: { change: NodeChange; node: WorkNode | null } | null = null
  let rereadAfterEdit = false
  let flashTimer: ReturnType<typeof setTimeout> | undefined
  const store = context.live?.store ?? liveNodes
  const busy = () => !!context.live?.busy.value
  const mine = (change: NodeChange) => !!change.actorId && change.actorId === context.live?.me()

  // A node that left this project (a project move) is gone from here, like a deleted one.
  const leftProject = (target: ListItem, change: NodeChange) =>
    change.fields.includes('project_id') && !!target.project && change.projectId !== target.project.id

  function applyLive(target: ListItem, change: NodeChange, node: WorkNode | null) {
    if (!node || leftProject(target, change)) { gone.value = true; return }
    if (compareRevision(node.updated_at, target.updated_at) <= 0) return
    const parentChanged = node.parent_id !== target.parent_id
    merge(target, node)
    if (parentChanged) void resolveParent(target)
    if (mine(change)) return
    clearTimeout(flashTimer)
    liveFields.value = change.fields
    flashTimer = setTimeout(() => { liveFields.value = [] }, 2000)
    const words = describeFields(change.fields)
    liveMessage.value = words ? `${target.key} was updated elsewhere: ${words}.` : `${target.key} was updated elsewhere.`
  }
  // The parent chip after a move by someone else: the project itself, or the
  // new epic (a task's ticket) by its preview.
  async function resolveParent(target: ListItem) {
    const id = target.parent_id
    if (!id) { target.parent = null; return }
    if (target.project?.id === id) { target.parent = { id, key: target.project.key, title: target.project.title, kind_slug: 'project' }; return }
    try {
      const preview = (await lookupNodes([id])).items.find(node => node.id === id)
      if (!preview || target.parent_id !== id) return
      target.parent = { id, key: preview.key, title: preview.title, kind_slug: target.kind_slug === 'task' ? 'ticket' : 'epic' }
    } catch { /* the chip keeps the former parent until the next load */ }
  }
  const liveView: LiveView = {
    shows: id => item.value?.id === id || children.value.some(child => child.id === id),
    changed(change, node) {
      const target = item.value
      if (!target || node === undefined) return
      if (change.id !== target.id) {
        // Children patch in place; one that went away stays until the next load.
        const child = children.value.find(entry => entry.id === change.id)
        if (child && node && node.parent_id === target.id && compareRevision(node.updated_at, child.updated_at) > 0) merge(child, node)
        return
      }
      if (busy()) {
        if (mine(change) || (node && compareRevision(node.updated_at, target.updated_at) <= 0)) return
        held = { change, node }
        liveHeld.value = !node || leftProject(target, change) ? 'deleted' : 'changed'
        return
      }
      applyLive(target, change, node)
    },
    // After a gap nothing is known to have changed: read the ticket again,
    // quietly, once the viewer is done editing.
    resync() {
      if (!item.value) return
      if (busy()) rereadAfterEdit = true
      else void refresh()
    },
  }
  onScopeDispose(store.subscribe(liveView))
  onScopeDispose(() => clearTimeout(flashTimer))
  watch(() => busy(), now => {
    const target = item.value
    if (now || !target) return
    const waiting = held, reread = rereadAfterEdit
    held = null; liveHeld.value = null; rereadAfterEdit = false
    if (waiting) applyLive(target, waiting.change, waiting.node)
    if (reread) void refresh()
  })
  watch(() => item.value?.id, () => { held = null; rereadAfterEdit = false; liveHeld.value = null; liveFields.value = []; liveMessage.value = '' })
  // Loads and saves tell the store which version the panel has, so the
  // event of the viewer's own save needs no refetch. A conflict reload that
  // already shows the held change settles it.
  watch(() => item.value?.updated_at, () => {
    const target = item.value
    if (!target) return
    store.put([target])
    if (held?.node && compareRevision(target.updated_at, held.node.updated_at) >= 0) { held = null; liveHeld.value = null }
  }, { immediate: true })

  function childProgress() {
    const list = children.value
    const scope = list.filter(child => statusMeta(child.state).key !== 'cancelled')
    const done = scope.filter(child => ['done', 'delivered', 'accepted'].includes(statusMeta(child.state).key)).length
    return { done, total: scope.length, percent: scope.length ? Math.round((done / scope.length) * 100) : 0 }
  }

  return { loading, error, gone, readOnly, children, childrenLoading, related, relationsReady, refresh, patch, setEstimate, setTitle, setBody, setField, setPriority, setAssignee, moveTo, remove, addChild, childProgress, link, unlink, liveFields, liveMessage, liveHeld }
}
