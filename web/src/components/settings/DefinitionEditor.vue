<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { DEFINITION_BLOCKS, DEFINITION_CATALOG, DEFINITION_CONDITIONS, checkCount, cloneChecks, definitionDiff, manualRule, ruleCondition, ruleLabel, ruleWords, type DefinitionChecks, type DefinitionCondition, type DefinitionRule, type DefinitionStatus } from '../../lib/definitions'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import type { ProjectPerson } from '../../lib/api'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'
import KeyCap from '../KeyCap.vue'

const props = withDefaults(defineProps<{ checks: DefinitionChecks; workspace?: DefinitionChecks; base?: DefinitionChecks | null; projectKey: string; own?: boolean; editable: boolean; busy?: boolean; error?: string; people?: ProjectPerson[] }>(), { base: null, own: false, busy: false, error: '', people: () => [] })
const emit = defineEmits<{ change: [checks: DefinitionChecks, applied: () => void] }>()
const root = ref<HTMLElement>(), panelBody = ref<HTMLElement>(), uid = useId(), phone = ref(false)
type Panel = { kind: 'add' | 'menu' | 'conditions' | 'manual' | 'owner'; block: DefinitionStatus; rule?: DefinitionRule; anchor: HTMLElement }
const panel = ref<Panel | null>(null), condition = ref<DefinitionCondition>('all'), kinds = ref(''), labels = ref(''), text = ref(''), owner = ref('')
const register = useProjectSettingsOverlay()
let unregister: (() => void) | undefined, release: (() => void) | undefined
const ghosts = ref<{ block: DefinitionStatus; item: DefinitionRule; index: number; height: number }[]>([])
const tailSlots = ref<{ block: DefinitionStatus; item: DefinitionRule }[]>([])
const folded = ref<{ block: DefinitionStatus; item: DefinitionRule }[]>([]), foldOpen = ref(false), restored = ref<string[]>([])
const diff = computed(() => props.own ? definitionDiff(props.checks, props.workspace, props.base) : [])
const left = computed(() => diff.value.filter(row => row.kind === 'left' || row.kind === 'waiting_new'))
const fold = computed(() => folded.value.length > 0)
watch(left, rows => {
  if (!checkCount(props.checks) || !props.own) return
  for (const row of rows) if (!tailSlots.value.some(r => r.block === row.block && r.item.id === row.item.id)) tailSlots.value.push({ block: row.block, item: row.item })
  if (rows.length > 3 || folded.value.length) for (const row of rows) if (!folded.value.some(r => r.block === row.block && r.item.id === row.item.id)) folded.value.push({ block: row.block, item: row.item })
}, { immediate: true })
function rows(block: DefinitionStatus) {
  const out = (props.checks[block] ?? []).filter(item => !restored.value.includes(item.id)).map(item => ({ item, ghost: false, height: 0 }))
  for (const ghost of ghosts.value.filter(g => g.block === block).sort((a, b) => a.index - b.index)) out.splice(Math.min(ghost.index, out.length), 0, { item: ghost.item, ghost: true, height: ghost.height })
  return out
}
function tail(block: DefinitionStatus) { return checkCount(props.checks) && !fold.value ? tailSlots.value.filter(row => row.block === block && (left.value.some(r => r.item.id === row.item.id) || restored.value.includes(row.item.id)) && !ghosts.value.some(g => g.item.id === row.item.id)) : [] }
function note(rule: DefinitionRule, block: DefinitionStatus) {
  const row = diff.value.find(r => r.block === block && r.item.id === rule.id)
  return row?.kind === 'added' ? `Added for ${props.projectKey}` : row?.kind === 'moved' ? `Moved for ${props.projectKey}` : row?.kind === 'changed' ? `Changed for ${props.projectKey}` : row?.kind === 'waiting_changed' || row?.kind === 'waiting_removed' ? 'Workspace default changed; this copy is kept' : ''
}
function wordsOf(rule: DefinitionRule) {
  const who = rule.owner_id ? props.people.find(person => person.id === rule.owner_id)?.name ?? 'a named person' : 'the ticket’s assignee, else a project admin'
  return `${ruleWords(rule)}${manualRule(rule) ? ` · Who ticks: ${who}` : ''}`
}
function words(rule: DefinitionRule, block: DefinitionStatus) {
  const row = diff.value.find(r => r.block === block && r.item.id === rule.id)
  const current = wordsOf(rule)
  if ((row?.kind === 'changed' || row?.kind === 'waiting_changed') && row.default) {
    const label = ruleLabel(rule) === ruleLabel(row.default) ? '' : `${ruleLabel(row.default)} · `
    return `${current} · workspace default: ${label}${wordsOf(row.default)}`
  }
  return current
}
const catalog = computed(() => DEFINITION_CATALOG.filter(item => !DEFINITION_BLOCKS.some(b => props.checks[b.id]?.some(r => r.id === item.id))).sort((a, b) => Number(b.block === panel.value?.block) - Number(a.block === panel.value?.block)))
const panelTitle = computed(() => panel.value?.kind === 'add' ? `Add to ${DEFINITION_BLOCKS.find(b => b.id === panel.value?.block)?.name}` : panel.value?.kind === 'manual' ? 'Add a free-text item' : panel.value?.kind === 'owner' ? 'Who ticks this' : panel.value?.kind === 'conditions' ? `${ruleLabel(panel.value.rule!)} applies to` : panel.value?.rule ? ruleLabel(panel.value.rule) : '')
const split = (value: string) => [...new Set(value.split(',').map(s => s.trim()).filter(Boolean))]
const valid = computed(() => panel.value?.kind === 'manual' ? !!text.value.trim() && [...text.value.trim()].length <= 200 : split(kinds.value).length <= 16 && split(labels.value).length <= 16 && split(kinds.value).every(v => v.length <= 64) && split(labels.value).every(v => v.length <= 100))
function close(focus = true) {
  const anchor = panel.value?.anchor
  panel.value = null; unregister?.(); unregister = undefined; release?.(); release = undefined
  if (focus && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
async function open(kind: Panel['kind'], block: DefinitionStatus, event: MouseEvent, rule?: DefinitionRule) {
  if (!props.editable || props.busy) return
  close(false); phone.value = innerWidth <= 600
  panel.value = { kind, block, rule: rule ? { ...rule } : undefined, anchor: event.currentTarget as HTMLElement }
  release = claimSettingsPopover(() => close(false))
  unregister = register({ element: () => panelBody.value, close, submit })
  condition.value = rule ? ruleCondition(rule) : 'all'; kinds.value = rule?.kinds?.join(', ') ?? ''; labels.value = rule?.labels?.join(', ') ?? ''; text.value = ''; owner.value = rule?.owner_id ?? ''
  await nextTick(); panelBody.value?.querySelector<HTMLElement>('input, button:not([disabled])')?.focus({ preventScroll: true })
}
function change(checks: DefinitionChecks, applied: () => void = () => {}) { if (props.editable && !props.busy) emit('change', checks, applied) }
function add(rule: DefinitionRule) {
  const target = panel.value
  if (!target || (props.checks[target.block]?.length ?? 0) >= 32) return
  const next = cloneChecks(props.checks); (next[target.block] ??= []).push(rule)
  change(next, () => { ghosts.value = ghosts.value.filter(g => g.item.id !== rule.id); close() })
}
function freeText() { if (panel.value) { panel.value.kind = 'manual'; text.value = ''; void nextTick(() => panelBody.value?.querySelector('input')?.focus()) } }
function submit() {
  if (!valid.value || !panel.value || props.busy) return
  if (panel.value.kind === 'manual') { add({ id: `manual:item-${crypto.randomUUID()}`, label: text.value.trim(), condition: 'all', ...(owner.value ? { owner_id: owner.value } : {}) }); return }
  if (panel.value.kind !== 'conditions' && panel.value.kind !== 'owner') return
  const next = cloneChecks(props.checks), target = panel.value, index = next[target.block]?.findIndex(r => r.id === target.rule?.id) ?? -1
  if (index < 0) return
  next[target.block]![index] = target.kind === 'owner' ? { ...target.rule!, owner_id: owner.value || null } : { ...target.rule!, condition: condition.value, kinds: split(kinds.value), labels: split(labels.value) }
  change(next, () => close())
}
function move(direction: number) {
  const target = panel.value
  if (!target?.rule) return
  const next = cloneChecks(props.checks), list = next[target.block]!, index = list.findIndex(r => r.id === target.rule!.id), other = index + direction
  if (index < 0 || other < 0 || other >= list.length) return
  ;[list[index], list[other]] = [list[other]!, list[index]!]
  change(next, () => close())
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
function addBack(block: DefinitionStatus, item: DefinitionRule) {
  if ((props.checks[block]?.length ?? 0) >= 32 || DEFINITION_BLOCKS.some(b => props.checks[b.id]?.some(r => r.id === item.id))) return
  const next = cloneChecks(props.checks); (next[block] ??= []).push(item)
  change(next, () => { restored.value.push(item.id) })
}
function conditions() { if (panel.value) panel.value.kind = 'conditions' }
function indexOfPanel() { return panel.value ? props.checks[panel.value.block]?.findIndex(r => r.id === panel.value?.rule?.id) ?? -1 : -1 }
watch(() => props.editable, allowed => { if (!allowed) close(false) })
onBeforeUnmount(() => close(false))
</script>

<template>
  <div ref="root" class="definition-editor">
    <section v-for="block in DEFINITION_BLOCKS" :key="block.id" class="dblock" :aria-labelledby="`${uid}-${block.id}`" :data-block="block.id">
      <header class="db-head"><h3 :id="`${uid}-${block.id}`">{{ block.name }} <span>· to enter {{ block.to }}</span></h3><button v-if="editable" type="button" class="add" :disabled="busy || (checks[block.id]?.length ?? 0) >= 32" :aria-label="`Add check to ${block.name}`" aria-haspopup="dialog" @click="open('add', block.id, $event)"><AppIcon name="plus" :size="13" />Add check</button></header>
      <ul class="drs">
        <li v-for="row in rows(block.id)" :key="row.item.id" class="dr" :class="{ ghost: row.ghost }" :style="row.height ? { height: `${row.height}px` } : undefined" :data-check="row.item.id">
          <AppIcon :name="manualRule(row.item) ? 'person-check' : 'list-checks'" :size="15" class="dr-ic" />
          <span class="dr-l"><b>{{ ruleLabel(row.item) }}</b><small v-clip-tip="row.ghost ? 'Removed' : words(row.item, block.id)">{{ row.ghost ? 'Removed' : words(row.item, block.id) }}</small></span>
          <span v-clip-tip="note(row.item, block.id)" class="dr-note">{{ row.ghost ? '' : note(row.item, block.id) }}</span>
          <span class="dr-action"><button v-if="row.ghost && editable" type="button" class="link-btn undo" :disabled="busy" @click="undo(block.id, row.item)">Undo</button>
          <button v-else-if="editable" type="button" class="icon-btn sm flat" :disabled="busy" :aria-label="`More for ${ruleLabel(row.item)}`" aria-haspopup="menu" @click="open('menu', block.id, $event, row.item)"><AppIcon name="more" :size="15" /></button></span>
        </li>
        <li v-for="row in tail(block.id)" :key="`left-${row.item.id}`" class="dr left" :data-left="row.item.id"><AppIcon name="list-checks" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ ruleLabel(row.item) }}</b><small>{{ restored.includes(row.item.id) ? `Added back to ${block.name}` : `In the workspace default · left out for ${projectKey}` }}</small></span><button v-if="editable" type="button" class="link-btn" :disabled="busy || restored.includes(row.item.id)" :style="restored.includes(row.item.id) ? { visibility: 'hidden' } : undefined" @click="addBack(block.id, row.item)">Add back</button></li>
        <li v-if="!rows(block.id).length && !tail(block.id).length" class="dr none"><AppIcon name="minus" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ checks[block.id]?.length ? `${checks[block.id]!.length} ${checks[block.id]!.length === 1 ? 'check' : 'checks'}` : 'No checks' }}</b><small>{{ checks[block.id]?.length ? 'Added back below; these checks apply.' : `Tickets enter ${block.to} freely.` }}</small></span></li>
      </ul>
    </section>
    <div v-if="fold" class="lfold"><div class="dr fold-head"><AppIcon name="layers" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ left.length }} checks from the workspace default are left out</b><small>They stay out of {{ projectKey }} until someone adds one back.</small></span><button type="button" class="link-btn" :aria-expanded="foldOpen" :aria-controls="`${uid}-fold`" @click="foldOpen = !foldOpen">{{ foldOpen ? 'Hide' : 'Show' }}</button></div>
      <ul v-if="foldOpen" :id="`${uid}-fold`" class="drs"><li v-for="row in folded" :key="`${row.block}/${row.item.id}`" class="dr left" :data-fold-row="row.item.id"><AppIcon name="list-checks" :size="15" class="dr-ic" /><span class="dr-l"><b>{{ ruleLabel(row.item) }}</b><small>{{ restored.includes(row.item.id) ? `Added back to ${DEFINITION_BLOCKS.find(b => b.id === row.block)?.name}` : `${DEFINITION_BLOCKS.find(b => b.id === row.block)?.name} · in the workspace default, left out for ${projectKey}` }}</small></span><button v-if="editable" type="button" class="link-btn" :disabled="busy || restored.includes(row.item.id)" :style="restored.includes(row.item.id) ? { visibility: 'hidden' } : undefined" @click="addBack(row.block, row.item)">Add back</button></li></ul>
    </div>
    <p v-if="checkCount(checks)" class="enforce"><AppIcon name="shield" :size="14" /><span>Agents can’t move a ticket until every check passes. People can move it anyway with a reason, and it’s logged. <ProjectSettingsExplainer id="enforce" :project-key="projectKey" /></span></p>
    <FloatingPanel v-if="panel" :key="panel.kind" :anchor="panel.anchor" :label="panelTitle" :width="380" :tallest="700" :sheet="phone || panel.kind === 'manual' || panel.kind === 'conditions' || panel.kind === 'owner'" compact-sheet cycle field-escape settings-keys class="definition-pop" @close="close">
      <div ref="panelBody" class="def-panel" tabindex="-1">
        <header class="pop-head"><h3>{{ panelTitle }}</h3><button type="button" class="icon-btn sm" aria-label="Close check editor" @click="close()"><AppIcon name="close" :size="14" /></button></header>
        <p class="panel-feedback" role="status">{{ error }}</p>
        <div v-if="panel.kind === 'add'" role="menu" :aria-label="panelTitle" class="pop-body">
          <button v-for="item in catalog" :key="item.id" type="button" class="mi" role="menuitem" :disabled="busy" @click="add({ id: item.id, condition: item.condition })"><AppIcon name="list-checks" :size="14" /><span><b>{{ item.en }}</b><small>{{ DEFINITION_CONDITIONS[item.condition] }}</small></span></button>
          <p v-if="!catalog.length" class="hint">Every catalog check is already used.</p>
          <div class="manual-add"><button type="button" role="menuitem" class="mi" @click="freeText"><AppIcon name="person-check" :size="14" /><span><b>Free-text item…</b><small>Something a person confirms</small></span></button><ProjectSettingsExplainer id="manual" :project-key="projectKey" /></div>
        </div>
        <div v-else-if="panel.kind === 'menu'" role="menu" :aria-label="panelTitle" class="pop-body">
          <button type="button" class="mi" role="menuitem" :disabled="busy || indexOfPanel() <= 0" @click="move(-1)"><AppIcon name="arrow-up" :size="14" />Move up</button><button type="button" class="mi" role="menuitem" :disabled="busy || indexOfPanel() >= (checks[panel.block]?.length ?? 0) - 1" @click="move(1)"><AppIcon name="arrow-down" :size="14" />Move down</button>
          <button type="button" class="mi" role="menuitem" @click="conditions"><AppIcon name="filter" :size="14" />Applies to…</button><button v-if="panel.rule && manualRule(panel.rule)" type="button" class="mi" role="menuitem" @click="panel.kind = 'owner'"><AppIcon name="person-check" :size="14" />Who ticks this…</button><button type="button" class="mi danger" role="menuitem" :disabled="busy" @click="remove"><AppIcon name="trash" :size="14" />Remove</button>
        </div>
        <form v-else class="def-form" @submit.prevent="submit">
          <div class="pop-body">
            <template v-if="panel.kind === 'manual'"><label>What a person confirms<input v-model="text" class="field" maxlength="200" autocomplete="off" /></label><p class="hint">The ticket’s assignee ticks this. If the assignee is an agent or nobody, a project admin ticks it. Only its owner or a project admin may tick.</p></template>
            <template v-if="panel.kind === 'manual' || panel.kind === 'owner'"><label>Who ticks this<select v-model="owner" class="field"><option value="">The ticket’s assignee, else a project admin</option><option v-if="owner && !people.some(p => p.id === owner)" :value="owner">Current named person</option><option v-for="person in people.filter(p => p.kind === 'person')" :key="person.id" :value="person.id">{{ person.name }}</option></select></label></template>
            <template v-if="panel.kind === 'conditions'"><fieldset><legend>Applies to</legend><label v-for="(words, value) in DEFINITION_CONDITIONS" :key="value" class="condition"><input v-model="condition" type="radio" :value="value" name="definition-condition" /><span>{{ words }}</span></label></fieldset><label>Ticket kinds (comma-separated)<input v-model="kinds" class="field" maxlength="1039" autocomplete="off" /></label><label>Required labels (comma-separated)<input v-model="labels" class="field" maxlength="1615" autocomplete="off" /></label><p class="hint">The condition, ticket kind and every required label must match. Leave kinds and labels empty for no extra restriction.</p></template>
          </div><footer class="pop-actions"><button type="button" class="btn" @click="close()">Cancel</button><button type="submit" class="btn primary" :disabled="busy || !valid">{{ panel.kind === 'manual' ? 'Add' : 'Save' }}<KeyCap k="mod" /><KeyCap k="enter" /></button></footer>
        </form>
      </div>
    </FloatingPanel>
  </div>
</template>

<style scoped>
.dblock { padding-top: 14px; }.db-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 40px; padding: 0 2px 6px; }.db-head h3 { font: 600 14px/1.35 var(--font); }.db-head h3 span { font-weight: 500; color: var(--ink-3); }.add { display: inline-flex; align-items: center; gap: 6px; flex: none; min-height: 32px; padding: 0 8px; border: 0; border-radius: 10px; background: transparent; color: var(--teal-ink); font: 600 12.5px var(--font); }.add:hover { background: var(--row-hover); }.drs { border-top: 1px solid var(--line); list-style: none; padding: 0; margin: 0; }.dr { display: grid; grid-template-columns: 28px minmax(0,1fr) minmax(0, auto) 44px; align-items: center; column-gap: 12px; min-height: 64px; padding: 8px 2px; border-bottom: 1px solid var(--line); box-sizing: border-box; }.dr-ic { justify-self: center; color: var(--ink-3); }.dr-l { display: grid; gap: 2px; min-width: 0; }.dr-l b { font: 600 13.5px/1.5 var(--font); overflow-wrap: anywhere; }.dr-l small { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.dr-note { font-size: 12px; color: var(--ink-2); max-width: 13em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.dr-action { justify-self: end; min-width: 44px; height: 44px; display: grid; place-items: center; }.dr-action .icon-btn { width: 44px; height: 44px; }.dr-action .undo { min-width: 44px; min-height: 44px; }.left .dr-l b, .ghost .dr-l b { color: var(--ink-3); font-weight: 500; }.left, .fold-head { grid-template-columns: 28px minmax(0,1fr) auto; }.none { grid-template-columns: 28px minmax(0,1fr); }.none .dr-l b { font-weight: 500; color: var(--ink-2); }.link-btn { padding: 0; color: var(--ink-2); font-size: 12.5px; background: transparent; border: 0; white-space: nowrap; min-height: 32px; }.undo { justify-self: end; }.lfold { margin-top: 10px; }.fold-head .link-btn { min-inline-size: 4em; }.enforce { display: flex; gap: 10px; align-items: flex-start; margin-top: 16px; font-size: 12.5px; line-height: 1.6; color: var(--ink-2); }.enforce > svg { flex: none; margin-top: 3px; }.enforce :deep(.how) { margin-left: 4px; }
:global(.floating.definition-pop) { padding: 0; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); border-radius: 12px; overflow: hidden; }.def-panel { display: flex; flex-direction: column; min-height: 0; max-height: inherit; }.def-panel:focus { outline: none; }.pop-head { display: flex; align-items: center; gap: 10px; padding: 10px 12px; flex: none; border-bottom: 1px solid var(--line); }.pop-head h3 { flex: 1; font: 600 14px/1.4 var(--font); }.panel-feedback { height: 3em; flex: none; padding: 4px 12px; margin: 0; font-size: 12px; line-height: 1.4; color: var(--danger-ink); overflow: auto; }.pop-body { padding: 8px 12px; overflow: auto; min-height: 0; }.mi { display: flex; align-items: center; gap: 10px; min-height: 44px; width: 100%; padding: 8px; border: 0; border-radius: 8px; background: transparent; text-align: left; font-size: 13px; color: var(--ink); }.mi:hover { background: var(--row-hover); }.mi > svg { flex: none; color: var(--ink-3); }.mi span { min-width: 0; }.mi b, .mi small { display: block; font-weight: 500; }.mi small { font-size: 12px; color: var(--ink-3); line-height: 1.5; }.mi.danger { color: var(--danger-ink); }.manual-add { border-top: 1px solid var(--line); margin-top: 6px; padding-bottom: 8px; }.manual-add :deep(.how) { margin-left: 8px; }.def-form { display: flex; flex-direction: column; overflow: hidden; min-height: 0; }.def-form .pop-body { display: grid; gap: 14px; }.def-form label:not(.condition) { display: grid; gap: 6px; font-size: 12.5px; }.hint { color: var(--ink-3); font-size: 12.5px; line-height: 1.5; }.condition { display: flex; align-items: center; gap: 10px; height: 44px; font-size: 13px; }.condition input { accent-color: var(--teal-ink); }.def-form fieldset { border: 0; padding: 0; }.def-form legend { font-size: 12px; color: var(--ink-3); }.pop-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding: 12px; border-top: 1px solid var(--line); flex: none; }
@container project-settings-body (max-width: 600px) { .dr { grid-template-columns: 24px minmax(0,1fr) 44px; gap: 2px 8px; }.dr-note { grid-column: 2; grid-row: 2; max-width: none; height: 18px; }.dr > .dr-action { grid-column: 3; grid-row: 1 / span 2; }.left, .fold-head { grid-template-columns: 24px minmax(0,1fr) auto; }.none { grid-template-columns: 24px minmax(0,1fr); }.db-head h3 span { display: block; font-size: 12px; } }
@media (max-width: 600px) { :global(.floating.definition-pop.sheet) { height: 100dvh; max-height: 100dvh !important; border-radius: 0; }.def-panel { height: 100%; }.def-form { flex: 1; }.def-form .pop-body { flex: 1; align-content: start; }.pop-actions { padding-bottom: max(14px,env(safe-area-inset-bottom)); }.pop-actions .btn { flex: 1; }.pop-head { padding-top: max(12px,env(safe-area-inset-top)); } }
@media (pointer: coarse), (max-width: 600px) { .add, .link-btn, .pop-actions .btn { min-height: 44px; }.dr .icon-btn, .pop-head .icon-btn { height: 44px; width: 44px; } }
</style>
