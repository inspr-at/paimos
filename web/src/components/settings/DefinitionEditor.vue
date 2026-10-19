<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { DEFINITION_BLOCKS, DEFINITION_CATALOG, DEFINITION_CONDITION_OPTIONS, definitionBlocks, definitionChangeWords, definitionDecisionWords, definitionConditionWords, checkCount, cloneChecks, definitionDiff, fixedRuleCondition, manualRule, ruleCondition, ruleIcon, ruleLabel, ruleOwnerWords, ruleWords, sameRule, type DefinitionChange, type DefinitionChecks, type DefinitionCondition, type DefinitionRule, type DefinitionStatus } from '../../lib/definitions'
import { useProfile } from '../../stores/profile'
import { displayLanguage } from '../../lib/displayLanguage'
import { claimSettingsPopover, settingsSubmitKey } from '../../lib/settingsOverlays'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import type { ProjectPerson } from '../../lib/api'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'
import KeyCap from '../KeyCap.vue'

const props = withDefaults(defineProps<{ checks: DefinitionChecks; workspace?: DefinitionChecks; base?: DefinitionChecks | null; projectKey: string; own?: boolean; editable: boolean; busy?: boolean; error?: string; people?: ProjectPerson[]; pending?: DefinitionChange[]; changedDate?: string; scope?: 'project' | 'workspace' }>(), { base: null, own: false, busy: false, error: '', people: () => [], pending: () => [], changedDate: '', scope: 'project' })
const emit = defineEmits<{ change: [checks: DefinitionChecks, applied: () => void]; decide: [change: DefinitionChange, decision: 'accept' | 'keep', applied: () => void] }>()
const panelBody = ref<HTMLElement>(), uid = useId(), root = ref<HTMLElement>(), profile = useProfile()
const language = computed(() => displayLanguage(profile.profile?.locale, 'definitions'))
const t = (en: string, de: string) => language.value === 'de' ? de : en
const blocks = computed(() => definitionBlocks(language.value))
const blockName = (id: DefinitionStatus) => blocks.value.find(block => block.id === id)!.name
const keyParts = (text: string) => text.split(props.projectKey)
const decisionHeights = ref<Record<string, number>>({})
let rowObserver: ResizeObserver | undefined
onMounted(() => {
  let width = root.value?.getBoundingClientRect().width
  rowObserver = new ResizeObserver(([entry]) => {
    if (!entry || entry.contentRect.width === width) return
    width = entry.contentRect.width; decisionHeights.value = {}
  })
  if (root.value) rowObserver.observe(root.value)
})
watch(language, () => { decisionHeights.value = {}; restoredHeights.value = {} })
const labelOf = (rule: DefinitionRule) => ruleLabel(rule, language.value)
const visitRows = ref<{block: DefinitionStatus; item: DefinitionRule}[] | null>(null)
const originals = ref<Record<string, DefinitionChange>>({}), decided = ref<Record<string, 'added' | 'kept' | 'removed' | 'left'>>({})
const decisionVisit = computed(() => visitRows.value !== null)
const waiting = (id: string) => props.pending.find(change => change.check_id === id)
watch(() => props.pending, pending => {
  if (!pending.length) return
  if (!visitRows.value) visitRows.value = DEFINITION_BLOCKS.flatMap(({id:block}) => (props.checks[block] ?? []).map(item => ({block,item})))
  for (const change of pending) {
    originals.value[change.check_id] ??= JSON.parse(JSON.stringify(change))
    if (!visitRows.value.some(row => row.item.id === change.check_id)) visitRows.value.push({block:change.block, item:change.before ?? change.current!})
  }
}, {immediate:true})
function pendingWords(id: string) {
  const change=originals.value[id],item=visitRows.value?.find(row=>row.item.id===id)?.item
  const current=change?.current
  const words=current ? `${item && labelOf(item)!==labelOf(current) ? `${labelOf(current)} · ` : ''}${summaryOf(current)}` : undefined
  return change ? definitionChangeWords(change,props.changedDate,language.value,words) : ''
}
function summaryOf(rule: DefinitionRule) { return wordsOf(rule) }
function pendingSummary(id: string) {
  const change=originals.value[id],item=visitRows.value?.find(row=>row.item.id===id)?.item
  if (!change || !item) return ''
  return change.kind === 'new' ? `${pendingWords(id)} · ${summaryOf(item)}` : `${summaryOf(item)} · ${pendingWords(id)}`
}
function decisionWords(id: string) { const change = originals.value[id]; return change ? definitionDecisionWords(change, visitRows.value?.find(row => row.item.id === id)?.item, language.value) : ['', ''] }
function rowWords(item: DefinitionRule) {
  if (waiting(item.id)) return pendingSummary(item.id)
  return decided.value[item.id] === 'removed' ? t('Removed','Entfernt') : decided.value[item.id] === 'left' ? t(`Left out for ${props.projectKey}`,`Für ${props.projectKey} weggelassen`) : decided.value[item.id] === 'added' ? `${summaryOf(item)} · ${t(`Added for ${props.projectKey}`,`Für ${props.projectKey} hinzugefügt`)}` : summaryOf(item)
}
async function focusWaiting(afterID?: string) {
  await nextTick()
  const rows = Array.from(root.value?.querySelectorAll<HTMLElement>('li[data-check]') ?? [])
  const index = rows.findIndex(row => row.dataset.check === afterID)
  const next = [...rows.slice(index + 1), ...rows.slice(0, index + 1)].find(row => row.dataset.waiting === 'true')
  if (next) next.querySelector<HTMLElement>('[data-decision="accept"]')?.focus({preventScroll:true})
  else rows[index]?.focus({preventScroll:true})
}
function rowHeight(id: string) {
  return Array.from(root.value?.querySelectorAll<HTMLElement>('[data-check]') ?? []).find(row => row.dataset.check === id)?.getBoundingClientRect().height
}
function decide(id: string, choice: 'accept' | 'keep') {
  const change = waiting(id)
  if (!change || !props.editable || props.busy) return
  const height = rowHeight(id)
  emit('decide', change, choice, () => {
    if (height) decisionHeights.value[id] = height
    decided.value[id] = choice === 'keep' ? change.kind === 'new' ? 'left' : 'kept' : change.kind === 'removed' ? 'removed' : change.kind === 'new' ? 'added' : 'kept'
    void nextTick(() => { const measured = rowHeight(id); if (measured) decisionHeights.value[id] = measured })
    void focusWaiting(id)
  })
}
function undoDecision(id: string) {
  const slot = visitRows.value?.find(row => row.item.id === id)
  if (!slot) return
  if (ghosts.value.some(ghost => ghost.item.id === id)) { undo(slot.block,slot.item); return }
  const height = rowHeight(id)
  const next = cloneChecks(props.checks), index = visitRows.value!.filter(row => row.block === slot.block).indexOf(slot)
  ;(next[slot.block] ??= []).splice(Math.min(index,next[slot.block]?.length ?? 0),0,slot.item)
  change(next, () => { if (height) decisionHeights.value[id] = height; decided.value[id] = 'kept'; void nextTick(() => root.value?.querySelector<HTMLElement>(`[data-check="${id}"]`)?.focus({preventScroll:true})) })
}
type Panel = { kind: 'add' | 'menu' | 'conditions' | 'manual' | 'owner'; block: DefinitionStatus; rule?: DefinitionRule; anchor: HTMLElement }
const panel = ref<Panel | null>(null), condition = ref<DefinitionCondition>('all'), kinds = ref(''), labels = ref(''), text = ref(''), owner = ref('')
const formOpen = computed(() => !!panel.value && ['conditions', 'manual', 'owner'].includes(panel.value.kind))
defineExpose({ formOpen, focusWaiting })
const ownerMode = ref<'assignee' | 'named'>('assignee'), fallbackOwner = ref('')
const personOptions = computed(() => props.people.filter(person => person.kind === 'person'))
const ownership = () => ({ owner_id: ownerMode.value === 'named' ? owner.value : null, fallback_owner_id: fallbackOwner.value || null })
const register = useProjectSettingsOverlay()
let unregister: (() => void) | undefined, release: (() => void) | undefined
const ghosts = ref<{ block: DefinitionStatus; item: DefinitionRule; index: number; height: number }[]>([])
const tailSlots = ref<{ block: DefinitionStatus; item: DefinitionRule }[]>([])
const folded = ref<{ block: DefinitionStatus; item: DefinitionRule }[]>([]), foldOpen = ref(false), restored = ref<string[]>([])
const restoredHeights = ref<Record<string, number>>({})
const fixedCondition = computed(() => panel.value?.rule ? fixedRuleCondition(panel.value.rule) : undefined)
const diff = computed(() => props.own ? definitionDiff(props.checks, props.workspace, props.base) : [])
const left = computed(() => diff.value.filter(row => (row.kind === 'left' || row.kind === 'waiting_new') && !originals.value[row.item.id]))
const fold = computed(() => folded.value.length > 0)
watch(left, rows => {
  if (!checkCount(props.checks) || !props.own) return
  for (const row of rows) if (!tailSlots.value.some(r => r.block === row.block && r.item.id === row.item.id)) tailSlots.value.push({ block: row.block, item: row.item })
  if (rows.length > 3 || folded.value.length) for (const row of rows) if (!folded.value.some(r => r.block === row.block && r.item.id === row.item.id)) folded.value.push({ block: row.block, item: row.item })
}, { immediate: true })
function rows(block: DefinitionStatus) {
  if (visitRows.value) {
    const all = DEFINITION_BLOCKS.flatMap(b => props.checks[b.id] ?? [])
    const pinned = visitRows.value.filter(row => row.block === block && !restored.value.includes(row.item.id)).map(row => ({ item: all.find(item => item.id === row.item.id) ?? row.item, ghost: decided.value[row.item.id] === 'removed' || ghosts.value.some(ghost => ghost.item.id === row.item.id), height: decisionHeights.value[row.item.id] ?? ghosts.value.find(ghost => ghost.item.id === row.item.id)?.height ?? 0 }))
    for (const item of props.checks[block] ?? []) if (!visitRows.value.some(row => row.item.id === item.id) && !restored.value.includes(item.id)) pinned.push({item,ghost:false,height:0})
    return pinned
  }
  const out = (props.checks[block] ?? []).filter(item => !restored.value.includes(item.id)).map(item => ({ item, ghost: false, height: 0 }))
  for (const ghost of ghosts.value.filter(g => g.block === block).sort((a, b) => a.index - b.index)) out.splice(Math.min(ghost.index, out.length), 0, { item: ghost.item, ghost: true, height: ghost.height })
  return out
}
function tail(block: DefinitionStatus) { return checkCount(props.checks) && !fold.value ? tailSlots.value.filter(row => row.block === block && (left.value.some(r => r.item.id === row.item.id) || restored.value.includes(row.item.id)) && !ghosts.value.some(g => g.item.id === row.item.id)) : [] }
function note(rule: DefinitionRule, block: DefinitionStatus) {
  const row = diff.value.find(r => r.block === block && r.item.id === rule.id)
  return row?.kind === 'added' ? t(`Added for ${props.projectKey}`,`Für ${props.projectKey} hinzugefügt`) : row?.kind === 'moved' ? t(`Moved for ${props.projectKey}`,`Für ${props.projectKey} verschoben`) : row?.kind === 'changed' ? t(`Changed for ${props.projectKey}`,`Für ${props.projectKey} geändert`) : row?.kind === 'waiting_changed' || row?.kind === 'waiting_removed' ? t('Workspace default changed; this copy is kept','Workspace-Standard geändert; diese Kopie bleibt erhalten') : ''
}
function wordsOf(rule: DefinitionRule) {
  return ruleWords(rule, personOptions.value, language.value)
}
function words(rule: DefinitionRule, block: DefinitionStatus) {
  const row = diff.value.find(r => r.block === block && r.item.id === rule.id)
  const current = wordsOf(rule)
  if (row?.default && !sameRule(rule, row.default)) {
    const label = labelOf(rule) === labelOf(row.default) ? '' : `${labelOf(row.default)} · `
    return `${current} · ${t('workspace default','Workspace-Standard')}: ${label}${wordsOf(row.default)}`
  }
  return current
}
// Reserve the actual possible ownership summaries, including their diff, so
// saving a policy never moves this row's menu or the controls in later rows.
function ownerSummaries(rule: DefinitionRule, block: DefinitionStatus) {
  const ids = [...new Set([rule.owner_id, rule.fallback_owner_id, ...personOptions.value.map(person => person.id)].filter((id): id is string => !!id))]
  const policies = [{ owner_id: null, fallback_owner_id: null }, ...ids.flatMap(id => [{ owner_id: id, fallback_owner_id: null }, { owner_id: null, fallback_owner_id: id }])]
  return [...new Set(policies.map(policy => words({ ...rule, ...policy }, block)))]
}
const catalog = computed(() => DEFINITION_CATALOG.filter(item => !DEFINITION_BLOCKS.some(b => props.checks[b.id]?.some(r => r.id === item.id))).sort((a, b) => Number(b.block === panel.value?.block) - Number(a.block === panel.value?.block)))
const panelTitle = computed(() => panel.value?.kind === 'add' ? t(`Add to ${blockName(panel.value.block)}`,`Zu ${blockName(panel.value.block)} hinzufügen`) : panel.value?.kind === 'manual' ? t('Add a free-text item','Freitext-Prüfung hinzufügen') : panel.value?.kind === 'owner' ? t('Who ticks this','Wer bestätigt diese Prüfung') : panel.value?.kind === 'conditions' ? t(`${labelOf(panel.value.rule!)} applies to`,`${labelOf(panel.value.rule!)} gilt für`) : panel.value?.rule ? labelOf(panel.value.rule) : '')
const split = (value: string) => [...new Set(value.split(',').map(s => s.trim()).filter(Boolean))]
const valid = computed(() => {
  if (panel.value?.kind === 'manual') return !!text.value.trim() && [...text.value.trim()].length <= 200
  if (panel.value?.kind === 'owner') return ownerMode.value === 'assignee' || !!owner.value
  return split(kinds.value).length <= 16 && split(labels.value).length <= 16 && split(kinds.value).every(v => v.length <= 64) && split(labels.value).every(v => v.length <= 100)
})
function close(focus = true) {
  const anchor = panel.value?.anchor
  panel.value = null; unregister?.(); unregister = undefined; release?.(); release = undefined
  if (focus && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
async function open(kind: Panel['kind'], block: DefinitionStatus, event: MouseEvent, rule?: DefinitionRule) {
  if (!props.editable || props.busy) return
  close(false)
  panel.value = { kind, block, rule: rule ? { ...rule } : undefined, anchor: event.currentTarget as HTMLElement }
  release = claimSettingsPopover(() => close(false))
  unregister = register({ element: () => panelBody.value, close, submit })
  condition.value = fixedCondition.value ?? (rule ? ruleCondition(rule) : 'all'); kinds.value = rule?.kinds?.join(', ') ?? ''; labels.value = rule?.labels?.join(', ') ?? ''; text.value = ''; owner.value = rule?.owner_id ?? ''
  ownerMode.value = rule?.owner_id ? 'named' : 'assignee'; fallbackOwner.value = rule?.fallback_owner_id ?? ''
  await nextTick(); panelBody.value?.querySelector<HTMLElement>('input, button:not([disabled])')?.focus({ preventScroll: true })
}
function change(checks: DefinitionChecks, applied: () => void = () => {}) { if (props.editable && !props.busy) emit('change', checks, applied) }
function add(rule: DefinitionRule) {
  const target = panel.value
  if (!target || (props.checks[target.block]?.length ?? 0) >= 32) return
  const next = cloneChecks(props.checks); (next[target.block] ??= []).push(rule)
  change(next, () => { ghosts.value = ghosts.value.filter(g => g.item.id !== rule.id); close() })
}
function freeText() { if (panel.value) { panel.value.kind = 'manual'; text.value = ''; owner.value = ''; ownerMode.value = 'assignee'; fallbackOwner.value = ''; void nextTick(() => panelBody.value?.querySelector('input')?.focus()) } }
function chooseOwner() { if (panel.value) { panel.value.kind = 'owner'; if (!owner.value) owner.value = personOptions.value[0]?.id ?? '' } }
function submit() {
  if (!valid.value || !panel.value || props.busy) return
  if (panel.value.kind === 'manual') { add({ id: `manual:item-${crypto.randomUUID()}`, label: text.value.trim(), condition: 'all', ...(owner.value ? { owner_id: owner.value } : {}) }); return }
  if (panel.value.kind !== 'conditions' && panel.value.kind !== 'owner') return
  const next = cloneChecks(props.checks), target = panel.value, index = next[target.block]?.findIndex(r => r.id === target.rule?.id) ?? -1
  if (index < 0) return
  next[target.block]![index] = target.kind === 'owner' ? { ...target.rule!, ...ownership() } : { ...target.rule!, condition: fixedCondition.value ?? condition.value, kinds: split(kinds.value), labels: split(labels.value) }
  change(next, () => close())
}
function move(direction: number) {
  const target = panel.value
  if (!target?.rule) return
  const next = cloneChecks(props.checks), list = next[target.block]!, index = list.findIndex(r => r.id === target.rule!.id), other = index + direction
  if (index < 0 || other < 0 || other >= list.length) return
  const movingID=list[index]!.id, otherID=list[other]!.id
  ;[list[index], list[other]] = [list[other]!, list[index]!]
  change(next, () => {
    if (visitRows.value) {
      const from=visitRows.value.findIndex(row=>row.item.id===movingID),to=visitRows.value.findIndex(row=>row.item.id===otherID)
      if (from>=0 && to>=0) [visitRows.value[from],visitRows.value[to]]=[visitRows.value[to]!,visitRows.value[from]!]
    }
    close()
  })
}
function remove() {
  const target = panel.value
  if (!target?.rule) return
  const next = cloneChecks(props.checks), index = next[target.block]?.findIndex(r => r.id === target.rule!.id) ?? -1
  if (index < 0) return
  const height = target.anchor.closest('li')?.getBoundingClientRect().height ?? 60
  next[target.block]!.splice(index, 1)
  change(next, () => { ghosts.value.push({ block: target.block, item: target.rule!, index, height }); close(false) })
}
function undo(block: DefinitionStatus, item: DefinitionRule) {
  const ghost = ghosts.value.find(g => g.block === block && g.item.id === item.id)
  if (!ghost) return
  const next = cloneChecks(props.checks); (next[block] ??= []).splice(Math.min(ghost.index, next[block]?.length ?? 0), 0, item)
  change(next, () => { ghosts.value = ghosts.value.filter(g => g !== ghost) })
}
function addBack(block: DefinitionStatus, item: DefinitionRule, event: MouseEvent) {
  if ((props.checks[block]?.length ?? 0) >= 32 || DEFINITION_BLOCKS.some(b => props.checks[b.id]?.some(r => r.id === item.id))) return
  const next = cloneChecks(props.checks); (next[block] ??= []).push(item)
  const height = (event.currentTarget as HTMLElement).closest('li')?.getBoundingClientRect().height
  change(next, () => { if (height) restoredHeights.value[item.id] = height; restored.value.push(item.id) })
}
function conditions() { if (panel.value) panel.value.kind = 'conditions' }
function workspaceKeys(event: KeyboardEvent) {
  if (props.scope === 'workspace' && settingsSubmitKey(event)) { event.preventDefault(); event.stopPropagation(); submit() }
}
function indexOfPanel() { return panel.value ? props.checks[panel.value.block]?.findIndex(r => r.id === panel.value?.rule?.id) ?? -1 : -1 }
// A failed save stays readable in its footer even when a conflict disables
// editing. Identity/access resets still unmount the editor and discard it.
watch(() => props.editable, allowed => { if (!allowed && !props.error) close(false) })
onBeforeUnmount(() => { rowObserver?.disconnect(); close(false) })
</script>

<template>
  <div ref="root" class="definition-editor" :class="{ 'decision-visit': decisionVisit }" :data-scope="scope">
    <section v-for="block in blocks" :key="block.id" class="dblock" :aria-labelledby="`${uid}-${block.id}`" :data-block="block.id">
      <header class="db-head"><h3 :id="`${uid}-${block.id}`">{{ block.name }} <span><span class="db-dot" aria-hidden="true">· </span>{{ t('to enter','vor') }} {{ block.to }}</span></h3><button v-if="editable" type="button" class="add" :disabled="busy || (checks[block.id]?.length ?? 0) >= 32" :aria-label="t(`Add check to ${block.name}`,`Prüfung zu ${block.name} hinzufügen`)" aria-haspopup="dialog" @click="open('add', block.id, $event)"><AppIcon name="plus" :size="13" />{{ t('Add check','Prüfung hinzufügen') }}</button></header>
      <ul class="drs">
        <li v-for="row in rows(block.id)" :key="row.item.id" class="dr" :class="{ ghost: row.ghost }" :style="row.height ? { minHeight: `${row.height}px` } : undefined" :data-check="row.item.id" :data-waiting="!!waiting(row.item.id)" :tabindex="decisionVisit ? -1 : undefined">
          <AppIcon :name="ruleIcon(row.item)" :size="15" class="dr-ic" />
          <template v-if="decisionVisit">
            <span class="dr-l"><b>{{ labelOf(row.item) }}</b><small><template v-for="(part, index) in keyParts(row.ghost ? t('Removed','Entfernt') : rowWords(row.item))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></small></span>
            <span class="decision-actions">
              <span v-if="waiting(row.item.id)" class="decision-links">
                <button v-if="editable" type="button" class="link-btn" data-decision="accept" :disabled="busy || !waiting(row.item.id)" :tabindex="waiting(row.item.id) ? 0 : -1" @click="decide(row.item.id,'accept')">{{ decisionWords(row.item.id)[0] }}</button><span v-if="editable" class="decision-last"><span aria-hidden="true">·</span><button type="button" class="link-btn" data-decision="keep" :disabled="busy || !waiting(row.item.id)" :tabindex="waiting(row.item.id) ? 0 : -1" @click="decide(row.item.id,'keep')">{{ decisionWords(row.item.id)[1] }}</button></span>
              </span>
              <button v-if="row.ghost && editable && originals[row.item.id]" type="button" class="link-btn decision-undo" :disabled="busy" @click="undoDecision(row.item.id)">{{ t('Undo','Rückgängig') }}</button>
            </span>
            <span class="dr-action dr-more-sp"><button v-if="row.ghost && editable && !originals[row.item.id]" type="button" class="link-btn undo" :disabled="busy" @click="undoDecision(row.item.id)">{{ t('Undo','Rückgängig') }}</button><button v-else-if="editable && !waiting(row.item.id) && !row.ghost && decided[row.item.id] !== 'left'" type="button" class="icon-btn sm flat" :disabled="busy" :aria-label="t(`More for ${labelOf(row.item)}`,`Mehr zu ${labelOf(row.item)}`)" aria-haspopup="menu" @click="open('menu', block.id, $event, row.item)"><AppIcon name="more" :size="15" /></button></span>
          </template>
          <template v-else>
          <span class="dr-l"><b>{{ labelOf(row.item) }}</b><small :class="{ 'owner-words': manualRule(row.item) && !row.ghost }"><span>{{ row.ghost ? t('Removed','Entfernt') : words(row.item, block.id) }}<span v-if="!row.ghost && note(row.item, block.id)" class="phone-note"> · <template v-for="(part, index) in keyParts(note(row.item, block.id))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></span></span><template v-if="manualRule(row.item) && !row.ghost"><span v-for="summary in ownerSummaries(row.item, block.id)" :key="summary" class="owner-space" aria-hidden="true">{{ summary }}<span class="phone-note"> · <template v-for="(part, index) in keyParts(note(row.item, block.id) || t(`Changed for ${projectKey}`,`Für ${projectKey} geändert`))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></span></span></template></small></span>
          <span v-clip-tip="note(row.item, block.id)" class="dr-note" :class="{ 'owner-words': manualRule(row.item) }"><span>{{ row.ghost ? '' : note(row.item, block.id) }}</span><span v-if="manualRule(row.item)" class="owner-space" aria-hidden="true"><template v-for="(part, index) in keyParts(t(`Changed for ${projectKey}`,`Für ${projectKey} geändert`))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></span></span>
          <span class="dr-action"><button v-if="row.ghost && editable" type="button" class="link-btn undo" :disabled="busy" @click="undo(block.id, row.item)">{{ t('Undo','Rückgängig') }}</button>
          <button v-else-if="editable" type="button" class="icon-btn sm flat" :disabled="busy" :aria-label="t(`More for ${labelOf(row.item)}`,`Mehr zu ${labelOf(row.item)}`)" aria-haspopup="menu" @click="open('menu', block.id, $event, row.item)"><AppIcon name="more" :size="15" /></button></span>
          </template>
        </li>
        <li v-for="row in tail(block.id)" :key="`left-${row.item.id}`" class="dr left" :data-left="row.item.id" :style="restoredHeights[row.item.id] ? { minHeight: `${restoredHeights[row.item.id]}px` } : undefined"><AppIcon :name="ruleIcon(row.item)" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ labelOf(row.item) }}</b><small><template v-for="(part, index) in keyParts(restored.includes(row.item.id) ? t(`Added back to ${block.name}`,`Wieder zu ${block.name} hinzugefügt`) : t(`In the workspace default · left out for ${projectKey}`,`Im Workspace-Standard · für ${projectKey} weggelassen`))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></small></span><button v-if="editable" type="button" class="link-btn" :disabled="busy || restored.includes(row.item.id)" :style="restored.includes(row.item.id) ? { visibility: 'hidden' } : undefined" @click="addBack(block.id, row.item, $event)">{{ t('Add back','Wieder hinzufügen') }}</button></li>
        <li v-if="!rows(block.id).length && !tail(block.id).length" class="dr none"><AppIcon name="minus" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ checks[block.id]?.length ? t(`${checks[block.id]!.length} ${checks[block.id]!.length === 1 ? 'check' : 'checks'}`,`${checks[block.id]!.length} ${checks[block.id]!.length === 1 ? 'Prüfung' : 'Prüfungen'}`) : t('No checks','Keine Prüfungen') }}</b><small>{{ checks[block.id]?.length ? t('Added back below; these checks apply.','Unten wieder hinzugefügt; diese Prüfungen gelten.') : t(`Tickets enter ${block.to} freely.`,`Tickets wechseln ohne Prüfung zu ${block.to}.`) }}</small></span></li>
      </ul>
    </section>
    <div v-if="fold" class="lfold"><div class="dr fold-head"><AppIcon name="layers" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ t(`${left.length} checks from the workspace default are left out`,`${left.length} ${left.length === 1 ? 'Prüfung aus dem Workspace-Standard wird' : 'Prüfungen aus dem Workspace-Standard werden'} weggelassen`) }}</b><small><template v-for="(part, index) in keyParts(t(`They stay out of ${projectKey} until someone adds one back.`,`Sie bleiben in ${projectKey} weggelassen, bis eine Prüfung wieder hinzugefügt wird.`))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></small></span><button type="button" class="link-btn" :aria-expanded="foldOpen" :aria-controls="`${uid}-fold`" @click="foldOpen = !foldOpen">{{ foldOpen ? t('Hide','Ausblenden') : t('Show','Anzeigen') }}</button></div>
      <ul v-if="foldOpen" :id="`${uid}-fold`" class="drs"><li v-for="row in folded" :key="`${row.block}/${row.item.id}`" class="dr left" :data-fold-row="row.item.id" :style="restoredHeights[row.item.id] ? { minHeight: `${restoredHeights[row.item.id]}px` } : undefined"><AppIcon :name="ruleIcon(row.item)" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ labelOf(row.item) }}</b><small><template v-for="(part, index) in keyParts(restored.includes(row.item.id) ? t(`Added back to ${blockName(row.block)}`,`Wieder zu ${blockName(row.block)} hinzugefügt`) : t(`${blockName(row.block)} · in the workspace default, left out for ${projectKey}`,`${blockName(row.block)} · im Workspace-Standard, für ${projectKey} weggelassen`))" :key="index"><span v-if="index" class="project-key">{{ projectKey }}</span>{{ part }}</template></small></span><button v-if="editable" type="button" class="link-btn" :disabled="busy || restored.includes(row.item.id)" :style="restored.includes(row.item.id) ? { visibility: 'hidden' } : undefined" @click="addBack(row.block, row.item, $event)">{{ t('Add back','Wieder hinzufügen') }}</button></li></ul>
    </div>
    <p v-if="checkCount(checks)" class="enforce"><AppIcon name="shield" :size="14" /><span>{{ t('Agents can’t move a ticket until every check passes. People can move it anyway with a reason, and it’s logged.','Agenten können ein Ticket erst verschieben, wenn alle Prüfungen bestanden sind. Personen können es mit einer protokollierten Begründung trotzdem verschieben.') }} <ProjectSettingsExplainer id="enforce" :language="language" :project-key="projectKey" /></span></p>
    <FloatingPanel v-if="panel" :key="panel.kind" :anchor="panel.anchor" :label="panelTitle" :width="380" :tallest="700" :sheet="panel.kind === 'manual' || panel.kind === 'conditions' || panel.kind === 'owner'" compact-sheet cycle field-escape settings-keys class="definition-pop" @close="close">
      <div ref="panelBody" class="def-panel" :class="{ 'as-form': panel.kind !== 'add' && panel.kind !== 'menu' }" tabindex="-1" @keydown="workspaceKeys">
        <header class="pop-head"><h3>{{ panelTitle }}</h3><button type="button" class="icon-btn sm" :aria-label="t('Close check editor','Prüfungseditor schließen')" @click="close()"><AppIcon name="close" :size="14" /></button></header>
        <div v-if="panel.kind === 'add'" role="menu" :aria-label="panelTitle" class="pop-body">
          <button v-for="item in catalog" :key="item.id" type="button" class="mi" role="menuitem" :disabled="busy" @click="add({ id: item.id, condition: item.condition })"><AppIcon :name="item.icon" :size="14" /><span><b>{{ language === 'de' ? item.de : item.en }}</b><small>{{ definitionConditionWords(item, language) }}<template v-if="item.block !== panel.block"> · {{ t('usually','üblicherweise') }} {{ blockName(item.block) }}</template></small></span></button>
          <p v-if="!catalog.length" class="hint">{{ t('Every catalog check is already used.','Alle Prüfungen aus dem Katalog werden bereits verwendet.') }}</p>
          <div class="manual-add"><button type="button" role="menuitem" class="mi" @click="freeText"><AppIcon name="person-check" :size="14" /><span><b>{{ t('Free-text item…','Freitext-Prüfung…') }}</b><small>{{ t('Something a person confirms','Eine Prüfung, die eine Person bestätigt') }}</small></span></button><ProjectSettingsExplainer id="manual" :language="language" :project-key="projectKey" /></div>
        </div>
        <div v-else-if="panel.kind === 'menu'" role="menu" :aria-label="panelTitle" class="pop-body">
          <button type="button" class="mi" role="menuitem" :disabled="busy || indexOfPanel() <= 0" @click="move(-1)"><AppIcon name="arrow-up" :size="14" />{{ t('Move up','Nach oben') }}</button><button type="button" class="mi" role="menuitem" :disabled="busy || indexOfPanel() >= (checks[panel.block]?.length ?? 0) - 1" @click="move(1)"><AppIcon name="arrow-down" :size="14" />{{ t('Move down','Nach unten') }}</button>
          <button type="button" class="mi" role="menuitem" @click="conditions"><AppIcon name="filter" :size="14" />{{ t('Applies to…','Gilt für…') }}</button><button v-if="panel.rule && manualRule(panel.rule)" type="button" class="mi" role="menuitem" @click="chooseOwner"><AppIcon name="person-check" :size="14" /><span><b>{{ t('Who ticks this…','Wer bestätigt diese Prüfung…') }}</b><small>{{ ruleOwnerWords(panel.rule, personOptions, language) }}</small></span></button><button type="button" class="mi danger" role="menuitem" :disabled="busy" @click="remove"><AppIcon name="trash" :size="14" />{{ t('Remove','Entfernen') }}</button>
        </div>
        <form v-else class="def-form" @submit.prevent="submit">
          <div class="pop-body">
            <template v-if="panel.kind === 'manual'"><label>{{ t('What a person confirms','Was eine Person bestätigt') }}<input v-model="text" class="field" maxlength="200" autocomplete="off" /></label><label>{{ t('Who ticks this','Wer bestätigt diese Prüfung') }}<select v-model="owner" class="field"><option value="">{{ t('The ticket’s assignee, else a project admin','Die ticketverantwortliche Person, sonst ein Projektadministrator') }}</option><option v-for="person in personOptions" :key="person.id" :value="person.id">{{ person.name }}</option></select></label><p class="hint">{{ t('One person ticks it on each ticket, from their Decision Desk; agents can’t. It applies to all tickets.','Eine Person bestätigt die Prüfung für jedes Ticket auf ihrem Entscheidungsdesk; Agenten können dies nicht. Sie gilt für alle Tickets.') }}</p></template>
            <template v-if="panel.kind === 'owner'"><fieldset class="owner-options"><legend>{{ t(`Who ticks “${labelOf(panel.rule!)}”`,`Wer bestätigt „${labelOf(panel.rule!)}“`) }}</legend>
              <div class="owner-option"><label class="condition"><input v-model="ownerMode" type="radio" value="assignee" name="definition-owner" :aria-label="t('The ticket’s assignee','Die ticketverantwortliche Person')" /><b>{{ t('The ticket’s assignee','Die ticketverantwortliche Person') }}</b></label><label class="owner-person">{{ t('If the assignee is an agent or nobody:','Falls ein Agent oder niemand zugewiesen ist:') }}<select v-model="fallbackOwner" class="field" :disabled="ownerMode !== 'assignee'"><option value="">{{ t('A project admin','Ein Projektadministrator') }}</option><option v-if="fallbackOwner && !personOptions.some(p => p.id === fallbackOwner)" :value="fallbackOwner">{{ t('Current fallback person','Derzeitige Ersatzperson') }}</option><option v-for="person in personOptions" :key="person.id" :value="person.id">{{ person.name }}</option></select></label></div>
              <div class="owner-option"><label class="condition"><input v-model="ownerMode" type="radio" value="named" name="definition-owner" :aria-label="t('One named person','Eine benannte Person')" /><b>{{ t('One named person','Eine benannte Person') }}</b></label><label class="owner-person">{{ t('For every ticket in this block:','Für jedes Ticket in diesem Abschnitt:') }}<select v-model="owner" class="field" :aria-label="t('The named person','Die benannte Person')" :disabled="ownerMode !== 'named'"><option v-if="!owner" value="" disabled>{{ t('Choose a person','Person auswählen') }}</option><option v-if="owner && !personOptions.some(p => p.id === owner)" :value="owner">{{ t('Current named person','Derzeit benannte Person') }}</option><option v-for="person in personOptions" :key="person.id" :value="person.id">{{ person.name }}</option></select></label></div>
            </fieldset><p class="hint">{{ t('They get each ticket’s step on their Decision Desk once it matters. Project admins can always tick it too.','Der Schritt erscheint auf dem Entscheidungsdesk dieser Person, sobald er für das Ticket ansteht. Projektadministratoren können ihn ebenfalls bestätigen.') }}</p></template>
            <template v-if="panel.kind === 'conditions'"><p v-if="fixedCondition" class="fixed-condition"><span>{{ t('Applies to','Gilt für') }}</span><b>{{ definitionConditionWords({ condition: fixedCondition, id: '' }, language) }}</b></p><fieldset v-else><legend>{{ t('Applies to','Gilt für') }}</legend><label v-for="option in DEFINITION_CONDITION_OPTIONS" :key="option.value" class="condition"><input v-model="condition" type="radio" :value="option.value" name="definition-condition" :aria-label="definitionConditionWords({ condition: option.value, id: '' }, language)" :aria-describedby="`${uid}-${option.value}-detail`" /><span><b>{{ definitionConditionWords({ condition: option.value, id: '' }, language) }}</b><small :id="`${uid}-${option.value}-detail`">{{ language === 'de' ? option.detail_de : option.detail }}</small></span></label></fieldset><label>{{ t('Ticket kinds (comma-separated)','Ticketarten (durch Kommas getrennt)') }}<input v-model="kinds" class="field" maxlength="1039" autocomplete="off" /></label><label>{{ t('Required labels (comma-separated)','Erforderliche Labels (durch Kommas getrennt)') }}<input v-model="labels" class="field" maxlength="1615" autocomplete="off" /></label><p class="hint">{{ t('The condition, ticket kind and every required label must match. Leave kinds and labels empty for no extra restriction.','Bedingung, Ticketart und alle erforderlichen Labels müssen übereinstimmen. Ohne zusätzliche Einschränkung bleiben Arten und Labels leer.') }}</p></template>
          </div><footer class="pop-actions"><p class="form-error" role="status">{{ error }}</p><button type="button" class="btn" @click="close()">{{ t('Cancel','Abbrechen') }}</button><button type="submit" class="btn primary" :disabled="busy || !valid || !editable">{{ panel.kind === 'manual' ? t('Add','Hinzufügen') : t('Save','Speichern') }}<KeyCap k="mod" /><KeyCap k="enter" /></button></footer>
        </form>
      </div>
    </FloatingPanel>
  </div>
</template>

<style scoped>
.dblock { padding-top: 14px; }.db-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 40px; padding: 0 2px 6px; }.db-head h3 { font: 600 14px/1.35 var(--font); }.db-head h3 > span { font-weight: 500; color: var(--ink-3); }.add { display: inline-flex; align-items: center; gap: 6px; flex: none; min-height: 32px; padding: 0 8px; border: 0; border-radius: 10px; background: transparent; color: var(--teal-ink); font: 600 12.5px var(--font); }.add:hover { background: var(--row-hover); }.drs { border-top: 1px solid var(--line); list-style: none; padding: 0; margin: 0; }.dr { display: grid; grid-template-columns: 28px minmax(0,1fr) minmax(0, auto) 44px; align-items: center; column-gap: 12px; min-height: 64px; padding: 8px 2px; border-bottom: 1px solid var(--line); box-sizing: border-box; }.dr-ic { justify-self: center; color: var(--ink-3); }.dr-l { display: grid; gap: 2px; min-width: 0; }.dr-l b { font: 600 13.5px/1.5 var(--font); overflow-wrap: anywhere; }.dr-l small { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); overflow-wrap: anywhere; }.phone-note { display: none; }.dr-note { font-size: 12px; color: var(--ink-2); max-width: 13em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.dr-action { justify-self: end; min-width: 44px; height: 44px; display: grid; place-items: center; }.dr-action .icon-btn { width: 44px; height: 44px; }.dr-action .undo { min-width: 44px; min-height: 44px; }.left .dr-l b, .ghost .dr-l b { color: var(--ink-3); font-weight: 500; }.left, .fold-head { grid-template-columns: 28px minmax(0,1fr) auto; }.none { grid-template-columns: 28px minmax(0,1fr); }.none .dr-l b { font-weight: 500; color: var(--ink-2); }.link-btn { padding: 0; color: var(--ink-2); font-size: 12.5px; background: transparent; border: 0; white-space: nowrap; min-height: 32px; }.undo { justify-self: end; }.lfold { margin-top: 10px; }.fold-head .link-btn { min-inline-size: 4em; }.enforce { display: flex; gap: 10px; align-items: flex-start; margin-top: 16px; font-size: 12.5px; line-height: 1.6; color: var(--ink-2); }.enforce > svg { flex: none; margin-top: 3px; }.enforce :deep(.how) { margin-left: 4px; }
:global(.floating.definition-pop) { padding: 0; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); border-radius: 12px; overflow: hidden; }.def-panel { display: flex; flex-direction: column; min-height: 0; max-height: inherit; }.def-panel:focus { outline: none; }.pop-head { display: flex; align-items: center; gap: 10px; padding: 10px 12px; flex: none; border-bottom: 1px solid var(--line); }.pop-head h3 { flex: 1; font: 600 14px/1.4 var(--font); }.pop-body { padding: 8px 12px; overflow: auto; min-height: 0; }.mi { display: flex; align-items: center; gap: 10px; min-height: 44px; width: 100%; padding: 8px; border: 0; border-radius: 8px; background: transparent; text-align: left; font-size: 13px; color: var(--ink); }.mi:hover { background: var(--row-hover); }.mi > svg { flex: none; color: var(--ink-3); }.mi span { min-width: 0; }.mi b, .mi small { display: block; font-weight: 500; }.mi small { font-size: 12px; color: var(--ink-3); line-height: 1.5; }.mi.danger { color: var(--danger-ink); }.manual-add { border-top: 1px solid var(--line); margin-top: 6px; padding-bottom: 8px; }.manual-add :deep(.how) { margin-left: 8px; }.def-form { display: flex; flex-direction: column; overflow: hidden; min-height: 0; }.def-form .pop-body { display: grid; gap: 14px; }.def-form label:not(.condition) { display: grid; gap: 6px; font-size: 12.5px; }.hint { color: var(--ink-3); font-size: 12.5px; line-height: 1.5; }.condition { display: flex; align-items: center; gap: 10px; height: 44px; font-size: 13px; }.condition input { accent-color: var(--teal-ink); }.def-form fieldset { border: 0; padding: 0; }.def-form legend { font-size: 12px; color: var(--ink-3); }.condition b, .fixed-condition b { display: block; font-weight: 500; }.condition small { display: block; font-size: 12px; line-height: 1.4; color: var(--ink-3); }.fixed-condition { display: grid; gap: 6px; font-size: 13px; }.fixed-condition > span { font-size: 12px; color: var(--ink-3); }.form-error { height: 2.8em; overflow: auto; overflow-wrap: anywhere; font-size: 12px; line-height: 1.4; color: var(--danger-ink); }.pop-actions { display: grid; grid-template-columns: minmax(0,1fr) auto auto; align-items: center; gap: 8px; padding: 12px; border-top: 1px solid var(--line); flex: none; }
.owner-words { display: grid; }.owner-words > span { grid-area: 1 / 1; }.owner-space { visibility: hidden; pointer-events: none; user-select: none; }.owner-options legend { overflow-wrap: anywhere; }.owner-option + .owner-option { margin-top: 12px; }.def-form .owner-person { margin-inline-start: 24px; color: var(--ink-3); }.owner-person .field { min-inline-size: 0; inline-size: 100%; color: var(--ink); }
@container project-settings-body (max-width: 600px) { .dr { grid-template-columns: 24px minmax(0,1fr) 44px; gap: 2px 8px; }.dr-note { display: none; }.phone-note { display: inline; }.dr > .dr-action { grid-column: 3; grid-row: 1; }.left, .fold-head { grid-template-columns: 24px minmax(0,1fr) auto; }.none { grid-template-columns: 24px minmax(0,1fr); }.db-head { min-height: 50px; }.db-head h3 > span { display: block; font-size: 12px; }.db-head .db-dot { display: none; } }
@media (max-width: 600px) { :global(.floating.definition-pop.sheet) { height: 100dvh; max-height: 100dvh !important; border-radius: 0; }.def-panel.as-form { height: 100%; }.def-form { flex: 1; }.def-form .pop-body { flex: 1; align-content: start; }.pop-actions { padding-bottom: max(14px,env(safe-area-inset-bottom)); }.pop-actions .btn { flex: 1; }.pop-head { padding-top: max(12px,env(safe-area-inset-top)); } }
@media (pointer: coarse), (max-width: 600px) { .add, .link-btn, .pop-actions .btn { min-height: 44px; }.dr .icon-btn, .pop-head .icon-btn { height: 44px; width: 44px; } }
/* Reserve a real action column on desktop; the second line can use the
   space beneath it. Phone rows size to their own content. Decisions retain
   only the clicked row's measured height for the rest of this visit. */
.project-key { white-space: nowrap; overflow-wrap: normal; }
@media (min-width: 1024px) {
  .decision-visit .dr { grid-template-columns: 28px minmax(0,1fr) minmax(26ch,auto) 44px; grid-template-rows: 32px auto; row-gap: 0; }
  .decision-visit .dr-l { display: contents; }
  .decision-visit .dr-l b { grid-column: 2; grid-row: 1; }
  .decision-visit .dr-l small { grid-column: 2 / 4; grid-row: 2; }
  .decision-visit .dr-ic { grid-row: 1 / 3; }
  .decision-visit .decision-actions { grid-column: 3; grid-row: 1; min-inline-size: 26ch; }
  .decision-visit .dr-action { grid-column: 4; grid-row: 1 / 3; }
}
.decision-actions { display: grid; justify-self: end; }.decision-actions > * { grid-area: 1 / 1; }.decision-links { display: inline-flex; flex-wrap: wrap; align-items: center; justify-content: end; gap: 0 8px; }.decision-last { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; }.decision-undo { justify-self: end; }.decision-visit .dr:focus-visible { outline: 1px solid var(--teal-ink); outline-offset: -2px; }
@container project-settings-body (max-width: 600px) { .decision-visit .decision-actions { grid-column: 2 / -1; grid-row: 2; justify-self: start; }.decision-visit .decision-links .link-btn { min-height: 44px; }.decision-visit .dr { row-gap: 0; } }
@media (max-width: 760px) { .decision-visit .decision-actions { grid-column: 2 / -1; grid-row: 2; justify-self: start; }.decision-visit .decision-links { justify-content: start; }.decision-visit .decision-links .link-btn { min-height: 44px; }.decision-visit .dr { grid-template-columns: 24px minmax(0,1fr) 44px; row-gap: 0; }.decision-visit .dr-action { grid-column: 3; grid-row: 1; }.decision-visit .dr-l { grid-column: 2; grid-row: 1; } }
</style>
