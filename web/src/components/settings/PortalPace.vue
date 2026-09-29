<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, listNodes, type ListItem } from '../../lib/api'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'

const props = defineProps<{ features: ListItem[]; wishes: ListItem[] }>()

interface Pace {
  project_id?: string
  project_title?: string
  releases_30d?: number
  median_release_gap_days?: number
  wish_to_live_median_days?: number
  fulfillments?: { wish_id: string; feature_id: string }[]
}

const loading = ref(true)
const error = ref('')
const saving = ref('')
const pace = ref<Pace>({ fulfillments: [] })
const projects = ref<{ id: string; title: string }[]>([])

const live = computed(() => props.features.filter(item => item.state === 'live'))
const countable = computed(() => props.wishes.filter(item => item.state === 'published' || item.state === 'hidden'))
const figures = computed(() => {
  const rows: { value: string; label: string }[] = []
  if (typeof pace.value.releases_30d === 'number') rows.push({ value: String(pace.value.releases_30d), label: 'releases in 30 days' })
  if (typeof pace.value.median_release_gap_days === 'number') rows.push({ value: `${pace.value.median_release_gap_days} days`, label: 'between releases' })
  if (typeof pace.value.wish_to_live_median_days === 'number') rows.push({ value: `${pace.value.wish_to_live_median_days} days`, label: 'from wish to live' })
  return rows
})
const quiet = computed(() => !figures.value.length && !(live.value.length && countable.value.length))
const projectTitle = computed(() => projects.value.find(item => item.id === pace.value.project_id)?.title ?? pace.value.project_title ?? '')

function linkedFeature(wishId: string) {
  return pace.value.fulfillments?.find(item => item.wish_id === wishId)?.feature_id ?? ''
}

async function read<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, {
    method,
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    const message = data && typeof data === 'object' && typeof (data as { error?: unknown }).error === 'string'
      ? (data as { error: string }).error
      : 'That was not saved.'
    throw new Error(message)
  }
  return data as T
}

function store(next: Pace) {
  pace.value = { ...next, fulfillments: next.fulfillments ?? [] }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [current, listed] = await Promise.all([
      read<Pace>('/portal/pace'),
      listNodes({ kind: ['project'], limit: 200 }),
    ])
    store(current)
    projects.value = listed.items.map(item => ({ id: item.id, title: item.title })).sort((a, b) => a.title.localeCompare(b.title))
  } catch (cause) {
    error.value = cause instanceof Error && cause.message ? cause.message : 'Pace could not be loaded.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function chooseProject(id: string) {
  if (saving.value) return
  saving.value = 'project'
  error.value = ''
  try {
    store(await read<Pace>('/portal/pace', 'PUT', { project_id: id || null }))
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function chooseFeature(wishId: string, featureId: string) {
  if (saving.value) return
  saving.value = wishId
  error.value = ''
  try {
    store(await read<Pace>(`/portal/wishes/${encodeURIComponent(wishId)}/fulfillment`, 'PUT', { feature_id: featureId || null }))
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}
</script>

<template>
  <SettingsCard title="Pace" icon="gauge" anchor="pace">
    <template #lead>Release count from one project, and days from a wish to the feature that went live.</template>
    <div v-if="loading" class="set-skeleton" role="status" aria-label="Loading pace"><span class="skeleton" /></div>
    <template v-else>
      <p v-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <label class="choice">Releases from
        <select class="field" :value="pace.project_id ?? ''" :title="projectTitle" :disabled="!!saving" @change="chooseProject(($event.target as HTMLSelectElement).value)">
          <option value="">None</option>
          <option v-for="project in projects" :key="project.id" :value="project.id" :title="project.title">{{ project.title }}</option>
        </select>
      </label>
      <div v-if="figures.length" class="figures">
        <p v-for="figure in figures" :key="figure.label" class="figure">
          <strong>{{ figure.value }}</strong>
          <span>{{ figure.label }}</span>
        </p>
      </div>
      <p v-else-if="quiet" class="empty-line">Nothing to publish yet.</p>
      <div v-if="live.length && countable.length" class="links">
        <label v-for="wish in countable" :key="wish.id" class="choice">
          <span class="name" :title="wish.title">{{ wish.title }}</span>
          <select class="field" :value="linkedFeature(wish.id)" :disabled="saving === wish.id" :aria-label="`Feature that fulfilled ${wish.title}`" @change="chooseFeature(wish.id, ($event.target as HTMLSelectElement).value)">
            <option value="">Not linked</option>
            <option v-for="feature in live" :key="feature.id" :value="feature.id" :title="feature.title">{{ feature.title }}</option>
          </select>
        </label>
      </div>
    </template>
  </SettingsCard>
</template>

<style scoped>
.choice { display: grid; gap: 4px; margin: 0 0 12px; font-size: 12.5px; color: var(--ink-2); }
.figures { display: flex; flex-wrap: wrap; gap: 10px; margin-bottom: 12px; }
.figure {
  flex: 1 1 140px;
  min-width: 0;
  padding: 12px 14px;
  border-radius: 12px;
  background: var(--surface);
  box-shadow: inset 0 0 0 1px var(--line);
}
.figure strong { display: block; font: 650 22px/1.1 var(--serif); letter-spacing: -0.02em; font-variant-numeric: tabular-nums; }
.figure span { display: block; margin-top: 4px; color: var(--ink-2); font-size: 13px; }
.empty-line { margin: 0; color: var(--ink-2); }
.links { display: grid; gap: 10px; }
.name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
