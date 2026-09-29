<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'

interface Competitor { id: string; name: string; published: boolean }
interface Aspect { id: string; label: string }
interface Cell {
  id: string
  aspect_id: string
  competitor_id: string
  stance: string
  quote?: string
  source_url?: string
  retrieved_on?: string
  approved: boolean
  stale: boolean
  recheck: boolean
}
interface Revision {
  cell_id: string
  quote?: string
  retrieved_on?: string
  approved: boolean
  at: string
}
interface Correction { id: string; competitor: string; aspect: string; statement: string; source_url?: string }
interface Market {
  competitors: Competitor[]
  aspects: Aspect[]
  cells: Cell[]
  history: Revision[]
  corrections: Correction[]
}

const STANCES = [
  { id: 'unknown', label: 'No claim' },
  { id: 'yes', label: 'Yes' },
  { id: 'no', label: 'No' },
  { id: 'partial', label: 'Partly' },
] as const

const loading = ref(true)
const error = ref('')
const saving = ref('')
const market = ref<Market>(blank())
const adding = ref<'competitor' | 'aspect' | ''>('')
const draftName = ref('')
const confirmRemove = ref('')
const aspectId = ref('')
const competitorId = ref('')
const stance = ref('unknown')
const quote = ref('')
const source = ref('')
const day = ref('')

const ready = computed(() => market.value.competitors.length > 0 && market.value.aspects.length > 0)
const stored = computed(() => market.value.cells.find(item => item.aspect_id === aspectId.value && item.competitor_id === competitorId.value))
const dirty = computed(() => {
  const cell = stored.value
  if (!cell) return stance.value !== 'unknown' || quote.value.trim() !== '' || source.value.trim() !== '' || day.value !== ''
  return stance.value !== cell.stance || quote.value !== (cell.quote ?? '') || source.value !== (cell.source_url ?? '') || day.value !== (cell.retrieved_on ?? '')
})
const approvable = computed(() => {
  const cell = stored.value
  if (!cell || cell.approved || dirty.value) return false
  if (cell.stance === 'unknown') return true
  if (cell.stance !== 'yes' && cell.stance !== 'no' && cell.stance !== 'partial') return false
  return (cell.quote ?? '').trim().length >= 8 && (cell.source_url ?? '').startsWith('https://') && /^\d{4}-\d{2}-\d{2}$/.test(cell.retrieved_on ?? '')
})
const older = computed(() => {
  const id = stored.value?.id
  if (!id) return []
  return market.value.history.filter(item => item.cell_id === id).slice(1)
})

function blank(): Market {
  return { competitors: [], aspects: [], cells: [], history: [], corrections: [] }
}
function stanceWord(value: string) {
  return STANCES.find(item => item.id === value)?.label ?? 'No claim'
}
function pair(aspect: string, competitor: string) {
  return market.value.cells.find(item => item.aspect_id === aspect && item.competitor_id === competitor)
}
function stanceOf(aspect: string, competitor: string) {
  return stanceWord(pair(aspect, competitor)?.stance ?? 'unknown')
}
function flagOf(aspect: string, competitor: string) {
  const cell = pair(aspect, competitor)
  if (cell?.stale) return 'Stale'
  if (cell?.recheck) return 'Due again'
  return ''
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

function open(nextAspect: string, nextCompetitor: string) {
  aspectId.value = nextAspect
  competitorId.value = nextCompetitor
  const cell = pair(nextAspect, nextCompetitor)
  stance.value = cell?.stance || 'unknown'
  quote.value = cell?.quote ?? ''
  source.value = cell?.source_url ?? ''
  day.value = cell?.retrieved_on ?? ''
  confirmRemove.value = ''
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const next = await read<Partial<Market>>('/portal/market')
    market.value = {
      competitors: next.competitors ?? [],
      aspects: next.aspects ?? [],
      cells: next.cells ?? [],
      history: next.history ?? [],
      corrections: next.corrections ?? [],
    }
    if (market.value.aspects.some(item => item.id === aspectId.value) && market.value.competitors.some(item => item.id === competitorId.value)) {
      open(aspectId.value, competitorId.value)
    } else if (market.value.aspects[0] && market.value.competitors[0]) {
      open(market.value.aspects[0].id, market.value.competitors[0].id)
    } else {
      aspectId.value = ''
      competitorId.value = ''
    }
  } catch (cause) {
    error.value = cause instanceof Error && cause.message ? cause.message : 'The comparison could not be loaded.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function add(kind: 'competitor' | 'aspect') {
  const value = draftName.value.trim()
  if (!value || saving.value) return
  saving.value = kind
  error.value = ''
  try {
    if (kind === 'competitor') await read('/portal/competitors', 'POST', { name: value })
    else await read('/portal/aspects', 'POST', { label: value })
    draftName.value = ''
    adding.value = ''
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function publish(item: Competitor, published: boolean) {
  if (saving.value) return
  saving.value = item.id
  error.value = ''
  try {
    await read(`/portal/competitors/${encodeURIComponent(item.id)}`, 'PATCH', { published })
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function remove(kind: 'competitor' | 'aspect', id: string) {
  if (confirmRemove.value !== id) {
    confirmRemove.value = id
    return
  }
  if (saving.value) return
  saving.value = id
  error.value = ''
  try {
    const path = kind === 'competitor' ? `/portal/competitors/${encodeURIComponent(id)}` : `/portal/aspects/${encodeURIComponent(id)}`
    await read(path, 'DELETE')
    confirmRemove.value = ''
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function saveCell() {
  if (!aspectId.value || !competitorId.value || saving.value) return
  saving.value = 'cell'
  error.value = ''
  try {
    await read('/portal/cells', 'PUT', {
      aspect_id: aspectId.value,
      competitor_id: competitorId.value,
      stance: stance.value,
      quote: quote.value.trim(),
      source_url: source.value.trim(),
      retrieved_on: day.value,
    })
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function approve() {
  const id = stored.value?.id
  if (!id || saving.value) return
  saving.value = 'approve'
  error.value = ''
  try {
    await read(`/portal/cells/${encodeURIComponent(id)}/approve`, 'POST', {})
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}

async function closeCorrection(id: string) {
  if (saving.value) return
  saving.value = id
  error.value = ''
  try {
    await read(`/portal/corrections/${encodeURIComponent(id)}/close`, 'POST', {})
    await load()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'That was not saved.'
  } finally {
    saving.value = ''
  }
}
</script>

<template>
  <SettingsCard title="Comparison" icon="compare" anchor="comparison">
    <template #lead>Approve each sourced fact. It is due again after 90 days and stale after 180.</template>
    <template v-if="ready && !loading" #aside>
      <button type="button" class="btn sm" @click="adding = 'competitor'; draftName = ''">Add competitor</button>
      <button type="button" class="btn sm" @click="adding = 'aspect'; draftName = ''">Add aspect</button>
    </template>
    <div v-if="loading" class="set-skeleton" role="status" aria-label="Loading the comparison"><span class="skeleton" /></div>
    <template v-else>
      <p v-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>

      <form v-if="adding" class="form" @submit.prevent="add(adding)">
        <label>{{ adding === 'competitor' ? 'Competitor' : 'Aspect' }}
          <input v-model="draftName" class="field" required maxlength="120" />
        </label>
        <div class="actions">
          <button :class="ready ? 'btn' : 'btn primary'" type="submit" :disabled="!!saving || !draftName.trim()">Add</button>
          <button class="btn ghost" type="button" @click="adding = ''">Cancel</button>
        </div>
      </form>

      <template v-if="!market.competitors.length && !adding">
        <p class="empty-line">No competitors yet.</p>
        <button type="button" class="btn primary" @click="adding = 'competitor'"><AppIcon name="plus" :size="13" />Add competitor</button>
      </template>
      <template v-else-if="!market.aspects.length && !adding">
        <div v-for="item in market.competitors" :key="item.id" class="person">
          <div class="copy">
            <p class="name" :title="item.name">{{ item.name }}</p>
            <p v-if="!item.published" class="meta">Hidden</p>
          </div>
          <label class="public"><input type="checkbox" :checked="item.published" :disabled="!!saving" @change="publish(item, ($event.target as HTMLInputElement).checked)" /> Public</label>
          <button type="button" class="btn ghost sm" :disabled="saving === item.id" @click="remove('competitor', item.id)">
            <AppIcon v-if="confirmRemove !== item.id" name="trash" :size="13" />
            <span v-if="confirmRemove === item.id">Remove</span>
            <span v-else class="sr-only">Take {{ item.name }} off the list</span>
          </button>
        </div>
        <button type="button" class="btn primary" @click="adding = 'aspect'"><AppIcon name="plus" :size="13" />Add aspect</button>
      </template>

      <template v-if="ready">
        <div v-for="item in market.competitors" :key="item.id" class="person">
          <div class="copy">
            <p class="name" :title="item.name">{{ item.name }}</p>
            <p v-if="!item.published" class="meta">Hidden</p>
          </div>
          <label class="public"><input type="checkbox" :checked="item.published" :disabled="!!saving" @change="publish(item, ($event.target as HTMLInputElement).checked)" /> Public</label>
          <button type="button" class="btn ghost sm" :disabled="saving === item.id" @click="remove('competitor', item.id)">
            <AppIcon v-if="confirmRemove !== item.id" name="trash" :size="13" />
            <span v-if="confirmRemove === item.id">Remove</span>
            <span v-else class="sr-only">Take {{ item.name }} off the list</span>
          </button>
        </div>
        <article v-for="aspect in market.aspects" :key="aspect.id" class="aspect">
          <div class="aspect-head">
            <h3 :title="aspect.label">{{ aspect.label }}</h3>
            <button type="button" class="btn ghost sm" :disabled="saving === aspect.id" @click="remove('aspect', aspect.id)">
              <AppIcon v-if="confirmRemove !== aspect.id" name="trash" :size="13" />
              <span v-if="confirmRemove === aspect.id">Remove</span>
              <span v-else class="sr-only">Take {{ aspect.label }} off the list</span>
            </button>
          </div>
          <div class="cells">
            <button
              v-for="competitor in market.competitors"
              :key="competitor.id"
              type="button"
              class="cell"
              :aria-pressed="aspectId === aspect.id && competitorId === competitor.id"
              @click="open(aspect.id, competitor.id)"
            >
              <span class="who" :title="competitor.name">{{ competitor.name }}</span>
              <span class="stance">{{ stanceOf(aspect.id, competitor.id) }}</span>
              <span v-if="flagOf(aspect.id, competitor.id)" class="flag">{{ flagOf(aspect.id, competitor.id) }}</span>
            </button>
          </div>
        </article>

        <form v-if="aspectId && competitorId" class="form editor" @submit.prevent="saveCell">
          <label>Stance
            <select v-model="stance" class="field">
              <option v-for="item in STANCES" :key="item.id" :value="item.id">{{ item.label }}</option>
            </select>
          </label>
          <label>Quote<textarea v-model="quote" class="field" maxlength="400" rows="3" /></label>
          <label>Source page<input v-model="source" class="field" type="url" maxlength="500" /></label>
          <label>Read on<input v-model="day" class="field" type="date" /></label>
          <p v-if="stored?.approved && !dirty" class="meta">Approved.</p>
          <p v-if="stored?.stale" class="flag">Stale</p>
          <p v-else-if="stored?.recheck" class="flag">Due again</p>
          <div class="actions">
            <button v-if="dirty" class="btn primary" type="submit" :disabled="!!saving">Save</button>
            <button v-else-if="approvable" class="btn primary" type="button" :disabled="!!saving" @click="approve">Approve</button>
          </div>
          <details v-if="older.length">
            <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Earlier versions</summary>
            <div v-for="(item, index) in older" :key="`${item.at}-${index}`" class="revision">
              <p v-if="item.quote" class="quote" :title="item.quote">{{ item.quote }}</p>
              <p class="meta"><span v-if="item.retrieved_on">Read {{ item.retrieved_on }}</span><span v-if="item.approved">Approved</span></p>
            </div>
          </details>
        </form>
      </template>

      <div v-if="market.corrections.length" class="corrections">
        <h3>Corrections</h3>
        <div v-for="item in market.corrections" :key="item.id" class="person">
          <div class="copy">
            <p class="name" :title="item.statement">{{ item.competitor }} · {{ item.aspect }}</p>
            <p class="meta" :title="item.statement">{{ item.statement }}</p>
          </div>
          <button type="button" class="btn sm" :disabled="saving === item.id" @click="closeCorrection(item.id)">Done</button>
        </div>
      </div>
    </template>
  </SettingsCard>
</template>

<style scoped>
.empty-line { margin: 0 0 12px; color: var(--ink-2); }
.form { display: grid; gap: 10px; }
.form label { display: grid; gap: 4px; font-size: 12.5px; color: var(--ink-2); }
textarea.field { height: auto; min-height: 76px; padding-block: 8px; line-height: 1.45; resize: vertical; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.person { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; min-width: 0; padding: 10px 0; }
.copy { flex: 1 1 8rem; min-width: 0; }
.name { margin: 0; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta { margin: 3px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.4; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.public { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-2); font-size: 13px; }
.aspect { margin-top: 8px; }
.aspect-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 0; }
h3 { margin: 0; min-width: 0; font: 600 14px/1.35 var(--font); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cells { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 180px), 1fr)); gap: 8px; margin-top: 8px; }
.cell {
  display: grid;
  gap: 2px;
  min-width: 0;
  padding: 10px 12px;
  border: 0;
  border-radius: 12px;
  background: var(--surface);
  box-shadow: inset 0 0 0 1px var(--line);
  color: var(--ink);
  text-align: left;
  cursor: pointer;
}
.cell[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.cell:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.who { color: var(--ink-3); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.stance { font-weight: 650; }
.flag { margin: 0; color: var(--ink-2); font-size: 12.5px; }
.editor { margin-top: 14px; }
.quote { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.revision { padding-top: 8px; }
.revision .meta { display: flex; gap: 8px; }
.corrections { margin-top: 16px; }
.corrections h3 { margin-bottom: 4px; }
details { color: var(--ink-2); font-size: 13px; }
summary { cursor: pointer; }
</style>
