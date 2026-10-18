<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, ref } from 'vue'
import { useBusiness, type AreaId } from '../../stores/business'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { availability, statusLabel, type AreaAvailability } from './catalog'
import { businessParts, businessSections, partLabel } from './areas'
import AppIcon from './BizIcon.vue'

// Workspace admins enable the four business parts here, in Settings › Business,
// the only place that switches them; the overview only reads them. Enabling pins
// each plugin to this build with the permissions it declares and adds the node
// types it needs; the server rechecks every business action against that pin.
const business = useBusiness()
const busy = ref<string | null>(null)
const error = ref('')
// Why a part that was set up before is closed now, and what switching it on does.
const GRANT: Record<string, string> = { 'integrations.call': 'connecting other services', 'nodes.contribute': 'writing its records', 'views.provide': 'showing its pages', 'steps.apply': 'its workflow steps' }
const grants = (missing: string[]) => missing.map(p => GRANT[p] ?? p).join(' and ')
function gateNote(state: AreaAvailability | null) {
  switch (state?.gate.state) {
    case 'digest_mismatch': return 'Pinned to a different build. Enabling pins it to this one.'
    case 'under_granted': return `Not allowed ${grants(state.gate.missing)} yet. Enabling grants what it needs.`
    case 'absent': return `Not part of this build of ${brand.value.short_name}.`
    default: return ''
  }
}
// Until the parts are read, each row holds its place without claiming a state,
// so nothing jumps when the answer lands.
const ready = computed(() => !!business.plugins || !!business.pluginsError)
const rows = computed(() => businessParts.map(area => {
  const section = businessSections.find(s => s.id === area.id)!
  const state = business.plugins ? availability(section, business.plugins) : null
  const missing = area.needs.filter(need => !business.open[need])
  return { ...area, summary: section.summary, on: business.open[area.id], state, status: state ? statusLabel(state) : '', missing, note: business.open[area.id] ? '' : gateNote(state), absent: state?.gate.state === 'absent' }
}))
const closedIds = computed(() => rows.value.filter(row => !row.on && !row.absent).map(row => row.id))

async function enable(ids: AreaId[]) {
  const withNeeds = [...new Set(ids.flatMap(id => [...businessParts.find(a => a.id === id)!.needs, id]))]
  const alongside = withNeeds.filter(id => !ids.includes(id) && !business.open[id]).map(id => partLabel(id))
  busy.value = ids.length > 1 ? 'all' : ids[0]; error.value = ''
  try {
    await business.enable(withNeeds)
    toast(ids.length > 1 ? 'Business is enabled for this workspace.' : `${partLabel(ids[0])} is enabled${alongside.length ? `, with ${alongside.join(' and ')}` : ''}.`)
  } catch (e) { error.value = e instanceof Error ? e.message : 'Business could not be enabled. Please try again.' }
  finally { busy.value = null }
}
async function disable(id: AreaId) {
  const dependants = businessParts.filter(area => area.needs.includes(id) && business.open[area.id]).map(area => partLabel(area.id))
  const ok = await confirmAction({
    title: `Disable ${partLabel(id)}?`,
    body: `${partLabel(id)} closes for everyone in this workspace${dependants.length ? `, and so does ${dependants.join(' and ')}` : ''}. Nothing is deleted; enabling it again brings everything back.`,
    confirmLabel: `Disable ${partLabel(id)}`, danger: true,
  })
  if (!ok) return
  busy.value = id; error.value = ''
  try { await business.disable(id); toast(`${partLabel(id)} is disabled.`) }
  catch (e) { error.value = e instanceof Error ? e.message : 'That did not work. Please try again.' }
  finally { busy.value = null }
}
function toggle(id: AreaId, on: boolean) { if (on) void disable(id); else void enable([id]) }
</script>

<template>
  <section class="setup glass-card manage" aria-labelledby="setup-title">
    <header class="setup-head">
      <span class="setup-icon"><AppIcon name="briefcase" :size="18" /></span>
      <div>
        <h2 id="setup-title">Business parts</h2>
        <p>Enable or disable each part for everyone in this workspace. Disabling closes a part; nothing is deleted.</p>
      </div>
    </header>
    <ul class="areas" aria-label="Business parts" :aria-busy="!ready">
      <li v-for="row in rows" :key="row.id" class="area" :class="{ on: row.on }">
        <span class="area-icon"><AppIcon :name="row.icon" :size="15" /></span>
        <span class="area-text">
          <span class="area-name">{{ row.label }}<span v-if="row.on" class="state-chip on">Enabled</span><span v-else-if="row.status && row.status !== 'Not enabled'" class="state-chip">{{ row.status }}</span></span>
          <span class="area-summary">{{ row.summary }}</span>
          <template v-if="ready">
            <span v-if="row.note" class="area-needs">{{ row.note }}</span>
            <span v-if="!row.on && !row.absent && row.missing.length" class="area-needs">Also enables {{ row.missing.map(id => partLabel(id)).join(' and ') }}.</span>
          </template>
        </span>
        <span v-if="!ready" class="skeleton switch-sk" aria-hidden="true" />
        <label v-else class="switch" :data-tip="row.on ? `Disable ${row.label}` : `Enable ${row.label}`">
          <input type="checkbox" :checked="row.on" :disabled="!!busy || !business.admin || row.absent" :aria-label="`${row.label} enabled`" @click.prevent="toggle(row.id, row.on)" />
        </label>
      </li>
    </ul>
    <p v-if="error" class="setup-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
    <footer v-if="ready && closedIds.length > 1 && business.admin" class="setup-foot">
      <span class="foot-note">You can disable any part here later.</span>
      <button type="button" class="btn primary" :disabled="!!busy" @click="enable(closedIds)"><AppIcon name="check" :size="14" />{{ busy === 'all' ? 'Enabling…' : closedIds.length === rows.length ? 'Enable Business' : 'Enable the rest' }}</button>
    </footer>
  </section>
</template>

<style scoped>
.setup { padding: 20px 22px 18px; }
.setup-head { display: flex; gap: 14px; align-items: flex-start; }
.setup-head h2 { font-size: 17px; font-weight: 650; }
.setup-head p { margin-top: 4px; font-size: 13.5px; max-width: 70ch; }
.setup-icon { display: grid; place-items: center; flex-shrink: 0; width: 40px; height: 40px; border-radius: 12px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.areas { display: grid; gap: 2px; margin: 16px 0 0; padding: 0; list-style: none; }
.area { display: flex; align-items: center; gap: 14px; padding: 10px 6px 10px 10px; border-top: 1px solid var(--line); }
.area-icon { display: grid; place-items: center; flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; background: var(--code-bg); color: var(--ink-2); }
.area.on .area-icon { background: var(--chip-teal-bg); color: var(--teal-ink); }
.area-text { display: grid; gap: 2px; flex: 1; min-width: 0; }
.area-name { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 650; color: var(--ink); }
.area-summary { font-size: 12.5px; color: var(--ink-2); }
.area-needs { font-size: 12px; color: var(--warn-ink); }
.switch-sk { flex-shrink: 0; width: 34px; height: 20px; }
.state-chip { display: inline-flex; align-items: center; height: 18px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 10px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.state-chip.on { background: var(--ok-bg); box-shadow: inset 0 0 0 1px var(--ok-line); color: var(--ok); }
.setup-error { display: flex; align-items: center; gap: 8px; margin-top: 12px; padding: 8px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.setup-foot { display: flex; align-items: center; justify-content: flex-end; gap: 14px; margin-top: 12px; padding-top: 14px; border-top: 1px solid var(--line); }
.foot-note { font-size: 12.5px; color: var(--ink-3); }
.manage { padding: 16px 18px 14px; }
.manage .setup-head p { font-size: 12.5px; }
.manage .area-summary { display: none; }
@media (max-width: 720px) {
  .setup { padding: 16px 14px; }
  .setup-foot { flex-direction: column; align-items: stretch; }
  .setup-foot .btn { height: 44px; }
}
</style>
