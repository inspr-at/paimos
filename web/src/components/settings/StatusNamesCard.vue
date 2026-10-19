<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1401 (Tick spec v1): the workspace's own words for its statuses, one row
// per status with an English and a German name. Only the words change: stored
// states, transitions, checks, the API and --json keep the built-in names. A
// field saves on blur or Enter; Reset, a refusal and the saved line take
// reserved slots, so nothing moves while someone types.
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { displayLanguage } from '../../lib/displayLanguage'
import { BUILT_IN_STATUS_NAMES, STATUS_LOCALES, STATUS_NAME_KEYS, cleanStatusNames, readStatusNames, renamedStatus, statusName, statusNameField, statusNameProblems, type StatusLocale, type StatusNameDrafts, type StatusNameKey, type StatusNameMap } from '../../lib/statusNames'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useProfile } from '../../stores/profile'
import { useSession } from '../../stores/session'
import { useStatusNames } from '../../stores/statusNames'
import AppIcon from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import SettingsCard from './SettingsCard.vue'

const session = useSession(), profile = useProfile(), store = useStatusNames()
const scope = useIdentityScope(() => can('settings.manage') && session.identity?.principal.kind === 'person')
// The app reads in English for now (displayLanguage.ts); the copy is ready in both.
const language = computed(() => displayLanguage(profile.profile?.locale))
const t = (en: string, de: string) => language.value === 'de' ? de : en
const editable = computed(() => !!scope.owner.value)
const LOCALE_NAME: Record<StatusLocale, [string, string]> = { en: ['English', 'Englisch'], de: ['German', 'Deutsch'] }

const drafts = reactive<StatusNameDrafts>({})
const problems = ref(new Map<string, { code: 'too_long' | 'taken'; name: string }>())
const busy = ref(false), footer = ref(''), failed = ref(false)
let focused = '', again = false, savedTimer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(savedTimer))

// Drafts follow the saved names, except the field someone is typing in and a
// refused name still on screen. A save in flight adopts its own answer.
const held = (field: string) => field === focused || problems.value.has(field)
function adopt(names: StatusNameMap, keep: (field: string) => boolean = held) {
  for (const key of STATUS_NAME_KEYS) for (const locale of STATUS_LOCALES) {
    if (keep(statusNameField(key, locale))) continue
    drafts[key] = { ...drafts[key], [locale]: names[key]?.[locale] ?? '' }
  }
}
watch(() => store.value, value => { if (!busy.value) adopt(value.names) }, { immediate: true })
watch(scope.owner, () => { focused = ''; again = false; busy.value = false; footer.value = ''; failed.value = false; problems.value = new Map(); clearTimeout(savedTimer); adopt(store.value.names) }, { flush: 'sync' })

const differs = (key: StatusNameKey) => STATUS_LOCALES.some(locale => !!renamedStatus(key, locale, cleanStatusNames(drafts)) || !!renamedStatus(key, locale, store.value.names))
function problemText(key: StatusNameKey): string {
  for (const locale of STATUS_LOCALES) {
    const problem = problems.value.get(statusNameField(key, locale))
    if (problem?.code === 'too_long') return t('At most 24 characters.', 'Höchstens 24 Zeichen.')
    if (problem) return t(`“${problem.name}” is already the name of another status.`, `„${problem.name}“ ist schon der Name eines anderen Status.`)
  }
  return ''
}
const same = (a: StatusNameMap, b: StatusNameMap) => STATUS_NAME_KEYS.every(key => STATUS_LOCALES.every(locale => (a[key]?.[locale] ?? '') === (b[key]?.[locale] ?? '')))

function edit(key: StatusNameKey, locale: StatusLocale, event: Event) {
  drafts[key] = { ...drafts[key], [locale]: (event.target as HTMLInputElement).value }
  // A refusal clears as soon as the field is fixed; a new one waits for blur or Enter.
  const field = statusNameField(key, locale)
  if (problems.value.has(field) && !statusNameProblems(drafts, field).has(field)) { const next = new Map(problems.value); next.delete(field); problems.value = next }
}
function blurred(key: StatusNameKey, locale: StatusLocale) {
  const field = statusNameField(key, locale)
  if (focused === field) focused = ''
  commit(field)
}
function commit(field: string) {
  if (!editable.value) return
  const found = statusNameProblems(drafts, field)
  problems.value = found
  if (found.size) return
  const names = cleanStatusNames(drafts)
  if (same(names, store.value.names)) { adopt(store.value.names); return }
  void save(names)
}
function reset(key: StatusNameKey) {
  drafts[key] = { en: '', de: '' }
  const next = new Map(problems.value); for (const locale of STATUS_LOCALES) next.delete(statusNameField(key, locale)); problems.value = next
  commit('')
}
function markSaved(text: string) { footer.value = text; failed.value = false; clearTimeout(savedTimer); savedTimer = setTimeout(() => { if (!failed.value) footer.value = '' }, 4000) }
// One save at a time, under the revision on screen; a later edit saves after it.
async function save(names: StatusNameMap) {
  if (busy.value) { again = true; return }
  busy.value = true; failed.value = false; clearTimeout(savedTimer); footer.value = t('Saving…', 'Wird gespeichert …')
  const revision = store.value.revision
  try {
    await scope.run(({ after, signal }) => after(api('/settings/status-names', { method: 'PUT', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision, names }) }), async response => {
      const body = await response.json().catch(() => ({})) as { error?: unknown; code?: unknown; fields?: unknown }
      if (response.status === 409) {
        await store.load(true)
        again = false; adopt(store.value.names, field => field === focused)
        throw new Error(t('Someone else changed the names meanwhile. Their names are shown; yours were not saved.', 'Jemand anderes hat die Namen inzwischen geändert. Deren Namen sind zu sehen; Ihre wurden nicht gespeichert.'))
      }
      if (response.status === 400 && Array.isArray(body.fields) && typeof body.fields[0] === 'string' && (body.code === 'status_name_taken' || body.code === 'status_name_too_long')) {
        const field = body.fields[0].replace(/^names\./, ''), [key] = field.split('.') as [StatusNameKey]
        problems.value = new Map([[field, { code: body.code === 'status_name_taken' ? 'taken' : 'too_long', name: statusName(key, field.endsWith('.de') ? 'de' : 'en', names) }]])
        throw new Error(t('Not saved. Fix the marked name.', 'Nicht gespeichert. Bitte den markierten Namen ändern.'))
      }
      if (!response.ok) throw new Error(t('Status names were not saved. Try again.', 'Die Statusnamen wurden nicht gespeichert. Bitte erneut versuchen.'))
      store.accept(readStatusNames(body))
      // Fields changed after this save was sent keep their drafts for the next save.
      const now = cleanStatusNames(drafts)
      adopt(store.value.names, field => { const [key, locale] = field.split('.') as [StatusNameKey, StatusLocale]; return held(field) || (now[key]?.[locale] ?? '') !== (names[key]?.[locale] ?? '') })
      markSaved(t('Saved. Everyone in this workspace reads the new names.', 'Gespeichert. Alle in diesem Workspace sehen die neuen Namen.'))
    }), { failed: cause => { failed.value = true; footer.value = cause instanceof Error ? cause.message : t('Status names were not saved. Try again.', 'Die Statusnamen wurden nicht gespeichert. Bitte erneut versuchen.') } })
  } finally {
    busy.value = false
    if (again) { again = false; commit('') }
  }
}
function keys(key: StatusNameKey, locale: StatusLocale, event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.isComposing) { event.preventDefault(); commit(statusNameField(key, locale)) }
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); (event.target as HTMLInputElement).blur() }
}
const placeholder = (key: StatusNameKey, locale: StatusLocale) => BUILT_IN_STATUS_NAMES[key][locale]
</script>
<template>
  <SettingsCard :title="t('Status names', 'Statusnamen')" icon="tag" anchor="status-names">
    <template #lead>{{ t('Rename how statuses read for everyone in this workspace. Only the words change: workflows, checks, the API and --json keep the built-in names.', 'Benennen Sie, wie Status für alle in diesem Workspace heißen. Nur die Wörter ändern sich: Abläufe, Prüfungen, die API und --json behalten die eingebauten Namen.') }}</template>
    <div class="sn" :class="{ editable }" data-status-names>
      <div class="sn-head" aria-hidden="true"><span /><span>{{ t('Built-in', 'Eingebaut') }}</span><span v-for="locale in STATUS_LOCALES" :key="locale">{{ LOCALE_NAME[locale][language === 'de' ? 1 : 0] }}</span><span /></div>
      <div v-for="key in STATUS_NAME_KEYS" :key="key" class="sn-row" :data-status="key">
        <span class="sn-icon" aria-hidden="true"><AppIcon v-if="key === 'queued'" name="queue" :size="14" /><StatusIcon v-else :state="key" /></span>
        <span :id="`sn-${key}`" class="sn-canon">{{ BUILT_IN_STATUS_NAMES[key][language] }}</span>
        <template v-if="editable">
          <label v-for="locale in STATUS_LOCALES" :key="locale" class="sn-field" :data-locale="locale">
            <span class="sn-locale">{{ LOCALE_NAME[locale][language === 'de' ? 1 : 0] }}</span>
            <input class="field" type="text" autocomplete="off" spellcheck="false" :value="drafts[key]?.[locale] ?? ''" :placeholder="placeholder(key, locale)"
              :aria-label="`${BUILT_IN_STATUS_NAMES[key].en}, ${LOCALE_NAME[locale][0]} name`" :aria-invalid="problems.has(`${key}.${locale}`) || undefined" :aria-describedby="`sn-${key}-error`"
              @focus="focused = `${key}.${locale}`" @input="edit(key, locale, $event)" @blur="blurred(key, locale)" @keydown="keys(key, locale, $event)" />
          </label>
          <span class="sn-reset"><button type="button" class="icon-btn sm flat" :class="{ hidden: !differs(key) }" :tabindex="differs(key) ? 0 : -1" :aria-hidden="!differs(key) || undefined" :disabled="busy || !differs(key)"
            :data-tip="t(`Back to “${BUILT_IN_STATUS_NAMES[key].en}”`, `Zurück zu „${BUILT_IN_STATUS_NAMES[key].de}“`)" :aria-label="t(`Back to “${BUILT_IN_STATUS_NAMES[key].en}”`, `Zurück zu „${BUILT_IN_STATUS_NAMES[key].de}“`)" @click="reset(key)"><AppIcon name="rollback" :size="14" /></button></span>
          <p :id="`sn-${key}-error`" class="sn-error" :aria-live="problemText(key) ? 'polite' : undefined">{{ problemText(key) }}</p>
        </template>
        <template v-else>
          <span v-for="locale in STATUS_LOCALES" :key="locale" class="sn-value" :class="{ quiet: !renamedStatus(key, locale, store.value.names) }" :data-locale="locale"><span class="sn-locale">{{ LOCALE_NAME[locale][language === 'de' ? 1 : 0] }}</span>{{ statusName(key, locale, store.value.names) }}</span>
        </template>
      </div>
    </div>
    <p class="sn-foot" :class="{ error: failed || (!!store.error && !store.loaded) }" role="status">{{ footer || (!store.loaded && store.error ? store.error : '') || (!editable ? t('Only admins change these names. Everyone sees them.', 'Nur Admins ändern diese Namen. Alle sehen sie.') : '') }}</p>
  </SettingsCard>
</template>
<style scoped>
.sn { display: grid; grid-template-columns: 16px 140px minmax(0, 1fr) minmax(0, 1fr) 44px; column-gap: 12px; }
.sn-head, .sn-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; }
.sn-head { padding-bottom: 6px; font-size: 12px; color: var(--ink-3); }
.sn-row { row-gap: 0; padding-top: 6px; border-top: 1px solid var(--line); }
.sn-icon { display: grid; place-items: center; color: var(--ink-3); }
.sn-canon { font-size: 13px; color: var(--ink-2); overflow-wrap: anywhere; }
.sn-field { display: block; min-width: 0; }
.sn-field .field { height: 32px; font-size: 13px; }
.sn-field .field[aria-invalid="true"] { border-color: var(--danger-line); box-shadow: 0 0 0 1px var(--danger-line); }
.sn-locale { display: none; }
.sn-reset { display: grid; place-items: center; }
.sn-reset .hidden { visibility: hidden; }
/* The refusal has its own reserved line under the fields: it never pushes a row. */
.sn-error { grid-column: 3 / 5; min-height: 18px; margin: 0; padding: 1px 0 3px; font-size: 12px; line-height: 14px; color: var(--danger); overflow-wrap: anywhere; }
.sn-value { min-width: 0; min-height: 32px; display: flex; align-items: center; font-size: 13px; color: var(--ink); overflow-wrap: anywhere; }
.sn-value.quiet { color: var(--ink-3); }
.sn:not(.editable) .sn-row { padding-bottom: 6px; }
.sn-foot { min-height: 18px; margin: 10px 0 0; padding-top: 10px; border-top: 1px solid var(--line); font-size: 12.5px; line-height: 18px; color: var(--ink-2); overflow-wrap: anywhere; }
.sn-foot.error { color: var(--danger); }
@media (max-width: 600px) {
  /* Phones: the built-in name, then the English field, then the German one. */
  .sn { grid-template-columns: 16px minmax(0, 1fr) 44px; column-gap: 10px; }
  .sn-head { display: none; }
  .sn-row { padding-top: 10px; }
  .sn-icon { grid-column: 1; grid-row: 1; }
  .sn-canon { grid-column: 2; grid-row: 1; font-weight: 600; color: var(--ink); }
  .sn-reset { grid-column: 3; grid-row: 1; }
  .sn-field, .sn-value { grid-column: 1 / -1; display: grid; grid-template-columns: 64px minmax(0, 1fr); align-items: center; gap: 10px; margin-top: 6px; }
  .sn-locale { display: block; font-size: 12px; color: var(--ink-3); }
  .sn-field .field { height: 44px; font-size: 16px; }
  .sn-reset .icon-btn { width: 44px; height: 44px; }
  .sn-error { grid-column: 1 / -1; }
}
@media (pointer: coarse) { .sn-field .field { height: 44px; } .sn-reset .icon-btn { width: 44px; height: 44px; } }
</style>
