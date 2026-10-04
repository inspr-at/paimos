<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { APIError, api, createNode, getKinds, listNodes, type Kind, type ListItem } from '../../lib/api'
import { calendarVersion, validVersion } from '../../lib/rules'
import { useReleases } from '../../stores/releases'
import AppIcon from '../AppIcon.vue'
import ReleaseName from '../ReleaseName.vue'
import PortalMarket from './PortalMarket.vue'
import PortalPace from './PortalPace.vue'
import SettingsCard from './SettingsCard.vue'

const FEATURE_STATUSES = [
  { id: 'idea', label: 'Idea' },
  { id: 'reviewed', label: 'Reviewed' },
  { id: 'planned', label: 'Planned' },
  { id: 'in_progress', label: 'In progress' },
  { id: 'live', label: 'Live' },
  { id: 'declined', label: 'Declined' },
] as const
type FeatureStatus = typeof FEATURE_STATUSES[number]['id']
const statusLabel = (state: string) => FEATURE_STATUSES.find(status => status.id === state)?.label ?? state

interface PortalSettings { enabled: boolean; slug: string }
interface Draft { title: string; summary: string; status: FeatureStatus; legal: string; reason: string; liveSince: string }

const loading = ref(true)
const error = ref('')
const notice = ref('')
const settings = ref<PortalSettings | null>(null)
const kinds = ref<Kind[]>([])
const product = ref<ListItem | null>(null)
const features = ref<ListItem[]>([])
const wishes = ref<ListItem[]>([])
const saving = ref('')
const copied = ref(false)
const composing = ref<'product' | 'feature' | 'wish' | ''>('')
const editing = ref('')
const draft = ref<Draft>(blank())
const formError = ref('')

const productKind = computed(() => kinds.value.find(kind => kind.slug === 'portal_product') ?? null)
const featureKind = computed(() => kinds.value.find(kind => kind.slug === 'portal_feature') ?? null)
const wishKind = computed(() => kinds.value.find(kind => kind.slug === 'portal_wish') ?? null)
const ready = computed(() => !!(productKind.value && featureKind.value && wishKind.value))
const publicURL = computed(() => settings.value?.slug ? new URL(`/portal/${settings.value.slug}`, window.location.origin).href : '')
const pending = computed(() => wishes.value.filter(wish => wish.state === 'pending'))
const publishedWishes = computed(() => wishes.value.filter(wish => wish.state === 'published'))
const hiddenWishes = computed(() => wishes.value.filter(wish => wish.state === 'hidden'))
const productDirty = computed(() => {
  if (!product.value) return draft.value.title.trim() !== '' || draft.value.summary.trim() !== ''
  return draft.value.title.trim() !== product.value.title || draft.value.summary.trim() !== product.value.body
})

function blank(): Draft {
  return { title: '', summary: '', status: 'planned', legal: '', reason: '', liveSince: '' }
}
function textField(node: ListItem, key: string) {
  const value = node.fields?.[key]
  return typeof value === 'string' ? value : ''
}
function byPosition(a: ListItem, b: ListItem) {
  return a.position.localeCompare(b.position, undefined, { numeric: true }) || a.key.localeCompare(b.key)
}
function explain(cause: unknown) {
  if (cause instanceof APIError && cause.status === 403) return 'Changing the portal needs permission to manage workspace settings and work.'
  return cause instanceof Error && cause.message ? cause.message : 'That was not saved.'
}

async function readSettings() {
  const response = await api('/portal/settings')
  if (response.status === 401) throw new APIError(401, 'your session has ended')
  if (!response.ok) throw new APIError(response.status, 'The portal settings could not be loaded.')
  const body = await response.json() as PortalSettings
  if (typeof body.enabled !== 'boolean' || typeof body.slug !== 'string') throw new APIError(500, 'The portal settings could not be loaded.')
  return body
}
async function listKind(slug: string, parent?: string) {
  const items: ListItem[] = []
  let cursor = ''
  for (let page = 0; page < 5; page++) {
    const result = await listNodes({ kind: [slug], parent_id: parent, limit: 200, sort: 'position', ...(cursor ? { cursor } : {}) })
    items.push(...result.items)
    if (!result.next_cursor) break
    cursor = result.next_cursor
  }
  return items.sort(byPosition)
}
async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const [portal, catalog] = await Promise.all([readSettings(), getKinds()])
    settings.value = portal
    kinds.value = catalog.items
    if (!productKind.value) {
      product.value = null
      features.value = []
      wishes.value = []
      return
    }
    const roots = (await listKind('portal_product')).filter(node => !node.parent_id)
    product.value = roots.find(node => node.state === 'published') ?? roots[0] ?? null
    if (!product.value) {
      features.value = []
      wishes.value = []
      return
    }
    ;[features.value, wishes.value] = await Promise.all([
      listKind('portal_feature', product.value.id),
      listKind('portal_wish', product.value.id),
    ])
  } catch (cause) {
    error.value = explain(cause)
  } finally {
    loading.value = false
  }
}
// "Live since" names the release; the history knows its name (AEON-430).
onMounted(() => { void useReleases().load(); void load() })

async function setEnabled(enabled: boolean) {
  if (!settings.value || saving.value) return
  const previous = settings.value.enabled
  settings.value = { ...settings.value, enabled }
  saving.value = 'switch'
  notice.value = ''
  try {
    const response = await api('/portal/settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) })
    if (!response.ok) throw new APIError(response.status, 'The portal was not updated.')
    settings.value = await response.json() as PortalSettings
  } catch (cause) {
    if (settings.value) settings.value = { ...settings.value, enabled: previous }
    notice.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
async function copyURL() {
  copied.value = false
  try {
    await navigator.clipboard.writeText(publicURL.value)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1600)
  } catch {
    notice.value = 'The address could not be copied.'
  }
}

function startProduct() {
  composing.value = 'product'
  editing.value = ''
  formError.value = ''
  draft.value = product.value
    ? { ...blank(), title: product.value.title, summary: product.value.body }
    : blank()
}
function startFeature(node?: ListItem) {
  composing.value = node ? '' : 'feature'
  editing.value = node?.id ?? ''
  formError.value = ''
  draft.value = node
    ? { title: node.title, summary: node.body, status: (FEATURE_STATUSES.some(status => status.id === node.state) ? node.state : 'planned') as FeatureStatus, legal: textField(node, 'legal_basis'), reason: textField(node, 'decline_reason'), liveSince: textField(node, 'live_since') }
    : blank()
}
function startWish() {
  composing.value = 'wish'
  editing.value = ''
  formError.value = ''
  draft.value = blank()
}
function closeForm() {
  composing.value = ''
  editing.value = ''
  formError.value = ''
}

async function writePortal(path: string, method: string, body?: unknown) {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  const response = await api(path, init)
  if (!response.ok) throw new APIError(response.status, 'The portal was not updated.')
}
async function saveProduct() {
  const title = draft.value.title.trim()
  if (!title) { formError.value = 'A product needs a title.'; return }
  if (!productKind.value) return
  saving.value = 'product'
  formError.value = ''
  try {
    if (product.value) {
      await writePortal(`/portal/products/${encodeURIComponent(product.value.id)}`, 'PATCH', { title, summary: draft.value.summary.trim(), published: true })
    } else {
      await createNode({ kind_id: productKind.value.id, title, body: draft.value.summary.trim(), state: 'published' })
    }
    closeForm()
    await load()
  } catch (cause) {
    formError.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
async function unpublish() {
  if (!product.value || saving.value) return
  saving.value = 'product'
  try {
    await writePortal(`/portal/products/${encodeURIComponent(product.value.id)}`, 'PATCH', { published: false })
    closeForm()
    await load()
  } catch (cause) {
    notice.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
async function saveFeature() {
  const title = draft.value.title.trim()
  if (!title) { formError.value = 'A feature needs a title.'; return }
  if (draft.value.status === 'declined' && !draft.value.reason.trim()) { formError.value = 'A declined feature needs a public reason.'; return }
  if (!featureKind.value || !product.value) return
  const fields: Record<string, string> = {}
  if (draft.value.legal.trim()) fields.legal_basis = draft.value.legal.trim()
  if (draft.value.status === 'declined') fields.decline_reason = draft.value.reason.trim()
  if (draft.value.status === 'live') fields.live_since = validVersion(draft.value.liveSince) ? draft.value.liveSince : calendarVersion()
  saving.value = 'feature'
  formError.value = ''
  try {
    if (editing.value) {
      await writePortal(`/portal/features/${encodeURIComponent(editing.value)}`, 'PATCH', {
        title,
        summary: draft.value.summary.trim(),
        status: draft.value.status,
        legal_basis: draft.value.legal.trim(),
        ...(draft.value.status === 'declined' ? { decline_reason: draft.value.reason.trim() } : {}),
        ...(draft.value.status === 'live' ? { live_since: fields.live_since } : {}),
      })
    } else {
      await createNode({ kind_id: featureKind.value.id, parent_id: product.value.id, title, body: draft.value.summary.trim(), state: draft.value.status, fields })
    }
    closeForm()
    await load()
  } catch (cause) {
    formError.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
async function saveWish() {
  const title = draft.value.title.trim()
  if (!title) { formError.value = 'A wish needs a title.'; return }
  if (!wishKind.value || !product.value) return
  saving.value = 'wish'
  formError.value = ''
  try {
    await createNode({ kind_id: wishKind.value.id, parent_id: product.value.id, title, body: draft.value.summary.trim(), state: 'published' })
    closeForm()
    await load()
  } catch (cause) {
    formError.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
async function setWish(node: ListItem, state: 'published' | 'hidden' | 'rejected') {
  if (saving.value) return
  saving.value = node.id
  notice.value = ''
  try {
    const action = state === 'published' ? 'publish' : state === 'hidden' ? 'hide' : 'reject'
    await writePortal(`/portal/wishes/${encodeURIComponent(node.id)}/${action}`, 'POST')
    await load()
  } catch (cause) {
    notice.value = explain(cause)
  } finally {
    saving.value = ''
  }
}
</script>

<template>
  <div class="section">
    <SettingsCard title="Product portal" icon="globe" anchor="publication">
      <template #lead>The public catalog and wishes for this workspace.</template>
      <template v-if="settings" #aside>
        <label class="switch">
          <input type="checkbox" :checked="settings.enabled" :disabled="!!saving" aria-labelledby="publication-title portal-state" @change="setEnabled(($event.target as HTMLInputElement).checked)" />
          <span id="portal-state">{{ settings.enabled ? 'On' : 'Off' }}</span>
        </label>
      </template>
      <div v-if="loading" class="set-skeleton" role="status" aria-label="Loading the portal"><span class="skeleton" /><span class="skeleton" /></div>
      <p v-else-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}<button type="button" class="btn sm" @click="load">Try again</button></p>
      <template v-else-if="settings">
        <p v-if="notice" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ notice }}</p>
        <div v-if="settings.enabled && publicURL" class="url-row">
          <a class="url" :href="publicURL" :title="publicURL">{{ publicURL }}</a>
          <button type="button" class="btn sm" @click="copyURL"><AppIcon name="copy" :size="13" />{{ copied ? 'Copied' : 'Copy' }}</button>
        </div>
        <p v-else class="empty-line">The public page is off.</p>
      </template>
    </SettingsCard>

    <SettingsCard v-if="!loading && !error && settings" title="Product" icon="box" anchor="product">
      <template #lead>One published product is the public page.</template>
      <p v-if="!ready" class="empty-line">Portal content is not available in this workspace.</p>
      <form v-else-if="composing === 'product'" class="form" @submit.prevent="saveProduct">
        <label>Title<input v-model="draft.title" class="field" required maxlength="300" /></label>
        <label>Summary<textarea v-model="draft.summary" class="field" maxlength="4000" rows="3" /></label>
        <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
        <div class="actions">
          <button class="btn primary" type="submit" :disabled="saving === 'product' || (product?.state === 'published' && !productDirty)">{{ product?.state === 'published' ? 'Save' : 'Publish' }}</button>
          <button v-if="product?.state === 'published'" class="btn ghost" type="button" :disabled="!!saving" @click="unpublish">Unpublish</button>
          <button class="btn ghost" type="button" @click="closeForm">Cancel</button>
        </div>
      </form>
      <template v-else-if="!product">
        <p class="empty-line">No product is published.</p>
        <button type="button" class="btn primary" @click="startProduct"><AppIcon name="plus" :size="13" />Publish product</button>
      </template>
      <template v-else>
        <p v-clip-tip="product.title" class="name">{{ product.title }}</p>
        <p v-if="product.body" class="meta">{{ product.body }}</p>
        <p v-if="product.state !== 'published'" class="meta">Not on the public page.</p>
        <div class="actions">
          <button type="button" :class="product.state === 'published' ? 'btn sm' : 'btn primary'" @click="startProduct">{{ product.state === 'published' ? 'Edit' : 'Publish' }}</button>
        </div>
      </template>
    </SettingsCard>

    <SettingsCard v-if="product && ready" title="Features" icon="layers" anchor="features">
      <template #lead>Status, summary, legal basis and, when declined, the reason visitors see.</template>
      <template v-if="features.length && composing !== 'feature' && !editing" #aside>
        <button type="button" class="btn sm" @click="startFeature()"><AppIcon name="plus" :size="13" />Add a feature</button>
      </template>
      <p v-if="!features.length && composing !== 'feature'" class="empty-line">No features yet.</p>
      <button v-if="!features.length && composing !== 'feature'" type="button" class="btn primary" @click="startFeature()"><AppIcon name="plus" :size="13" />Add a feature</button>
      <ul v-else class="rows">
        <li v-for="item in features" :key="item.id" class="row">
          <form v-if="editing === item.id" class="form" @submit.prevent="saveFeature">
            <label>Title<input v-model="draft.title" class="field" required maxlength="300" /></label>
            <label>Status
              <select v-model="draft.status" class="field">
                <option v-for="status in FEATURE_STATUSES" :key="status.id" :value="status.id">{{ status.label }}</option>
              </select>
            </label>
            <label>Summary<textarea v-model="draft.summary" class="field" maxlength="4000" rows="3" /></label>
            <label>Legal basis<input v-model="draft.legal" class="field" maxlength="240" /></label>
            <label v-if="draft.status === 'declined'">Reason<textarea v-model="draft.reason" class="field" maxlength="500" rows="2" /></label>
            <p v-if="draft.status === 'live' && validVersion(draft.liveSince)" class="live">Live since <ReleaseName :version="draft.liveSince" /></p>
            <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
            <div class="actions">
              <button class="btn primary" type="submit" :disabled="saving === 'feature'">Save</button>
              <button class="btn ghost" type="button" @click="closeForm">Cancel</button>
            </div>
          </form>
          <template v-else>
            <div class="row-copy">
              <p v-clip-tip="item.title" class="name">{{ item.title }}</p>
              <p class="meta"><template v-if="item.state === 'live' && textField(item, 'live_since')">Live since <ReleaseName :version="textField(item, 'live_since')" /></template><template v-else>{{ statusLabel(item.state) }}</template></p>
            </div>
            <button type="button" class="btn sm" @click="startFeature(item)">Edit</button>
          </template>
        </li>
      </ul>
      <form v-if="composing === 'feature'" class="form inset" @submit.prevent="saveFeature">
        <label>Title<input v-model="draft.title" class="field" required maxlength="300" /></label>
        <label>Status
          <select v-model="draft.status" class="field">
            <option v-for="status in FEATURE_STATUSES" :key="status.id" :value="status.id">{{ status.label }}</option>
          </select>
        </label>
        <label>Summary<textarea v-model="draft.summary" class="field" maxlength="4000" rows="3" /></label>
        <label>Legal basis<input v-model="draft.legal" class="field" maxlength="240" /></label>
        <label v-if="draft.status === 'declined'">Reason<textarea v-model="draft.reason" class="field" maxlength="500" rows="2" /></label>
        <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
        <div class="actions">
          <button class="btn primary" type="submit" :disabled="saving === 'feature'">Add feature</button>
          <button class="btn ghost" type="button" @click="closeForm">Cancel</button>
        </div>
      </form>
    </SettingsCard>

    <SettingsCard v-if="product && ready" title="Wishes" icon="star" anchor="wishes">
      <template #lead>Publish a wish for the public page, or leave it hidden.</template>
      <template v-if="(publishedWishes.length || hiddenWishes.length || pending.length) && composing !== 'wish'" #aside>
        <button type="button" class="btn sm" @click="startWish"><AppIcon name="plus" :size="13" />Add a wish</button>
      </template>
      <form v-if="composing === 'wish'" class="form" @submit.prevent="saveWish">
        <label>Title<input v-model="draft.title" class="field" required maxlength="300" /></label>
        <label>Summary<textarea v-model="draft.summary" class="field" maxlength="4000" rows="3" /></label>
        <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
        <div class="actions">
          <button class="btn primary" type="submit" :disabled="saving === 'wish'">Publish wish</button>
          <button class="btn ghost" type="button" @click="closeForm">Cancel</button>
        </div>
      </form>
      <template v-if="pending.length">
        <h3 class="queue">Waiting for review</h3>
        <ul class="rows">
          <li v-for="wish in pending" :key="wish.id" class="row">
            <div class="row-copy">
              <p v-clip-tip="wish.title" class="name">{{ wish.title }}</p>
              <p v-if="wish.body" class="meta">{{ wish.body }}</p>
            </div>
            <div class="actions">
              <button type="button" class="btn primary sm" :disabled="saving === wish.id" @click="setWish(wish, 'published')">Publish</button>
              <button type="button" class="btn ghost sm" :disabled="saving === wish.id" @click="setWish(wish, 'rejected')">Reject</button>
            </div>
          </li>
        </ul>
      </template>
      <p v-if="!publishedWishes.length && !hiddenWishes.length && !pending.length && composing !== 'wish'" class="empty-line">No wishes yet.</p>
      <button v-if="!publishedWishes.length && !hiddenWishes.length && !pending.length && composing !== 'wish'" type="button" class="btn primary" @click="startWish"><AppIcon name="plus" :size="13" />Add a wish</button>
      <ul v-if="publishedWishes.length" class="rows">
        <li v-for="wish in publishedWishes" :key="wish.id" class="row">
          <div class="row-copy">
            <p v-clip-tip="wish.title" class="name">{{ wish.title }}</p>
            <p v-if="wish.body" class="meta">{{ wish.body }}</p>
          </div>
          <button type="button" class="btn sm" :disabled="saving === wish.id" @click="setWish(wish, 'hidden')">Hide</button>
        </li>
      </ul>
      <ul v-if="hiddenWishes.length" class="rows">
        <li v-for="wish in hiddenWishes" :key="wish.id" class="row">
          <div class="row-copy">
            <p v-clip-tip="wish.title" class="name">{{ wish.title }}</p>
            <p class="meta">Hidden</p>
          </div>
          <button type="button" class="btn sm" :disabled="saving === wish.id" @click="setWish(wish, 'published')">Publish</button>
        </li>
      </ul>
    </SettingsCard>

    <PortalPace v-if="product && ready" :features="features" :wishes="wishes" />
    <PortalMarket v-if="product && ready" />
  </div>
</template>

<style scoped>
.section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.empty-line { margin: 0 0 12px; color: var(--ink-2); }
.url-row { display: flex; align-items: center; gap: 10px; min-width: 0; }
.url { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 500 13px/1.4 var(--mono); color: var(--ink); font-variant-ligatures: none; }
.form { display: grid; gap: 10px; }
.form.inset { margin-top: 14px; }
.form label { display: grid; gap: 4px; font-size: 12.5px; color: var(--ink-2); }
textarea.field { height: auto; min-height: 76px; padding-block: 8px; line-height: 1.45; resize: vertical; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.form-error { margin: 0; color: var(--danger); font-size: 13px; }
.rows { list-style: none; margin: 0; padding: 0; }
.row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 12px; align-items: center; min-width: 0; padding: 12px 0; border-top: 1px solid var(--line); }
.row > .form { grid-column: 1 / -1; }
.row:first-child { border-top: 0; }
.row-copy { min-width: 0; }
.name { margin: 0; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta { margin: 3px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.4; overflow-wrap: anywhere; }
.live { margin: 0; color: var(--ink-2); font-size: 13px; }
.queue { margin: 4px 0 0; font: 600 13px/1.4 var(--font); color: var(--ink-2); }
@media (max-width: 720px) {
  .name { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .row { align-items: start; }
}
@media (max-width: 600px) {
  .row, .url-row { grid-template-columns: minmax(0, 1fr); display: grid; }
  .url { white-space: normal; }
}
</style>
