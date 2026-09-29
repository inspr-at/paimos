<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import { setPageTitle } from '../lib/brand'
import { resilientFetch } from '../lib/api'

// A public catalog for one tenant. The page asks only for that portal, keeps
// the ballot cookie on a vote, and never renders a field the server did not name.
const props = defineProps<{ tenantSlug: string }>()

interface PortalFeature {
  key: string
  title: string
  summary: string
  status: 'idea' | 'reviewed' | 'planned' | 'in_progress' | 'live' | 'declined' | string
  live_since?: string
  legal_basis?: string
  decline_reason?: string
}
interface PortalWish {
  key: string
  title: string
  summary: string
  votes: number
}
interface PortalCell {
  competitor: string
  stance: 'yes' | 'no' | 'partial' | 'unknown' | string
  quote?: string
  source_url?: string
  retrieved_on?: string
  stale?: boolean
}
interface PortalComparison {
  aspect: string
  cells: PortalCell[]
}
interface PortalPace {
  releases_30d?: number
  median_release_gap_days?: number
  wish_to_live_median_days?: number
}
interface PortalDocument {
  product: { key: string; title: string; summary: string } | null
  catalog: PortalFeature[]
  wishes: PortalWish[]
  comparison?: PortalComparison[]
  pace?: PortalPace
}

const statusLabel: Record<string, string> = {
  idea: 'Idea',
  reviewed: 'Reviewed',
  planned: 'Planned',
  in_progress: 'In progress',
  live: 'Live',
  declined: 'Declined',
}
const FILTERS = [
  { id: 'live', label: 'Live' },
  { id: 'planned', label: 'Planned' },
  { id: 'in_progress', label: 'In progress' },
  { id: 'declined', label: 'Declined' },
] as const

const loading = ref(true)
const missing = ref(false)
const error = ref('')
const voteError = ref('')
const doc = ref<PortalDocument | null>(null)
const voted = ref<Record<string, boolean>>({})
const busy = ref('')
const filter = ref('')
const wishTitle = ref('')
const wishSummary = ref('')
const website = ref('')
const wishSent = ref(false)
const wishError = ref('')
const wishing = ref(false)
const correctionCompetitor = ref('')
const correctionAspect = ref('')
const correctionStatement = ref('')
const correctionSource = ref('')
const correctionSite = ref('')
const correctionSent = ref(false)
const correctionError = ref('')
const correcting = ref(false)

const stanceLabel: Record<string, string> = { yes: 'Yes', no: 'No', partial: 'Partly' }
const paceFigures = computed(() => {
  const pace = doc.value?.pace
  if (!pace) return []
  const figures: { value: string; label: string }[] = []
  if (typeof pace.releases_30d === 'number') figures.push({ value: String(pace.releases_30d), label: 'releases in 30 days' })
  if (typeof pace.median_release_gap_days === 'number') figures.push({ value: `${pace.median_release_gap_days} days`, label: 'between releases' })
  if (typeof pace.wish_to_live_median_days === 'number') figures.push({ value: `${pace.wish_to_live_median_days} days`, label: 'from wish to live' })
  return figures
})
const comparison = computed(() => doc.value?.comparison ?? [])

const showFilters = computed(() => (doc.value?.catalog.length ?? 0) > 6)
const visibleCatalog = computed(() => {
  const items = doc.value?.catalog ?? []
  return filter.value ? items.filter(item => item.status === filter.value) : items
})

const base = computed(() => `/api/public/portal/${encodeURIComponent(props.tenantSlug)}`)

function votesLabel(count: number) {
  return `${count} ${count === 1 ? 'vote' : 'votes'}`
}

async function load() {
  loading.value = true
  error.value = ''
  missing.value = false
  voteError.value = ''
  voted.value = {}
  filter.value = ''
  wishTitle.value = ''
  wishSummary.value = ''
  website.value = ''
  wishSent.value = false
  wishError.value = ''
  correctionCompetitor.value = ''
  correctionAspect.value = ''
  correctionStatement.value = ''
  correctionSource.value = ''
  correctionSite.value = ''
  correctionSent.value = false
  correctionError.value = ''
  try {
    const response = await resilientFetch(base.value, { credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' })
    if (response.status === 404) {
      missing.value = true
      doc.value = null
      setPageTitle('Portal unavailable')
      return
    }
    if (!response.ok) throw new Error('unavailable')
    doc.value = await response.json() as PortalDocument
    setPageTitle(doc.value.product?.title || 'Product portal')
  } catch {
    error.value = 'The portal could not be loaded.'
    doc.value = null
    setPageTitle('Product portal')
  } finally {
    loading.value = false
  }
}

function readOn(day: string) {
  const parsed = new Date(`${day}T00:00:00Z`)
  if (Number.isNaN(parsed.getTime())) return day
  return parsed.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

function sinceLabel(version: string | undefined) {
  const match = /^(\d{2})(\d{2})(\d{2})\d{6}\.0\.0$/.exec(version ?? '')
  if (!match) return ''
  const year = 2000 + Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const date = new Date(Date.UTC(year, month - 1, day))
  if (date.getUTCFullYear() !== year || date.getUTCMonth() + 1 !== month || date.getUTCDate() !== day) return ''
  const formatted = date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
  return `since ${formatted}`
}

async function sendCorrection() {
  const competitor = correctionCompetitor.value.trim()
  const aspect = correctionAspect.value.trim()
  const statement = correctionStatement.value.trim()
  if (!competitor || !aspect || statement.length < 8 || correcting.value) return
  correcting.value = true
  correctionError.value = ''
  try {
    const response = await resilientFetch(`${base.value}/corrections`, {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        competitor,
        aspect,
        statement,
        source_url: correctionSource.value.trim(),
        website: correctionSite.value,
      }),
    })
    if (response.status === 429) throw new Error('Too many corrections from this network. Try again in a minute.')
    if (response.status === 404) throw new Error('This portal is not taking corrections.')
    if (!response.ok) throw new Error('The correction was not sent.')
    correctionSent.value = true
    correctionCompetitor.value = ''
    correctionAspect.value = ''
    correctionStatement.value = ''
    correctionSource.value = ''
    correctionSite.value = ''
  } catch (cause) {
    correctionError.value = cause instanceof Error ? cause.message : 'The correction was not sent.'
  } finally {
    correcting.value = false
  }
}

function toggleFilter(id: string) {
  filter.value = filter.value === id ? '' : id
}

async function sendWish() {
  const title = wishTitle.value.trim()
  const summary = wishSummary.value.trim()
  if (!title || !summary || wishing.value) return
  wishing.value = true
  wishError.value = ''
  try {
    const response = await resilientFetch(`${base.value}/wishes`, {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title, summary, website: website.value }),
    })
    if (response.status === 429) throw new Error('Too many wishes from this network. Try again in a minute.')
    if (response.status === 404) throw new Error('This portal is not taking wishes.')
    if (!response.ok) throw new Error('The wish was not sent.')
    wishSent.value = true
    wishTitle.value = ''
    wishSummary.value = ''
    website.value = ''
  } catch (cause) {
    wishError.value = cause instanceof Error ? cause.message : 'The wish was not sent.'
  } finally {
    wishing.value = false
  }
}

async function vote(wish: PortalWish) {
  if (busy.value || voted.value[wish.key]) return
  busy.value = wish.key
  voteError.value = ''
  try {
    const response = await resilientFetch(`${base.value}/wishes/${encodeURIComponent(wish.key)}/votes`, {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })
    if (response.status === 429) throw new Error('Too many votes from this network. Try again in a minute.')
    if (response.status === 404) throw new Error('This wish is not open for votes.')
    if (!response.ok) throw new Error('The vote was not saved.')
    const body = await response.json() as { votes?: number }
    if (typeof body.votes === 'number') wish.votes = body.votes
    voted.value = { ...voted.value, [wish.key]: true }
  } catch (cause) {
    voteError.value = cause instanceof Error ? cause.message : 'The vote was not saved.'
  } finally {
    busy.value = ''
  }
}

watch(() => props.tenantSlug, () => { void load() }, { immediate: true })
</script>

<template>
  <div class="portal">
    <div class="sheet">
      <p v-if="loading" class="eyebrow" role="status">Loading the portal…</p>
      <template v-else-if="missing">
        <p class="eyebrow">Product portal</p>
        <h1>This portal is not available</h1>
        <p class="lead">The address may be wrong, or this workspace has not published a portal.</p>
      </template>
      <template v-else-if="error">
        <p class="eyebrow">Product portal</p>
        <h1>The portal could not be loaded</h1>
        <p class="lead" role="alert">{{ error }}</p>
        <button class="vote" type="button" @click="load"><AppIcon name="refresh" :size="14" />Try again</button>
      </template>
      <template v-else-if="doc && !doc.product">
        <p class="eyebrow">Product portal</p>
        <h1>Nothing published yet</h1>
        <p class="lead">This workspace has opened its portal and has not published a product.</p>
      </template>
      <template v-else-if="doc?.product">
        <p class="eyebrow">Product portal</p>
        <h1>{{ doc.product.title }}</h1>
        <p v-if="doc.product.summary" class="lead">{{ doc.product.summary }}</p>

        <section v-if="paceFigures.length" class="block" aria-labelledby="pace-heading">
          <h2 id="pace-heading">Pace</h2>
          <div class="pace">
            <p v-for="figure in paceFigures" :key="figure.label" class="figure">
              <strong>{{ figure.value }}</strong>
              <span>{{ figure.label }}</span>
            </p>
          </div>
        </section>

        <section class="block" aria-labelledby="catalog-heading">
          <h2 id="catalog-heading">Catalog</h2>
          <div v-if="showFilters" class="filters" role="group" aria-label="Feature status">
            <button v-for="item in FILTERS" :key="item.id" type="button" :aria-pressed="filter === item.id" @click="toggleFilter(item.id)">{{ item.label }}</button>
          </div>
          <p v-if="!doc.catalog.length" class="quiet">No public features yet.</p>
          <p v-else-if="!visibleCatalog.length" class="quiet">No features with this status.</p>
          <ul v-else class="catalog">
            <li v-for="item in visibleCatalog" :key="item.key" class="card">
              <div class="card-top">
                <h3>{{ item.title }}</h3>
                <p :class="['status', item.status]">{{ statusLabel[item.status] || item.status }}</p>
              </div>
              <p v-if="item.summary" class="summary">{{ item.summary }}</p>
              <p v-if="sinceLabel(item.live_since)" class="meta"><span :title="item.live_since">{{ sinceLabel(item.live_since) }}</span></p>
              <p v-if="item.legal_basis" class="meta"><span>Legal basis</span> {{ item.legal_basis }}</p>
              <p v-if="item.decline_reason" class="meta">{{ item.decline_reason }}</p>
            </li>
          </ul>
        </section>

        <section class="block" aria-labelledby="wishes-heading">
          <h2 id="wishes-heading">Wishes</h2>
          <p class="quiet">One vote from this browser. No account and no name.</p>
          <p v-if="voteError" class="alert" role="alert">{{ voteError }}</p>
          <p v-if="!doc.wishes.length" class="quiet">No published wishes yet.</p>
          <ul v-else class="wishes">
            <li v-for="wish in doc.wishes" :key="wish.key" class="wish">
              <div class="wish-copy">
                <h3>{{ wish.title }}</h3>
                <p v-if="wish.summary" class="summary">{{ wish.summary }}</p>
                <p class="count">{{ votesLabel(wish.votes) }}</p>
              </div>
              <button
                class="vote"
                type="button"
                :disabled="!!voted[wish.key] || busy === wish.key"
                :aria-label="voted[wish.key] ? `Voted for ${wish.title}` : `Vote for ${wish.title}`"
                @click="vote(wish)"
              >
                <AppIcon :name="voted[wish.key] ? 'check' : 'star'" :size="14" />
                {{ busy === wish.key ? 'Voting…' : voted[wish.key] ? 'Voted' : 'Vote' }}
              </button>
            </li>
          </ul>
          <form v-if="!wishSent" class="wish-form" @submit.prevent="sendWish">
            <label>Title<input v-model="wishTitle" class="field" name="title" required maxlength="300" autocomplete="off" /></label>
            <label>Summary<textarea v-model="wishSummary" class="field" name="summary" required maxlength="4000" rows="3" autocomplete="off" /></label>
            <div class="hp" aria-hidden="true">
              <label>Website<input v-model="website" type="text" tabindex="-1" autocomplete="off" name="website" /></label>
            </div>
            <p v-if="wishError" class="alert" role="alert">{{ wishError }}</p>
            <button class="vote" type="submit" :disabled="wishing || !wishTitle.trim() || !wishSummary.trim()"><AppIcon name="send" :size="14" />{{ wishing ? 'Sending…' : 'Send wish' }}</button>
          </form>
          <p v-else class="quiet" role="status">Sent for review.</p>
        </section>

        <section v-if="comparison.length" class="block" aria-labelledby="comparison-heading">
          <h2 id="comparison-heading">Comparison</h2>
          <p class="quiet">A sourced fact, or no claim.</p>
          <div class="aspects">
            <article v-for="row in comparison" :key="row.aspect" class="aspect">
              <h3 :title="row.aspect">{{ row.aspect }}</h3>
              <div class="cells">
                <div v-for="cell in row.cells" :key="cell.competitor" class="fact">
                  <p class="who" :title="cell.competitor">{{ cell.competitor }}</p>
                  <p v-if="stanceLabel[cell.stance]" class="stance">{{ stanceLabel[cell.stance] }}</p>
                  <p v-else class="stance"><span aria-label="Not sourced">—</span></p>
                  <a v-if="cell.quote && cell.source_url" class="quote-link" :href="cell.source_url" :title="cell.quote" target="_blank" rel="noopener noreferrer nofollow"><span class="quote">{{ cell.quote }}</span><AppIcon name="external" :size="13" /></a>
                  <p v-if="cell.retrieved_on" class="meta"><span>Read</span> {{ readOn(cell.retrieved_on) }}</p>
                  <p v-if="cell.stale" class="stale">Stale</p>
                </div>
              </div>
            </article>
          </div>
          <form v-if="!correctionSent" class="wish-form" @submit.prevent="sendCorrection">
            <h3>Correction</h3>
            <p class="quiet">A factual correction and the public page it comes from. No name.</p>
            <label>Competitor<input v-model="correctionCompetitor" class="field" name="competitor" required maxlength="80" autocomplete="off" /></label>
            <label>Aspect<input v-model="correctionAspect" class="field" name="aspect" required maxlength="120" autocomplete="off" /></label>
            <label>Statement<textarea v-model="correctionStatement" class="field" name="statement" required minlength="8" maxlength="2000" rows="3" autocomplete="off" /></label>
            <label>Source page<input v-model="correctionSource" class="field" name="source_url" type="url" maxlength="500" autocomplete="off" /></label>
            <div class="hp" aria-hidden="true">
              <label>Website<input v-model="correctionSite" type="text" tabindex="-1" autocomplete="off" name="website" /></label>
            </div>
            <p v-if="correctionError" class="alert" role="alert">{{ correctionError }}</p>
            <button class="vote" type="submit" :disabled="correcting || !correctionCompetitor.trim() || !correctionAspect.trim() || correctionStatement.trim().length < 8"><AppIcon name="send" :size="14" />{{ correcting ? 'Sending…' : 'Send correction' }}</button>
          </form>
          <p v-else class="quiet" role="status">Sent.</p>
        </section>
      </template>
    </div>
  </div>
</template>

<style scoped>
.portal {
  min-height: 100%;
  background:
    radial-gradient(900px 420px at 0% -10%, var(--wash-1), transparent 70%),
    var(--surface);
  color: var(--ink);
  font-family: var(--font);
}
.sheet { width: min(920px, 100%); margin: 0 auto; padding: 36px 20px 72px; }
.eyebrow {
  margin: 0 0 10px;
  font: 600 11px/1.4 var(--mono);
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--ink-3);
}
h1 {
  margin: 0;
  font: 650 clamp(34px, 5vw, 56px)/1.02 var(--serif);
  letter-spacing: -0.03em;
  text-wrap: balance;
  overflow-wrap: anywhere;
}
.lead { max-width: 38rem; margin: 14px 0 0; font-size: 18px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.block { margin-top: 40px; }
h2 { margin: 0 0 8px; font: 650 22px/1.2 var(--serif); letter-spacing: -0.02em; }
.quiet { margin: 0 0 14px; color: var(--ink-2); line-height: 1.45; }
.alert { margin: 0 0 14px; color: var(--danger); }
.catalog, .wishes { list-style: none; margin: 0; padding: 0; }
.catalog { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr)); gap: 12px; }
.pace { display: flex; flex-wrap: wrap; gap: 12px; }
.figure, .aspect, .fact {
  min-width: 0;
  border-radius: 16px;
  background: var(--surface-raised);
  box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(20, 40, 40, 0.04);
}
.figure { flex: 1 1 160px; padding: 16px 18px; }
.figure strong { display: block; font: 650 28px/1.1 var(--serif); letter-spacing: -0.03em; font-variant-numeric: tabular-nums; }
.figure span { display: block; margin-top: 6px; color: var(--ink-2); font-size: 13.5px; line-height: 1.35; }
.aspects { display: grid; gap: 12px; }
.aspect { padding: 16px; }
.cells { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr)); gap: 10px; margin-top: 12px; }
.fact { padding: 12px 14px; background: var(--surface); }
.who, .stance { margin: 0; }
.who { color: var(--ink-3); font: 600 12px/1.3 var(--font); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.stance { margin-top: 4px; font-weight: 650; }
.quote-link { display: flex; align-items: flex-start; gap: 6px; margin-top: 8px; color: var(--ink); }
.quote {
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-width: 0;
  font-size: 14px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}
.quote-link :deep(svg) { flex: 0 0 auto; margin-top: 3px; }
.stale { margin: 8px 0 0; color: var(--ink-2); font-size: 12.5px; }
.card, .wish {
  min-width: 0;
  border-radius: 16px;
  background: var(--surface-raised);
  box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(20, 40, 40, 0.04);
}
.card { padding: 18px; }
.card-top, .wish { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; }
.wish { padding: 16px 16px 16px 18px; margin-top: 12px; align-items: center; }
.wish-copy { min-width: 0; }
h3 { margin: 0; font-size: 17px; line-height: 1.3; font-weight: 650; overflow-wrap: anywhere; }
.summary { margin: 8px 0 0; color: var(--ink-2); font-size: 14.5px; line-height: 1.5; overflow-wrap: anywhere; }
.meta { display: flex; flex-wrap: wrap; gap: 6px; align-items: baseline; margin: 10px 0 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.4; overflow-wrap: anywhere; }
.meta span:first-child { color: var(--ink-3); }
.count { margin: 8px 0 0; font: 600 13px/1.3 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink); }
.filters { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 14px; }
.filters button {
  min-height: 36px;
  padding: 0 12px;
  border: 0;
  border-radius: 999px;
  background: var(--chip-bg);
  box-shadow: inset 0 0 0 1px var(--chip-line);
  color: var(--ink-2);
  font: 600 13px/1 var(--font);
  cursor: pointer;
}
.filters button[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.filters button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.wish-form { position: relative; display: grid; gap: 10px; margin-top: 18px; }
.wish-form label { display: grid; gap: 6px; font-size: 13px; color: var(--ink-2); }
.wish-form textarea.field { height: auto; min-height: 88px; padding-block: 10px; line-height: 1.45; resize: vertical; }
.hp { position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.status {
  display: inline-flex;
  align-items: center;
  align-self: flex-start;
  flex: 0 0 auto;
  min-height: 26px;
  padding: 0 10px;
  border-radius: 999px;
  background: var(--chip-bg);
  box-shadow: inset 0 0 0 1px var(--chip-line);
  color: var(--ink-2);
  font: 600 12px/1 var(--font);
}
.status.live { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.status.in_progress { background: var(--aqua-wash); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.status.declined { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.vote {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 44px;
  padding: 0 16px;
  border: 0;
  border-radius: 999px;
  background: var(--btn-bg);
  box-shadow: var(--shadow-btn);
  color: var(--ink);
  font: 600 14px/1 var(--font);
  cursor: pointer;
}
.vote:disabled { cursor: default; color: var(--ink-2); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.vote:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .vote:not(:disabled):hover { background: var(--btn-bg-hover); } }
@media (max-width: 560px) {
  .sheet { padding: 24px 16px 56px; }
  .card-top, .wish { flex-direction: column; align-items: stretch; }
  .vote { width: 100%; }
}
main:has(> .page-flow > .portal) { background: var(--surface); }
</style>
