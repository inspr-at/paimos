<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { useAgentPause } from '../../stores/agentPause'
import { LEVELS, prediction, type PauseLevel } from '../../lib/agentPause'
const pause = useAgentPause(), dialog = ref<HTMLDialogElement>(), description = ref(false)
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const resume = computed(() => pause.mode?.includes('resume')), stop = computed(() => pause.mode === 'stop'), bulk = computed(() => pause.mode?.endsWith('-all'))
const title = computed(() => bulk.value ? resume.value ? 'Resume all' : 'Pause all' : `${resume.value ? 'Resume' : stop.value ? 'Stop now' : 'Pause'} ${pause.targets[0]?.name ?? 'agent'}`)
const levels = computed(() => LEVELS.filter(l => bulk.value || l.value !== 'stop_now'))
const selected = computed(() => pause.rows.filter(row => pause.selectedLevel(row.session) !== 'keep'))
const command = computed(() => resume.value ? `Resume ${pause.resumeIds.length}` : stop.value ? 'Stop now' : `Pause ${selected.value.filter(row => pause.selectedLevel(row.session) !== 'stop_now').length}${selected.value.some(row => pause.selectedLevel(row.session) === 'stop_now') ? ` · Stop ${selected.value.filter(row => pause.selectedLevel(row.session) === 'stop_now').length}` : ''}`)
watch(() => pause.mode, async mode => {
  if (!mode) { dialog.value?.close(); return }
  description.value = false
  await nextTick(); dialog.value?.showModal()
  dialog.value?.querySelector<HTMLElement>('[data-submit]')?.focus({ preventScroll: true })
})
onBeforeUnmount(() => dialog.value?.close())
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if (event.target instanceof HTMLElement && ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName)) event.target.blur()
    else pause.close()
  }
  if (event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); event.stopPropagation(); if (resume.value ? pause.resumeIds.length : selected.value.length) void pause.submit() }
}
function levelKeys(event: KeyboardEvent) {
  if (!['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const index = levels.value.findIndex(l => l.value === pause.level), next = event.key === 'Home' ? 0 : event.key === 'End' ? levels.value.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + levels.value.length) % levels.value.length
  pause.level = levels.value[next]!.value
  ;(event.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-level="${pause.level}"]`)?.focus()
}
const detail = (level: PauseLevel) => pause.rows[0] ? prediction(pause.rows[0].session, level, Date.now(), pause.interval) : null
</script>
<template>
  <dialog ref="dialog" class="pause-dialog" aria-labelledby="pause-dialog-title" @keydown="keydown" @cancel.prevent="pause.close()" @click="event => { if (event.target === dialog) pause.close() }">
    <div v-if="pause.mode" class="pause-card">
      <header class="pause-head"><h2 id="pause-dialog-title">{{ title }}</h2><button class="icon-btn flat" type="button" aria-label="Close pause dialog" :disabled="pause.busy" @click="pause.close()"><AppIcon name="close" /></button></header>
      <!-- Desktop pattern A: actions stay above all variable content. Phone pattern B pins this bar. -->
      <div class="pause-actions">
        <button type="button" data-submit class="btn primary" :class="{ danger: stop || (!resume && pause.level === 'stop_now') }" :disabled="pause.busy || (resume ? !pause.resumeIds.length : !selected.length)" @click="pause.submit()">{{ command }}<span class="submit-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button>
        <button type="button" class="btn ghost" :disabled="pause.busy" @click="pause.close()">Cancel<kbd class="keycap">Esc</kbd></button>
      </div>
      <div class="pause-body">
        <p v-if="pause.error" class="pause-error" role="alert">{{ pause.error }}</p>
        <template v-if="resume">
          <p>Continue from the saved handover, on the same branch and harness. The request is saved; a launcher must start the continuation.</p>
          <label v-for="row in pause.rows" :key="row.target.id" class="resume-row"><input v-if="bulk" v-model="pause.resumeIds" type="checkbox" :value="row.target.id" :aria-label="`Resume ${row.target.name}`" :disabled="pause.busy" /><span><strong>{{ row.target.name }}</strong><span>{{ row.session.host }} · {{ row.session.branch || 'branch not reported' }}<template v-if="row.session.pause?.handover?.open_questions.length"> · {{ row.session.pause.handover.open_questions.length }} open questions</template></span></span></label>
          <button class="btn sm ghost" type="button" :aria-expanded="description" @click="description = !description">{{ description ? 'Hide' : 'Show' }} continuation brief<AppIcon name="chevron" :size="12" /></button>
          <div v-if="description" class="continuation"><article v-for="row in pause.rows" :key="row.target.id"><strong>{{ row.target.name }}</strong><p>{{ row.session.pause?.handover?.state || 'No handover available.' }}</p><ol><li v-for="step in row.session.pause?.handover?.next_steps" :key="step">{{ step }}</li></ol><ul><li v-for="question in row.session.pause?.handover?.open_questions" :key="question">{{ question }}</li></ul></article></div>
        </template>
        <template v-else-if="stop"><p>Request an immediate stop for this generation. No handover is saved; work in progress may be lost. Process exit is shown only when reported.</p></template>
        <template v-else>
          <div class="pause-levels" role="radiogroup" aria-label="Pause level" @keydown="levelKeys">
            <button v-for="option in levels" :key="option.value" type="button" role="radio" :data-level="option.value" :aria-checked="pause.level === option.value" :tabindex="pause.level === option.value ? 0 : -1" :disabled="pause.busy" @click="pause.level = option.value">
              <span class="level-name"><AppIcon :name="option.value === 'stop_now' ? 'halt' : 'pause'" :size="15" />{{ option.name }}</span><span class="level-result">{{ bulk ? option.value === 'stop_now' ? 'now · no handover' : option.value === 'pause_quickly' ? 'limit 3 min · WIP handover' : 'limit 10 min · handover' : `${detail(option.value)?.time} · ${detail(option.value)?.outcome}` }}</span>
            </button>
          </div>
          <div class="description-slot"><p v-for="option in levels" :key="option.value" :class="{ chosen: pause.level === option.value }" :aria-hidden="pause.level !== option.value">{{ option.rule }} {{ bulk ? '' : detail(option.value)?.detail }}</p></div>
          <p class="source">Predictions use the agent’s latest planning report. Missing or stale timing is shown as unknown; a limit is not an exit confirmation.</p>
          <label v-if="pause.workerOptions.length && !bulk" class="workers-choice"><input type="checkbox" :checked="pause.workersIncluded" :disabled="pause.busy" @change="pause.includeWorkers(($event.target as HTMLInputElement).checked)" />Also pause its {{ pause.workerOptions.length }} workers at this level</label>
          <div v-if="bulk" class="pause-agents">
            <div v-for="row in pause.rows" :key="row.target.id" class="override-row"><strong>{{ row.target.name }}</strong><select :aria-label="`Level for ${row.target.name}`" :disabled="pause.busy" :value="pause.overrides[row.target.id] || ''" @change="pause.overrides[row.target.id] = ($event.target as HTMLSelectElement).value as PauseLevel | 'keep'"><option value="">Everyone’s level</option><option v-for="option in LEVELS.filter(l => row.session.supported_pause_levels?.includes(l.value) ?? (l.value === 'stop_now' || row.session.advertised_capabilities.includes('inbox') || row.session.advertised_capabilities.includes('pause')))" :key="option.value" :value="option.value">{{ option.name }}</option><option value="keep">Keep running</option></select><span>{{ pause.selectedLevel(row.session) === 'keep' ? 'keeps running' : `${prediction(row.session, pause.selectedLevel(row.session) as PauseLevel, Date.now(), pause.interval).time} · ${prediction(row.session, pause.selectedLevel(row.session) as PauseLevel, Date.now(), pause.interval).outcome}` }}</span></div>
          </div>
          <label class="pause-note">Note for the handover <span>optional</span><textarea v-model="pause.note" rows="2" maxlength="2000" :disabled="pause.busy || pause.level === 'stop_now'" placeholder="What should the next session know?" /></label>
        </template>
      </div>
    </div>
  </dialog>
</template>
<style scoped>
.submit-keys{display:inline-flex;gap:2px;align-items:center;flex:none}

.pause-dialog{position:fixed;inset:64px auto auto 50%;transform:translateX(-50%);margin:0;padding:0;width:min(700px,calc(100vw - 32px));max-width:none;max-height:calc(100dvh - 100px);border:1px solid var(--glass-edge);border-radius:16px;background:var(--surface-raised);color:var(--ink);box-shadow:var(--shadow-pop);overflow:hidden}
.pause-dialog::backdrop{background:var(--scrim)}.pause-card{display:grid;grid-template-rows:auto auto minmax(0,1fr);max-height:calc(100dvh - 102px)}.pause-head{display:flex;align-items:center;justify-content:space-between;padding:16px 20px 4px;gap:12px}.pause-head h2{font-size:19px;overflow-wrap:anywhere}.pause-actions{display:flex;gap:8px;padding:12px 20px;border-bottom:1px solid var(--line)}.pause-actions .primary{width:270px;min-width:270px}.pause-body{overflow:auto;overscroll-behavior:contain;padding:16px 20px 20px;display:grid;gap:14px}.pause-body p{margin:0;font-size:13px;line-height:1.6}.pause-levels{border-top:1px solid var(--line)}.pause-levels button{display:grid;grid-template-columns:150px minmax(0,1fr);align-items:center;gap:12px;width:100%;height:50px;text-align:left;padding:6px 10px;border:0;border-bottom:1px solid var(--line);background:transparent;color:var(--ink)}.pause-levels button[aria-checked=true]{background:var(--row-selected);box-shadow:inset 0 0 0 1px var(--glass-rim)}.level-name{display:flex;gap:8px;align-items:center;font-weight:600}.level-result{font-size:12px;color:var(--ink-2);font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.description-slot{display:grid}.description-slot p{grid-area:1/1;visibility:hidden}.description-slot p.chosen{visibility:visible}.source{color:var(--ink-3)}.pause-note{display:grid;gap:8px;font-size:13px}.pause-note span{color:var(--ink-3)}.pause-note textarea{width:100%;height:64px;resize:none;padding:9px 12px;border:1px solid var(--line-2);border-radius:8px;background:var(--field-bg);color:var(--ink);font:inherit}.override-row{display:grid;grid-template-columns:minmax(0,1fr) 150px 210px;gap:8px;align-items:center;min-height:54px;border-bottom:1px solid var(--line)}.override-row strong{font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.override-row select{width:100%;height:32px;background:var(--field-bg);color:var(--ink);border:1px solid var(--line);border-radius:6px;font-size:12px}.override-row>span{font-size:12px;color:var(--ink-2)}.pause-error{color:var(--danger);overflow-wrap:anywhere}.resume-row{display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--line);padding-bottom:12px}.resume-row>span{display:grid;gap:4px}.resume-row>input{flex:none}.resume-row span span{color:var(--ink-2);overflow-wrap:anywhere}.continuation p,.continuation li{white-space:pre-wrap;overflow-wrap:anywhere}.continuation article+article{border-top:1px solid var(--line);padding-top:12px}
@media(max-width:600px){.pause-dialog{inset:0;width:100%;height:100dvh;max-height:none;transform:none;border-radius:0}.pause-card{height:100dvh;max-height:none;grid-template-rows:auto minmax(0,1fr) auto}.pause-head{padding:12px 16px}.pause-head .icon-btn{width:44px;height:44px}.pause-actions{grid-row:3;border-top:1px solid var(--line);border-bottom:0;padding:12px 12px calc(12px + env(safe-area-inset-bottom))}.pause-actions .primary{width:auto;min-width:0;flex:1}.pause-actions .btn{min-height:44px;font-size:12px;padding-inline:10px}.pause-body{grid-row:2;padding:12px 16px}.pause-levels button{grid-template-columns:120px minmax(0,1fr);height:60px;gap:6px}.override-row{grid-template-columns:minmax(0,1fr) 145px;min-height:78px}.override-row>span{grid-column:1/-1;height:24px}}
</style>
