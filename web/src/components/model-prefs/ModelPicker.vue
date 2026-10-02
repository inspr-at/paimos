<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import RulesDialog from '../rules/RulesDialog.vue'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import { compareModelVersions, pickerReason, profileLine, type ModelSelector, type PickerCandidate, type PrefProfile, type PrefChoice, type ResidencyView } from '../../lib/modelPrefs'
const props = defineProps<{ label: string; profiles: PrefProfile[]; choices?: PrefChoice[]; candidates: PickerCandidate[]; residency: ResidencyView; review: boolean; selector: ModelSelector; catalogError: string }>()
const emit = defineEmits<{ close: []; choose: [selector: ModelSelector] }>()
const initialSelector = props.selector
const initialEffort = (initialSelector.mode === 'latest' ? initialSelector.effort : initialSelector.mode === 'pinned' ? (props.choices?.find(c => c.profile.id === initialSelector.profile_id)?.profile ?? props.profiles.find(p => p.id === initialSelector.profile_id))?.effort || 'high' : props.review ? 'xhigh' : 'high')
const efforts = ref<Record<string, string>>({})
const groups = computed(() => {
  const grouped = new Map<string, { key: string; label: string; profile: PrefProfile; line: string; versions: PrefProfile[]; efforts: string[] }>()
  for (const choice of props.choices ?? []) {
    const p = choice.profile
    if (choice.retired) continue
    if (!p.enabled || (props.review && (!['strong', 'frontier'].includes(p.tier) || p.effort !== 'xhigh' || !choice.review_ladder))) continue
    const line = choice.line, key = `${p.family}/${p.harness}/${line}`
    const g = grouped.get(key) ?? { key, label: p.display_name || line, profile: p, line, versions: [], efforts: [] }
    g.versions.push(p); if (!g.efforts.includes(p.effort)) g.efforts.push(p.effort)
    grouped.set(key, g)
  }
  return [...grouped.values()].sort((a, b) => a.profile.family.localeCompare(b.profile.family) || a.label.localeCompare(b.label))
})
const chosenEffort = (group: typeof groups.value[number]) => efforts.value[group.key] ?? (group.efforts.includes(initialEffort) ? initialEffort : group.efforts[0]!)
function reason(p: PrefProfile) {
  const choice = props.choices?.find(c => c.profile.id === p.id)
  if (choice) {
    if (props.review && choice.review_reason) return choice.review_reason
    if (props.residency.value !== 'any' && !choice.residency_routes) return `No ${props.residency.value === 'eu' ? 'EU-hosted' : 'local'} route for this model today`
  }
  return pickerReason(p, props.review, props.candidates, props.residency)
}
const modelVersion = (p: PrefProfile) => props.choices?.find(c => c.profile.id === p.id)?.model_version || profileLine(p).version || p.model
const currentProfile = (group: typeof groups.value[number]) => versions(group).find(p => p.effort === chosenEffort(group)) ?? group.profile
function chooseLatest(group: typeof groups.value[number]) { const selected = chosenEffort(group); emit('choose', { mode: 'latest', family: group.profile.family, harness: group.profile.harness, line: group.line, effort: selected }) }
const versions = (group: typeof groups.value[number]) => {
  const all = new Map<string, PrefProfile>()
  for (const p of group.versions) {
    const version = modelVersion(p)
    const old = all.get(version)
    if (!old || p.effort === chosenEffort(group)) all.set(version, p)
  }
  return [...all.values()].sort((a, b) => compareModelVersions(modelVersion(b), modelVersion(a)))
}
const latestReason = (group: typeof groups.value[number]) => reason(currentProfile(group))
const pinReason = (group: typeof groups.value[number], p: PrefProfile) => p.effort !== chosenEffort(group) ? 'This version does not offer that effort' : reason(p)
</script>
<template>
  <RulesDialog :title="label" lede="Choose Automatic, follow new versions, or pin a registered version." class="model-picker" @close="emit('close')">
    <template #footer><button class="btn" type="button" @click="emit('close')">Cancel <kbd class="keycap">Esc</kbd></button></template>
    <button type="button" class="automatic" data-autofocus :aria-pressed="selector.mode === 'auto'" @click="emit('choose', { mode: 'auto' })"><AppIcon name="sparkle" :size="16" /><span><b>Automatic</b><small>{{ review ? 'Strongest qualified reviewer, never the author’s family' : 'Follows the ticket’s own role ladder' }}</small></span></button>
    <p v-if="catalogError" class="picker-note" role="status">{{ catalogError }}. Automatic is still available.</p>
    <section v-for="group in groups" :key="group.key" class="picker-line">
      <h3><HarnessMark :harness="group.profile.harness" :provider="group.profile.provider || group.profile.family" :size="15" />{{ group.label }}<small>{{ group.profile.harness }}</small></h3>
      <div class="effort-options" role="group" :aria-label="`${group.label} effort`"><button v-for="value in group.efforts" :key="value" type="button" :aria-pressed="chosenEffort(group) === value" @click="efforts[group.key] = value">{{ value }}</button></div>
      <div class="version-options">
        <button type="button" :disabled="!!latestReason(group)" @click="chooseLatest(group)"><AppIcon name="refresh" :size="13" />Follows new versions</button>
        <button v-for="p in versions(group)" :key="p.id" type="button" :disabled="!!pinReason(group, p)" @click="emit('choose', { mode: 'pinned', profile_id: p.id })"><AppIcon name="pin" :size="13" />{{ choices?.find(c => c.profile.id === p.id)?.model_version || profileLine(p).version || p.model }}</button>
      </div>
      <p v-if="latestReason(group)" class="picker-note">{{ latestReason(group) }}</p>
    </section>
    <p v-if="!choices" class="picker-note">This server does not expose safe model choices yet. Automatic remains available.</p>
    <p class="picker-note">{{ review ? 'Preferences only reorder the qualified review ladder. Grok also needs a qualified macOS arm64 account. ' : '' }}Provider evidence, live accounts and availability are checked when work starts. A disallowed or unavailable preference falls back to Automatic within the allowed providers.</p>
  </RulesDialog>
</template>
<style scoped>
.automatic { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 54px; padding: 8px; border: 0; border-radius: 8px; background: var(--row-selected); text-align: left; color: var(--ink); } .automatic small { display: block; font-size: 12px; color: var(--ink-3); }
.picker-line { display: grid; gap: 8px; padding: 10px 0; border-bottom: 1px solid var(--line); } h3 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 13px; } h3 small { margin-left: auto; color: var(--ink-3); font-size: 11px; font-weight: 400; }
.effort-options, .version-options { display: flex; flex-wrap: wrap; gap: 6px; } .effort-options button, .version-options button { display: flex; align-items: center; gap: 6px; height: 34px; padding: 0 9px; border: 0; border-radius: 8px; background: var(--surface-sunken); color: var(--ink); font-size: 12px; }
.effort-options button[aria-pressed="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--teal); }
.picker-note { margin: 0; font-size: 12px; color: var(--ink-3); }
@media (max-width: 600px) { .effort-options button, .version-options button { height: 44px; } }
</style>
