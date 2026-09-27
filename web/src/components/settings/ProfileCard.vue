<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { deriveInitials, uploadProblem } from '../../lib/avatar'
import { confirmAction } from '../../lib/confirm'
import { fieldMessage, ProfileError, shortNameProblem, type Profile, type ProfilePatch } from '../../lib/profile'
import { toast } from '../../lib/toast'
import { browserZone, datePreview, LOCALES, localeName, matchZone, timeZones, WEEKDAYS, zoneOffset, zoneParts, zoneTime } from '../../lib/zones'
import { useProfile } from '../../stores/profile'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import Avatar, { forgetMissing } from '../Avatar.vue'
import ChoicePicker, { type Choice } from './ChoicePicker.vue'

const AvatarCropDialog = defineAsyncComponent(() => import('./AvatarCropDialog.vue'))

// Your profile: photo, names, handle, initials, time zone and language. Each field
// saves on its own (Enter or leaving it), with Undo in the toast; the server's
// objections show under the field they are about.
const store = useProfile()
const session = useSession()
type TextField = 'first_name' | 'last_name' | 'preferred_name' | 'short_name' | 'initials'
const TEXT: { key: TextField; label: string; hint?: string; placeholder?: string; wide?: boolean }[] = [
  { key: 'first_name', label: 'First name' },
  { key: 'last_name', label: 'Last name' },
  { key: 'preferred_name', label: 'What should we call you?' },
  { key: 'short_name', label: 'Handle', hint: 'Lowercase letters, digits, dots, dashes and underscores.' },
  { key: 'initials', label: 'Initials', hint: 'Shown when there is no photo.' },
]
const SKELETON_ROWS = [...TEXT.map(field => field.key), 'email', 'zone', 'locale']
const LABEL: Record<string, string> = { first_name: 'First name', last_name: 'Last name', preferred_name: 'What we call you', short_name: 'Handle', initials: 'Initials', timezone: 'Time zone', locale: 'Language' }
const draft = reactive<Record<TextField, string>>({ first_name: '', last_name: '', preferred_name: '', short_name: '', initials: '' })
const errors = reactive<Record<string, string>>({})
const saving = ref<string | null>(null)
const saved = ref<string | null>(null)
let savedTimer: ReturnType<typeof setTimeout> | undefined
const p = computed(() => store.profile)
// The stored override: the profile's initials, unless they are the derived ones.
const derived = computed(() => deriveInitials({ ...draft }))
// Left empty, the greeting and teammates use the first name (else the sign-in name's first word).
const callName = computed(() => draft.first_name || (session.identity?.principal.name ?? '').split(/\s+/)[0] || '')
const storedOverride = (profile: Profile) => profile.initials === deriveInitials(profile) ? '' : profile.initials
function fill(profile: Profile | null) {
  if (!profile) return
  for (const { key } of TEXT) draft[key] = key === 'initials' ? storedOverride(profile) : profile[key]
}
watch(p, fill, { immediate: true })
const liveProblem = computed<Record<string, string>>(() => ({
  short_name: shortNameProblem(draft.short_name),
  initials: [...draft.initials].length > 3 ? 'Use up to 3 characters.' : '',
}))
const problem = (key: string) => liveProblem.value[key] || errors[key] || ''
function current(key: TextField, profile: Profile) { return key === 'initials' ? storedOverride(profile) : profile[key] }

// ---------- Saving one field, with Undo ----------
async function commit(fields: ProfilePatch, label: string, undo?: ProfilePatch) {
  const keys = Object.keys(fields)
  saving.value = keys[0]
  for (const key of keys) delete errors[key]
  try {
    await store.save(fields)
    saved.value = keys[0]
    clearTimeout(savedTimer); savedTimer = setTimeout(() => { saved.value = null }, 1800)
    if (undo) toast(`${label} saved.`, { action: { label: 'Undo', run: () => void commit(undo, label) } })
    return true
  } catch (e) {
    if (e instanceof ProfileError && Object.keys(e.errors).length) for (const [key, message] of Object.entries(e.errors)) errors[key] = fieldMessage(key, message)
    else toast(`${label} could not be saved. Please try again.`, { tone: 'error' })
    return false
  } finally { saving.value = null }
}
async function commitText(key: TextField) {
  const profile = p.value
  if (!profile) return
  let value = draft[key].trim()
  if (key === 'short_name') value = value.toLowerCase()
  draft[key] = value
  if (value === current(key, profile) || liveProblem.value[key]) return
  await commit({ [key]: value }, LABEL[key], { [key]: current(key, profile) })
}
function onHandleInput(event: Event) { draft.short_name = (event.target as HTMLInputElement).value.toLowerCase().replace(/\s/g, ''); delete errors.short_name }
function enter(event: KeyboardEvent) { (event.target as HTMLInputElement).blur() }

// ---------- Time zone and language ----------
const zoneAnchor = ref<HTMLElement | null>(null)
const localeAnchor = ref<HTMLElement | null>(null)
const now = ref(new Date())
let clock: ReturnType<typeof setInterval> | undefined
const zoneChoices = computed<Choice[]>(() => timeZones().map(zone => ({ value: zone, label: zoneParts(zone).city, detail: zoneParts(zone).region || undefined, hint: zoneOffset(zone, now.value) })))
const zoneMatch = (choice: Choice, needle: string) => matchZone(choice.value, needle) || (choice.hint ?? '').toLowerCase().includes(needle)
const localeChoices = computed<Choice[]>(() => LOCALES.map(tag => ({ value: tag, label: localeName(tag).english, detail: localeName(tag).native || undefined, hint: tag })))
const detected = browserZone()
async function chooseZone(zone: string) {
  zoneAnchor.value = null
  const profile = p.value
  if (profile && zone !== profile.timezone) await commit({ timezone: zone }, LABEL.timezone, { timezone: profile.timezone })
}
async function chooseLocale(tag: string) {
  localeAnchor.value = null
  const profile = p.value
  if (profile && tag !== profile.locale) await commit({ locale: tag }, LABEL.locale, { locale: profile.locale })
}
const preview = computed(() => p.value ? datePreview(p.value.locale, p.value.timezone, now.value) : '')
const localeLabel = computed(() => p.value ? localeName(p.value.locale).english : '')

// ---------- Photo: pick, drop or paste, then crop ----------
const picker = ref<HTMLInputElement>()
const cropping = ref<File | null>(null)
const photoError = ref('')
const dragging = ref(false)
const removing = ref(false)
function take(file: File | null | undefined) {
  photoError.value = ''
  if (!file) return
  const problemText = uploadProblem(file)
  if (problemText) { photoError.value = problemText; return }
  cropping.value = file
}
function picked(event: Event) { const input = event.target as HTMLInputElement; take(input.files?.[0]); input.value = '' }
function dropped(event: DragEvent) { dragging.value = false; take(event.dataTransfer?.files?.[0]) }
function dragOver(event: DragEvent) { if ([...(event.dataTransfer?.items ?? [])].some(item => item.kind === 'file')) { event.preventDefault(); dragging.value = true } }
// Paste a copied image anywhere on this page (outside a text field).
function pasted(event: ClipboardEvent) {
  const target = event.target as HTMLElement | null
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
  if (document.querySelector('dialog[open]')) return
  const file = [...(event.clipboardData?.files ?? [])].find(f => f.type.startsWith('image/'))
  if (file) { event.preventDefault(); take(file) }
}
function cropped(profile: Profile) {
  store.adopt(profile)
  forgetMissing(profile.principal_id)
  cropping.value = null
  toast('Your photo is updated.')
}
async function remove() {
  const ok = await confirmAction({ title: 'Remove your photo?', body: 'Your initials show instead, on your colour. You can add a photo again at any time.', confirmLabel: 'Remove photo', danger: true })
  if (!ok) return
  removing.value = true
  try { await store.dropAvatar(); toast('Your photo is removed.') }
  catch (e) { photoError.value = e instanceof Error ? e.message : 'Your photo could not be removed.' }
  finally { removing.value = false }
}
onMounted(() => {
  window.addEventListener('paste', pasted)
  clock = setInterval(() => { now.value = new Date() }, 30_000)
})
onBeforeUnmount(() => { window.removeEventListener('paste', pasted); clearInterval(clock); clearTimeout(savedTimer) })
const name = computed(() => [p.value?.first_name, p.value?.last_name].filter(Boolean).join(' ') || session.identity?.principal.name || '')
</script>

<template>
  <!-- The loading card has the form's own shape, so the page below it stays put. -->
  <div v-if="!p" class="profile" role="status" aria-label="Loading your profile">
    <div class="photo-col" aria-hidden="true"><span class="skeleton sk-photo" /><span class="skeleton sk-button" /><span class="skeleton sk-hint" /></div>
    <div class="fields" aria-hidden="true">
      <div v-for="row in SKELETON_ROWS" :key="row" class="field-row" :class="row"><span class="skeleton sk-label" /><span class="skeleton sk-field" /><span class="skeleton sk-note" /></div>
    </div>
  </div>
  <div v-else class="profile" :class="{ dragging }" @dragover="dragOver" @dragleave.self="dragging = false" @drop.prevent="dropped">
    <div class="photo-col">
      <button type="button" class="photo" :aria-label="store.hasPicture ? 'Change your photo' : 'Add a photo'" @click="picker?.click()">
        <Avatar :id="p.principal_id" :name="name" :size="104" />
        <span class="change" aria-hidden="true"><AppIcon name="image" :size="16" />{{ store.hasPicture ? 'Change' : 'Add' }}</span>
      </button>
      <input ref="picker" type="file" accept="image/png,image/jpeg,image/webp" class="file-input" tabindex="-1" aria-hidden="true" @change="picked" />
      <div class="photo-actions">
        <button type="button" class="btn sm" @click="picker?.click()"><AppIcon name="upload" :size="13" />{{ store.hasPicture ? 'New photo' : 'Upload' }}</button>
        <button v-if="store.hasPicture" type="button" class="btn sm ghost" :disabled="removing" @click="remove">{{ removing ? 'Removing…' : 'Remove' }}</button>
      </div>
      <p class="photo-hint">Drop or paste an image · PNG, JPEG or WebP, up to 8 MB</p>
      <p v-if="photoError" class="photo-error" role="alert"><AppIcon name="alert" :size="13" />{{ photoError }}</p>
    </div>

    <div class="fields">
      <div v-for="field in TEXT" :key="field.key" class="field-row" :class="field.key">
        <label :for="`profile-${field.key}`" class="label">{{ field.label }}
          <span v-if="saving === field.key" class="state">Saving…</span>
          <span v-else-if="saved === field.key" class="state ok"><AppIcon name="check" :size="11" />Saved</span>
        </label>
        <div class="input-wrap" :class="{ handle: field.key === 'short_name' }">
          <span v-if="field.key === 'short_name'" class="at" aria-hidden="true">@</span>
          <input
            v-if="field.key === 'short_name'" :id="`profile-${field.key}`" :value="draft.short_name" class="field" autocomplete="off" spellcheck="false" autocapitalize="off" maxlength="24"
            :aria-invalid="!!problem(field.key)" :aria-describedby="`profile-${field.key}-note`" @input="onHandleInput" @change="commitText(field.key)" @keydown.enter.prevent="enter"
          />
          <input
            v-else v-model="draft[field.key]" :id="`profile-${field.key}`" class="field" :autocomplete="field.key === 'first_name' ? 'given-name' : field.key === 'last_name' ? 'family-name' : field.key === 'preferred_name' ? 'nickname' : 'off'"
            :placeholder="field.key === 'initials' ? derived : field.key === 'preferred_name' ? callName : field.placeholder" :maxlength="field.key === 'initials' ? 3 : 100"
            :aria-invalid="!!problem(field.key)" :aria-describedby="`profile-${field.key}-note`" @input="delete errors[field.key]" @change="commitText(field.key)" @keydown.enter.prevent="enter"
          />
        </div>
        <p :id="`profile-${field.key}-note`" class="note" :class="{ bad: !!problem(field.key) }" :role="problem(field.key) ? 'alert' : undefined">
          <template v-if="problem(field.key)"><AppIcon name="alert" :size="12" />{{ problem(field.key) }}</template>
          <template v-else-if="field.key === 'initials' && !draft.initials">Derived from your name: {{ derived }}</template>
          <template v-else-if="field.key === 'preferred_name'">{{ draft.preferred_name ? 'The greeting and your teammates use this.' : callName ? `Empty, so the greeting says ${callName}.` : 'The greeting and your teammates use this.' }}</template>
          <template v-else>{{ field.hint }}</template>
        </p>
      </div>

      <div class="field-row email">
        <span class="label" id="profile-email-label">Email</span>
        <p class="static" aria-labelledby="profile-email-label">{{ p.email ?? 'Not shared by your sign-in' }}</p>
        <p class="note"><AppIcon name="shield" :size="12" />Managed by sign-in</p>
      </div>

      <div class="field-row zone">
        <span class="label" id="profile-zone-label">Time zone
          <span v-if="saving === 'timezone'" class="state">Saving…</span>
          <span v-else-if="saved === 'timezone'" class="state ok"><AppIcon name="check" :size="11" />Saved</span>
        </span>
        <div class="pick-row">
          <button type="button" class="pick" aria-haspopup="listbox" :aria-expanded="!!zoneAnchor" aria-labelledby="profile-zone-label profile-zone-value" @click="zoneAnchor = zoneAnchor ? null : ($event.currentTarget as HTMLElement)">
            <BizIcon name="globe" :size="14" /><span id="profile-zone-value" class="pick-value">{{ zoneParts(p.timezone).city }}<span v-if="zoneParts(p.timezone).region" class="pick-region"> · {{ zoneParts(p.timezone).region }}</span></span><span class="pick-hint mono">{{ zoneOffset(p.timezone, now) }} · {{ zoneTime(p.timezone, now) }}</span><AppIcon name="chevron" :size="12" class="chev" />
          </button>
          <button v-if="detected && detected !== p.timezone" type="button" class="btn sm ghost" :data-tip="`This device is set to ${detected}`" @click="chooseZone(detected)">Detect</button>
        </div>
        <p class="note" :class="{ bad: !!errors.timezone }" :role="errors.timezone ? 'alert' : undefined">{{ errors.timezone || 'Times and the greeting follow it.' }}</p>
        <ChoicePicker v-if="zoneAnchor" :anchor="zoneAnchor" label="Time zones" :choices="zoneChoices" :current="p.timezone" :match="zoneMatch" placeholder="City, region or GMT+2" @choose="chooseZone" @close="zoneAnchor = null" />
      </div>

      <div class="field-row locale">
        <span class="label" id="profile-locale-label">Language and region
          <span v-if="saving === 'locale'" class="state">Saving…</span>
          <span v-else-if="saved === 'locale'" class="state ok"><AppIcon name="check" :size="11" />Saved</span>
        </span>
        <button type="button" class="pick" aria-haspopup="listbox" :aria-expanded="!!localeAnchor" aria-labelledby="profile-locale-label profile-locale-value" @click="localeAnchor = localeAnchor ? null : ($event.currentTarget as HTMLElement)">
          <BizIcon name="calendar" :size="14" /><span id="profile-locale-value" class="pick-value">{{ localeLabel }}</span><span class="pick-hint mono">{{ p.locale }}</span><AppIcon name="chevron" :size="12" class="chev" />
        </button>
        <p class="note preview" :class="{ bad: !!errors.locale }">
          <template v-if="errors.locale">{{ errors.locale }}</template>
          <template v-else>Dates read <b>{{ preview }}</b> · weeks start on {{ WEEKDAYS[p.week_start] }}</template>
        </p>
        <ChoicePicker v-if="localeAnchor" :anchor="localeAnchor" label="Languages" :choices="localeChoices" :current="p.locale" :limit="40" placeholder="Language or country" @choose="chooseLocale" @close="localeAnchor = null" />
      </div>
    </div>
    <AvatarCropDialog v-if="cropping" :file="cropping" @saved="cropped" @close="cropping = null" />
  </div>
</template>

<style scoped>
.profile { position: relative; display: grid; grid-template-columns: 180px minmax(0, 1fr); gap: 28px; border-radius: 12px; }
/* A file dragged over the card: the whole card is the drop target, outlined all round. */
.profile.dragging { outline: 2px dashed var(--teal); outline-offset: 8px; }
.photo-col { display: grid; justify-items: center; align-content: start; gap: 10px; text-align: center; }
.photo { position: relative; display: grid; place-items: center; width: 112px; height: 112px; padding: 4px; border: 0; border-radius: 50%; background: transparent; cursor: pointer; }
.photo:focus-visible { box-shadow: var(--focus-ring); }
.change {
  position: absolute; inset: 4px; display: grid; place-content: center; justify-items: center; gap: 4px; border-radius: 50%;
  background: rgba(8, 20, 22, .55); color: #fff; font-size: 12.5px; font-weight: 600; opacity: 0; transition: opacity .15s ease;
}
.photo:hover .change, .photo:focus-visible .change { opacity: 1; }
@media (hover: none) { .change { opacity: 1; inset: auto 4px 4px 4px; height: 30px; border-radius: 0 0 52px 52px; grid-auto-flow: column; gap: 5px; font-size: 11.5px; } }
.file-input { position: absolute; width: 1px; height: 1px; opacity: 0; pointer-events: none; }
.photo-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 6px; }
.photo-hint { font-size: 11.5px; color: var(--ink-3); line-height: 1.5; }
.photo-error { display: flex; align-items: flex-start; gap: 6px; padding: 8px 10px; border-radius: 10px; background: var(--danger-bg); color: var(--ink); font-size: 12.5px; text-align: left; }
.photo-error svg { margin-top: 2px; color: var(--danger); flex-shrink: 0; }
.fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 18px; align-content: start; }
.field-row { display: grid; gap: 5px; min-width: 0; align-content: start; }
.field-row.zone, .field-row.locale { grid-column: 1 / -1; }
.label { display: flex; align-items: center; gap: 8px; font-size: 12.5px; font-weight: 600; color: var(--ink); }
.state { display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; font-weight: 500; color: var(--ink-3); }
.state.ok { color: color-mix(in oklab, var(--ok), var(--ink) 30%); }
.input-wrap { position: relative; }
.input-wrap .field { height: 38px; }
.handle .field { padding-left: 26px; font-family: var(--mono); font-size: 13px; }
.at { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); color: var(--ink-3); font: 500 13px/1 var(--mono); pointer-events: none; }
.field[aria-invalid="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.note { display: flex; align-items: flex-start; gap: 5px; min-height: 18px; font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.note svg { margin-top: 2px; flex-shrink: 0; color: var(--ink-3); }
.note.bad { color: var(--danger); }
.note.bad svg { color: var(--danger); }
.note.preview { display: block; }
.note.preview b { font-weight: 600; color: var(--ink); }
.static { display: flex; align-items: center; min-height: 38px; padding: 0 12px; border-radius: var(--radius-s); background: var(--surface-2); color: var(--ink); font-size: 13.5px; overflow-wrap: anywhere; }
.pick-row { display: flex; gap: 6px; align-items: center; }
.pick {
  display: flex; align-items: center; gap: 8px; width: 100%; min-width: 0; height: 38px; padding: 0 10px 0 12px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s);
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font-size: 13.5px; text-align: left;
}
.pick svg { color: var(--ink-3); flex-shrink: 0; }
@media (hover: hover) { .pick:hover { box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); } }
.pick:focus-visible { box-shadow: var(--focus-ring); }
.pick-value { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.pick-hint { flex-shrink: 0; font-size: 11.5px; color: var(--ink-2); }
.pick-region { color: var(--ink-2); }
.chev { margin-left: 2px; }
.sk-photo { width: 104px; height: 104px; margin: 4px; border-radius: 50%; }
.sk-button { width: 92px; height: 30px; border-radius: 9px; }
.sk-hint { width: 150px; height: 28px; border-radius: 8px; }
.sk-label { width: 38%; height: 12px; margin: 3px 0; }
.sk-field { height: 38px; border-radius: var(--radius-s); }
.sk-note { width: 62%; height: 12px; margin: 3px 0; }
@media (max-width: 900px) { .profile { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 600px) {
  .fields { grid-template-columns: minmax(0, 1fr); }
  .note:empty { display: none; }
  .input-wrap .field, .pick, .static, .sk-field { height: 44px; min-height: 44px; }
  .sk-button { height: 40px; }
  .pick { height: auto; padding-block: 8px; flex-wrap: wrap; }
  .photo-actions .btn { height: 40px; }
}
@media (prefers-reduced-motion: reduce) { .change { transition: none; } }
</style>
