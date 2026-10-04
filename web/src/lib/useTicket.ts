// SPDX-License-Identifier: AGPL-3.0-only
import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { APIError, convertNode, createNode, createRelation, deleteNode, deleteRelation, getKinds, getNode, getRelations, listNodes, lookupNodes, moveNode, updateNode, type Kind, type ListItem, type ListParent, type NodePatch, type Relation, type WorkNode } from './api'
import { liveNodes, type LiveNodeStore, type LiveView, type NodeChange } from './liveNodes'
import { compareRevision, describeFields } from './liveUpdates'
import { RefreshRetry } from './refreshRetry'
import { rowStore, type EditorSession, type RowStore } from './rowStore'
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
  return { ...node, kind_slug: kind.slug, kind_label: node.level_name || kind.label, priority, assignee: null, parent, children_count: node.work_children_count ?? 0, project }
}

export type SaveResult = 'ok' | 'conflict' | 'error'
// A change to one ticket: title, body and state as given, fields as only the
// keys that change (undefined removes one). The save builds the fields on one
// server copy and sends that copy's revision (AEON-326).
export interface TicketChange { title?: string; body?: string; state?: string; human_check?: string | null; fields?: Record<string, unknown> }

// Move a ticket under another parent (an epic, or the project for "No epic").
// The move carries the revision the person sees; the server checks it under
// the row lock and answers 412 with the current node, which the row then shows.
// It acts on the row store's display object for the node, which every view shows.
export async function guardedMove(given: ListItem, parent: ListParent, after?: (item: ListItem, fromParentId: string | null) => void, rows: RowStore = rowStore): Promise<SaveResult> {
  const item = rows.row(given.id) ?? rows.adopt(given, 0, { full: false }) ?? given
  const from = item.parent
  const fromParentId = item.parent_id
  const since = rows.shown(item.id)?.updated_at ?? item.updated_at
  const sent = rows.mark()
  const conflict = (latest: WorkNode) => {
    rows.adoptNode(latest, sent)
    rows.reshow(item.id)
    toast(`${item.key} was changed elsewhere, so it was not moved. The newer version is shown.`, { tone: 'error' })
    return 'conflict' as const
  }
  try {
    let node: WorkNode
    try { node = await moveNode(item.id, parent.id, null, { ifUnmodifiedSince: since }) }
    catch (e) {
      if (!(e instanceof APIError) || e.status !== 412) throw e
      const current = e.body.node as WorkNode | undefined
      return conflict(current && typeof current.updated_at === 'string' ? current : await getNode(item.id))
    }
    // The answer is in the row store (this tab's write); the parent chip and
    // the epic a list groups by are known here, for this answer's revision
    // and parent only: a newer move that landed first keeps its own.
    rows.wrote(node, sent)
    const epic = ['work','epic'].includes(parent.kind_slug) ? { epic: { id: parent.id, key: parent.key, title: parent.title } } : parent.kind_slug === 'project' ? { epic: null } : {}
    rows.amend(item.id, node.updated_at, { parent, ...epic })
    after?.(item, fromParentId)
    const where = parent.kind_slug === 'project' ? 'to the project root' : `to ${parent.key} ${parent.title}`
    toast(`${item.key} moved ${where}`, from ? { action: { label: 'Undo', run: () => void guardedMove(item, from, after, rows) } } : {})
    return 'ok'
  } catch (e) {
    if (e instanceof APIError && (e.status === 404 || e.status === 410)) rows.gone(item.id, sent)
    toast(`${item.key} could not be moved: ${message(e)}`, { tone: 'error' })
    return 'error'
  }
}
export interface RelatedTicket { id: string; key: string; title: string; state: string }
export interface RelatedNode { relation: Relation; label: string; node: RelatedTicket | null }

// State and writes for one open ticket. The ticket is the row store's display
// object for the node (rowStore.ts): every read and write goes through the
// store, so an older answer never shows. Every write sends the revision of
// the copy it builds on; a 412 loads the newer version and reports a
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
  const store = context.live?.store ?? liveNodes
  const rows = store.rows
  const busy = () => !!context.live?.busy.value
  const liveFields = ref<string[]>([]) // what someone else just changed, for a brief highlight
  const liveMessage = ref('') // said politely to screen readers
  const liveHeld = ref<'changed' | 'deleted' | null>(null) // a change waiting while the viewer edits
  let flashTimer: ReturnType<typeof setTimeout> | undefined
  // A write this tab made (its exact revision); the same person's change in another tab is not one.
  const mine = (change: NodeChange) => !!change.actorId && change.actorId === context.live?.me() && rows.isOwn(change.id, change.revision)

  // ---------- The editor's base ----------
  // While the viewer edits (an editor open, a save running) the row is pinned:
  // nothing moves under them, and the session keeps the copy they started
  // from. Only this session's own save or a conflict moves its base.
  let session: EditorSession | null = null
  const editing = (id: string) => session?.open && session.id === id ? session : null
  // Editing starts now (idempotent). Returns the copy the draft builds on.
  function hold(): ListItem | null {
    const target = item.value
    if (!target) return null
    const open = editing(target.id)
    if (open) return open.base
    session?.end()
    session = rows.edit(target.id)
    return session?.base ?? null
  }
  function release() {
    const ending = session
    session = null
    ending?.end()
  }
  // The copy an edit builds on: the editor's base, or the one the ticket shows.
  function base(): ListItem | null {
    const target = item.value
    if (!target) return null
    return editing(target.id)?.base ?? rows.shown(target.id) ?? target
  }

  // ---------- What the panel shows ----------
  // Moved to another project: gone from here, like a deleted ticket.
  let away = false
  // Fields others changed since the panel last showed a newer version, for the
  // tint, and the revision it showed then.
  let heldFields: string[] = []
  let seen: string | null = null
  let seenParent: string | null = null
  // The panel shows what the store has. While the viewer edits, a newer copy
  // or a deletion waits (liveHeld) and comes in once they are done.
  function sync(target: ListItem, change: NodeChange | null) {
    if (item.value?.id !== target.id) return
    if (change) {
      if (change.fields.includes('project_id') && target.project) away = change.projectId !== target.project.id
      if (!mine(change) && change.type !== 'status_autopilot.derived') heldFields = [...new Set([...heldFields, ...change.fields])]
    }
    const deleted = rows.isDeleted(target.id) || away
    if (busy() && editing(target.id)) {
      liveHeld.value = deleted ? 'deleted' : rows.waiting(target.id) ? 'changed' : null
      return
    }
    liveHeld.value = null
    gone.value = deleted
    if (deleted) return
    rows.show(target.id)
    // Newer than what the panel showed before (whichever view brought it in).
    const newer = compareRevision(target.updated_at, seen) > 0
    const moved = target.parent_id !== seenParent
    seen = target.updated_at; seenParent = target.parent_id
    const fields = heldFields
    heldFields = []
    if (!newer) return
    if (moved) void resolveParent(target)
    // The viewer's own version needs no word.
    if (rows.isOwn(target.id, target.updated_at)) return
    clearTimeout(flashTimer)
    liveFields.value = fields
    flashTimer = setTimeout(() => { liveFields.value = [] }, 2000)
    const words = describeFields(fields)
    liveMessage.value = words ? `${target.key} was updated elsewhere: ${words}.` : `${target.key} was updated elsewhere.`
  }

  // ok: the ticket is current, or the editor is holding what this read found.
  // failed: the read itself failed and should be tried again. stale: a newer
  // read already answered; not a failure.
  async function refresh(): Promise<'ok' | 'failed' | 'stale'> {
    const target = item.value
    if (!target) return 'stale'
    const request = ++generation
    loading.value = true; error.value = ''
    const sent = rows.mark()
    // What the read found goes to the store even when the panel moved on,
    // unless a gap makes it doubtful (the read after the gap answers then).
    const trusted = () => !rows.gapSince(sent)
    try {
      const node = await getNode(target.id)
      // Kept only when at least as new as what the store knows, and never
      // over a deletion that arrived meanwhile.
      if (trusted()) rows.adoptNode(node, sent)
      if (request !== generation || item.value?.id !== target.id) return 'stale'
      // Loss now invalidates reads before reconnect. The resumed stream may
      // need no resync, so this request must replace its own doubtful answer.
      if (!trusted()) return refresh()
      // Held off by a deletion the store learned after this read was sent
      // (without knowing when): a read sent now can tell.
      if (trusted() && rows.isDeleted(target.id) && rows.touchedSince(target.id, sent)) return refresh()
      sync(target, null)
      return 'ok'
    } catch (e) {
      const missing = e instanceof APIError && (e.status === 404 || e.status === 410)
      // Unless a restore arrived after the read was sent.
      if (missing && trusted()) rows.gone(target.id, sent)
      if (request !== generation || item.value?.id !== target.id) return 'stale'
      if (missing) {
        if (!trusted()) return refresh()
        // News arrived after the read was sent, so the store could not tell: read again.
        if (trusted() && !rows.isDeleted(target.id)) return refresh()
        sync(target, null)
        return 'ok'
      }
      error.value = message(e)
      return 'failed'
    } finally {
      if (request === generation) loading.value = false
    }
  }
  // A gap read that failed waits, then tries again, including when the stream resumes.
  const gapRetry = new RefreshRetry(async () => (await refresh()) === 'failed')

  // The children as the row store has them: its newest copy of each, none it
  // knows deleted. A page read before a gap is read again, like any other.
  // again: after a gap, the children shown stay until the new page lands.
  async function loadChildren(again = false) {
    const target = item.value
    if (!again) children.value = []
    if (!target || (target.is_leaf !== false && target.kind_slug !== 'epic' && !target.children_count)) return
    if (!again) childrenLoading.value = true
    try {
      for (;;) {
        const sent = rows.mark()
        const page = await listNodes({ parent_id: target.id, limit: 200, sort: 'position' })
        if (rows.gapSince(sent)) { if (item.value?.id === target.id) continue; return }
        const shown = page.items.flatMap(child => rows.adopt(child, sent) && !rows.isDeleted(child.id) ? [rows.latest(child.id)!] : [])
        if (item.value?.id === target.id) children.value = shown
        return
      }
    } catch { /* the section shows nothing rather than a broken list */ }
    finally { if (!again) childrenLoading.value = false }
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
    // The open ticket changed: a retry or an editor for the previous one must not land here.
    gapRetry.clear()
    release()
    gone.value = false; error.value = ''
    away = false; heldFields = []; seen = item.value?.updated_at ?? null; seenParent = item.value?.parent_id ?? null
    liveHeld.value = null; liveFields.value = []; liveMessage.value = ''
    if (!id) return
    // The panel works on the store's display object for the node.
    if (!rows.row(id) && item.value) rows.adopt(item.value, 0, { full: false })
    if (busy()) hold()
    void refresh(); void loadChildren(); void loadRelations()
  }, { immediate: true })

  async function patch(change: TicketChange, onOk?: (node: WorkNode) => void): Promise<SaveResult> {
    const target = item.value
    if (!target) return 'error'
    const copy = base() ?? target
    const body: NodePatch = {}
    if (change.title !== undefined) body.title = change.title
    if (change.body !== undefined) body.body = change.body
    if (change.state !== undefined) body.state = change.state
    if (change.human_check !== undefined) body.human_check = change.human_check
    if (change.fields) {
      // Only the keys the viewer changed, over the copy the save is checked against.
      const fields = { ...(copy.fields ?? {}) }
      for (const [key, value] of Object.entries(change.fields)) {
        if (value === undefined) delete fields[key]
        else fields[key] = value
      }
      body.fields = fields
    }
    const sent = rows.mark()
    try {
      const node = await updateNode(target.id, body, { ifUnmodifiedSince: copy.updated_at })
      rows.wrote(node, sent)
      // An open editor builds on its own save from now on.
      editing(target.id)?.saved(node)
      sync(target, null)
      onOk?.(node)
      return 'ok'
    } catch (e) {
      if (e instanceof APIError && e.status === 412) {
        const read = rows.mark()
        try { rows.adoptNode(await getNode(target.id), read) }
        catch (e2) { if (e2 instanceof APIError && (e2.status === 404 || e2.status === 410)) rows.gone(target.id, read) }
        // The editor open now rebases onto the store's copy for this id: the
        // newest known, never an object a reload replaced.
        editing(target.id)?.rebase()
        sync(target, null)
        toast(`${target.key} was changed elsewhere. The newer version is shown; your draft is kept.`, { tone: 'error' })
        return 'conflict'
      }
      if (e instanceof APIError && e.status === 403 && e.message !== 'only a person can mark a human check checked' && e.message !== 'only a person can undo a human check') {
        readOnly.value = true
        toast(`You can read ${target.key} but not change it.`, { tone: 'error' })
        return 'error'
      }
      if (e instanceof APIError && (e.status === 404 || e.status === 410)) {
        // Gone as far as the store can tell; when something arrived after the
        // save was sent, the store cannot, and the ticket is read again.
        rows.gone(target.id, sent)
        if (!rows.isDeleted(target.id)) void refresh()
        sync(target, null)
        return 'error'
      }
      if (benefitGateError(e)) {
        toast(`${target.key} needs a 2–4 word pill and a benefit in both languages.`, { tone: 'error' })
        return 'error'
      }
      toast(`${target.key} was not saved: ${message(e)}`, { tone: 'error' })
      return 'error'
    }
  }

  // An empty value removes the field.
  const fieldValue = (value: unknown) => value === null || value === undefined || value === '' ? undefined : value
  const setEstimate = (hours: number | null) =>
    patch({ fields: { estimate_hours: hours, estimate_source: undefined, estimate_by: undefined, estimate_at: undefined, estimate_confirmed: undefined } })
  const setTitle = (title: string) => patch({ title })
  const setBody = (body: string) => patch({ body })
  const setField = (name: string, value: string) => patch({ fields: { [name]: fieldValue(value) } })
  async function setPriority(priority: string | null) {
    const target = item.value
    if (!target || (target.priority ?? null) === priority) return
    const result = await patch({ fields: { priority: fieldValue(priority) } })
    if (result === 'ok') toast(`${target.key} priority is now ${priority ? priority[0].toUpperCase() + priority.slice(1) : 'not set'}`)
  }
  async function setAssignee(person: { id: string; name: string } | null) {
    const target = item.value
    if (!target || (target.assignee?.id ?? null) === (person?.id ?? null)) return
    // The answer names the assignee by id: the store names them from here.
    if (person) { context.names.set(person.id, person.name); rows.learnName(person.id, person.name) }
    const result = await patch({ fields: { assignee: fieldValue(person?.id) } })
    if (result === 'ok') toast(person ? `${target.key} is assigned to ${person.name}` : `${target.key} is unassigned`)
  }

  async function moveTo(epic: { id: string; key: string; title: string }) {
    const target = item.value
    if (!target || target.parent?.id === epic.id) return
    // A move that found the ticket gone told the store; the panel follows it.
    if (await guardedMove(target, { id: epic.id, key: epic.key, title: epic.title, kind_slug: target.kind_slug === 'work' ? 'work' : 'epic' }, context.onMoved, rows) === 'error') sync(target, null)
  }

  async function remove(): Promise<boolean> {
    const target = item.value
    if (!target) return false
    const sent = rows.mark()
    try {
      const { revision } = await deleteNode(target.id)
      rows.deleted(target.id, revision, sent)
      // It leaves the views only when the store says it is gone: a restore
      // that arrived before this answer keeps it open and listed.
      if (!rows.isDeleted(target.id)) {
        toast(`${target.key} was deleted and has been restored since`)
        return true
      }
      toast(`${target.key} was deleted`)
      context.onRemoved(target)
      return true
    } catch (e) {
      // Already gone: the store learns it (or reads again when it cannot tell).
      if (e instanceof APIError && (e.status === 404 || e.status === 410)) {
        rows.gone(target.id, sent)
        if (!rows.isDeleted(target.id)) void refresh()
        sync(target, null)
      }
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
      const kind = all.find(k => k.slug === 'work') ?? all.find(k => k.slug === (target.kind_slug === 'epic' ? 'ticket' : 'task'))
      if (!kind) throw new Error('this workspace has no such kind')
      const sent = rows.mark()
      const node = await createNode({ kind_id: kind.id, title: title.trim(), parent_id: target.id, state: 'new', key_prefix: keyPrefix(routeKey) })
      const created = asListItem(node, kind, { id: target.id, key: target.key, title: target.title, kind_slug: target.kind_slug }, target.project)
      // The child and the parent's count go through the row store: the count
      // follows once for this child, however many views report it.
      if (rows.adopt(created, sent, { full: false }) && item.value?.id === target.id) children.value = [...children.value, rows.latest(node.id)!]
      rows.child(target.id, node.id, true)
      context.onCreated(created)
      return created
    } catch (e) {
      toast(`The ${target.kind_slug === 'epic' ? 'ticket' : 'task'} was not created: ${message(e)}`, { tone: 'error' })
      return null
    }
  }

  // ---------- Live: someone else's change comes in through the store (AEON-326) ----------
  // The stream refetches the node through the normal API into the row store.
  // While the viewer edits, the change waits: nothing moves under them, and a
  // save still sends the revision they started from, so it meets the change
  // as a conflict.

  // The parent chip after a move by someone else: the project itself, or the
  // new epic (a task's ticket) by its preview. Every copy in the store takes it.
  async function resolveParent(target: ListItem) {
    const id = target.parent_id
    const revision = target.updated_at
    // For the revision and parent it was resolved for: a newer move keeps its own chip.
    const set = (parent: ListParent | null) => rows.amend(target.id, revision, { parent })
    if (!id) { set(null); return }
    if (target.project?.id === id) { set({ id, key: target.project.key, title: target.project.title, kind_slug: 'project' }); return }
    try {
      const preview = (await lookupNodes([id])).items.find(node => node.id === id)
      if (!preview || target.parent_id !== id) return
      set({ id, key: preview.key, title: preview.title, kind_slug: target.kind_slug === 'work' ? 'work' : target.kind_slug === 'task' ? 'ticket' : 'epic' })
    } catch { /* the chip keeps the former parent until the next load */ }
  }
  const liveView: LiveView = {
    shows: id => item.value?.id === id || children.value.some(child => child.id === id),
    changed(change, node) {
      const target = item.value
      if (!target || node === undefined) return
      if (change.id !== target.id) {
        // A child takes the store's newer copy; one that went away (deleted,
        // moved elsewhere) stays until the next load.
        const at = children.value.findIndex(entry => entry.id === change.id)
        const copy = at >= 0 && node && !rows.isDeleted(change.id) ? rows.latest(change.id) : undefined
        if (copy && copy.parent_id === target.id && (compareRevision(copy.updated_at, children.value[at].updated_at) > 0 || change.type === 'status_autopilot.derived')) children.value = children.value.map((child, i) => i === at ? copy : child)
        return
      }
      sync(target, change)
    },
    // After a gap nothing is known to have changed: read the ticket again.
    // While the viewer edits the answer waits like a live change. A failed
    // read is tried again, on the same schedule as the list, until one succeeds.
    resync() { if (item.value) { gapRetry.request(); void loadChildren(true) } },
    resumed() { gapRetry.resume() },
  }
  onScopeDispose(store.subscribe(liveView))
  // The open ticket stays in the store while the panel shows it.
  onScopeDispose(rows.hold(() => item.value ? [item.value.id] : []))
  onScopeDispose(() => {
    gapRetry.clear()
    clearTimeout(flashTimer)
    release()
  })
  watch(busy, now => {
    const target = item.value
    if (!target) return
    // The editor owns its base from the moment it is busy; once it is done,
    // what waited meanwhile comes in.
    if (now) hold()
    else release()
    sync(target, null)
  })

  function childProgress() {
    const list = children.value
    const scope = list.filter(child => statusMeta(child.state).key !== 'cancelled')
    const done = scope.filter(child => ['done', 'delivered', 'accepted'].includes(statusMeta(child.state).key)).length
    return { done, total: scope.length, percent: scope.length ? Math.round((done / scope.length) * 100) : 0 }
  }

  async function convert(toKind: string) {
    const target = item.value
    if (!target) throw new Error('nothing is open')
    const copy = base() ?? target
    rows.learnKinds(await kinds())
    const sent = rows.mark()
    const node = await convertNode(target.id, toKind, { ifUnmodifiedSince: copy.updated_at })
    rows.wrote(node, sent)
    editing(target.id)?.saved(node)
    sync(target, null)
    if (item.value?.id === target.id) await loadChildren()
  }

  return { loading, error, gone, readOnly, children, childrenLoading, related, relationsReady, refresh, patch, hold, base, setEstimate, setTitle, setBody, setField, setPriority, setAssignee, moveTo, remove, addChild, childProgress, link, unlink, convert, liveFields, liveMessage, liveHeld }
}
