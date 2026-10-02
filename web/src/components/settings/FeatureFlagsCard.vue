<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { APIError, getProjects, type ProjectSummary } from '../../lib/api'
import { getFeatureSettings, setFeatureOverride, type FeatureSetting } from '../../lib/features'
import SettingsCard from './SettingsCard.vue'

const projectId = ref('')
const projects = ref<ProjectSummary[]>([])
const items = ref<FeatureSetting[]>([])
const loading = ref(true)
const busy = ref(false)
const problem = ref('')
const notice = ref('')
let turn = 0
const mode = (item: FeatureSetting) => item.override === null ? 'inherit' : item.override ? 'on' : 'off'

async function load() {
  const current = ++turn
  loading.value = true; problem.value = ''; notice.value = ''; items.value = []
  try {
    const page = await getFeatureSettings(projectId.value || undefined)
    if (current === turn) items.value = page.items
  } catch { if (current === turn) problem.value = 'Feature settings could not be loaded. Try again.' }
  finally { if (current === turn) loading.value = false }
}
onMounted(() => {
  void getProjects().then(page => { projects.value = page.items }).catch(() => { /* workspace controls still work */ })
  void load()
})

async function save(item: FeatureSetting, event: Event) {
  const control = event.target as HTMLSelectElement
  const enabled = control.value === 'inherit' ? null : control.value === 'on'
  if (busy.value) return
  busy.value = true; problem.value = ''; notice.value = ''
  try {
    const result = await setFeatureOverride(item.key, enabled, item.revision, projectId.value || undefined)
    items.value = items.value.map(current => current.key === item.key ? result.feature : current)
    notice.value = 'Saved. The feature takes effect without a deployment.'
  } catch (error) {
    control.value = mode(item)
    problem.value = error instanceof APIError && error.status === 409
      ? 'Another admin changed this feature. Reload before saving.'
      : 'The feature could not be saved. Try again.'
  } finally { busy.value = false }
}
</script>

<template>
  <SettingsCard title="Feature flags" icon="sliders" anchor="feature-flags">
    <template #lead>Enable newly shipped features when your workspace is ready. New features start off.</template>
    <label class="scope" for="feature-scope">Apply to</label>
    <select id="feature-scope" v-model="projectId" :disabled="busy" @change="load">
      <option value="">Whole workspace</option>
      <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.key }} · {{ project.title }}</option>
    </select>
    <p class="hint">{{ projectId ? 'Projects inherit the workspace choice unless you set an override.' : 'Workspace defaults are off. Project overrides take precedence.' }}</p>
    <p v-if="loading" role="status">Loading feature settings…</p>
    <template v-else><div v-for="item in items" :key="item.key" class="feature">
      <div>
        <label :for="`feature-${item.key}`">{{ item.label }}</label>
        <p class="hint">{{ item.description }}</p>
        <p class="hint">Currently {{ item.enabled ? 'on' : 'off' }} · {{ item.source === 'default' ? 'default' : item.source === 'tenant' ? 'workspace choice' : 'project override' }}</p>
      </div>
      <select :id="`feature-${item.key}`" :value="mode(item)" :disabled="busy" @change="save(item, $event)">
        <option value="inherit">{{ projectId ? 'Use workspace choice' : 'Use default (off)' }}</option>
        <option value="on">On</option>
        <option value="off">Off</option>
      </select>
    </div></template>
    <p v-if="problem" class="problem" role="alert">{{ problem }} <button type="button" class="btn sm" :disabled="busy" @click="load">Reload</button></p>
    <p v-if="notice" class="hint" role="status">{{ notice }}</p>
  </SettingsCard>
</template>

<style scoped>
.scope { display: block; margin-bottom: 8px; }
label { color: var(--ink); font-size: 13px; font-weight: 500; }
select { max-width: 100%; min-height: 36px; padding: 7px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--ink); font: 13px var(--font); }
select:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 2px; }
.hint { margin-top: 5px; color: var(--ink-2); font-size: 12px; line-height: 1.5; }
.feature { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding-top: 16px; }
.feature > div { min-width: 0; }
.feature select { flex-shrink: 0; }
.problem { margin-top: 12px; color: var(--red-ink); font-size: 13px; }
@media (max-width: 600px) { .feature { flex-direction: column; align-items: stretch; gap: 8px; } select, .btn { min-height: 44px; } }
</style>
