<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import RuleHistory from './RuleHistory.vue'
import RuleSetCard, { type SetDraft } from './RuleSetCard.vue'
import RulesComparisonPanel from './RulesComparisonPanel.vue'
import RuleTick from './RuleTick.vue'
import RulesBudgetSection from './RulesBudgetSection.vue'
import RulesImportDialog from './RulesImportDialog.vue'
import RulesPreview from './RulesPreview.vue'
import RulesPublishDialog, { type Budget, type PublishItem } from './RulesPublishDialog.vue'
import { accountName, getProjects } from '../../lib/api'
import { can } from '../../lib/authz'
import { getMembers } from '../../lib/access'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import {
  DEFAULT_BUDGET, MAX_RULES, ROLE_LABEL, ROLES, RulesError, applyEnabled, getBudget, saveTldrs, blankRule, copyName, createLayer, createSet, diffSet, duplicateRule, getVersion, groupsState, hasMovable, importBlock, largestProjected,
  listLayers, listSets, publishBlock, publishSets, replyUncertain, rulesEqual, rulesMessage, saveDraft, scopeKey, scopeRank,
  setState, validateDraft, writeBlock,
  type AgentRule, type Caller, type CheckGroup, type RuleBudgetView, type CheckState, type ImportReport, type MergeInput, type NamedAgent, type RoleName, type RuleLayer, type RuleScope, type RuleSet, type RuleSnapshot, type SetState,
} from '../../lib/rules'

// Agent rules answer three questions at a glance: what applies, what waits to
// go live, and the one next step. Layers read top to bottom in precedence order.
interface Working { remote: RuleSet; live: RuleSnapshot | null }
interface Bundle { layer: RuleLayer; sets: Working[] }
type SectionKey = 'company' | 'project' | 'person' | 'agent'

const session = useSession()
const model = ref<Bundle[]>([])
const loading = ref(true)
const loadError = ref('')
const projects = ref<{ id: string; title: string }[]>([])
const agents = ref<NamedAgent[]>([])
const projectId = ref('')
const openSets = ref(new Set<string>())
const editing = ref<(SetDraft & { setId: string; error: string }) | null>(null)
const saving = ref(false)
const importOpen = ref(false)
const previewOpen = ref(false)
const historyId = ref('')
const publishTarget = ref<string[] | null>(null)
const publishing = ref(false)
const publishError = ref('')
const adding = ref<{ section: SectionKey; name: string; role: RoleName; error: string; busy: boolean } | null>(null)
const startedByHand = ref(false)
const budgetView = ref<RuleBudgetView>(DEFAULT_BUDGET)
const canManageBudget = computed(() => caller.value?.kind === 'person' && can('settings.manage'))

const me = computed(() => session.identity?.principal.id ?? '')
const myName = computed(() => session.identity ? accountName(session.identity) : 'You')
const tenantName = computed(() => session.identity?.tenant.name ?? 'this workspace')
const tenantId = computed(() => session.identity?.tenant.id ?? '')
const caller = computed<Caller | null>(() => {
  const identity = session.identity
  if (!identity) return null
  return { id: identity.principal.id, kind: identity.principal.kind === 'agent' ? 'agent' : 'person', allows: (permission, project) => can(permission, project) }
})

// ---------- Names people read instead of ids ----------
const projectTitle = (id?: string) => projects.value.find(project => project.id === id)?.title ?? 'Project'
const agentName = (id?: string) => agents.value.find(agent => agent.id === id)?.name ?? 'Named agent'
function where(scope: RuleScope): string {
  if (scope.layer === 'company') return 'Company'
  if (scope.layer === 'project') return `${projectTitle(scope.project_id)} project`
  if (scope.layer === 'person') return scope.owner_id === me.value ? 'Your rules' : 'Person'
  if (scope.role) return `${ROLE_LABEL[scope.role]} role`
  if (scope.task_id) return `${agentName(scope.agent_id)} · one task`
  return agentName(scope.agent_id)
}
function scopeTitle(scope: RuleScope): string {
  if (scope.layer === 'company') return `Company · ${tenantName.value}`
  if (scope.layer === 'project') return `Project · ${projectTitle(scope.project_id)}`
  if (scope.layer === 'person') return scope.owner_id === me.value ? `Your rules · ${myName.value}` : 'Another person’s rules'
  return `Agents · ${where(scope)}`
}

// How a higher layer is named in "Locked in … rules".
function heldLabel(scope: RuleScope): string {
  if (scope.layer === 'company') return 'company'
  if (scope.layer === 'project') return `${projectTitle(scope.project_id)} project`
  if (scope.layer === 'person') return 'your'
  return where(scope).toLowerCase()
}

// ---------- Load ----------
async function withLive(set: RuleSet): Promise<Working> {
  if (!set.published_version) return { remote: set, live: null }
  try { return { remote: set, live: await getVersion(set.id, set.published_version) } } catch { return { remote: set, live: null } }
}
async function load(quiet = false) {
  if (!quiet) loading.value = true
  loadError.value = ''
  try {
    const { layers } = await listLayers()
    model.value = await Promise.all(layers.map(async layer => {
      const { sets } = await listSets(layer.id)
      return { layer, sets: await Promise.all(sets.map(withLive)) }
    }))
    rulesLoaded.value = true
    pickProject()
  } catch (cause) { loadError.value = rulesMessage(cause) } finally { loading.value = false }
}
async function loadContext() {
  try {
    const page = await getProjects(false)
    projects.value = page.items.map(item => ({ id: item.id, title: item.title }))
    pickProject()
  } catch { /* the project section falls back to the rules' own project ids */ }
  if (!can('members.read')) return
  try { agents.value = (await getMembers()).agents.map(agent => ({ id: agent.principal_id, name: agent.name, preview: agent.preview })) } catch { /* names fall back */ }
}
// The page opens on the project the rules actually target, not the first one listed.
// Nothing is picked before the rules have loaded: an early pick would fall back
// to whichever project happens to be listed first.
const rulesLoaded = ref(false)
function pickProject(prefer?: string) {
  if (prefer) { projectId.value = prefer; return }
  if (projectId.value || !rulesLoaded.value) return
  const targeted = model.value.filter(bundle => bundle.layer.scope.layer === 'project' && bundle.sets.length).map(bundle => bundle.layer.scope.project_id!)
  projectId.value = targeted.find(id => projects.value.some(project => project.id === id)) ?? targeted[0] ?? projects.value[0]?.id ?? ''
}
watch(() => session.identity?.principal.id, () => { if (session.identity) void load(true) })

// ---------- What applies, what waits ----------
const allSets = computed(() => model.value.flatMap(bundle => bundle.sets.map(set => ({ bundle, set }))))
const stateOf = (set: Working): SetState => setState(set.remote, set.live)
const ruleCount = computed(() => allSets.value.reduce((n, item) => n + item.set.remote.rules.length, 0))
const waiting = computed(() => allSets.value.filter(item => stateOf(item.set) !== 'live'))
const publishable = computed(() => waiting.value.filter(item => !publishBlock(caller.value, item.bundle.layer.scope)))
const liveCount = computed(() => allSets.value.length - waiting.value.length)
const empty = computed(() => !loading.value && !loadError.value && allSets.value.length === 0)
const status = computed(() => {
  const sets = allSets.value.length
  const parts = [`${ruleCount.value} ${ruleCount.value === 1 ? 'rule' : 'rules'} in ${sets} ${sets === 1 ? 'set' : 'sets'}`]
  parts.push(liveCount.value === sets ? 'all live' : `${liveCount.value} live`)
  if (waiting.value.length) parts.push(`${waiting.value.length} waiting to publish`)
  return parts.join(' · ')
})
const publishNote = computed(() => {
  if (!waiting.value.length || publishable.value.length) return ''
  if (caller.value?.kind === 'agent') return 'Agents read rules; a person publishes them.'
  return publishBlock(caller.value, waiting.value[0]!.bundle.layer.scope) ?? ''
})
const dirty = computed(() => {
  const draft = editing.value
  if (!draft) return false
  const set = allSets.value.find(item => item.set.remote.id === draft.setId)?.set.remote
  return !!set && (draft.name !== set.name || !rulesEqual(draft.rules, set.rules))
})
const importHold = computed(() => importBlock(caller.value) ?? (dirty.value ? 'Save or cancel the set you are editing before importing.' : ''))

// ---------- Sections in precedence order ----------
const heldAbove = (rank: number, context: { role?: RoleName }) => {
  const held = new Map<string, string>()
  for (const { bundle, set } of allSets.value) {
    const scope = bundle.layer.scope
    if (scopeRank(scope) >= rank) continue
    if (scope.layer === 'project' && scope.project_id !== projectId.value) continue
    if (scope.layer === 'person' && scope.owner_id !== me.value) continue
    if (scope.layer === 'agent' && (scope.role ? scope.role !== context.role : true)) continue
    for (const rule of set.remote.rules) if (rule.strength === 'locked' && !held.has(rule.identity)) held.set(rule.identity, heldLabel(scope))
  }
  return held
}
const sections = computed(() => {
  const inSection = (key: SectionKey) => allSets.value.filter(({ bundle }) => {
    const scope = bundle.layer.scope
    if (key === 'company') return scope.layer === 'company'
    if (key === 'project') return scope.layer === 'project' && scope.project_id === projectId.value
    if (key === 'person') return scope.layer === 'person' && scope.owner_id === me.value
    return scope.layer === 'agent'
  })
  const project = projectTitle(projectId.value)
  const list: { key: SectionKey; title: string; lede: string; scope: RuleScope | null; sets: typeof allSets.value }[] = [
    { key: 'company', title: 'Company', lede: `Applies to everyone in ${tenantName.value}. Wins over everything below.`, scope: { layer: 'company' }, sets: inSection('company') },
    { key: 'project', title: 'Project', lede: projectId.value ? `Applies to work in ${project}.` : 'Choose a project to see its rules.', scope: projectId.value ? { layer: 'project', project_id: projectId.value } : null, sets: inSection('project') },
    { key: 'person', title: 'Your rules', lede: 'How agents work with you. Only you see them.', scope: me.value ? { layer: 'person', owner_id: me.value } : null, sets: inSection('person') },
    { key: 'agent', title: 'Agents', lede: 'Rules for one role, such as builders, or for one named agent.', scope: { layer: 'agent', role: 'builder' }, sets: inSection('agent') },
  ]
  return list
})
function pendingOf(set: Working): Set<string> | undefined {
  if (stateOf(set) !== 'changed' || !set.live) return undefined
  const live = new Map(set.live.rules.map(rule => [rule.identity, rule]))
  return new Set(set.remote.rules.filter(rule => { const old = live.get(rule.identity); return !old || !rulesEqual([old], [rule]) }).map(rule => rule.identity))
}
function heldFor(scope: RuleScope) { return heldAbove(scopeRank(scope), { role: scope.role }) }
const addReason = (section: { key: SectionKey; scope: RuleScope | null }) => section.scope ? writeBlock(caller.value, section.scope) : 'Choose a project first.'
function subtitle(scope: RuleScope) { return scope.layer === 'agent' ? where(scope) : '' }
function lockReason(scope: RuleScope) {
  const block = publishBlock(caller.value, scope)
  return block ? `Locking needs permission to publish these rules. ${block}` : null
}

// ---------- Editing in place ----------
function find(setId: string) { return allSets.value.find(item => item.set.remote.id === setId) ?? null }
function toggle(setId: string) {
  const next = new Set(openSets.value)
  if (next.has(setId)) next.delete(setId)
  else next.add(setId)
  openSets.value = next
}
function edit(setId: string) {
  if (editing.value && editing.value.setId !== setId && dirty.value) { toast('Save or cancel the set you are editing first.', { tone: 'error' }); return }
  const found = find(setId)
  if (!found) return
  const block = writeBlock(caller.value, found.bundle.layer.scope)
  if (block) { toast(block, { tone: 'error' }); return }
  editing.value = { setId, name: found.set.remote.name, rules: found.set.remote.rules.map(rule => ({ ...rule, source: { ...rule.source } })), error: '' }
}
function rulesOf(setId: string): AgentRule[] {
  if (editing.value?.setId === setId) return editing.value.rules
  return find(setId)?.set.remote.rules ?? []
}
function groupsOf(setId: string): CheckGroup[] {
  const found = find(setId)
  if (!found || writeBlock(caller.value, found.bundle.layer.scope)) return []
  return [{ rules: rulesOf(setId), held: heldFor(found.bundle.layer.scope) }]
}
function tickFor(setId: string): CheckState | null {
  const groups = groupsOf(setId)
  if (!hasMovable(groups)) return null
  return groupsState(groups)
}
function tickLocked(setId: string) {
  return saving.value || (!!editing.value && editing.value.setId !== setId && dirty.value)
}
const draftTip = 'Saves a draft; agents see it after you publish.'
function tickTip(setId: string) {
  if (editing.value && editing.value.setId !== setId && dirty.value) return 'Save or cancel the set you are editing first.'
  return draftTip
}
function duplicateReason(setId: string) {
  const found = find(setId)
  if (!found) return 'That set is no longer here.'
  return writeBlock(caller.value, found.bundle.layer.scope) ?? (dirty.value ? 'Save or cancel the set you are editing first.' : undefined)
}
function sectionGroups(key: SectionKey): CheckGroup[] {
  return (sections.value.find(section => section.key === key)?.sets ?? []).flatMap(({ bundle, set }) => {
    if (writeBlock(caller.value, bundle.layer.scope)) return []
    return [{ rules: rulesOf(set.remote.id), held: heldFor(bundle.layer.scope) }]
  })
}
function sectionTick(key: SectionKey): CheckState | null {
  const groups = sectionGroups(key)
  if (!hasMovable(groups)) return null
  return groupsState(groups)
}
const layerTickLocked = computed(() => saving.value || dirty.value)
function layerTickTip() {
  if (dirty.value) return 'Save or cancel the set you are editing first.'
  return draftTip
}

type DraftSave = { status: 'saved' | 'same' } | { status: 'failed'; message: string }
async function persistRules(setId: string, rules: AgentRule[], options?: { quiet?: boolean }): Promise<DraftSave> {
  const found = find(setId)
  if (!found) return { status: 'failed', message: 'That set is no longer here.' }
  const previous = found.set.remote
  if (rulesEqual(rules, previous.rules)) return { status: 'same' }
  const issue = validateDraft(previous.name, rules)
  if (issue.error) {
    if (!options?.quiet) toast(issue.error, { tone: 'error' })
    return { status: 'failed', message: issue.error }
  }
  // Show the new ticks immediately. A failed save puts the previous draft back.
  found.set.remote = { ...previous, rules }
  try {
    found.set.remote = await saveDraft(setId, { expected_revision: previous.revision, name: previous.name, rules })
    return { status: 'saved' }
  } catch (cause) {
    found.set.remote = previous
    const conflict = cause instanceof RulesError && cause.code === 'revision_conflict'
    const message = conflict ? 'This set was saved elsewhere. The page was reloaded.' : rulesMessage(cause)
    if (!options?.quiet) toast(message, { tone: 'error' })
    if (conflict) await load(true)
    return { status: 'failed', message }
  }
}
async function onSetTick(setId: string, enabled: boolean) {
  const found = find(setId)
  if (!found || tickLocked(setId)) return
  const held = heldFor(found.bundle.layer.scope)
  if (editing.value?.setId === setId) {
    editing.value.rules = applyEnabled(editing.value.rules, enabled, held)
    return
  }
  saving.value = true
  try {
    if ((await persistRules(setId, applyEnabled(found.set.remote.rules, enabled, held))).status === 'saved') toast('Draft saved')
  } finally { saving.value = false }
}
async function onRuleTick(setId: string, identity: string, enabled: boolean) {
  const found = find(setId)
  if (!found || tickLocked(setId) || editing.value?.setId === setId) return
  const held = heldFor(found.bundle.layer.scope)
  const next = found.set.remote.rules.map(rule => rule.identity === identity ? applyEnabled([rule], enabled, held)[0]! : rule)
  saving.value = true
  try {
    if ((await persistRules(setId, next)).status === 'saved') toast('Draft saved')
  } finally { saving.value = false }
}
function layerTickNote(saved: number, failure: string) {
  if (!failure) {
    if (saved === 1) return 'Draft saved'
    if (saved > 1) return `Saved ${saved} drafts. Nothing is live until you publish.`
    return ''
  }
  const kept = saved > 0 ? `Saved ${saved} ${saved === 1 ? 'draft' : 'drafts'}. ` : ''
  const detail = saved > 0 && failure === 'The draft was not saved.' ? 'The next set was not saved.' : failure
  return `${kept}${detail}`
}
async function onSectionTick(key: SectionKey, enabled: boolean) {
  if (layerTickLocked.value) return
  const section = sections.value.find(item => item.key === key)
  if (!section) return
  saving.value = true
  let saved = 0
  let failure = ''
  try {
    for (const { bundle, set } of section.sets) {
      if (writeBlock(caller.value, bundle.layer.scope)) continue
      const next = applyEnabled(set.remote.rules, enabled, heldFor(bundle.layer.scope))
      const result = await persistRules(set.remote.id, next, { quiet: true })
      if (result.status === 'failed') { failure = result.message; break }
      if (result.status === 'saved') saved += 1
    }
  } finally { saving.value = false }
  const note = layerTickNote(saved, failure)
  if (note) toast(note, failure ? { tone: 'error' } : undefined)
}
async function duplicateSet(setId: string) {
  if (dirty.value) { toast('Save or cancel the set you are editing before duplicating.', { tone: 'error' }); return }
  const found = find(setId)
  if (!found) return
  const block = writeBlock(caller.value, found.bundle.layer.scope)
  if (block) { toast(block, { tone: 'error' }); return }
  const name = copyName(found.set.remote.name, allSets.value.map(item => item.set.remote.name))
  const taken = new Set<string>()
  const rules = found.set.remote.rules.map(rule => {
    const copy = duplicateRule(rule, taken)
    taken.add(copy.identity)
    return copy
  })
  const issue = validateDraft(name, rules)
  if (issue.error) { toast(issue.error, { tone: 'error' }); return }
  saving.value = true
  try {
    const created = await createSet(found.bundle.layer.id, name)
    const saved = await saveDraft(created.id, { expected_revision: created.revision, name, rules })
    found.bundle.sets = [...found.bundle.sets, { remote: saved, live: null }]
    openSets.value = new Set(openSets.value).add(saved.id)
    toast(`Duplicated as ${saved.name}. Nothing is live until you publish.`)
  } catch (cause) {
    toast(rulesMessage(cause), { tone: 'error' })
    await load(true)
  } finally { saving.value = false }
}

function addRule() {
  const draft = editing.value
  if (!draft) return
  const rule: AgentRule = blankRule(draft.rules.map(item => item.identity))
  rule.source.reference = 'Written by hand'
  draft.rules = [...draft.rules, rule]
}
function duplicateAt(index: number) {
  const draft = editing.value
  if (!draft) return
  if (draft.rules.length >= MAX_RULES) { draft.error = 'A set holds at most 100 rules.'; return }
  const source = draft.rules[index]
  if (!source) return
  const copy = duplicateRule(source, draft.rules.map(rule => rule.identity))
  const next = [...draft.rules]
  next.splice(index + 1, 0, copy)
  draft.rules = next
  draft.error = ''
}
async function save() {
  const draft = editing.value
  const found = draft ? find(draft.setId) : null
  if (!draft || !found) return
  const name = draft.name.trim()
  const issue = validateDraft(name, draft.rules)
  if (issue.error) { draft.error = issue.error; return }
  saving.value = true
  draft.error = ''
  try {
    found.set.remote = await saveDraft(draft.setId, { expected_revision: found.set.remote.revision, name, rules: draft.rules })
    editing.value = null
    toast('Draft saved')
  } catch (cause) {
    if (cause instanceof RulesError && cause.code === 'revision_conflict') {
      try { found.set.remote = (await listSets(found.bundle.layer.id)).sets.find(set => set.id === draft.setId) ?? found.set.remote } catch { /* keep the draft */ }
      draft.error = 'This set was saved elsewhere meanwhile. Your edits are still here; save again to replace that version.'
    } else draft.error = rulesMessage(cause)
  } finally { saving.value = false }
}

// ---------- Explanations for people (AEON-314) ----------
// They are written into the draft on their own, never touching rule text, and
// go live with the normal publish. Agents never receive them.
type Explanation = { en: string; de?: string } | null
async function writeExplanation(setId: string, body: { set?: Explanation; rules?: Record<string, Explanation> }): Promise<string | null> {
  const found = find(setId)
  if (!found) return 'That set is no longer here.'
  if (editing.value?.setId === setId) return 'Save or cancel your edits to this set first.'
  try {
    found.set.remote = await saveTldrs(setId, { expected_revision: found.set.remote.revision, ...body })
    toast('Draft saved')
    return null
  } catch (cause) {
    if (cause instanceof RulesError && cause.code === 'revision_conflict') {
      await load(true)
      return 'This set was saved elsewhere meanwhile. The page was reloaded; try again.'
    }
    return rulesMessage(cause)
  }
}
function explainSetFor(setId: string, scope: RuleScope) {
  if (writeBlock(caller.value, scope)) return undefined
  return (value: Explanation) => writeExplanation(setId, { set: value })
}
function explainRuleFor(setId: string, scope: RuleScope) {
  if (writeBlock(caller.value, scope)) return undefined
  return (identity: string, value: Explanation) => writeExplanation(setId, { rules: { [identity]: value } })
}

// ---------- Adding a set ----------
function startAdd(key: SectionKey) { adding.value = { section: key, name: '', role: 'builder', error: '', busy: false } }
async function add() {
  const form = adding.value
  const section = sections.value.find(item => item.key === form?.section)
  if (!form || !section?.scope) return
  const scope: RuleScope = form.section === 'agent' ? { layer: 'agent', role: form.role } : section.scope
  const name = form.name.trim()
  const issue = validateDraft(name, []).error ?? writeBlock(caller.value, scope)
  if (issue) { form.error = issue; return }
  form.busy = true
  try {
    let bundle = model.value.find(item => scopeKey(item.layer.scope) === scopeKey(scope))
    if (!bundle) {
      const layer = await createLayer(scope)
      bundle = { layer, sets: [] }
      model.value = [...model.value, bundle]
    }
    const created = await createSet(bundle.layer.id, name)
    bundle.sets = [...bundle.sets, { remote: created, live: null }]
    adding.value = null
    startedByHand.value = true
    edit(created.id)
    addRule()
  } catch (cause) { form.error = rulesMessage(cause) } finally { if (adding.value) adding.value.busy = false }
}

// ---------- One approval to publish ----------
function openPublish(ids?: string[]) {
  if (dirty.value) { toast('Save or cancel the set you are editing before publishing.', { tone: 'error' }); return }
  publishError.value = ''
  publishTarget.value = ids ?? publishable.value.map(item => item.set.remote.id)
}
const publishList = computed(() => (publishTarget.value ?? []).map(find).filter((item): item is NonNullable<ReturnType<typeof find>> => !!item))
const publishItems = computed<PublishItem[]>(() => publishList.value.map(({ bundle, set }) => ({
  id: set.remote.id, name: set.remote.name, where: where(bundle.layer.scope), state: stateOf(set), rules: set.remote.rules, tldr: set.remote.tldr,
  changes: diffSet(set.live, set.remote),
})))
const publishBlocked = computed(() => publishTarget.value && publishTarget.value.length === publishable.value.length
  ? waiting.value.filter(item => publishBlock(caller.value, item.bundle.layer.scope)).map(({ bundle, set }) => ({ id: set.remote.id, name: set.remote.name, where: where(bundle.layer.scope), reason: publishBlock(caller.value, bundle.layer.scope) ?? '' }))
  : [])
const budget = computed<Budget | null>(() => {
  if (!publishTarget.value || !me.value) return null
  const chosen = new Set(publishTarget.value)
  const inputs: MergeInput[] = allSets.value.flatMap(({ bundle, set }) => {
    const rules = chosen.has(set.remote.id) ? set.remote.rules : set.live?.rules
    return rules ? [{ id: set.remote.id, scope: bundle.layer.scope, rules }] : []
  })
  const largest = largestProjected(inputs, projectId.value || '00000000-0000-4000-8000-000000000000', me.value, new Date(), budgetView.value)
  return { ...largest, project: projectTitle(projectId.value), limits: budgetView.value }
})
async function confirmPublish(note: string) {
  const list = publishList.value
  if (!list.length || publishing.value) return
  publishing.value = true
  publishError.value = ''
  try {
    const result = await publishSets(list.map(({ set }) => ({ set_id: set.remote.id, expected_revision: set.remote.revision, version: 'auto' })), note)
    for (const snapshot of result.versions) {
      const found = find(snapshot.set_id)
      if (found) { found.set.remote = { ...found.set.remote, published_version: snapshot.version }; found.set.live = snapshot }
    }
    publishTarget.value = null
    toast(`Published ${list.length} ${list.length === 1 ? 'set' : 'sets'}. Agents receive them at their next session start.`)
  } catch (cause) {
    if (replyUncertain(cause)) {
      publishError.value = 'The server did not confirm the publication. The page was reloaded to show what is live now.'
      await load(true)
      return
    }
    const reason = cause instanceof RulesError && cause.code === 'revision_conflict'
      ? 'A set was saved elsewhere meanwhile. The page was reloaded; review the sets again.'
      : cause instanceof RulesError && cause.code === 'forbidden' ? 'You may not publish one of these sets.' : rulesMessage(cause)
    publishError.value = `Nothing was published. ${reason}`
    if (cause instanceof RulesError && cause.code === 'revision_conflict') await load(true)
  } finally { publishing.value = false }
}

// ---------- Import ----------
const existing = computed(() => model.value.map(bundle => ({ scope: bundle.layer.scope, sets: bundle.sets.map(set => set.remote) })))
function openImport() { if (!importHold.value) importOpen.value = true }
async function imported(report: ImportReport) {
  const project = report.results.find(result => result.scope.layer === 'project' && result.status === 'saved')?.scope.project_id
  await load(true)
  if (project) pickProject(project)
  if (report.status === 'imported') {
    importOpen.value = false
    const saved = report.results.filter(result => result.status === 'saved').length
    toast(`Imported ${saved} ${saved === 1 ? 'set' : 'sets'} as drafts. Nothing is live until you publish.`)
  }
}

// ---------- History ----------
const historySet = computed(() => historyId.value ? find(historyId.value) : null)
async function restored() {
  historyId.value = ''
  await load(true)
  toast('Restored as a new version')
}

onMounted(() => {
  void load()
  void loadContext()
  getBudget().then(view => { budgetView.value = view }).catch(() => { /* the default budget stays shown */ })
})
</script>

<template>
  <section class="rules-page" aria-labelledby="agent-rules-title">
    <header class="page-head">
      <div class="intro">
        <h2 id="agent-rules-title">Agent rules</h2>
        <p class="lede">Rules every agent receives at session start.</p>
        <p v-if="!loading && !empty && !loadError" class="status" role="status">{{ status }}</p>
      </div>
      <div v-if="!loading && !empty && !loadError" class="actions">
        <button type="button" class="btn sm ghost" data-tip="Each rule next to its explanation" @click="previewOpen = true"><BizIcon name="eye" :size="14" />Preview</button>
        <button type="button" class="btn sm ghost" :aria-disabled="!!importHold || undefined" :data-tip="importHold || 'Add or update sets from a rules file'" @click="openImport"><BizIcon name="upload" :size="14" />Import</button>
        <button v-if="publishable.length" type="button" class="btn primary" @click="openPublish()"><BizIcon name="seal" :size="15" />Review and publish ({{ publishable.length }} {{ publishable.length === 1 ? 'set' : 'sets' }})</button>
      </div>
    </header>
    <RulesComparisonPanel v-if="!loading && !empty && !loadError && projectId" :project-id="projectId" />
    <p v-if="publishNote" class="publish-note"><BizIcon name="lock" :size="13" />{{ publishNote }}</p>

    <p v-if="loadError" class="load-error" role="alert"><BizIcon name="alert" :size="14" /><span>{{ loadError }}</span><button type="button" class="btn sm" @click="load()">Try again</button></p>
    <div v-if="loading" class="waiting" role="status" aria-label="Loading rules"><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span></div>

    <div v-else-if="empty && !startedByHand" class="empty">
      <span class="empty-icon" aria-hidden="true"><BizIcon name="shield" :size="22" /></span>
      <p class="empty-line">No rules yet. Import a rules file to give every agent the same rules at session start.</p>
      <button type="button" class="btn primary" :disabled="!!importHold" :data-tip="importHold || undefined" @click="openImport"><BizIcon name="upload" :size="15" />Import rules</button>
      <button v-if="!writeBlock(caller, { layer: 'company' })" type="button" class="btn sm ghost" @click="startedByHand = true; startAdd('company')">Or write the first set by hand</button>
    </div>

    <div v-else-if="!loadError" class="layers">
      <section v-for="(section, index) in sections" :key="section.key" class="layer" :aria-labelledby="`rules-layer-${section.key}`">
        <header class="layer-head">
          <span class="rank" aria-hidden="true">{{ index + 1 }}</span>
          <div class="layer-titles">
            <h3 :id="`rules-layer-${section.key}`">
              <template v-if="section.key !== 'project' || projects.length < 2">{{ section.key === 'project' ? `Project · ${projectTitle(projectId)}` : section.title }}</template>
              <template v-else>Project ·
                <span class="project-pick">
                  <select v-model="projectId" aria-label="Project"><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.title }}</option></select>
                  <BizIcon name="chevron" :size="13" />
                </span>
              </template>
            </h3>
            <p>{{ section.lede }}</p>
          </div>
          <div v-if="sectionTick(section.key)" class="layer-tick">
            <RuleTick :state="sectionTick(section.key)!" :label="`Turn ${section.title} rules on or off`" :disabled="layerTickLocked" :tip="layerTickTip()" @toggle="onSectionTick(section.key, $event)" />
          </div>
        </header>
        <div class="cards">
          <div v-if="section.sets.length" class="group">
          <RuleSetCard
            v-for="{ bundle, set } in section.sets" :key="set.remote.id" :set="set.remote" :state="stateOf(set)" :subtitle="subtitle(bundle.layer.scope)"
            :open="openSets.has(set.remote.id)" :held="heldFor(bundle.layer.scope)" :pending="pendingOf(set)" :draft="editing?.setId === set.remote.id ? editing : null"
            :edit-reason="writeBlock(caller, bundle.layer.scope)" :publish-reason="publishBlock(caller, bundle.layer.scope)" :lock-reason="lockReason(bundle.layer.scope)"
            :saving="saving" :error="editing?.setId === set.remote.id ? editing.error : ''"
            :tick="tickFor(set.remote.id)" :tick-disabled="tickLocked(set.remote.id)" :tick-tip="tickTip(set.remote.id)" :duplicate-reason="duplicateReason(set.remote.id)"
            @toggle="toggle(set.remote.id)" @edit="edit(set.remote.id)" @cancel="editing = null" @save="save" @add-rule="addRule" @duplicate-rule="duplicateAt"
            @draft="value => editing && Object.assign(editing, value)" @publish="openPublish([set.remote.id])" @history="historyId = set.remote.id"
            @duplicate="duplicateSet(set.remote.id)" @tick="onSetTick(set.remote.id, $event)" @tick-rule="(identity, enabled) => onRuleTick(set.remote.id, identity, enabled)"
            :explain-set="explainSetFor(set.remote.id, bundle.layer.scope)" :explain-rule="explainRuleFor(set.remote.id, bundle.layer.scope)"
          />
          </div>
          <p v-if="!section.sets.length && adding?.section !== section.key && addReason(section)" class="none">None yet.</p>
          <form v-if="adding?.section === section.key" class="add-form" @submit.prevent="add">
            <input v-model="adding.name" class="field" maxlength="128" placeholder="Name of the new set" aria-label="Name of the new set" autofocus>
            <select v-if="section.key === 'agent'" v-model="adding.role" class="field role" aria-label="For which role"><option v-for="role in ROLES" :key="role" :value="role">For {{ ROLE_LABEL[role].toLowerCase() }}s</option></select>
            <button type="button" class="btn sm ghost" :disabled="adding.busy" @click="adding = null">Cancel</button>
            <button type="submit" class="btn sm" :disabled="adding.busy || !adding.name.trim()">Add set</button>
            <p v-if="adding.error" class="form-error" role="alert">{{ adding.error }}</p>
          </form>
          <button v-else-if="!addReason(section)" type="button" class="add-set" :class="{ alone: !section.sets.length }" @click="startAdd(section.key)"><BizIcon name="plus" :size="14" />{{ section.sets.length ? 'Add set' : section.key === 'project' ? `Add rules for ${projectTitle(projectId)}` : section.key === 'person' ? 'Add your first set' : 'Add a set for a role' }}</button>
        </div>
      </section>
      <RulesBudgetSection :view="budgetView" :can-manage="canManageBudget" @saved="view => { budgetView = view; toast('Budget saved') }" />
    </div>

    <RulesImportDialog
      v-if="importOpen" :tenant-id="tenantId" :tenant-name="tenantName" :caller="caller" :existing="existing" :scope-title="scopeTitle" :hold="importHold"
      @close="importOpen = false" @imported="imported"
    />
    <RulesPublishDialog
      v-if="publishTarget" :items="publishItems" :blocked="publishBlocked" :budget="budget" :busy="publishing" :error="publishError"
      :title="publishTarget.length === 1 && !publishBlocked.length && publishable.length !== 1 ? `Publish “${publishItems[0]?.name ?? ''}”` : undefined"
      @close="publishTarget = null" @confirm="confirmPublish"
    />
    <RulesPreview
      v-if="previewOpen" :projects="projects" :people="[{ id: me, name: myName }]" :agents="agents" :project-id="projectId" :person-id="me" :waiting="waiting.length" :where="where"
      @close="previewOpen = false"
    />
    <RuleHistory
      v-if="historySet" :set="historySet.set.remote" :publish-reason="publishBlock(caller, historySet.bundle.layer.scope)" :dirty="dirty && editing?.setId === historyId"
      @close="historyId = ''" @restored="restored"
    />
  </section>
</template>

<style scoped>
.rules-page { display: flex; flex-direction: column; gap: 20px; min-width: 0; max-width: 100%; }
.page-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 12px 20px; }
.intro { min-width: 0; }
.page-head h2 { margin: 0; font-size: 20px; font-weight: 650; letter-spacing: -.01em; }
.lede { margin: 4px 0 0; color: var(--ink-2); font-size: 14px; }
.status { margin: 6px 0 0; color: var(--ink-3); font-size: 13px; font-variant-numeric: tabular-nums; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.actions [aria-disabled="true"] { opacity: .5; cursor: not-allowed; }
.publish-note { display: flex; align-items: center; gap: 6px; margin: -8px 0 0; color: var(--ink-3); font-size: 13px; }
.load-error { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0; padding: 12px 14px; border-radius: 12px; background: var(--danger-bg); color: var(--danger); font-size: 13.5px; }
.load-error span { flex: 1 1 200px; }
.waiting { display: grid; gap: 10px; }
.waiting .skeleton { height: 56px; border-radius: 14px; }
.empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 48px 20px; border-radius: 16px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); text-align: center; }
.empty-icon { display: grid; place-items: center; width: 48px; height: 48px; border-radius: 14px; background: var(--chip-teal-bg); color: var(--teal-ink); }
.empty-line { max-width: 44ch; margin: 0; color: var(--ink-2); font-size: 14.5px; line-height: 1.5; }
.layers { display: flex; flex-direction: column; gap: 28px; }
.layer { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.layer-head { display: flex; align-items: flex-start; gap: 12px; }
.rank { flex: none; display: grid; place-items: center; width: 24px; height: 24px; margin-top: 1px; border-radius: 50%; background: var(--surface-2); color: var(--ink-3); font-size: 12px; font-weight: 650; font-variant-numeric: tabular-nums; }
.layer-titles { flex: 1; min-width: 0; }
.layer-titles h3 { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; margin: 0; font-size: 15px; font-weight: 650; }
.layer-titles p { margin: 2px 0 0; color: var(--ink-3); font-size: 13px; }
.layer-tick { flex: none; margin-left: auto; display: flex; }
.project-pick { position: relative; display: inline-flex; align-items: center; gap: 2px; color: var(--teal-ink); }
.project-pick select { field-sizing: content; appearance: none; border: 0; background: none; color: inherit; font: inherit; padding: 0 16px 0 2px; margin-right: -14px; border-radius: 6px; cursor: pointer; }
.project-pick select:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.project-pick svg { pointer-events: none; }
.cards { display: flex; flex-direction: column; gap: 6px; padding-left: 36px; min-width: 0; }
.group { display: flex; flex-direction: column; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); min-width: 0; }
.group > * + * { border-top: 1px solid var(--line); }
.none { margin: 0; padding: 4px 2px; color: var(--ink-3); font-size: 13px; }
.add-set { align-self: flex-start; display: inline-flex; align-items: center; gap: 6px; padding: 6px 10px; border: 0; border-radius: 8px; background: none; color: var(--ink-3); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .add-set:hover { background: var(--row-hover); color: var(--teal-ink); } }
.add-set.alone { align-self: stretch; justify-content: flex-start; min-height: 44px; padding: 0 14px; border-radius: 14px; border: 1px dashed var(--line-2); }
.add-form { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 10px; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.add-form .field { flex: 1 1 220px; }
.add-form .role { flex: 0 1 180px; }
.form-error { flex-basis: 100%; margin: 0; color: var(--danger); font-size: 12.5px; }
@media (max-width: 600px) {
  .page-head { align-items: stretch; }
  .actions { width: 100%; }
  .actions .primary { flex: 1 1 100%; order: -1; }
  .cards { padding-left: 0; }
  .rank { display: none; }
}
</style>
