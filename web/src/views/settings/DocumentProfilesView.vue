<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/settings.css'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRouter } from 'vue-router'
import { contentUrl } from '../../lib/attachments'
import { setPageTitle } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'
import { getQuoteSettings, saveQuoteSettings, type QuoteSettings } from '../../lib/settings'
import { archiveProfile, defaultProfile, duplicateProfile, listProfiles, profileAssetUrl, profileMoney, saveProfile, undoProfile, uploadProfileAsset, type QuoteProfile } from '../../lib/quotes/profile'
import {
  COLUMN_LABELS, COLUMN_RANGE, COLUMN_SHORT, COVER, LABELS, LOCALES, LOCALE_TEXT, MARGINS, MARGIN_RANGE, MARK_OFFSET_RANGE, MARK_WIDTH_RANGE, SIGNATURE_RANGE, TYPE_SCALE,
  clone, decimal, localeDate, num, problems as findProblems, stable, switchLocale, tableRoom, tableWidth, type Locale,
} from '../../lib/quotes/profileForm'
import type { QuoteLayout, QuoteProfileDefinition, QuoteSender } from '../../lib/quotes/types'
import { can } from '../../lib/authz'
import AppIcon, { type IconName } from '../../components/AppIcon.vue'
import MmField from '../../components/quotes/inspector/MmField.vue'
import ProfileColors from '../../components/settings/profiles/ProfileColors.vue'
import ProfileFonts from '../../components/settings/profiles/ProfileFonts.vue'
import ProfilePreview from '../../components/settings/profiles/ProfilePreview.vue'
import ProfileRail from '../../components/settings/profiles/ProfileRail.vue'
import ProfileThumb from '../../components/settings/profiles/ProfileThumb.vue'

// Settings › Business › Document profiles: how the workspace's quotes look. The
// profiles on the left, the chosen one's form in the middle and the real quote
// renderer beside it on an invented sample, updating as you edit. Saving makes a
// new revision (quotes keep the revision they were issued with); a profile can be
// the default for new quotes, duplicated, and archived with Undo.
const props = defineProps<{ profileId?: string }>()
const router = useRouter()
const admin = computed(() => can('quotes.manage'))
const id = useId()

const profiles = ref<QuoteProfile[]>([])
const settings = ref<QuoteSettings | null>(null)
const loading = ref(true)
const loadError = ref('')
const busy = ref<'' | 'save' | 'default' | 'archive' | 'duplicate' | 'mark'>('')
const defaultId = computed(() => settings.value?.default_profile_id ?? '')
const creating = computed(() => props.profileId === 'new')
const selected = computed(() => creating.value ? null : profiles.value.find(p => p.id === props.profileId) ?? null)
const archived = computed(() => !!selected.value?.archived)
const readOnly = computed(() => archived.value || !admin.value)

// ---------- The working copy ----------
const working = reactive<{ name: string; definition: QuoteProfileDefinition }>({ name: '', definition: defaultProfile() })
const savedJson = ref('')
const snapshot = () => stable({ name: working.name.trim(), definition: working.definition })
const dirty = computed(() => !loading.value && (creating.value || !!selected.value) && snapshot() !== savedJson.value)
const problems = computed(() => findProblems(working.name, working.definition))
const problemOf = (field: string) => problems.value.find(p => p.field === field)?.message
function adopt(profile: QuoteProfile | null) {
  working.name = profile?.name ?? ''
  working.definition = clone(profile?.definition ?? defaultProfile())
  savedJson.value = stable({ name: profile?.name.trim() ?? '', definition: profile?.definition ?? defaultProfile() })
}
// The key the preview's fonts load under: one per profile (and one for a new one).
const previewKey = computed(() => creating.value ? 'new' : props.profileId ?? 'none')
const sender = computed(() => (settings.value?.sender ?? null) as QuoteSender | null)
const layout = computed(() => (settings.value?.layout ?? null) as QuoteLayout | null)

async function load() {
  loading.value = true; loadError.value = ''
  try {
    const [list, current] = await Promise.all([listProfiles(), getQuoteSettings().catch(() => null)])
    profiles.value = list; settings.value = current
  } catch (e) { loadError.value = e instanceof Error ? e.message : 'Profiles could not be loaded.' }
  finally { loading.value = false }
}
// With no profile named in the address, the default (or the first) opens; with none at all, a new one.
async function arrive() {
  if (!props.profileId) {
    const first = profiles.value.find(p => p.id === defaultId.value && !p.archived) ?? profiles.value.find(p => !p.archived)
    void router.replace(`/settings/business/profiles/${first ? first.id : 'new'}`)
    return
  }
  adopt(creating.value ? null : selected.value)
  if (creating.value) working.name = ''
  setPageTitle(creating.value ? 'New document profile' : selected.value?.name ?? 'Document profiles')
}
onMounted(async () => { await load(); await arrive() })
watch(() => props.profileId, () => { if (!loading.value) void arrive() })

// ---------- Leaving with unsaved edits ----------
const discard = () => confirmAction({ title: 'Discard your changes?', body: `Your edits to ${working.name.trim() || 'the new profile'} have not been saved.`, confirmLabel: 'Discard', danger: true })
onBeforeRouteUpdate(async (to, from) => (to.params.profileId === from.params.profileId || !dirty.value) ? true : discard())
onBeforeRouteLeave(async to => {
  if (to.path === '/signin' && useSession().requiresSignIn) return true
  return !dirty.value || discard()
})
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) event.preventDefault() }
function keys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') { event.preventDefault(); void save() }
}
onMounted(() => { window.addEventListener('beforeunload', beforeUnload); window.addEventListener('keydown', keys) })
onBeforeUnmount(() => { window.removeEventListener('beforeunload', beforeUnload); window.removeEventListener('keydown', keys) })

// ---------- Saving, the default, duplicating, archiving ----------
function replace(profile: QuoteProfile) {
  const index = profiles.value.findIndex(p => p.id === profile.id)
  profiles.value = index === -1 ? [...profiles.value, profile].sort((a, b) => a.name.localeCompare(b.name)) : profiles.value.map(p => p.id === profile.id ? profile : p)
}
const errorText = (e: unknown, fallback: string) => e instanceof Error && e.message ? e.message : fallback
async function save() {
  if (busy.value || readOnly.value || (!dirty.value && !creating.value)) return
  if (problems.value.length) { toast(problems.value[0]!.message, { tone: 'error' }); focusProblem(); return }
  busy.value = 'save'
  try {
    const was = selected.value
    const saved = await saveProfile(working.name.trim(), working.definition, was ?? undefined)
    replace(saved)
    savedJson.value = stable({ name: saved.name.trim(), definition: saved.definition })
    if (!was) {
      await router.replace(`/settings/business/profiles/${saved.id}`)
      toast(`Created ${saved.name}.`, { action: { label: 'Undo', run: () => void undoSave(saved, 'created') }, timeout: 8000 })
    } else toast(`Saved ${saved.name} as revision ${saved.revision}. Issued quotes keep the revision they were made with.`, { action: { label: 'Undo', run: () => void undoSave(saved, 'saved') }, timeout: 8000 })
  } catch (e) { toast(errorText(e, 'The profile was not saved.'), { tone: 'error' }) }
  finally { busy.value = '' }
}
async function undoSave(profile: QuoteProfile, kind: 'created' | 'saved') {
  try {
    const restored = await undoProfile(profile)
    replace(restored)
    if (props.profileId === restored.id && !dirty.value) adopt(restored)
    toast(kind === 'created' ? `${profile.name} is archived again.` : `${restored.name} is back as it was, as revision ${restored.revision}.`)
  } catch (e) { toast(errorText(e, 'Undo did not work.'), { tone: 'error' }) }
}
function focusProblem() {
  const field = problems.value[0]?.field
  const el = field ? document.querySelector<HTMLElement>(`[data-problem="${field}"] input, [data-problem="${field}"]`) : null
  el?.scrollIntoView({ block: 'center' }); el?.focus?.()
}
async function setDefault(on: boolean) {
  const current = settings.value, profile = selected.value
  if (!current || !profile || busy.value) return
  const before = current.default_profile_id ?? ''
  busy.value = 'default'
  try {
    settings.value = await saveQuoteSettings(current, { sender: current.sender, default_currency: current.default_currency, numbering_time_zone: current.numbering_time_zone, default_profile_id: on ? profile.id : '' })
    toast(on ? `New quotes start with ${profile.name}.` : 'New quotes start with the standard look.', { action: { label: 'Undo', run: () => void restoreDefault(before) }, timeout: 8000 })
  } catch (e) { toast(errorText(e, 'The default was not changed.'), { tone: 'error' }) }
  finally { busy.value = '' }
}
async function restoreDefault(value: string) {
  try {
    const current = settings.value ?? await getQuoteSettings()
    settings.value = await saveQuoteSettings(current, { sender: current.sender, default_currency: current.default_currency, numbering_time_zone: current.numbering_time_zone, default_profile_id: value })
  } catch (e) { toast(errorText(e, 'Undo did not work.'), { tone: 'error' }) }
}
async function duplicate() {
  const profile = selected.value
  if (!profile || busy.value || !admin.value) return
  busy.value = 'duplicate'
  try {
    const source: QuoteProfile = { ...profile, definition: clone(working.definition) }
    const name = `${(working.name.trim() || profile.name).slice(0, 93)} (copy)`
    const copy = await duplicateProfile(source, name)
    replace(copy)
    // The copy took the edits on screen; the original stays as saved.
    savedJson.value = snapshot()
    await router.push(`/settings/business/profiles/${copy.id}`)
    toast(`Made ${copy.name}.`, { action: { label: 'Undo', run: () => void undoCopy(copy, profile.id) }, timeout: 8000 })
  } catch (e) { toast(errorText(e, 'The profile was not duplicated.'), { tone: 'error' }) }
  finally { busy.value = '' }
}
async function undoCopy(copy: QuoteProfile, back: string) {
  try {
    replace(await undoProfile(copy))
    if (props.profileId === copy.id) await router.replace(`/settings/business/profiles/${back}`)
    toast(`${copy.name} is gone again.`)
  } catch (e) { toast(errorText(e, 'Undo did not work.'), { tone: 'error' }) }
}
async function archive() {
  const profile = selected.value
  if (!profile || busy.value || !admin.value) return
  if (dirty.value && !await discard()) return
  const wasDefault = defaultId.value === profile.id
  busy.value = 'archive'
  try {
    await archiveProfile(profile.id)
    const current = { ...profile, archived: true }
    replace(current)
    if (wasDefault && settings.value) settings.value = await getQuoteSettings().catch(() => ({ ...settings.value!, default_profile_id: '' }))
    adopt(current)
    toast(`Archived ${profile.name}.${wasDefault ? ' New quotes start with the standard look.' : ''} Quotes that use it keep it.`, {
      action: { label: 'Undo', run: () => void restore(current, wasDefault) }, timeout: 8000,
    })
  } catch (e) { toast(errorText(e, 'The profile was not archived.'), { tone: 'error' }) }
  finally { busy.value = '' }
}
async function restore(profile: QuoteProfile, makeDefault = false) {
  try {
    const restored = await undoProfile(profile)
    replace(restored)
    if (makeDefault) await restoreDefault(restored.id)
    if (props.profileId === restored.id) adopt(restored)
    toast(`${restored.name} is back.`)
  } catch (e) { toast(errorText(e, 'The profile was not restored.'), { tone: 'error' }) }
}
function create() { void router.push('/settings/business/profiles/new') }

// ---------- The form ----------
const classic = computed(() => working.definition.layout_variant === 'classic-v1')
const VARIANTS: { value: QuoteProfileDefinition['layout_variant']; label: string; text: string }[] = [
  { value: 'standard', label: 'Standard', text: 'The built-in layout with your colours, fonts, labels and mark.' },
  { value: 'classic-v1', label: 'Classic', text: 'Spaced capitals, a running header, and your margins, sizes and columns.' },
]
const variantPreview = (value: QuoteProfileDefinition['layout_variant']) => ({ ...working.definition, layout_variant: value })
const localeNote = ref('')
function setLocale(value: Locale) {
  const changed = switchLocale(working.definition, value)
  localeNote.value = changed ? `Labels that were still the ${value === 'en' ? 'German' : 'English'} defaults are now ${value === 'en' ? 'English' : 'German'}; your own stay.` : ''
}
const sample = computed(() => `${localeDate(working.definition.locale as Locale, '2026-09-21')} · ${profileMoney(556000, 'EUR', { id: 'sample', revision: 1, definition: working.definition })}`)
const setNumber = (target: Record<string, string>, key: string) => (value: number) => { target[key] = decimal(value) }
const room = computed(() => tableRoom(working.definition))
const used = computed(() => tableWidth(working.definition))
function fitColumns() {
  // The description column takes up the difference, within its range.
  const cols = working.definition.positions_table.columns
  const description = cols.find(c => c.key === 'description')
  if (!description) return
  const others = used.value - num(description.width_mm)
  description.width_mm = decimal(Math.max(COLUMN_RANGE.min, Math.min(COLUMN_RANGE.max, room.value - others)))
}
const NUMBERING: { value: string; label: string }[] = [
  { value: 'upper-roman', label: 'I, II, III' }, { value: 'decimal', label: '1, 2, 3' }, { value: 'upper-alpha', label: 'A, B, C' },
  { value: 'lower-alpha', label: 'a, b, c' }, { value: 'lower-roman', label: 'i, ii, iii' }, { value: 'none', label: 'No numbers' },
]
function resetLabels() {
  const key = working.definition.locale === 'en' ? 'en' : 'de'
  for (const entry of LABELS) working.definition.labels[entry.key] = entry[key]
  const text = LOCALE_TEXT[working.definition.locale as Locale]
  working.definition.totals.net_label = text.net_label; working.definition.payment_terms.heading = text.payment; working.definition.footer.page_number_format = text.page
}
const markInput = ref<HTMLInputElement>()
const markNote = ref('')
async function markChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  markNote.value = ''
  if (!/\.(png|svg)$/i.test(file.name) && !/^image\/(png|svg\+xml)$/.test(file.type)) { markNote.value = 'Use a PNG or an SVG.'; return }
  busy.value = 'mark'
  try { working.definition.footer.asset_id = (await uploadProfileAsset(file)).id }
  catch (e) { markNote.value = errorText(e, 'The mark was not uploaded.') }
  finally { busy.value = '' }
}
const companyMark = computed(() => layout.value?.logo_file_id || sender.value?.logo_file_id || '')
const pagePreview = computed(() => working.definition.footer.page_number_format.replaceAll('{page}', '2').replaceAll('{total}', '3'))

// ---------- Sections: a jump list that follows the scroll ----------
const SECTIONS: { id: string; label: string; icon: IconName }[] = [
  { id: 'basics', label: 'Basics', icon: 'sliders' }, { id: 'type', label: 'Type', icon: 'edit' }, { id: 'colours', label: 'Colours', icon: 'eye' },
  { id: 'page', label: 'Page', icon: 'expand' }, { id: 'table', label: 'Table', icon: 'list' }, { id: 'footer', label: 'Footer', icon: 'layers' }, { id: 'labels', label: 'Labels', icon: 'tag' },
]
const form = ref<HTMLElement>()
const active = ref('basics')
// A jump holds its choice while the scroll it started settles.
let heldUntil = 0
function jump(section: string) {
  heldUntil = Date.now() + 700
  active.value = section
  document.getElementById(`${id}-${section}`)?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
}
// On phones the jump row scrolls sideways: the current section stays in view.
watch(active, section => {
  const pill = document.querySelector<HTMLElement>(`.jumps .jump:nth-child(${SECTIONS.findIndex(s => s.id === section) + 1})`)
  const row = pill?.parentElement
  if (!pill || !row || row.scrollWidth <= row.clientWidth) return
  row.scrollTo({ left: row.scrollLeft + pill.getBoundingClientRect().left - row.getBoundingClientRect().left - (row.clientWidth - pill.offsetWidth) / 2, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
})
function spy() {
  const root = form.value
  if (!root || Date.now() < heldUntil) return
  const box = root.getBoundingClientRect()
  let current = SECTIONS[0]!.id
  // At the very end, the last section that shows is the one being read.
  const end = root.scrollTop + root.clientHeight >= root.scrollHeight - 4
  for (const s of SECTIONS) {
    const top = document.getElementById(`${id}-${s.id}`)?.getBoundingClientRect().top ?? Infinity
    if (top <= box.top + 120 || (end && top < box.bottom - 160)) current = s.id
  }
  active.value = current
}
// The preview shows the part of the page the section shapes.
const FOCUS: Record<string, 'cover' | 'table' | 'end'> = { basics: 'cover', type: 'cover', colours: 'cover', page: 'cover', table: 'table', footer: 'end', labels: 'cover' }
const focus = computed(() => FOCUS[active.value] ?? 'cover')
// Phones show the form or the preview; wider screens both.
const pane = ref<'form' | 'preview'>('form')
// On a phone the preview mounts only when shown, so it lays out at its real size.
const narrowQuery = window.matchMedia('(max-width: 859px)')
const narrow = ref(narrowQuery.matches)
const narrowChange = () => { narrow.value = narrowQuery.matches }
onMounted(() => narrowQuery.addEventListener('change', narrowChange))
onBeforeUnmount(() => narrowQuery.removeEventListener('change', narrowChange))
watch(() => props.profileId, () => { void nextTick(() => form.value?.scrollTo({ top: 0 })) })
</script>

<template>
  <section class="profiles-page" :class="`show-${pane}`" aria-labelledby="profiles-title">
    <header class="profiles-head">
      <div class="head-left">
        <RouterLink class="icon-btn sm flat back" to="/settings/business#quote-profiles" aria-label="Back to Business settings" data-tip="Business settings"><AppIcon name="arrow-left" :size="15" /></RouterLink>
        <div class="head-titles">
          <p class="eyebrow" role="status" aria-live="polite">Document profile<template v-if="!loading && (selected || creating)"> · <span v-if="archived">archived</span><span v-else-if="dirty" class="dirty">unsaved changes</span><span v-else-if="selected">revision {{ selected.revision }}</span><span v-else>not saved yet</span></template></p>
          <h1 id="profiles-title">{{ creating ? 'New profile' : working.name.trim() || selected?.name || 'Document profiles' }}</h1>
        </div>
      </div>
      <div class="head-right">
        <div class="seg pane-switch" role="group" aria-label="Show">
          <button type="button" :aria-pressed="pane === 'form'" @click="pane = 'form'">Edit</button>
          <button type="button" :aria-pressed="pane === 'preview'" @click="pane = 'preview'">Preview</button>
        </div>
        <div class="head-actions">
        <button v-if="selected && !archived && admin" type="button" class="btn sm ghost" :disabled="!!busy" :aria-label="dirty ? 'Save as a copy' : 'Duplicate'" :data-tip="dirty ? 'A new profile from what you see; this one stays as saved' : 'A new profile with the same design'" @click="duplicate">
          <AppIcon name="copy" :size="14" /><span class="btn-text">{{ dirty ? 'Save as a copy' : 'Duplicate' }}</span>
        </button>
        <button v-if="selected && !archived && admin" type="button" class="btn sm ghost" :disabled="!!busy" aria-label="Archive" data-tip="New quotes no longer offer it; quotes that use it keep it" @click="archive"><AppIcon name="archive" :size="14" /><span class="btn-text">Archive</span></button>
        <button v-if="archived && admin" type="button" class="btn sm" :disabled="!!busy" @click="selected && restore(selected)"><AppIcon name="rollback" :size="14" />Restore</button>
        <button v-if="!archived && admin && (selected || creating)" type="button" class="btn sm primary" :disabled="!!busy || (!dirty && !creating)" :aria-keyshortcuts="'Meta+S Control+S'" data-tip="Save · Cmd S" @click="save">
          <AppIcon name="check" :size="14" />{{ busy === 'save' ? 'Saving…' : creating ? 'Create profile' : 'Save' }}
        </button>
        </div>
      </div>
    </header>

    <div v-if="!admin" class="gate glass-card">
      <span class="gate-icon"><AppIcon name="shield" :size="18" /></span>
      <h2>Document profiles are for workspace admins</h2>
      <p>A workspace admin sets how quotes look. You choose a profile on each draft quote.</p>
      <RouterLink class="btn" to="/business/quotes">Quotes</RouterLink>
    </div>
    <div v-else-if="loadError" class="gate glass-card" role="alert">
      <span class="gate-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>Profiles could not be loaded</h2>
      <p>{{ loadError }}</p>
      <button type="button" class="btn" @click="load().then(arrive)"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else class="profiles-body">
      <ProfileRail :profiles="profiles" :selected-id="profileId ?? ''" :default-id="defaultId" :creating="creating" :loading="loading" @create="create" @restore="p => restore(p)" />

      <div ref="form" class="profile-form" @scroll.passive="spy">
        <div class="form-top">
          <label class="sr-only" :for="`${id}-switch`">Profile</label>
          <select :id="`${id}-switch`" class="field profile-switch" :value="creating ? 'new' : profileId" @change="router.push(`/settings/business/profiles/${($event.target as HTMLSelectElement).value}`)">
            <option v-for="p in profiles.filter(p => !p.archived || p.id === profileId)" :key="p.id" :value="p.id">{{ p.name }}{{ p.id === defaultId ? ' (default)' : '' }}{{ p.archived ? ' (archived)' : '' }}</option>
            <option value="new">New profile…</option>
          </select>
          <nav class="jumps" aria-label="Profile sections">
            <button v-for="s in SECTIONS" :key="s.id" type="button" class="jump" :aria-current="active === s.id ? 'true' : undefined" @click="jump(s.id)">{{ s.label }}</button>
          </nav>
        </div>
        <div v-if="loading" class="set-skeleton form-skeleton" aria-hidden="true"><span v-for="i in 6" :key="i" class="skeleton" /></div>
        <fieldset v-else class="sections" :disabled="readOnly">
          <p v-if="archived" class="set-note"><AppIcon name="archive" :size="14" /><span>This profile is archived: new quotes no longer offer it and it cannot change. Quotes made with it keep it. Restore it to edit.</span></p>

          <section :id="`${id}-basics`" class="form-section" aria-labelledby="sec-basics">
            <h2 id="sec-basics">Basics</h2>
            <div class="f-row" data-problem="name">
              <label class="f-label" :for="`${id}-name`">Name</label>
              <input :id="`${id}-name`" v-model="working.name" class="field" maxlength="100" placeholder="Company quote" :aria-invalid="!!problemOf('name') && (!!working.name || !creating) || undefined" />
            </div>
            <div class="f-row">
              <span :id="`${id}-variant`" class="f-label">Layout</span>
              <div class="variants" role="radiogroup" :aria-labelledby="`${id}-variant`">
                <button
                  v-for="v in VARIANTS" :key="v.value" type="button" role="radio" class="variant" :aria-checked="working.definition.layout_variant === v.value"
                  :tabindex="working.definition.layout_variant === v.value ? 0 : -1" @click="working.definition.layout_variant = v.value"
                  @keydown.left.prevent="working.definition.layout_variant = 'standard'" @keydown.right.prevent="working.definition.layout_variant = 'classic-v1'"
                >
                  <ProfileThumb :definition="variantPreview(v.value)" :size="56" />
                  <span class="variant-text"><span class="variant-name">{{ v.label }}<AppIcon v-if="working.definition.layout_variant === v.value" name="check" :size="13" /></span><span class="variant-note">{{ v.text }}</span></span>
                </button>
              </div>
            </div>
            <div class="f-row">
              <span :id="`${id}-locale`" class="f-label">Language and formats</span>
              <div class="seg locale" role="radiogroup" :aria-labelledby="`${id}-locale`">
                <button v-for="l in LOCALES" :key="l.value" type="button" role="radio" :aria-checked="working.definition.locale === l.value" @click="setLocale(l.value)">{{ l.label }}</button>
              </div>
              <p class="f-hint">Dates and amounts print as {{ sample }}.<template v-if="localeNote"> {{ localeNote }}</template></p>
            </div>
            <label v-if="selected && !archived" class="check">
              <input type="checkbox" :checked="defaultId === selected.id" :disabled="!!busy || dirty" @change="setDefault(($event.target as HTMLInputElement).checked)" />
              <span><strong>Default for new quotes</strong><span class="check-note">{{ dirty ? 'Save first; new quotes take the saved revision.' : 'New quotes from your sender start with this profile. Each draft can pick another.' }}</span></span>
            </label>
          </section>

          <section :id="`${id}-type`" class="form-section" aria-labelledby="sec-type">
            <h2 id="sec-type">Type</h2>
            <ProfileFonts :definition="working.definition" :disabled="readOnly" />
            <h3>Sizes</h3>
            <p v-if="!classic" class="f-hint">The standard layout keeps its own sizes; they apply with the classic layout.</p>
            <div class="mm-grid">
              <MmField v-for="t in TYPE_SCALE" :key="t.key" :label="t.label" unit="pt" :value="num(working.definition.typography[t.key], t.fallback)" :range="t.range" :disabled="readOnly || !classic" @commit="setNumber(working.definition.typography, t.key)($event)" />
            </div>
          </section>

          <section :id="`${id}-colours`" class="form-section" aria-labelledby="sec-colours">
            <h2 id="sec-colours">Colours</h2>
            <ProfileColors :definition="working.definition" :disabled="readOnly" />
          </section>

          <section :id="`${id}-page`" class="form-section" aria-labelledby="sec-page">
            <h2 id="sec-page">Page</h2>
            <p class="f-hint">{{ classic ? 'A4, in millimetres from the paper’s edge. Show the margins in the preview to see them.' : 'The standard layout keeps its own A4 margins; they apply with the classic layout.' }}</p>
            <div class="margins">
              <div class="sheet" aria-hidden="true"><span class="sheet-area" :style="{ top: `${num(working.definition.page.top_mm) / 297 * 100}%`, right: `${num(working.definition.page.right_mm) / 210 * 100}%`, bottom: `${num(working.definition.page.bottom_mm) / 297 * 100}%`, left: `${num(working.definition.page.left_mm) / 210 * 100}%` }" /></div>
              <div class="mm-grid">
                <MmField v-for="m in MARGINS" :key="m.key" :label="m.label" :value="num(working.definition.page[m.key])" :range="MARGIN_RANGE" :disabled="readOnly || !classic" @commit="setNumber(working.definition.page, m.key)($event)" />
              </div>
            </div>
            <h3>Cover</h3>
            <div class="mm-grid one">
              <MmField v-for="c in COVER" :key="c.key" :label="c.label" :value="num(working.definition.cover[c.key], c.fallback)" :range="c.range" :disabled="readOnly || !classic" @commit="setNumber(working.definition.cover, c.key)($event)" />
            </div>
            <h3>Sections</h3>
            <div class="f-pair">
              <div class="f-row">
                <label class="f-label" :for="`${id}-numbering`">Numbering</label>
                <select :id="`${id}-numbering`" v-model="working.definition.sections.numbering" class="field">
                  <option v-for="n in NUMBERING" :key="n.value" :value="n.value">{{ n.label }}</option>
                </select>
              </div>
              <div class="f-row">
                <span :id="`${id}-case`" class="f-label">Headings</span>
                <div class="seg" role="radiogroup" :aria-labelledby="`${id}-case`">
                  <button type="button" role="radio" :aria-checked="working.definition.sections.heading_case === 'upper'" :disabled="readOnly || !classic" @click="working.definition.sections.heading_case = 'upper'">CAPITALS</button>
                  <button type="button" role="radio" :aria-checked="working.definition.sections.heading_case !== 'upper'" :disabled="readOnly || !classic" @click="working.definition.sections.heading_case = 'as-is'">As written</button>
                </div>
              </div>
            </div>
          </section>

          <section :id="`${id}-table`" class="form-section" aria-labelledby="sec-table">
            <h2 id="sec-table">Table and totals</h2>
            <h3>Column widths</h3>
            <div class="columns-bar" :class="{ over: used > room + 1e-9 }" aria-hidden="true">
              <span v-for="c in working.definition.positions_table.columns" :key="c.key" class="col" :style="{ flexGrow: num(c.width_mm) }">{{ COLUMN_SHORT[c.key] }}</span>
            </div>
            <p class="f-hint" :class="{ bad: !!problemOf('columns') }" data-problem="columns">
              {{ problemOf('columns') ?? `${decimal(used)} of ${decimal(room)} mm between the margins.` }}
              <button v-if="Math.abs(room - used) > 0.05 && classic && !readOnly" type="button" class="link-btn" @click="fitColumns">Fit the description</button>
            </p>
            <div class="mm-grid">
              <MmField v-for="c in working.definition.positions_table.columns" :key="c.key" :label="COLUMN_LABELS[c.key] ?? c.key" :value="num(c.width_mm)" :range="COLUMN_RANGE" :disabled="readOnly || !classic" @commit="c.width_mm = decimal($event)" />
            </div>
            <div class="checks">
              <label class="check small"><input v-model="working.definition.positions_table.repeat_header" type="checkbox" /><span>Repeat the column heads on every page</span></label>
              <label class="check small"><input type="checkbox" :checked="working.definition.positions_table.separator === 'rule'" @change="working.definition.positions_table.separator = ($event.target as HTMLInputElement).checked ? 'rule' : 'none'" /><span>Lines between the rows</span></label>
            </div>
            <h3>Totals</h3>
            <div class="f-pair">
              <div class="f-row">
                <label class="f-label" :for="`${id}-vat`">VAT</label>
                <select :id="`${id}-vat`" v-model="working.definition.totals.vat" class="field">
                  <option value="note">A note under the total</option><option value="line">A line with its label</option><option value="hidden">Not shown</option>
                </select>
              </div>
              <div class="f-row">
                <label class="f-label" :for="`${id}-discount`">Discount</label>
                <select :id="`${id}-discount`" v-model="working.definition.totals.discount" class="field">
                  <option value="hidden">Not shown</option><option value="line">A line above the total</option>
                </select>
              </div>
            </div>
            <div class="f-row" data-problem="net_label">
              <label class="f-label" :for="`${id}-net`">Total label</label>
              <input :id="`${id}-net`" v-model="working.definition.totals.net_label" class="field" maxlength="100" :placeholder="LOCALE_TEXT[working.definition.locale as Locale].net_label" />
            </div>
            <h3>Payment terms</h3>
            <div class="f-pair">
              <div class="f-row">
                <label class="f-label" :for="`${id}-pay-pos`">Place</label>
                <select :id="`${id}-pay-pos`" v-model="working.definition.payment_terms.position" class="field">
                  <option value="sections">As a section of the text</option><option value="after-totals">Right after the total</option>
                </select>
              </div>
              <div class="f-row" data-problem="payment_heading">
                <label class="f-label" :for="`${id}-pay-head`">Heading</label>
                <input :id="`${id}-pay-head`" v-model="working.definition.payment_terms.heading" class="field" maxlength="100" />
              </div>
            </div>
          </section>

          <section :id="`${id}-footer`" class="form-section" aria-labelledby="sec-footer">
            <h2 id="sec-footer">Signatures and footer</h2>
            <div class="f-row">
              <span :id="`${id}-sig`" class="f-label">Signature lines</span>
              <div class="seg" role="radiogroup" :aria-labelledby="`${id}-sig`">
                <button type="button" role="radio" :aria-checked="working.definition.acceptance.signature_columns === 2" @click="working.definition.acceptance.signature_columns = 2">Client and you</button>
                <button type="button" role="radio" :aria-checked="working.definition.acceptance.signature_columns === 1" @click="working.definition.acceptance.signature_columns = 1">Client only</button>
              </div>
            </div>
            <div class="mm-grid">
              <MmField label="Space above" :value="num(working.definition.acceptance.lead_mm)" :range="SIGNATURE_RANGE" :disabled="readOnly || !classic" @commit="working.definition.acceptance.lead_mm = decimal($event)" />
              <MmField label="Gap between" :value="num(working.definition.acceptance.gap_mm)" :range="SIGNATURE_RANGE" :disabled="readOnly || !classic" @commit="working.definition.acceptance.gap_mm = decimal($event)" />
            </div>
            <h3>Footer mark</h3>
            <div class="mark">
              <div class="mark-box">
                <img v-if="working.definition.footer.asset_id" :src="profileAssetUrl(working.definition.footer.asset_id)" alt="The profile’s footer mark" />
                <img v-else-if="companyMark" :src="contentUrl(companyMark, 'original')" alt="" class="fallback" />
                <span v-else class="mark-empty">No mark</span>
              </div>
              <div class="mark-text">
                <p>{{ working.definition.footer.asset_id ? 'This profile’s own mark, on every page.' : 'Without its own mark, the quote uses the company mark from the sender settings.' }} {{ classic ? 'Classic centres it in the footer.' : 'Standard sets it at the start of the footer.' }}</p>
                <div class="mark-actions">
                  <button type="button" class="btn sm" :disabled="readOnly || busy === 'mark'" @click="markInput?.click()"><AppIcon name="upload" :size="13" />{{ busy === 'mark' ? 'Uploading…' : working.definition.footer.asset_id ? 'Replace' : 'Upload PNG or SVG' }}</button>
                  <button v-if="working.definition.footer.asset_id" type="button" class="btn sm ghost" :disabled="readOnly" @click="working.definition.footer.asset_id = undefined">Remove</button>
                </div>
                <input ref="markInput" type="file" class="sr-only" accept=".png,.svg,image/png,image/svg+xml" tabindex="-1" aria-label="Footer mark file" @change="markChosen" />
                <p v-if="markNote" class="f-hint bad" role="alert">{{ markNote }}</p>
              </div>
            </div>
            <div class="mm-grid">
              <MmField label="Mark width" :value="num(working.definition.footer.width_mm)" :range="MARK_WIDTH_RANGE" :disabled="readOnly" @commit="working.definition.footer.width_mm = decimal($event)" />
              <MmField label="Up or down" :value="num(working.definition.footer.offset_mm)" :range="MARK_OFFSET_RANGE" hint="Negative moves it up." :disabled="readOnly" @commit="working.definition.footer.offset_mm = decimal($event)" />
            </div>
            <div class="f-row" data-problem="page_number_format">
              <label class="f-label" :for="`${id}-pages`">Page number</label>
              <input :id="`${id}-pages`" v-model="working.definition.footer.page_number_format" class="field mono-field" maxlength="100" :aria-invalid="!!problemOf('page_number_format') || undefined" :aria-describedby="`${id}-pages-hint`" />
              <p :id="`${id}-pages-hint`" class="f-hint" :class="{ bad: !!problemOf('page_number_format') }">{{ problemOf('page_number_format') ?? `{page} and {total} become the numbers: “${pagePreview}”.` }}</p>
            </div>
          </section>

          <section :id="`${id}-labels`" class="form-section" aria-labelledby="sec-labels">
            <h2 id="sec-labels">Labels</h2>
            <p class="f-hint">The words the document prints around your text. Empty fields use the {{ working.definition.locale === 'en' ? 'English' : 'German' }} default. <button type="button" class="link-btn" :disabled="readOnly" @click="resetLabels">Use the defaults</button></p>
            <div class="labels">
              <div v-for="l in LABELS" :key="l.key" class="f-row" :data-problem="`label.${l.key}`">
                <label class="f-label" :for="`${id}-label-${l.key}`">{{ l.label }}</label>
                <input :id="`${id}-label-${l.key}`" v-model="working.definition.labels[l.key]" class="field" maxlength="100" :placeholder="working.definition.locale === 'en' ? l.en : l.de" :aria-invalid="!!problemOf(`label.${l.key}`) || undefined" />
                <p v-if="problemOf(`label.${l.key}`)" class="f-hint bad">{{ problemOf(`label.${l.key}`) }}</p>
              </div>
            </div>
          </section>
        </fieldset>
      </div>

      <ProfilePreview v-if="!narrow || pane === 'preview'" :definition="working.definition" :profile-key="previewKey" :sender="sender" :layout="layout" :focus="focus" />
    </div>
  </section>
</template>

<style scoped>
.profiles-page { display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0; }
.profiles-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px 16px; min-height: 60px; padding: 10px 16px; border-bottom: 1px solid var(--line-2); background: var(--surface-raised-2); }
.head-left { display: flex; flex: 1; align-items: flex-start; gap: 10px; min-width: 0; }
.head-left .back { flex: none; }
.head-titles { min-width: 0; }
.head-titles .eyebrow { font: 500 10.5px/1.3 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.head-titles h1 { white-space: normal; overflow-wrap: anywhere; font: 600 17px/1.3 var(--font); letter-spacing: 0; color: var(--ink); }
.head-right { flex: none; display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.head-titles .dirty { color: var(--gold-ink); font-weight: 700; }
.head-actions { display: flex; align-items: center; gap: 8px; }
.pane-switch { display: none; }
.profiles-body { flex: 1; min-height: 0; display: grid; grid-template-columns: 256px minmax(380px, 456px) minmax(0, 1fr); }
.profile-form { min-height: 0; overflow: auto; overscroll-behavior: contain; border-right: 1px solid var(--line-2); background: var(--bg, transparent); container-type: inline-size; }
.form-top { position: sticky; top: 0; z-index: 2; display: grid; gap: 8px; padding: 10px 16px; background: var(--surface-raised-2); border-bottom: 1px solid var(--line); -webkit-backdrop-filter: blur(12px); backdrop-filter: blur(12px); }
.profile-switch { display: none; height: 34px; }
.jumps { display: flex; flex-wrap: wrap; gap: 2px; margin: 0 -4px; }
.jump { height: 26px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
/* Phones: the sections are one row that scrolls sideways, 40 px pills a finger's
   reach apart; the current one scrolls into view (see the watch on active). */
@media (max-width: 600px) {
  .head-left .icon-btn { width: 44px; height: 44px; }
  .jumps { flex-wrap: nowrap; gap: 4px; margin: 0 -12px; padding: 2px 12px; overflow-x: auto; scrollbar-width: none; overscroll-behavior-x: contain; }
  .jumps::-webkit-scrollbar { display: none; }
  .jump { flex-shrink: 0; height: 40px; padding: 0 12px; }
}
@media (hover: hover) { .jump:hover { background: var(--row-hover); color: var(--ink); } }
.jump[aria-current="true"] { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.jump:focus-visible { box-shadow: var(--focus-ring); }
.form-skeleton { padding: 16px; }
.sections { display: grid; gap: 0; min-width: 0; margin: 0; padding: 0 16px 40px; border: 0; }
.sections .set-note { margin: 14px 0 0; }
.form-section { display: grid; gap: 12px; padding: 20px 0; border-bottom: 1px solid var(--line); scroll-margin-top: 96px; }
.form-section:last-child { border-bottom: 0; }
.form-section h2 { font: 600 15px/1.35 var(--font); color: var(--ink); }
.form-section h3 { margin-top: 4px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.f-row { display: grid; gap: 6px; min-width: 0; }
.f-label { font-size: 12.5px; font-weight: 600; color: var(--ink); }
.f-hint { font-size: 12px; line-height: 1.45; color: var(--ink-2); }
.f-hint.bad { color: var(--danger); }
.f-pair { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
.field[aria-invalid="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.mono-field { font-family: var(--mono); font-variant-ligatures: none; font-size: 13px; }
.mm-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 4px 18px; }
.mm-grid :deep(.mm-row) { grid-template-columns: minmax(0, 1fr) 96px; }
.mm-grid.one { grid-template-columns: minmax(0, 1fr); }
.mm-grid.one :deep(.mm-row) { grid-template-columns: minmax(0, 1fr) 112px; }
.variants { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 8px; }
.variant { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: start; gap: 10px; padding: 10px; border: 0; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); text-align: left; }
@media (hover: hover) { .variant:hover { background: var(--row-hover); } }
.variant[aria-checked="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1.5px var(--teal); }
.variant:focus-visible { box-shadow: var(--focus-ring); }
.variant-text { display: grid; gap: 3px; min-width: 0; }
.variant-name { display: inline-flex; align-items: center; gap: 6px; font-size: 13.5px; font-weight: 650; }
.variant-name svg { color: var(--teal-ink); }
.variant-note { font-size: 12px; line-height: 1.4; color: var(--ink-2); }
.locale { justify-self: start; }
.check { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border-radius: 12px; background: var(--surface-2); cursor: pointer; }
.check input { flex-shrink: 0; width: 16px; height: 16px; margin-top: 2px; accent-color: var(--teal); }
.check > span { display: grid; gap: 2px; font-size: 13px; color: var(--ink); }
.check-note { font-size: 12px; color: var(--ink-2); }
.check.small { padding: 0; background: none; align-items: center; }
.check.small input { margin-top: 0; }
.checks { display: grid; gap: 8px; }
.margins { display: grid; grid-template-columns: 74px minmax(0, 1fr); align-items: center; gap: 16px; }
.sheet { position: relative; width: 74px; aspect-ratio: 210 / 297; border-radius: 4px; background: var(--quote-paper, #fffefa); box-shadow: 0 1px 4px rgba(32, 60, 61, .18), inset 0 0 0 1px var(--line-2); }
.sheet-area { position: absolute; outline: 1px dashed var(--teal); outline-offset: -1px; border-radius: 1px; background: var(--chip-teal-bg); }
.margins .mm-grid { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
.columns-bar { display: flex; gap: 2px; height: 26px; padding: 2px; border-radius: 8px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); overflow: hidden; }
.columns-bar .col { display: grid; place-items: center; min-width: 0; flex-basis: 0; border-radius: 6px; background: var(--chip-teal-bg); color: var(--teal-ink); font: 600 10px/1 var(--mono); letter-spacing: .04em; overflow: hidden; white-space: nowrap; font-variant-ligatures: none; }
.columns-bar.over .col { background: var(--danger-bg); color: var(--danger); }
.link-btn { margin-left: 4px; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn:disabled { color: var(--ink-3); cursor: default; text-decoration: none; }
.link-btn:focus-visible { box-shadow: var(--focus-ring); border-radius: 3px; }
.mark { display: grid; grid-template-columns: 112px minmax(0, 1fr); gap: 14px; align-items: start; }
.mark-box { display: grid; place-items: center; height: 72px; padding: 8px; border-radius: 10px; background: var(--quote-paper, #fffefa); box-shadow: inset 0 0 0 1px var(--line-2); }
.mark-box img { max-width: 100%; max-height: 100%; object-fit: contain; }
.mark-box img.fallback { opacity: .55; }
.mark-empty { font-size: 12px; color: #5b6b6d; }
.mark-text p { font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.mark-actions { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.labels { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 10px 12px; }
.gate { display: grid; justify-items: center; gap: 8px; max-width: 560px; margin: 40px auto; padding: 36px 28px; text-align: center; }
.gate h2 { font-size: 17px; }
.gate p { font-size: 13.5px; color: var(--ink-2); }
.gate-icon { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.gate-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
/* A narrow form stacks its pairs, so no label is cut. */
@container (max-width: 450px) {
  .mm-grid, .f-pair, .labels { grid-template-columns: minmax(0, 1fr); }
  .mm-grid :deep(.mm-row) { grid-template-columns: minmax(0, 1fr) 112px; }
}
/* No rail below 1200px: a picker at the top of the form stands in for it. */
@media (max-width: 1199px) {
  .profiles-body { grid-template-columns: minmax(360px, 420px) minmax(0, 1fr); }
  .profiles-body > :deep(.rail) { display: none; }
  .profile-switch { display: block; }
}
/* Phones and narrow windows: the form or the preview, switched in the head. */
@media (max-width: 859px) {
  .profiles-body { grid-template-columns: minmax(0, 1fr); }
  .pane-switch { display: inline-flex; }
  .show-preview .profile-form { display: none; }
  .profile-form { border-right: 0; }
  .btn-text { display: none; }
}
@media (max-width: 520px) {
  .profiles-head { padding: 8px 12px; flex-direction: column; }
  .head-left { width: 100%; }
  .head-right { order: -1; width: 100%; justify-content: space-between; }
  .sections { padding: 0 12px 32px; }
  /* Touch-sized pane switch. */
  .pane-switch button { height: 36px; }
  .form-top { padding: 10px 12px; }
  .variants, .f-pair, .labels, .mm-grid, .margins .mm-grid { grid-template-columns: minmax(0, 1fr); }
  .margins { grid-template-columns: 56px minmax(0, 1fr); }
  .sheet { width: 56px; }
  .mark { grid-template-columns: 84px minmax(0, 1fr); }
}
</style>
