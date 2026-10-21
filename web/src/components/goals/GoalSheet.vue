<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// New goal and Edit goal for people (AEON-1618, Tick spec v1 §1 and §3): the
// NewProjectSheet pattern. Title, Where (a project the person may write, or the
// whole workspace when they may write it) and an optional Lands day. The sheet
// is bound to the person and, when editing, to the goal it opened for: an answer
// for anyone else is dropped. Where stays as it is while editing.
import { computed, ref } from 'vue'
import { APIError } from '../../lib/api'
import { createGoal, landsDay, updateGoal, type Goal } from '../../lib/goals'
import { useIdentityScope } from '../../lib/useIdentityScope'
import type { Project } from '../../stores/projects'
import KeyCap from '../KeyCap.vue'
import ProjectSettingsSheet from '../settings/ProjectSettingsSheet.vue'
import TargetDateField from '../projects/TargetDateField.vue'

const props = defineProps<{ goal: Goal | null; projects: readonly Project[]; workspace: boolean; preferred?: string | null }>()
const emit = defineEmits<{ close: []; created: [id: string]; saved: [id: string] }>()
const scope = useIdentityScope()
const editing = props.goal
const WORKSPACE = 'workspace'
// The current project first, else the first the person may write; the workspace when that is all.
const firstPlace = () => {
  if (editing) return editing.project_id ?? WORKSPACE
  if (props.preferred && props.projects.some(project => project.id === props.preferred)) return props.preferred
  return props.projects[0]?.id ?? (props.workspace ? WORKSPACE : '')
}
const title = ref(editing?.title ?? ''), place = ref(firstPlace()), lands = ref(editing?.eta?.source === 'agent' ? landsDay(editing.eta.at) : '')
const busy = ref(false), error = ref('')
// A place the person cannot write anymore (or a goal elsewhere) is still named while editing.
const editedPlace = computed(() => editing?.projects.find(badge => badge.id === editing.project_id))
const valid = computed(() => !!title.value.trim() && title.value.trim().length <= 300 && !!place.value && !busy.value)
const dateText = (value: string) => {
  const [y, m, d] = value.split('-').map(Number)
  return y && m && d ? new Date(y, m - 1, d).toLocaleDateString('en', { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }) : value
}

function submit() {
  if (!valid.value) return
  const snapshot = { title: title.value.trim(), place: place.value, lands: lands.value }
  busy.value = true; error.value = ''
  if (editing) {
    const id = editing.id, before = { title: editing.title, lands: editing.eta?.source === 'agent' ? landsDay(editing.eta.at) : '' }
    const change = { ...(snapshot.title !== before.title ? { title: snapshot.title } : {}), ...(snapshot.lands !== before.lands ? { lands: snapshot.lands } : {}) }
    if (!Object.keys(change).length) { busy.value = false; emit('close'); return }
    void scope.run(({ after }) => after(updateGoal(id, change), () => emit('saved', id)), { failed: failed, settled: () => { busy.value = false } })
    return
  }
  void scope.run(({ after }) => after(createGoal(snapshot.title, snapshot.place === WORKSPACE ? null : snapshot.place, snapshot.lands), result => emit('created', result.id)), { failed, settled: () => { busy.value = false } })
}
function failed(cause: unknown) {
  error.value = cause instanceof APIError && cause.status === 403 ? 'You may not set goals there. Choose another place.'
    : cause instanceof Error ? cause.message : 'The goal could not be saved. Nothing was changed.'
}
</script>

<template>
  <ProjectSettingsSheet :title="editing ? 'Edit goal' : 'New goal'" @close="emit('close')" @submit="submit">
    <form id="goal-form" class="gs" novalidate @submit.prevent="submit">
      <label class="gs-f"><b>Title</b><input id="goal-title" v-model="title" class="field" autocomplete="off" maxlength="300" placeholder="What should be true when this is done?" :disabled="busy" autofocus></label>
      <label class="gs-f"><b>Where</b>
        <select id="goal-where" v-model="place" class="field" :disabled="busy || !!editing">
          <template v-if="editing && editing.project_id && !projects.some(project => project.id === editing!.project_id)"><option :value="editing.project_id">{{ editedPlace ? `${editedPlace.key} · ${editedPlace.name}` : 'This project' }}</option></template>
          <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.routeKey }} · {{ project.title }}</option>
          <option v-if="workspace || editing?.workspace" :value="WORKSPACE">Whole workspace</option>
        </select>
      </label>
      <div class="gs-f">
        <TargetDateField v-model="lands" label="Lands" optional="optional" empty="No date" clear="Clear the date" :text="dateText" :disabled="busy" />
        <small>Optional. Progress fills in from linked work.</small>
      </div>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
    <template #actions>
      <button type="button" class="btn" @click="emit('close')">Cancel <KeyCap k="Esc" /></button>
      <button id="goal-go" type="submit" form="goal-form" class="btn primary" :disabled="!valid" :aria-busy="busy">{{ editing ? 'Save' : 'Create goal' }} <KeyCap k="mod" /><KeyCap k="enter" /></button>
    </template>
  </ProjectSettingsSheet>
</template>

<style scoped>
.gs { display: grid; gap: 18px; padding-top: 8px; }
.gs-f { display: grid; gap: 6px; min-width: 0; }
.gs-f > b { font-size: 13px; font-weight: 600; }
.gs-f > small { font-size: 12px; line-height: 1.5; color: var(--ink-3); overflow-wrap: anywhere; }
.gs-f .field { width: 100%; height: 38px; min-width: 0; }
.gs .field:disabled { opacity: 1; color: var(--ink-2); background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); }
.error { font-size: 12.5px; color: var(--danger); overflow-wrap: anywhere; }
.btn.primary[aria-busy="true"] { cursor: progress; }
@media (pointer: coarse), (max-width: 600px) { .gs-f .field { min-height: 44px; height: 44px; } .btn { min-height: 44px; } }
/* Phones have no keyboard to hint at. */
@media (max-width: 600px) { .keycap { display: none; } }
</style>
