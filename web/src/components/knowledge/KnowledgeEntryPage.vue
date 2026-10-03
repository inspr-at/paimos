<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { brand, setPageTitle } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { lineDiff } from '../../lib/crm'
import {
  DETAIL_FIELDS, STATUSES, cliCommand, deleteKnowledge, detailText, entryParam, entryPath, getKnowledge, kindToken, KnowledgeError, mergeDetails, readingMinutes,
  resolveKnowledge, slugProblem, statusLabel, typeMeta, undoKnowledge, updateKnowledge, validUrl, wantsToc, withoutTitle,
  type Heading, type KnowledgeEntry, type KnowledgeLink, type KnowledgePatch, type KnowledgeStatus, type KnowledgeType,
} from '../../lib/knowledge'
import type { KnowledgeState } from '../../lib/useKnowledge'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import MarkdownBody from '../MarkdownBody.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import MarkdownEditor from '../work/MarkdownEditor.vue'
import StatusIcon from '../work/StatusIcon.vue'

// One knowledge entry as a reading page: the text with anchored headings and a
// table of contents, what it is (kind, slug, status, who and when), how agents
// read it, and what it is linked to. Edit mode is the ticket pattern (U11): one
// form, one Save, a stale copy answers with the newer version and the draft
// stays; every save and delete offers Undo through the event log.
// mode 'dock' (U25) is the same entry docked beside the list on wide screens,
// like a ticket's side panel: its own scroller, one column, the list's j and k
// move it, Esc closes it, and Expand opens the entry's own page (this same
// component, so a draft and the loaded entry carry over).
const props = withDefaults(defineProps<{
  project: { id: string; routeKey: string; title: string }
  type: KnowledgeType; slug: string; state: KnowledgeState; canWrite: boolean; canDelete: boolean; now: number
  // The list's search, kind, status and sort, carried so back returns to it.
  listQuery: Record<string, string>
  mode?: 'page' | 'dock'
}>(), { mode: 'page' })
const emit = defineEmits<{ close: [] }>()
const dock = computed(() => props.mode === 'dock')
const route = useRoute()
const router = useRouter()
const projects = useProjects()

const entry = ref<KnowledgeEntry | null>(null)
const loading = ref(false)
const error = ref('')
const missing = ref(false)
const readOnly = ref(false)
const root = ref<HTMLElement>()
const scroller = ref<HTMLElement>()
const article = ref<HTMLElement>()
const headings = ref<Heading[]>([])
const activeHeading = ref('')
const moreAnchor = ref<HTMLElement | null>(null)
const moreButton = ref<HTMLButtonElement>()
const editing = ref(false)
const saving = ref(false)
const locator = () => `${props.project.id}/${props.type}/${props.slug}`
const loadedLocator = ref('')
const currentEntry = computed(() => !!entry.value && loadedLocator.value === locator() && !loading.value && !error.value && !missing.value)
const writable = computed(() => props.canWrite && !readOnly.value && currentEntry.value)
const meta = computed(() => typeMeta(entry.value?.type ?? props.type))
let generation = 0

// ---------- Loading, and following a renamed slug ----------
async function load() {
  const request = ++generation, requestedLocator = locator()
  const projectId = props.project.id, type = props.type, slug = props.slug
  loadedLocator.value = ''
  loading.value = true; error.value = ''; missing.value = false
  try {
    const found = await resolveKnowledge(projectId, type, slug)
    if (request !== generation) return
    const switched = entry.value?.id !== found.id
    entry.value = found
    loadedLocator.value = requestedLocator
    // The pane starts each entry at its top, unless the address names a section.
    if (switched && dock.value && !route.hash) scroller.value?.scrollTo({ top: 0 })
    if (found.renamed_from && found.slug !== props.slug) {
      toast(`“${found.renamed_from}” is now called “${found.slug}”. Agents need the new slug.`)
      void router.replace(placeOf(found.type, found.slug))
    }
  } catch (e) {
    if (request !== generation) return
    if (e instanceof KnowledgeError && e.status === 404) { missing.value = true; entry.value = null }
    else { error.value = e instanceof Error ? e.message : 'The entry could not be loaded.'; entry.value = null }
  } finally {
    if (request === generation) loading.value = false
  }
}
watch([() => props.project.id, () => props.type, () => props.slug], ([projectId, type, slug]) => {
  // Our own rename (or its undo) changes the address, not the entry.
  generation++
  if (entry.value && entry.value.project?.id === projectId && entry.value.type === type && entry.value.slug === slug && !error.value) { loadedLocator.value = locator(); loading.value = false; return }
  editing.value = false; saving.value = false; readOnly.value = false
  headings.value = []
  void load()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++ })
watch(entry, current => { if (current) setPageTitle(`${current.title} · ${props.project.routeKey} Knowledge`) })
// Another entry is on its way: the one shown stays, quietly dimmed, so nothing jumps.
const switching = computed(() => loading.value && !!entry.value && (entry.value.type !== props.type || entry.value.slug !== props.slug))
// What the list already knows about the entry, to show its name while it loads.
const listed = computed(() => props.state.items.value.find(item => item.type === props.type && item.slug === props.slug) ?? null)

// ---------- Position in the list: previous, next and the rail ----------
const sequence = computed(() => props.state.sequence.value)
// By the address, so j and k keep going while the next entry is still loading.
const position = computed(() => {
  const index = sequence.value.findIndex(item => item.type === props.type && item.slug === props.slug)
  return index === -1 ? null : { index, count: sequence.value.length }
})
function go(step: number) {
  const at = position.value
  if (!at) return
  const next = sequence.value[at.index + step]
  if (next) void router.replace(placeOf(next.type, next.slug, false))
}
function listLink() { return { path: `/p/${encodeURIComponent(props.project.routeKey)}/knowledge`, query: props.listQuery } }
// Where an entry of this project shows: the pane's ?entry= beside the list, or
// its own page. keepHash keeps a section (a rename keeps the reader's place).
function placeOf(type: KnowledgeType, slug: string, keepHash = true) {
  const hash = keepHash ? route.hash : ''
  if (dock.value) return { path: route.path, query: { ...route.query, entry: entryParam(type, slug) }, hash }
  return { path: entryPath(props.project.routeKey, type, slug), query: route.query, hash }
}
// The entry's own page, with the list's place so back and Esc return to it.
function expand() {
  const current = entry.value
  void router.push({ path: entryPath(props.project.routeKey, current?.type ?? props.type, current?.slug ?? props.slug), query: props.listQuery, hash: route.hash })
}

// ---------- Reading: headings, table of contents, anchors ----------
const body = computed(() => entry.value ? withoutTitle(entry.value.body, entry.value.title) : '')
const minutes = computed(() => readingMinutes(body.value))
const toc = computed(() => wantsToc(headings.value, body.value) ? headings.value.filter(h => h.level >= 2 && h.level <= 3) : [])
const rule = computed(() => entry.value?.type === 'guideline' ? detailText(entry.value.metadata.rule) : '')
// External systems and related projects lead with where they live and what they are for.
const HEADLINE = ['rule', 'url', 'instance_url', 'purpose', 'relationship']
const headline = computed(() => {
  const current = entry.value
  if (!current || (current.type !== 'external-system' && current.type !== 'related-project')) return null
  const address = detailText(current.metadata[current.type === 'external-system' ? 'url' : 'instance_url'])
  const about = detailText(current.metadata[current.type === 'external-system' ? 'purpose' : 'relationship'])
  return address || about ? { address: validUrl(address) ? address : '', about } : null
})
const details = computed(() => {
  const current = entry.value
  if (!current) return []
  return DETAIL_FIELDS[current.type].map(field => ({ ...field, value: detailText(current.metadata[field.key]) }))
    .filter(field => field.value && !HEADLINE.includes(field.key))
    .map(field => field.kind === 'choice' ? { ...field, value: field.value[0].toUpperCase() + field.value.slice(1) } : field)
})
function linkFor(id: string) { return `${location.origin}${entryPath(props.project.routeKey, entry.value?.type ?? props.type, entry.value?.slug ?? props.slug, id)}` }
async function copy(text: string, label: string) {
  try { await navigator.clipboard.writeText(text); toast(`Copied ${label}`) } catch { toast(`${label[0].toUpperCase()}${label.slice(1)} could not be copied`, { tone: 'error' }) }
}
function scrollToHeading(id: string, smooth = true) {
  const el = document.getElementById(`h-${id}`)
  if (!el) return false
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  el.scrollIntoView({ behavior: smooth && !reduce ? 'smooth' : 'auto', block: 'start' })
  el.classList.add('flash')
  setTimeout(() => el.classList.remove('flash'), 1400)
  activeHeading.value = id
  return true
}
function jump(id: string) {
  if (!scrollToHeading(id)) return
  void router.replace({ path: route.path, query: route.query, hash: `#${id}` })
}
function anchor(id: string) { void copy(linkFor(id), 'the link to this section'); jump(id) }
// Opening a link with a section: scroll there once the text is on the page.
watch([() => route.hash, headings], ([hash, list]) => {
  const id = decodeURIComponent(hash.replace(/^#/, ''))
  if (id && list.some(h => h.id === id)) void nextTick(() => scrollToHeading(id, false))
}, { flush: 'post' })
// The section being read is marked in the table of contents.
let spy: IntersectionObserver | undefined
watch([toc, article, dock], async () => {
  spy?.disconnect()
  if (!toc.value.length || !article.value) return
  await nextTick()
  const visible = new Map<string, boolean>()
  const scrollRoot = scrollRootEl()
  spy = new IntersectionObserver(entries => {
    for (const item of entries) visible.set(item.target.id.slice(2), item.isIntersecting)
    const atEnd = !!scrollRoot && scrollRoot.scrollTop > 0 && scrollRoot.scrollTop + scrollRoot.clientHeight >= scrollRoot.scrollHeight - 4
    const first = atEnd ? toc.value[toc.value.length - 1] : toc.value.find(h => visible.get(h.id))
    if (first) activeHeading.value = first.id
  }, { root: scrollRoot, rootMargin: dock.value ? '-8px 0px -55% 0px' : '-64px 0px -55% 0px' })
  for (const h of toc.value) { const el = document.getElementById(`h-${h.id}`); if (el) spy.observe(el) }
}, { flush: 'post' })
// At the very end of the page the last sections cannot reach the top: the last one is where the reader is.
function scrolledToEnd(event: Event) {
  const el = event.target as HTMLElement
  if (toc.value.length && el.scrollTop + el.clientHeight >= el.scrollHeight - 4) activeHeading.value = toc.value[toc.value.length - 1].id
}
// The page scrolls in the app's main area; the docked pane in its own scroller.
function scrollRootEl() { return dock.value ? scroller.value ?? null : document.getElementById('main') }
let scrollListener: HTMLElement | null = null
function listenScroll() {
  scrollListener?.removeEventListener('scroll', scrolledToEnd)
  scrollListener = scrollRootEl()
  scrollListener?.addEventListener('scroll', scrolledToEnd, { passive: true })
}
watch(dock, () => void nextTick(listenScroll))

// ---------- Links to tickets and other entries ----------
const linkGroups = computed(() => {
  const links = entry.value?.links ?? []
  const work = links.filter(l => ['ticket', 'task', 'epic'].includes(l.node.kind))
  const knowledge = links.filter(l => l.node.type && l.node.slug)
  const other = links.filter(l => !work.includes(l) && !knowledge.includes(l))
  return [{ label: 'Tickets', items: work }, { label: 'Knowledge', items: knowledge }, { label: 'Other', items: other }].filter(g => g.items.length)
})
const RELATION: Record<string, [string, string]> = { blocks: ['Blocks', 'Blocked by'], implements: ['Implements', 'Implemented by'], cites: ['Cites', 'Cited by'], duplicates: ['Duplicates', 'Duplicated by'], relates: ['Relates to', 'Relates to'] }
const relationLabel = (l: KnowledgeLink) => (RELATION[l.type] ?? [l.type, l.type])[l.direction === 'out' ? 0 : 1]
function linkTarget(l: KnowledgeLink) {
  const owner = l.node.project_id ? projects.byId(l.node.project_id) : undefined
  const routeKey = owner?.routeKey ?? props.project.routeKey
  // Another entry of this project opens in the same pane.
  if (l.node.type && l.node.slug && dock.value && routeKey === props.project.routeKey) return placeOf(l.node.type, l.node.slug, false)
  if (l.node.type && l.node.slug) return entryPath(routeKey, l.node.type, l.node.slug)
  return owner ? `/p/${encodeURIComponent(routeKey)}/${encodeURIComponent(l.node.key)}` : null
}

// ---------- Edit mode: title, slug, status, details and text; one Save ----------
const titleField = ref<HTMLTextAreaElement>()
const slugField = ref<HTMLInputElement>()
const editor = ref<InstanceType<typeof MarkdownEditor>>()
const draft = reactive({ title: '', slug: '', status: 'active' as KnowledgeStatus, body: '', details: {} as Record<string, string> })
// base: the version the draft started from; stamp: the updated_at a save must still match.
let base = { title: '', slug: '', status: 'active' as KnowledgeStatus, body: '', details: {} as Record<string, string> }
const stamp = ref('')
const touched = ref(false)
const slugError = ref('')
const conflict = ref<KnowledgeEntry | null>(null)
const comparing = ref(false)
const wide = ref(window.matchMedia('(min-width: 1100px)').matches)
const wideQuery = window.matchMedia('(min-width: 1100px)')
const onWide = (event: MediaQueryListEvent) => { wide.value = event.matches }
function snapshot(current: KnowledgeEntry) {
  const values: Record<string, string> = {}
  for (const field of DETAIL_FIELDS[current.type]) values[field.key] = detailText(current.metadata[field.key])
  return { title: current.title, slug: current.slug, status: current.status, body: current.body, details: values }
}
const detailFields = computed(() => entry.value ? DETAIL_FIELDS[entry.value.type] : [])
const detailsChanged = () => Object.keys({ ...base.details, ...draft.details }).some(key => (draft.details[key] ?? '') !== (base.details[key] ?? ''))
const dirty = computed(() => editing.value && (draft.title !== base.title || draft.slug !== base.slug || draft.status !== base.status || draft.body !== base.body || detailsChanged()))
const slugChanged = computed(() => editing.value && !!entry.value && draft.slug !== entry.value.slug)
const slugIssue = computed(() => slugProblem(entry.value?.type ?? props.type, draft.slug.trim()) || slugError.value)
const titleIssue = computed(() => draft.title.trim() ? '' : 'A title is needed.')
const detailIssue = (key: string, kind: string) => kind === 'url' && !validUrl(draft.details[key] ?? '') ? 'Use a full address, starting with https://' : ''
const oldCommand = computed(() => entry.value ? cliCommand(brand.value.product, props.project.routeKey, entry.value.type, entry.value.slug) : '')
// Blank lines that only moved add noise to a comparison of prose.
const diffLines = computed(() => conflict.value ? lineDiff(conflict.value.body, draft.body).filter(line => line.kind === 'same' || line.text.trim()) : [])
const listWords = (words: string[]) => words.length < 2 ? words.join('') : `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
const conflictFields = computed(() => {
  const theirs = conflict.value
  if (!theirs) return []
  const out: string[] = []
  if (theirs.title !== base.title) out.push('title')
  if (theirs.slug !== base.slug) out.push('slug')
  if (theirs.status !== base.status) out.push('status')
  if (theirs.body !== base.body) out.push('text')
  return out
})

async function startEdit(focus: 'title' | 'body' = 'title') {
  const current = entry.value
  if (!writable.value || !current || editing.value) return
  base = snapshot(current)
  Object.assign(draft, { ...base, details: { ...base.details } })
  stamp.value = current.updated_at
  touched.value = false; slugError.value = ''; conflict.value = null; comparing.value = false
  editing.value = true
  await nextTick()
  if (focus === 'title') { const el = titleField.value; el?.focus(); el?.setSelectionRange(el.value.length, el.value.length); growTitle() }
  else { growTitle(); editor.value?.focus() }
}
function growTitle() { const el = titleField.value; if (el) { el.style.height = 'auto'; el.style.height = `${el.scrollHeight}px` } }
async function cancelEdit() {
  if (dirty.value && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${entry.value?.slug ?? 'this entry'} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
  editing.value = false; conflict.value = null
  void nextTick(() => root.value?.focus({ preventScroll: true }))
}
async function save() {
  const current = entry.value
  if (!current || !writable.value || saving.value) return
  touched.value = true
  if (titleIssue.value) { titleField.value?.focus(); return }
  if (slugIssue.value) { slugField.value?.focus(); return }
  if (detailFields.value.some(field => detailIssue(field.key, field.kind))) return
  if (!dirty.value && !conflict.value) { editing.value = false; return }
  const patch: KnowledgePatch = {}
  if (draft.title.trim() !== current.title) patch.title = draft.title.trim()
  if (draft.body !== current.body) patch.body = draft.body
  if (draft.status !== current.status) patch.status = draft.status
  if (draft.slug.trim() !== current.slug) patch.slug = draft.slug.trim()
  if (detailsChanged()) patch.metadata = mergeDetails(current.type, current.metadata, draft.details)
  if (!Object.keys(patch).length) { editing.value = false; conflict.value = null; return }
  const request = generation
  saving.value = true
  try {
    const saved = await updateKnowledge(current.id, patch, stamp.value)
    if (request !== generation) return
    editing.value = false; conflict.value = null
    applySaved(saved, current.slug)
    const renamed = saved.slug !== current.slug
    toast(renamed ? `Saved as ${saved.slug}. Agents need the new slug.` : `Saved ${saved.slug}`, saved.event_id ? { action: { label: 'Undo', run: () => void undo(saved.event_id!, saved.id) } } : {})
    void nextTick(() => root.value?.focus({ preventScroll: true }))
  } catch (e) {
    if (request !== generation) return
    if (e instanceof KnowledgeError && e.status === 412 && e.entry) { conflict.value = e.entry; comparing.value = false }
    else if (e instanceof KnowledgeError && e.code === 'slug_taken') { slugError.value = e.message; slugField.value?.focus() }
    else if (e instanceof KnowledgeError && e.status === 403) { readOnly.value = true; toast(e.message, { tone: 'error' }) }
    else if (e instanceof KnowledgeError && e.status === 404) { missing.value = true; editing.value = false }
    else toast(`Not saved: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
  } finally { if (request === generation || entry.value?.id === current.id && currentEntry.value) saving.value = false }
}
// The server's copy replaces ours everywhere; a new slug moves the address.
function applySaved(saved: KnowledgeEntry, previousSlug: string) {
  // A route load started for the previous slug must not replace this newer
  // server response after a rename or its undo.
  generation++
  loading.value = false; missing.value = false; error.value = ''
  entry.value = saved
  loadedLocator.value = locator()
  props.state.upsert(saved)
  if (saved.slug !== previousSlug || saved.type !== props.type) void router.replace(placeOf(saved.type, saved.slug))
}
async function undo(eventId: number, entryId: string) {
  const current = entry.value, request = generation
  if (!current || current.id !== entryId || !currentEntry.value) return
  try {
    await undoKnowledge(eventId)
    if (request !== generation) return
    const restored = await getKnowledge(current.id)
    if (request !== generation) return
    applySaved(restored, current.slug)
    toast(restored.slug !== current.slug ? `Undone. It is ${restored.slug} again.` : 'Undone')
  } catch (e) { toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' }) }
}
// A newer version arrived: keep writing on top of it, or take it. Keeping merges:
// what you changed stays yours, everything you left alone becomes theirs.
function keepMine() {
  const theirs = conflict.value
  if (!theirs) return
  const next = snapshot(theirs)
  if (draft.title === base.title) draft.title = next.title
  if (draft.slug === base.slug) draft.slug = next.slug
  if (draft.status === base.status) draft.status = next.status
  if (draft.body === base.body) draft.body = next.body
  for (const key of Object.keys(next.details)) if ((draft.details[key] ?? '') === (base.details[key] ?? '')) draft.details[key] = next.details[key]
  entry.value = theirs
  props.state.upsert(theirs)
  base = next
  stamp.value = theirs.updated_at
  conflict.value = null; comparing.value = false
  toast('Your changes stay on top of their version. Save when you are ready.')
  void nextTick(growTitle)
}
function useTheirs() {
  const theirs = conflict.value
  if (!theirs) return
  entry.value = theirs
  props.state.upsert(theirs)
  base = snapshot(theirs)
  Object.assign(draft, { ...base, details: { ...base.details } })
  stamp.value = theirs.updated_at
  conflict.value = null; comparing.value = false
  void nextTick(growTitle)
}
// While editing, a quiet look for someone else's save when the window comes back.
let checkingNewer = false
async function checkNewer() {
  const current = entry.value
  if (checkingNewer || !editing.value || !current || conflict.value || document.visibilityState !== 'visible') return
  checkingNewer = true
  try {
    const latest = await getKnowledge(current.id)
    if (editing.value && entry.value?.id === current.id && latest.updated_at !== stamp.value) conflict.value = latest
  } catch { /* the save will tell */ }
  finally { checkingNewer = false }
}
function editKeys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); void save() }
  else if (event.key === 'Escape' && !document.querySelector('dialog[open], .floating')) { event.preventDefault(); event.stopPropagation(); void cancelEdit() }
}
function setStatus(status: KnowledgeStatus) { draft.status = status }
function statusKeys(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return
  event.preventDefault()
  const index = STATUSES.findIndex(s => s.status === draft.status)
  const next = STATUSES[(index + (event.key === 'ArrowRight' ? 1 : -1) + STATUSES.length) % STATUSES.length]
  draft.status = next.status
  void nextTick(() => (event.currentTarget as HTMLElement | null)?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}

// ---------- More: copy, archive, delete ----------
function toggleMore(event: MouseEvent) { moreAnchor.value = moreAnchor.value ? null : event.currentTarget as HTMLElement }
function closeMore(restore: boolean) { moreAnchor.value = null; if (restore) moreButton.value?.focus() }
function menuKeys(event: KeyboardEvent) {
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault(); event.stopPropagation()
    items[event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)]?.focus()
  }
}
async function pick(action: 'link' | 'slug' | 'command' | 'archive' | 'delete') {
  moreAnchor.value = null
  const current = entry.value
  if (!current) return
  if (action === 'link') void copy(linkFor(''), 'the link')
  else if (action === 'slug') void copy(current.slug, current.slug)
  else if (action === 'command') void copy(oldCommand.value, 'the command')
  else if (action === 'archive') await setArchived(current.status !== 'archived')
  else await remove()
}
async function setArchived(archived: boolean) {
  const request = generation
  const current = entry.value
  if (!current || !writable.value) return
  try {
    const saved = await updateKnowledge(current.id, { status: archived ? 'archived' : 'active' }, current.updated_at)
    if (request !== generation) return
    applySaved(saved, current.slug)
    toast(archived ? `Archived ${saved.slug}. Agents skip it now.` : `${saved.slug} is active again`, saved.event_id ? { action: { label: 'Undo', run: () => void undo(saved.event_id!, saved.id) } } : {})
  } catch (e) {
    if (request !== generation) return
    if (e instanceof KnowledgeError && e.status === 412 && e.entry) { entry.value = e.entry; props.state.upsert(e.entry); toast('Someone changed this entry just now. The newer version is shown; nothing was archived.', { tone: 'error' }) }
    else toast(e instanceof Error ? e.message : 'That did not work.', { tone: 'error' })
  }
}
async function confirmProposed() {
  const request = generation
  const current = entry.value
  if (!current || !writable.value) return
  try {
    const saved = await updateKnowledge(current.id, { status: 'active' }, current.updated_at)
    if (request !== generation) return
    applySaved(saved, current.slug)
    toast(`${saved.slug} is confirmed. Agents rely on it now.`, saved.event_id ? { action: { label: 'Undo', run: () => void undo(saved.event_id!, saved.id) } } : {})
  } catch (e) {
    if (request !== generation) return
    if (e instanceof KnowledgeError && e.status === 412 && e.entry) { entry.value = e.entry; props.state.upsert(e.entry); toast('Someone changed this entry just now. Read the newer version before you confirm it.', { tone: 'error' }) }
    else toast(e instanceof Error ? e.message : 'That did not work.', { tone: 'error' })
  }
}
async function remove() {
  const request = generation
  const current = entry.value
  if (!current || !currentEntry.value || !props.canDelete) return
  const ok = await confirmAction({
    title: `Delete ${current.slug}?`,
    body: `“${current.title}” leaves the project’s knowledge and agents stop finding it. You can undo this right after; the history stays in the audit log.`,
    confirmLabel: `Delete ${meta.value.label.toLowerCase()}`, danger: true,
  })
  if (!ok || request !== generation) return
  try {
    const result = await deleteKnowledge(current.id, current.updated_at)
    if (request !== generation) return
    props.state.remove(current.id)
    skipGuard = true
    await router.replace(listLink())
    skipGuard = false
    toast(`Deleted ${current.type} ${current.slug}`, { action: { label: 'Undo', run: () => void restore(result.event_id, current) } })
  } catch (e) {
    if (request !== generation) return
    toast(e instanceof KnowledgeError && e.status === 412 ? 'Someone changed this entry just now, so it was not deleted.' : `Not deleted: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
    if (e instanceof KnowledgeError && e.entry) { entry.value = e.entry; props.state.upsert(e.entry) }
  }
}
async function restore(eventId: number, gone: KnowledgeEntry) {
  try {
    await undoKnowledge(eventId)
    await props.state.load()
    toast(`${gone.slug} is back`, { action: { label: 'Open', run: () => void router.push(placeOf(gone.type, gone.slug, false)) } })
  } catch (e) { toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' }) }
}

// ---------- Keys: e edits, j k next and previous, Esc back to the list ----------
let skipGuard = false
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || editing.value) return
  if (document.querySelector('dialog[open], .floating') || typing(event.target)) return
  switch (event.key) {
    case 'e': if (writable.value && entry.value) { event.preventDefault(); void startEdit() } break
    // Docked, the list moves the selection (and so this pane) with j and k.
    case 'j': if (!dock.value) { event.preventDefault(); go(1) } break
    case 'k': if (!dock.value) { event.preventDefault(); go(-1) } break
    // Beside the graph, Escape clears the graph's selection, which closes this pane too.
    case 'Escape': if (dock.value && route.query.mode === 'graph') return; event.preventDefault(); emit('close'); break
  }
}
// Opening a new entry from the Create dialog starts in the text.
watch(entry, current => {
  if (!current || editing.value) return
  const wanted = window.history.state?.knowledgeEdit
  if (wanted && writable.value) {
    window.history.replaceState({ ...window.history.state, knowledgeEdit: null }, '')
    void startEdit(wanted === 'body' ? 'body' : 'title')
  }
})
const poll = usePoller(checkNewer, 60_000, { enabled: () => editing.value && !!entry.value && !conflict.value })
onMounted(() => {
  window.addEventListener('keydown', keydown)
  window.addEventListener('focus', checkNewer)
  wideQuery.addEventListener('change', onWide)
  listenScroll()
  poll.start()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', keydown)
  window.removeEventListener('focus', checkNewer)
  wideQuery.removeEventListener('change', onWide)
  scrollListener?.removeEventListener('scroll', scrolledToEnd)
  poll.stop(); spy?.disconnect()
})
function applyServer(next: KnowledgeEntry) {
  if (editing.value || next.type !== props.type || next.slug !== props.slug) return
  entry.value = next
}
async function reload() {
  if (editing.value) return
  await load()
}
function discard() { generation++; editing.value = false; saving.value = false; conflict.value = null }
defineExpose({ discard, isDirty: () => !skipGuard && dirty.value, startEdit, editing, entryId: () => entry.value?.id ?? null, applyServer, reload })
const whoUpdated = computed(() => entry.value?.imported ? 'imported' : entry.value?.updated_by ? `by ${entry.value.updated_by.name}` : '')
</script>

<template>
  <component :is="dock ? 'aside' : 'article'" ref="root" class="entry-page" :class="[mode, { editing }]" tabindex="-1" :data-loaded="entry && entry.type === type && entry.slug === slug ? 'true' : 'false'" :aria-label="entry ? `${meta.label}: ${entry.title}` : 'Knowledge entry'">
    <!-- The bar: back, what it is, where it is in the list, and the actions. -->
    <header class="e-bar"><div class="e-bar-inner">
      <RouterLink v-if="!dock" class="icon-btn sm flat" :to="listLink()" aria-label="Back to Knowledge" data-tip="Back to Knowledge · Esc" @click.prevent="emit('close')"><AppIcon name="chevron-left" :size="16" /></RouterLink>
      <button type="button" class="kind-chip" :aria-label="`Copy the slug ${entry?.slug ?? slug}`" :data-tip="`Copy ${meta.label.toLowerCase()} slug`" @click="copy(entry?.slug ?? slug, entry?.slug ?? slug)">
        <AppIcon :name="meta.icon" :size="13" /><span class="kind-type">{{ meta.label }}</span><span class="kind-slug">{{ entry?.slug ?? slug }}</span><AppIcon name="copy" :size="11" class="copy-glyph" />
      </button>
      <template v-if="position && !editing">
        <span class="position mono">{{ position.index + 1 }} / {{ position.count }}</span>
        <span class="nav">
          <button type="button" class="icon-btn sm flat" aria-label="Previous entry" aria-keyshortcuts="k" data-tip="Previous · k" :disabled="position.index === 0" @click="go(-1)"><AppIcon name="chevron-up" :size="15" /></button>
          <button type="button" class="icon-btn sm flat" aria-label="Next entry" aria-keyshortcuts="j" data-tip="Next · j" :disabled="position.index >= position.count - 1" @click="go(1)"><AppIcon name="chevron" :size="15" /></button>
        </span>
      </template>
      <span class="spacer" />
      <template v-if="editing">
        <span v-if="dirty" class="unsaved" aria-live="polite">Unsaved</span>
        <button type="button" class="btn sm ghost" :disabled="saving" aria-keyshortcuts="Escape" data-tip="Cancel · Esc" @click="cancelEdit">Cancel</button>
        <button type="button" class="btn sm primary" :disabled="saving" data-tip="Save · Cmd or Ctrl Enter" @click="save"><AppIcon name="check" :size="13" />{{ saving ? 'Saving…' : conflict ? 'Save over theirs' : 'Save' }}</button>
      </template>
      <template v-else-if="entry">
        <button v-if="writable" type="button" class="btn sm edit-btn" aria-keyshortcuts="e" data-tip="Edit the text, title, slug and status · e" @click="startEdit()"><AppIcon name="edit" :size="13" />Edit</button>
        <button v-if="dock" type="button" class="icon-btn sm flat" aria-label="Open as full page" data-tip="Full page" @click="expand"><AppIcon name="expand" :size="14" /></button>
        <button ref="moreButton" type="button" class="icon-btn sm flat" aria-label="More actions" aria-haspopup="menu" :aria-expanded="!!moreAnchor" data-tip="More" @click="toggleMore"><AppIcon name="more" :size="15" /></button>
      </template>
      <button v-if="dock && !editing" type="button" class="icon-btn sm flat" aria-label="Close the preview" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
    </div></header>

    <div ref="scroller" class="e-scroll" :class="{ switching }" :aria-busy="switching || undefined">

    <div v-if="missing" class="e-state glass-card" role="alert">
      <span class="state-icon"><AppIcon :name="meta.icon" :size="20" /></span>
      <h2>No {{ meta.label.toLowerCase() }} called “{{ slug }}” in {{ project.title }}</h2>
      <p>It may have been deleted, or the link is older than its last rename that anyone recorded.</p>
      <div class="state-actions">
        <RouterLink class="btn" :to="{ path: `/p/${encodeURIComponent(project.routeKey)}/knowledge`, query: { q: slug } }"><AppIcon name="search" :size="14" />Search for “{{ slug }}”</RouterLink>
        <button type="button" class="btn" @click="emit('close')">{{ dock ? 'Close' : 'Back to Knowledge' }}</button>
      </div>
    </div>
    <div v-else-if="error" class="e-state glass-card" role="alert">
      <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>This entry could not be opened</h2>
      <p>{{ error }}</p>
      <button type="button" class="btn" @click="load()"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="!entry" class="e-skeleton" role="status" aria-label="Loading the entry">
      <template v-if="listed">
        <p class="e-eyebrow"><span class="e-type" :style="{ '--kind': `var(${kindToken(type)})` }"><AppIcon :name="meta.icon" :size="13" />{{ meta.label }}</span></p>
        <p class="e-title sk-known">{{ listed.title }}</p>
      </template>
      <template v-else><span class="skeleton sk-eyebrow" /><span class="skeleton sk-title" /></template>
      <span class="skeleton sk-meta" />
      <span v-for="n in 7" :key="n" class="skeleton sk-line" :style="{ width: `${62 + (n * 17) % 34}%` }" />
    </div>

    <!-- Edit mode: the whole entry as one form. -->
    <form v-else-if="editing" class="e-edit" :aria-label="`Edit ${entry.slug}`" novalidate @submit.prevent="save" @keydown="editKeys">
      <div v-if="conflict" class="e-conflict" role="alert">
        <div class="conflict-head">
          <span class="conflict-icon"><AppIcon name="history" :size="16" /></span>
          <div class="conflict-text">
            <p class="conflict-title">{{ conflict.updated_by?.name ?? 'Someone' }} saved a newer version {{ relativeTime(conflict.updated_at, { now, long: true }) }}</p>
            <p class="conflict-sub">They changed the {{ conflictFields.length ? listWords(conflictFields) : 'details' }}. Your draft is kept. Keep your changes on top of theirs, or take their version.</p>
          </div>
        </div>
        <div class="conflict-actions">
          <button type="button" class="btn sm" :aria-pressed="comparing" @click="comparing = !comparing"><AppIcon name="compare" :size="13" />{{ comparing ? 'Hide the comparison' : 'Compare the text' }}</button>
          <button type="button" class="btn sm" @click="useTheirs">Use their version</button>
          <button type="button" class="btn sm primary" @click="keepMine">Keep my changes</button>
        </div>
        <figure v-if="comparing" class="conflict-diff">
          <figcaption class="diff-caption"><span>Their text</span><AppIcon name="arrow" :size="12" /><span>your draft</span></figcaption>
          <ol class="diff-lines" aria-label="Their text against your draft">
            <li v-for="(line, i) in diffLines" :key="i" class="diff-line" :class="line.kind">
              <span class="gutter" aria-hidden="true"><AppIcon v-if="line.kind === 'add'" name="plus" :size="11" /><AppIcon v-else-if="line.kind === 'remove'" name="minus" :size="11" /></span>
              <span v-if="line.kind !== 'same'" class="sr-only">{{ line.kind === 'add' ? 'Only in your draft:' : 'Only in theirs:' }}</span>
              <span class="text">{{ line.text || ' ' }}</span>
            </li>
            <li v-if="!diffLines.some(line => line.kind !== 'same')" class="diff-line same"><span class="gutter" /><span class="text calm">The text is the same; they changed the {{ conflictFields.length ? listWords(conflictFields) : 'details' }}.</span></li>
          </ol>
        </figure>
      </div>

      <label class="sr-only" for="e-title">Title</label>
      <textarea id="e-title" ref="titleField" v-model="draft.title" class="edit-title" rows="1" maxlength="500" placeholder="Title" :aria-invalid="touched && !!titleIssue" @input="growTitle" @keydown.enter.exact.prevent />
      <p v-if="touched && titleIssue" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ titleIssue }}</p>

      <div class="e-props">
        <div class="e-prop e-prop-kind">
          <span class="prop-label">Kind</span>
          <span class="kind-static"><AppIcon :name="meta.icon" :size="14" />{{ meta.label }}</span>
        </div>
        <div class="e-prop e-prop-slug">
          <label class="prop-label" for="e-slug">Slug</label>
          <div class="slug-box" :class="{ bad: (touched || slugChanged) && !!slugIssue, changed: slugChanged && !slugIssue }">
            <span class="slug-prefix mono" aria-hidden="true">{{ entry.type }}/</span>
            <input id="e-slug" ref="slugField" v-model="draft.slug" class="slug-input mono" maxlength="64" autocomplete="off" spellcheck="false" autocapitalize="off" :aria-invalid="(touched || slugChanged) && !!slugIssue" aria-describedby="e-slug-note" @input="slugError = ''" />
          </div>
        </div>
        <div class="e-prop">
          <span id="e-status-label" class="prop-label">Status</span>
          <div class="seg status-seg" role="radiogroup" aria-labelledby="e-status-label" @keydown="statusKeys">
            <button v-for="option in STATUSES" :key="option.status" type="button" role="radio" :aria-checked="draft.status === option.status" :tabindex="draft.status === option.status ? 0 : -1" :data-tip="option.hint" @click="setStatus(option.status)">{{ option.label }}</button>
          </div>
        </div>
      </div>
      <p v-if="(touched || slugChanged) && slugIssue" id="e-slug-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ slugIssue }}</p>
      <div v-else-if="slugChanged" id="e-slug-note" class="e-rename" role="note">
        <AppIcon name="alert" :size="15" />
        <div>
          <p class="rename-title">Agents look this entry up by its slug</p>
          <p>After the rename, <code>{{ oldCommand }}</code> no longer finds it. Update the prompts, skills and runbooks that name <code>{{ entry.slug }}</code>. Links in this app follow the rename.</p>
        </div>
      </div>

      <div v-if="detailFields.length" class="f-grid e-details-grid">
        <div v-for="field in detailFields" :key="field.key" class="f-row" :class="{ wide: field.kind === 'text' && detailFields.length === 1 }">
          <label class="f-label" :for="`e-detail-${field.key}`">{{ field.label }} <span class="opt">optional</span></label>
          <select v-if="field.kind === 'choice'" :id="`e-detail-${field.key}`" v-model="draft.details[field.key]" class="field">
            <option value="">Not set</option>
            <option v-for="choice in field.choices" :key="choice" :value="choice">{{ choice[0].toUpperCase() + choice.slice(1) }}</option>
          </select>
          <input
            v-else :id="`e-detail-${field.key}`" v-model="draft.details[field.key]" class="field" :class="{ mono: field.kind === 'url' }" :type="field.kind === 'url' ? 'url' : 'text'"
            :inputmode="field.kind === 'url' ? 'url' : undefined" :placeholder="field.placeholder" autocomplete="off" :aria-invalid="!!detailIssue(field.key, field.kind)" :aria-describedby="`e-detail-${field.key}-note`"
          />
          <p v-if="detailIssue(field.key, field.kind)" :id="`e-detail-${field.key}-note`" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ detailIssue(field.key, field.kind) }}</p>
          <p v-else-if="field.hint" :id="`e-detail-${field.key}-note`" class="f-note">{{ field.hint }}</p>
        </div>
      </div>

      <section class="e-edit-body" aria-labelledby="e-body-label">
        <h3 id="e-body-label" class="eyebrow">Text</h3>
        <MarkdownEditor
          ref="editor" v-model="draft.body" label="Text" bare :split="wide && !dock" :min-rows="wide && !dock ? 18 : 12"
          :placeholder="`What should someone know before they ${entry.type === 'runbook' ? 'run this' : entry.type === 'guideline' ? 'follow this' : 'rely on this'}? Use ## headings; long entries get a table of contents.`"
          @save="save" @cancel="cancelEdit"
        />
      </section>
      <p class="edit-hint"><KeyCap k="mod" /><KeyCap k="enter" /> save · <kbd class="keycap">esc</kbd> cancel · <kbd class="keycap">##</kbd> headings make sections</p>
    </form>

    <!-- Reading. -->
    <div v-else class="e-grid" :class="{ 'has-toc': toc.length }">
      <nav v-if="state.groups.value.length" class="e-rail" aria-label="Entries in this project">
        <RouterLink class="rail-all" :to="listLink()"><AppIcon name="book" :size="14" />All knowledge<span class="mono">{{ state.sequence.value.length }}</span></RouterLink>
        <section v-for="group in state.groups.value" :key="group.type" class="rail-group" :aria-label="group.meta.plural">
          <p class="rail-label"><AppIcon :name="group.meta.icon" :size="12" />{{ group.meta.plural }}</p>
          <RouterLink
            v-for="item in group.items" :key="item.id" class="rail-item" :class="{ archived: item.status === 'archived' }"
            :aria-current="item.id === entry.id ? 'page' : undefined" :to="{ path: entryPath(project.routeKey, item.type, item.slug), query: listQuery }" replace
          >{{ item.title }}</RouterLink>
        </section>
      </nav>

      <div ref="article" class="e-article">
        <p class="e-eyebrow"><span class="e-type" :style="{ '--kind': `var(${kindToken(entry.type)})` }"><AppIcon :name="meta.icon" :size="13" />{{ meta.label }}</span><span v-if="entry.status !== 'active'" class="k-status" :class="entry.status">{{ statusLabel(entry.status) }}</span></p>
        <h1 class="e-title">{{ entry.title }}</h1>
        <p class="e-byline dot-list">
          <span>Updated <time :datetime="entry.updated_at" :data-tip="absoluteTime(entry.updated_at)">{{ relativeTime(entry.updated_at, { now, long: true }) }}</time> {{ whoUpdated }}</span>
          <span v-if="minutes >= 2">{{ minutes }} min read</span>
          <span class="mono e-key">{{ entry.key }}</span>
        </p>
        <p v-if="entry.status === 'archived'" class="e-note" role="note"><AppIcon name="archive" :size="14" /><span class="note-text">Archived: kept for the record, and agents skip it.</span><button v-if="writable" type="button" class="inline-link" @click="setArchived(false)">Make it active</button></p>
        <p v-else-if="entry.status === 'proposed'" class="e-note proposed" role="note">
          <AppIcon name="sparkle" :size="14" /><span class="note-text">Proposed{{ entry.author ? ` by ${entry.author.name}` : '' }}: a draft waiting for a person to confirm it.</span>
          <span v-if="writable" class="note-actions"><button type="button" class="inline-link" @click="startEdit('body')">Edit first</button><button type="button" class="btn sm" @click="confirmProposed"><AppIcon name="check" :size="13" />Confirm</button></span>
        </p>
        <aside v-if="rule" class="e-rule" aria-label="The rule"><span class="rule-label">The rule</span><p>{{ rule }}</p></aside>
        <aside v-if="headline" class="e-rule e-where" :aria-label="entry.type === 'external-system' ? 'Where it lives' : 'The project'">
          <span class="rule-label">{{ entry.type === 'external-system' ? 'Where it lives' : 'The project' }}</span>
          <a v-if="headline.address" class="where-link" :href="headline.address" target="_blank" rel="noopener noreferrer">{{ headline.address.replace(/^https?:\/\//, '').replace(/\/$/, '') }}<AppIcon name="external" :size="13" /></a>
          <p v-if="headline.about" class="where-about">{{ headline.about }}</p>
        </aside>

        <!-- Docked, what it is sits under the title: the pane has no side column. -->
        <dl v-if="dock" class="e-facts" aria-label="Details">
          <div :class="{ wide: entry.slug.length > 24 }"><dt>Slug</dt><dd><button type="button" class="slug-copy mono" :aria-label="`Copy the slug ${entry.slug}`" data-tip="Copy the slug" @click="copy(entry.slug, entry.slug)">{{ entry.slug }}<AppIcon name="copy" :size="11" /></button></dd></div>
          <div><dt>Status</dt><dd>{{ statusLabel(entry.status) }}</dd></div>
          <div><dt>Written by</dt><dd :class="{ unset: !entry.author }">{{ entry.author?.name ?? (entry.imported ? 'Imported' : 'Unknown') }}</dd></div>
          <div><dt>Key</dt><dd class="mono">{{ entry.key }}</dd></div>
          <div v-for="field in details" :key="field.key" class="wide">
            <dt>{{ field.label }}</dt>
            <dd v-if="field.kind === 'url' && validUrl(field.value)"><a :href="field.value" target="_blank" rel="noopener noreferrer" class="ext-link">{{ field.value.replace(/^https?:\/\//, '') }}<AppIcon name="external" :size="11" /></a></dd>
            <dd v-else :class="{ mono: field.key === 'secret_path' || field.key === 'key' }">{{ field.value }}</dd>
          </div>
        </dl>

        <details v-if="toc.length" class="e-toc-inline">
          <summary><AppIcon name="chevron-right" :size="13" class="disclosure-chev" />On this page<span class="mono">{{ toc.length }}</span></summary>
          <ol><li v-for="h in toc" :key="h.id" :class="`l${h.level}`"><a :href="`#${h.id}`" @click.prevent="jump(h.id)">{{ h.text }}</a></li></ol>
        </details>

        <MarkdownBody v-if="body.trim()" class="e-body" :body="body" anchors @headings="list => headings = list" @anchor="anchor" @jump="jump" />
        <div v-else class="e-empty">
          <p>Nothing written yet.</p>
          <button v-if="writable" type="button" class="btn" @click="startEdit('body')"><AppIcon name="edit" :size="14" />Write it</button>
        </div>
      </div>

      <aside class="e-aside" aria-label="About this entry">
        <section class="e-card" aria-labelledby="e-agents-title">
          <p id="e-agents-title" class="card-title"><AppIcon name="terminal" :size="14" />Agents read this</p>
          <p class="card-text">By its slug <code>{{ entry.slug }}</code>. Renaming it means updating every prompt that names it.</p>
          <span class="k-command"><code><template v-for="(part, i) in oldCommand.split(' ')" :key="i"><span class="tok">{{ part }}</span>{{ ' ' }}</template></code><button type="button" class="icon-btn sm flat" aria-label="Copy the agent command" data-tip="Copy" @click="copy(oldCommand, 'the command')"><AppIcon name="copy" :size="13" /></button></span>
        </section>

        <section v-if="!dock" class="e-card" aria-labelledby="e-details-title">
          <p id="e-details-title" class="card-title"><AppIcon name="info" :size="14" />Details</p>
          <dl class="facts">
            <div><dt>Kind</dt><dd>{{ meta.label }}</dd></div>
            <div><dt>Status</dt><dd>{{ statusLabel(entry.status) }}</dd></div>
            <div><dt>Written by</dt><dd :class="{ unset: !entry.author }">{{ entry.author?.name ?? (entry.imported ? 'Imported' : 'Unknown') }}</dd></div>
            <div><dt>Created</dt><dd><time :datetime="entry.created_at" :data-tip="absoluteTime(entry.created_at)">{{ relativeTime(entry.created_at, { now, long: true }) }}</time></dd></div>
            <div><dt>Updated</dt><dd><time :datetime="entry.updated_at" :data-tip="`${absoluteTime(entry.updated_at)} ${whoUpdated}`">{{ relativeTime(entry.updated_at, { now, long: true }) }}</time></dd></div>
            <div><dt>Key</dt><dd class="mono">{{ entry.key }}</dd></div>
            <div v-for="field in details" :key="field.key" class="wide">
              <dt>{{ field.label }}</dt>
              <dd v-if="field.kind === 'url' && validUrl(field.value)"><a :href="field.value" target="_blank" rel="noopener noreferrer" class="ext-link">{{ field.value.replace(/^https?:\/\//, '') }}<AppIcon name="external" :size="11" /></a></dd>
              <dd v-else :class="{ mono: field.key === 'secret_path' || field.key === 'key' }">{{ field.value }}</dd>
            </div>
          </dl>
        </section>

        <section class="e-card" aria-labelledby="e-links-title">
          <p id="e-links-title" class="card-title"><AppIcon name="link" :size="14" />Linked<span v-if="entry.links.length" class="mono card-count">{{ entry.links.length }}</span></p>
          <p v-if="!entry.links.length" class="card-text">No tickets or entries link here yet. Tickets that cite this entry show up here.</p>
          <div v-for="group in linkGroups" :key="group.label" class="link-group">
            <p class="link-label">{{ group.label }}</p>
            <ul class="links">
              <li v-for="l in group.items" :key="l.relation_id">
                <RouterLink v-if="linkTarget(l)" class="link-row" :to="linkTarget(l)!" :data-tip="`${relationLabel(l)} · ${statusMeta(l.node.state).label}`">
                  <StatusIcon v-if="!l.node.type" :state="l.node.state" :size="12" /><AppIcon v-else :name="typeMeta(l.node.type).icon" :size="12" class="link-kind" />
                  <span class="link-key mono">{{ l.node.type ? l.node.slug : l.node.key }}</span><span class="link-title">{{ l.node.title }}</span>
                </RouterLink>
                <span v-else class="link-row"><StatusIcon :state="l.node.state" :size="12" /><span class="link-key mono">{{ l.node.key }}</span><span class="link-title">{{ l.node.title }}</span></span>
              </li>
            </ul>
          </div>
        </section>

        <!-- Last, so it stays in view while the text scrolls (the column is as tall as the text). -->
        <nav v-if="toc.length" class="e-toc" aria-labelledby="e-toc-title">
          <p id="e-toc-title" class="eyebrow">On this page</p>
          <ol>
            <li v-for="h in toc" :key="h.id" :class="`l${h.level}`">
              <a :href="`#${h.id}`" :aria-current="activeHeading === h.id ? 'location' : undefined" @click.prevent="jump(h.id)">{{ h.text }}</a>
            </li>
          </ol>
        </nav>
      </aside>
    </div>
    </div>

    <FloatingPanel v-if="moreAnchor && entry" :anchor="moreAnchor" :width="248" align="end" :label="`Actions for ${entry.slug}`" @close="closeMore">
      <div class="more-menu" role="menu" :aria-label="`Actions for ${entry.slug}`" @keydown="menuKeys">
        <button type="button" role="menuitem" class="menu-item" data-autofocus @click="pick('link')"><AppIcon name="link" :size="14" />Copy link</button>
        <button type="button" role="menuitem" class="menu-item" @click="pick('slug')"><AppIcon name="copy" :size="14" />Copy slug</button>
        <button type="button" role="menuitem" class="menu-item" @click="pick('command')"><AppIcon name="terminal" :size="14" />Copy the agent command</button>
        <template v-if="writable || canDelete">
          <div class="menu-sep" role="separator" />
          <button v-if="writable" type="button" role="menuitem" class="menu-item" @click="pick('archive')"><AppIcon name="archive" :size="14" />{{ entry.status === 'archived' ? 'Make active again' : 'Archive' }}</button>
          <button v-if="canDelete" type="button" role="menuitem" class="menu-item danger" @click="pick('delete')"><AppIcon name="trash" :size="14" />Delete {{ meta.label.toLowerCase() }}…</button>
        </template>
      </div>
    </FloatingPanel>
  </component>
</template>

<style scoped>
.entry-page { width: 100%; padding-bottom: 32px; outline: none; }
.e-grid, .e-edit { max-width: 1480px; margin-inline: auto; }
/* ---------- The bar ---------- */
.e-bar-inner { display: flex; align-items: center; gap: 6px; max-width: 1480px; height: 52px; margin: 0 auto; }
.e-bar { position: sticky; top: 0; z-index: 6; margin: 0 calc(-1 * var(--gutter)); padding: 0 var(--gutter); background: var(--glass); box-shadow: 0 1px 0 var(--line); -webkit-backdrop-filter: blur(18px) saturate(1.2); backdrop-filter: blur(18px) saturate(1.2); }
.kind-chip { display: inline-flex; flex-shrink: 1; align-items: center; gap: 7px; min-width: 0; height: 28px; margin-left: 2px; padding: 0 10px 0 9px; border: 0; border-radius: 8px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.kind-chip:hover { box-shadow: inset 0 0 0 1px var(--teal); }
.kind-chip:focus-visible { box-shadow: var(--focus-ring); }
.kind-type { font: 600 10px/1 var(--mono); letter-spacing: .1em; text-transform: uppercase; font-variant-ligatures: none; }
.kind-slug { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 600 12px/1 var(--mono); font-variant-ligatures: none; }
.copy-glyph { opacity: .45; }
.kind-chip:hover .copy-glyph { opacity: .9; }
.position { flex-shrink: 0; margin-left: 8px; font-size: 11.5px; color: var(--ink-3); }
.nav { display: inline-flex; gap: 2px; }
.nav .icon-btn:disabled { opacity: .35; }
.spacer { flex: 1; }
.unsaved { font-size: 12px; font-weight: 600; color: var(--gold-ink); margin-right: 4px; }
.edit-btn { gap: 6px; margin-right: 2px; }

/* ---------- Reading layout: rail, text, aside ---------- */
.e-grid { display: grid; grid-template-columns: 236px minmax(0, 1fr) 300px; gap: 44px; align-items: start; padding-top: 30px; }
.e-rail { position: sticky; top: 72px; display: grid; gap: 14px; max-height: calc(100dvh - var(--header-h) - var(--footer-h) - 96px); overflow: auto; padding: 2px 6px 12px 2px; overscroll-behavior: contain; }
.rail-all { display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 8px; border-radius: 8px; color: var(--ink-2); font-size: 13px; font-weight: 600; }
.rail-all .mono { margin-left: auto; font-size: 11px; font-weight: 500; color: var(--ink-3); }
.rail-all:hover { background: var(--row-hover); color: var(--ink); }
.rail-group { display: grid; gap: 1px; }
.rail-label { display: flex; align-items: center; gap: 6px; padding: 0 8px 4px; font: 500 10px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.rail-item { display: block; padding: 5px 8px; border-radius: 7px; color: var(--ink-2); font-size: 13px; line-height: 1.35; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rail-item:hover { background: var(--row-hover); color: var(--ink); }
.rail-item.archived { color: var(--ink-3); }
.rail-item[aria-current="page"] { background: var(--row-selected); color: var(--teal-ink); font-weight: 600; box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.rail-all:focus-visible, .rail-item:focus-visible { box-shadow: var(--focus-ring); }
.e-article { min-width: 0; max-width: 760px; }
.e-eyebrow { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.e-type { display: inline-flex; align-items: center; gap: 6px; font: 500 10.5px/1.5 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--teal-ink); font-variant-ligatures: none; }
.e-type svg { color: var(--kind, currentColor); }
.k-status { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; font-variant-ligatures: none; }
.k-status.proposed { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: var(--gold-ink); }
.k-status.archived { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.e-title { font: 350 clamp(28px, 2.6vw, 38px)/1.12 var(--serif); letter-spacing: -.025em; color: var(--ink); overflow-wrap: anywhere; }
.e-byline { margin-top: 12px; font-size: 13px; color: var(--ink-2); }
.e-byline time { color: var(--ink); }
.e-key { font-size: 12px; color: var(--ink-3); }
.e-note { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 18px; padding: 10px 14px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); font-size: 13px; color: var(--ink-2); }
.e-note svg { color: var(--ink-3); flex-shrink: 0; }
.e-note.proposed { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); color: var(--ink); }
.e-note.proposed svg { color: var(--gold-ink); }
.note-text { flex: 1 1 220px; min-width: 0; }
.note-actions { display: inline-flex; align-items: center; gap: 12px; margin-left: auto; }
.note-actions .inline-link { margin-left: 0; }
.inline-link { margin-left: auto; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; }
.inline-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.e-rule { margin-top: 18px; padding: 14px 18px; border-radius: 14px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.rule-label { font: 500 10px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--teal-ink); font-variant-ligatures: none; }
.e-rule p { margin-top: 4px; font-size: 15.5px; font-weight: 550; line-height: 1.5; color: var(--ink); }
.e-where { display: grid; justify-items: start; gap: 4px; }
.where-link { display: inline-flex; align-items: center; gap: 6px; margin-top: 2px; border-radius: 6px; color: var(--teal-ink); font-size: 16px; font-weight: 600; overflow-wrap: anywhere; }
.where-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.where-link:focus-visible { box-shadow: var(--focus-ring); }
.e-where .where-about { margin-top: 0; font-size: 14px; font-weight: 400; color: var(--ink-2); }
.e-body { margin-top: 26px; font-size: 15px; line-height: 1.72; }
.e-body :deep(h2) { font-size: 1.28em; margin-top: 1.8em; }
.e-body :deep(h3) { font-size: 1.08em; margin-top: 1.5em; }
.e-body :deep(h1) { font-size: 1.45em; }
.e-empty { display: grid; justify-items: start; gap: 10px; margin-top: 26px; padding: 22px; border-radius: 14px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.e-empty p { color: var(--ink-3); }
.e-toc-inline { display: none; }
.e-aside { align-self: stretch; display: flex; flex-direction: column; gap: 14px; min-width: 0; }
.e-toc { position: sticky; top: 72px; max-height: calc(100dvh - var(--header-h) - var(--footer-h) - 96px); overflow: auto; margin-top: 8px; padding: 0 4px 6px 2px; overscroll-behavior: contain; }
.e-toc .eyebrow { margin-bottom: 8px; }
.e-toc ol, .e-toc-inline ol { display: grid; gap: 1px; margin: 0; padding: 0; list-style: none; }
.e-toc a { display: block; padding: 4px 10px; border-radius: 7px; color: var(--ink-2); font-size: 13px; line-height: 1.4; }
.e-toc li.l3 a { padding-left: 22px; font-size: 12.5px; }
.e-toc a:hover { background: var(--row-hover); color: var(--ink); }
.e-toc a[aria-current="location"] { background: var(--row-selected); color: var(--teal-ink); font-weight: 600; }
.e-toc a:focus-visible, .e-toc-inline a:focus-visible { box-shadow: var(--focus-ring); }
.e-card { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; padding: 14px 16px 16px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow); }
.card-title { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 650; color: var(--ink); }
.card-title svg { color: var(--teal-ink); }
.card-count { margin-left: auto; font-size: 11.5px; font-weight: 500; color: var(--ink-3); }
.card-text { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.card-text code, .e-rename code { padding: 1px 5px; border-radius: 5px; background: var(--code-bg); font-size: 11.5px; color: var(--ink); overflow-wrap: anywhere; }
.k-command { display: flex; align-items: flex-start; gap: 4px; padding: 6px 4px 6px 10px; border-radius: 9px; background: var(--code-bg); box-shadow: inset 0 0 0 1px var(--line); }
.k-command code { flex: 1; min-width: 0; padding-top: 5px; font-size: 11.5px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
.k-command .tok { white-space: nowrap; }
.e-card .facts { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 16px; }
.e-card .facts .wide { grid-column: 1 / -1; }
.e-card .facts dd { font-size: 13px; }
.ext-link { display: inline-flex; align-items: center; gap: 4px; max-width: 100%; overflow-wrap: anywhere; }
.ext-link svg { flex-shrink: 0; }
.link-group { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; }
.link-label { font: 500 10px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.links { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; margin: 0; padding: 0; list-style: none; }
.link-row { display: flex; align-items: center; gap: 8px; min-height: 30px; padding: 0 8px; margin: 0 -8px; border-radius: 8px; color: var(--ink); font-size: 13px; }
a.link-row:hover { background: var(--row-hover); }
a.link-row:focus-visible { box-shadow: var(--focus-ring); }
.link-kind { color: var(--ink-3); }
.link-key { flex-shrink: 0; font-size: 11.5px; color: var(--ink-2); }
.link-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* ---------- States ---------- */
.e-state { display: grid; justify-items: center; gap: 8px; max-width: 1480px; margin: 30px auto 0; padding: 56px 24px; text-align: center; }
.e-state h2 { font-size: 18px; }
.e-state > p { max-width: 460px; font-size: 13.5px; color: var(--ink-2); }
.state-icon { display: grid; place-items: center; width: 48px; height: 48px; margin-bottom: 6px; border-radius: 15px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 10px; }
.e-skeleton { display: grid; gap: 14px; max-width: 760px; margin: 30px auto 0; }
.sk-eyebrow { width: 110px; height: 10px; }
.sk-title { width: 70%; height: 30px; border-radius: 10px; }
.sk-meta { width: 40%; height: 10px; margin-bottom: 18px; }
.sk-line { height: 11px; }

/* ---------- Edit mode ---------- */
.e-edit { display: grid; gap: 16px; max-width: 1480px; padding: 26px 0 40px; }
.edit-title {
  width: 100%; min-height: 48px; padding: 6px 12px; border: 1px solid var(--glass-edge); border-radius: 12px; resize: none; overflow: hidden;
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font: 650 28px/1.3 var(--font); letter-spacing: -.01em;
}
.edit-title:focus { box-shadow: var(--focus-ring); }
.edit-title[aria-invalid="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.e-props { display: grid; grid-template-columns: minmax(150px, 200px) minmax(260px, 1fr) auto; gap: 12px 14px; align-items: end; }
.e-prop { display: grid; gap: 5px; min-width: 0; }
.prop-label { font: 500 10px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.kind-static { display: flex; align-items: center; gap: 8px; height: 38px; padding: 0 12px; border-radius: var(--radius-s); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 13.5px; }
.slug-box {
  display: flex; align-items: center; height: 38px; padding: 0 12px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s);
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); cursor: text;
}
.slug-box:focus-within { box-shadow: var(--focus-ring); }
.slug-box.changed { box-shadow: var(--field-inset), 0 0 0 1px rgba(214, 155, 49, .6); }
.slug-box.bad { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.slug-prefix { flex-shrink: 0; font-size: 13px; color: var(--ink-3); font-variant-ligatures: none; }
.slug-input { flex: 1; min-width: 0; height: 100%; padding: 0; border: 0; background: transparent; color: var(--ink); font-size: 13px; font-variant-ligatures: none; }
.slug-input:focus { box-shadow: none; }
.status-seg { height: 38px; align-items: center; }
.status-seg button { height: 30px; padding: 0 12px; }
.e-rename { display: flex; align-items: flex-start; gap: 10px; padding: 12px 14px; border-radius: 12px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); }
.e-rename > svg { flex-shrink: 0; margin-top: 1px; color: var(--gold-ink); }
.rename-title { font-size: 13px; font-weight: 650; color: var(--ink); }
.e-rename p + p { margin-top: 3px; font-size: 12.5px; line-height: 1.55; color: var(--ink); }
.e-details-grid { grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); }
.e-edit-body { display: grid; gap: 8px; }
.e-edit-body :deep(.split .md-area), .e-edit-body :deep(.split .md-preview) { min-height: 420px; }
.edit-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; font-size: 12px; color: var(--ink-3); }
.edit-hint .keycap { padding: 0 4px; }
.e-conflict { display: grid; gap: 12px; padding: 14px 16px; border-radius: 14px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); }
.conflict-head { display: flex; align-items: flex-start; gap: 12px; }
.conflict-icon { display: grid; place-items: center; flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: var(--gold-ink); }
.conflict-title { font-size: 14px; font-weight: 650; color: var(--ink); }
.conflict-sub { margin-top: 2px; font-size: 12.5px; color: var(--ink); }
.conflict-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-left: 42px; }
.conflict-diff { margin: 0; border-radius: 12px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); overflow: hidden; }
.diff-caption { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--line); font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.diff-lines { max-height: min(40vh, 360px); margin: 0; padding: 6px 0; overflow: auto; list-style: none; font: 12.5px/1.55 var(--mono); font-variant-ligatures: none; }
.diff-line { display: grid; grid-template-columns: 26px minmax(0, 1fr); padding: 1px 12px 1px 0; color: var(--ink); }
.diff-line .text { white-space: pre-wrap; overflow-wrap: anywhere; }
.diff-line.add { background: color-mix(in oklab, var(--ok) 13%, transparent); }
.diff-line.add .gutter { color: var(--ok); }
.diff-line.remove { background: var(--danger-bg); }
.diff-line.remove .gutter { color: var(--danger); }
.diff-line.remove .text { text-decoration: line-through; text-decoration-color: color-mix(in oklab, var(--danger) 55%, transparent); }
.diff-line .calm { font-family: var(--font); color: var(--ink-2); }
.gutter { display: grid; place-items: center; height: 19px; color: var(--ink-3); }

/* ---------- More ---------- */
.more-menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { color: var(--ink-2); }
@media (hover: hover) { .menu-item:hover { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger, .menu-item.danger svg { color: var(--danger); }
.menu-item.danger:hover { background: var(--danger-bg); }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }

/* ---------- Narrower screens ---------- */
@media (max-width: 1439px) {
  .e-grid { grid-template-columns: minmax(0, 1fr) 290px; gap: 40px; }
  .e-rail { display: none; }
  .e-article { max-width: 780px; }
}
@media (max-width: 1099px) {
  .e-grid { grid-template-columns: minmax(0, 1fr); gap: 22px; padding-top: 22px; }
  .e-article { max-width: none; }
  .e-aside { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); align-items: start; }
  .e-toc { display: none; }
  .e-toc-inline { display: block; margin-top: 18px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
  .e-toc-inline summary { display: flex; align-items: center; gap: 8px; min-height: 42px; padding: 0 14px; font-size: 13px; font-weight: 600; cursor: pointer; }
  .e-toc-inline summary .mono { margin-left: auto; font-size: 11.5px; font-weight: 500; color: var(--ink-3); }
  .e-toc-inline summary:focus-visible { box-shadow: var(--focus-ring); border-radius: 12px; }
  .e-toc-inline ol { padding: 0 8px 10px; }
  .e-toc-inline a { display: block; padding: 6px 8px; border-radius: 7px; color: var(--ink-2); font-size: 13.5px; }
  .e-toc-inline li.l3 a { padding-left: 22px; }
  .e-props { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .e-prop-slug { grid-column: 1 / -1; grid-row: 2; }
}
@media (max-width: 720px) {
  .e-bar { margin: 0 -12px; padding: 0 6px 0 8px; }
  .e-bar-inner { height: 56px; }
  .e-bar .icon-btn { width: 44px; height: 44px; }
  .kind-type, .position, .copy-glyph { display: none; }
  .edit-btn { height: 40px; }
  .e-title { font-size: 27px; }
  .e-body { font-size: 15px; }
  /* On a phone, code wraps instead of hiding half a command off screen. */
  .e-body :deep(pre) { white-space: pre-wrap; overflow-wrap: anywhere; }
  .edit-title { font-size: 22px; }
  .e-props { grid-template-columns: minmax(0, 1fr); }
  .e-prop-kind { display: none; }
  .e-prop-slug { grid-row: auto; }
  .status-seg { display: grid; grid-auto-flow: column; grid-auto-columns: 1fr; height: auto; }
  .status-seg button { height: 38px; }
  .conflict-actions { margin-left: 0; }
  .conflict-actions .btn { flex: 1 1 auto; }
  .slug-box { height: 48px; }
  .kind-static { height: 44px; }
  .kind-chip { height: 44px; margin-left: 0; padding: 0 12px; background: transparent; box-shadow: none; }
  .kind-chip > svg:first-child { display: none; }
  .kind-chip .kind-slug { display: inline-block; max-width: 100%; height: 28px; padding: 0 9px; border-radius: 8px; line-height: 28px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
  .kind-chip:hover { box-shadow: none; }
  .kind-chip:focus-visible { box-shadow: var(--focus-ring); }
  .slug-input, .slug-prefix { font-size: 16px; }
}
@media (max-width: 600px) { .nav { display: none; } }

/* ---------- Docked beside the list (U25) ----------
   The ticket side panel's glass pane: fixed at the right, as wide as --panel-w
   (the page sets it, and the splitter beside it), with its own scroller. */
.entry-page.dock {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: var(--panel-w);
  display: flex; flex-direction: column; padding: 0; overflow: hidden;
  border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.entry-page.dock:focus-visible { box-shadow: var(--shadow-pop), var(--focus-ring); }
@media (prefers-reduced-motion: no-preference) {
  .entry-page.dock { animation: dock-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes dock-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
.dock .e-bar { position: static; flex-shrink: 0; margin: 0; padding: 0 8px 0 12px; background: transparent; box-shadow: none; border-bottom: 1px solid var(--line); -webkit-backdrop-filter: none; backdrop-filter: none; }
.dock .e-bar-inner { max-width: none; height: 52px; }
.dock .kind-chip { margin-left: 0; }
.dock .e-scroll { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 22px 26px 30px; }
/* Its own formatting context: a state's top margin stays inside, so the scroller never moves. */
.e-scroll { display: flow-root; }
.e-scroll.switching { opacity: .55; }
@media (prefers-reduced-motion: no-preference) { .e-scroll { transition: opacity .15s ease .08s; } }
.dock .e-grid, .dock .e-edit { display: block; max-width: none; margin: 0; padding: 0; }
.dock .e-edit { display: grid; gap: 16px; }
.dock .e-rail, .dock .e-toc { display: none; }
.dock .e-article { max-width: 72ch; }
.dock .e-title { font-size: clamp(24px, 1.9vw, 30px); }
.dock .e-body { margin-top: 22px; font-size: 14.5px; line-height: 1.7; }
.dock .e-state { margin: 8px 0 0; padding: 40px 12px; border: 0; background: transparent; box-shadow: none; -webkit-backdrop-filter: none; backdrop-filter: none; }
.dock .e-skeleton { max-width: 72ch; margin: 0; }
.sk-known { margin-bottom: 4px; }
/* One column: what it is under the title, the text, then how agents read it and its links. */
.dock .e-aside { position: static; display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; max-width: 72ch; max-height: none; margin-top: 30px; overflow: visible; padding: 0; }
.dock .e-card { background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); border-color: transparent; }
.e-facts { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 12px 20px; margin: 20px 0 0; padding: 14px 16px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.e-facts > div { display: grid; gap: 3px; min-width: 0; }
.e-facts .wide { grid-column: 1 / -1; }
.e-facts dt { font: 500 10px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.e-facts dd { margin: 0; min-width: 0; font-size: 13px; line-height: 1.45; color: var(--ink); overflow-wrap: anywhere; }
.e-facts dd.mono { font-family: var(--mono); font-size: 12.5px; font-variant-ligatures: none; }
.e-facts dd.unset { color: var(--ink-3); }
.slug-copy { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; padding: 0; border: 0; border-radius: 5px; background: transparent; color: var(--ink); font-size: 12.5px; text-align: left; overflow-wrap: anywhere; }
.slug-copy svg { flex-shrink: 0; color: var(--ink-3); }
.slug-copy:hover, .slug-copy:hover svg { color: var(--teal-ink); }
.slug-copy:focus-visible { box-shadow: var(--focus-ring); }
.dock .e-toc-inline { display: block; margin-top: 16px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.dock .e-toc-inline summary { display: flex; align-items: center; gap: 8px; min-height: 40px; padding: 0 14px; font-size: 13px; font-weight: 600; cursor: pointer; }
.dock .e-toc-inline summary .mono { margin-left: auto; font-size: 11.5px; font-weight: 500; color: var(--ink-3); }
.dock .e-toc-inline summary:focus-visible { box-shadow: var(--focus-ring); border-radius: 12px; }
.dock .e-toc-inline ol { padding: 0 8px 10px; }
.dock .e-toc-inline a { display: block; padding: 5px 8px; border-radius: 7px; color: var(--ink-2); font-size: 13px; }
.dock .e-toc-inline a:hover { background: var(--row-hover); color: var(--ink); }
.dock .e-toc-inline li.l3 a { padding-left: 22px; }
.dock .e-props { grid-template-columns: minmax(0, 1fr) auto; }
.dock .e-prop-kind { display: none; }
.dock .edit-title { font-size: 22px; }
.dock .e-edit-body :deep(.md-area) { min-height: 300px; }
</style>
