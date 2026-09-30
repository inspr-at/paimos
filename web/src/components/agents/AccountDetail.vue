<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { accountName } from '../../lib/accountCascade'
import {
  amountText, dateRange, draftFor, draftWrite, limitBinds, limitSummary, limitUseLine, listReadings, noWindowLine, PERIODS, readingWho,
  recentReadings, removeLimit, removeWindow, renameAccount, renameProblem, repeatWindow, putLimit, sameLimit, setByYou, setByYouText,
  sortWindows, sourceText, spendLine, UNIT_WORD, unitChoices, windowFacts, windowLabel, type LimitDraft,
} from '../../lib/accountLimits'
import type { AgentAccount, AllowanceWindow } from '../../lib/agents'
import { pct, when, type AccountCapacity, type AccountRow, type CapacityReading } from '../../lib/capacity'
import { confirmAction } from '../../lib/confirm'
import AppIcon from '../AppIcon.vue'

// The inline detail of one account in Settings → Accounts (AEON-384): its
// name, each window with source and freshness, the last three readings,
// money for an API key, and the Advanced sentence. Limits set by hand before
// the sentence stay as "Set by you" with Remove and Make this repeat.
const props = defineProps<{
  account: AgentAccount; row?: AccountRow; cap?: AccountCapacity; now: number; timezone: string; mayManage: boolean; rename?: number; renameTrigger?: HTMLElement | null
}>()
const emit = defineEmits<{ changed: [] }>()

const name = computed(() => accountName(props.account))
const host = computed(() => props.row?.host || props.account.host_label || '')
const windows = computed(() => sortWindows(props.cap?.windows ?? []))
const primary = computed(() => windows.value[0] ?? null)
const offline = computed(() => props.row?.state === 'offline')
const use = computed(() => props.cap?.limit)
const mine = computed(() => setByYou(props.account.windows, props.now))

// ---------- The last readings ----------
const readings = ref<CapacityReading[] | null>(null)
const recent = computed(() => recentReadings(readings.value ?? [], primary.value))
onMounted(async () => {
  try { readings.value = await listReadings(props.account.id) } catch { readings.value = [] }
})

// ---------- Name ----------
const renaming = ref(false)
const nameDraft = ref('')
const nameError = ref('')
const nameBusy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)
const nameButton = ref<HTMLButtonElement | null>(null)
let nameReturn: HTMLElement | null = null
watch(() => props.rename, activation => { if (activation) void startRename(props.renameTrigger) }, { immediate: true })
async function startRename(trigger?: HTMLElement | null) {
  nameReturn = trigger ?? nameButton.value
  nameDraft.value = name.value === 'Unlabeled account' ? '' : name.value
  nameError.value = ''
  renaming.value = true
  await nextTick()
  nameInput.value?.focus()
  nameInput.value?.select()
}
async function cancelRename() {
  renaming.value = false; nameError.value = ''
  await nextTick()
  const target = nameReturn?.isConnected ? nameReturn : nameButton.value
  target?.focus({ preventScroll: true })
}
async function saveName() {
  const problem = renameProblem(nameDraft.value)
  if (problem) { nameError.value = problem; return }
  if (nameDraft.value.trim() === props.account.label.trim()) { renaming.value = false; return }
  nameBusy.value = true
  try {
    await renameAccount(props.account.id, nameDraft.value.trim())
    renaming.value = false
    emit('changed')
  } catch (e) {
    nameError.value = e instanceof Error ? e.message : 'The name did not change. Please try again.'
  } finally { nameBusy.value = false }
}

// ---------- The Advanced sentence ----------
const choices = computed(() => unitChoices({ harness: props.account.harness, measured: windows.value.length > 0, money: props.cap?.cost_limit_supported === true, current: use.value?.unit }))
const draft = ref<LimitDraft>(draftFor(use.value, choices.value))
const sentenceOpen = ref(false)
const showSentence = computed(() => !!use.value || sentenceOpen.value)
const limitBusy = ref(false)
const limitError = ref('')
const limitStatus = ref('')
const amountInput = ref<HTMLInputElement | null>(null)
// A saved change from elsewhere (or this Apply) resets the draft to the server's sentence.
watch(() => use.value && `${use.value.id}`, () => { draft.value = draftFor(use.value, choices.value) })
const parsed = computed(() => draftWrite(draft.value))
const dirty = computed(() => !parsed.value.ok || !sameLimit(parsed.value.body, use.value))
async function openSentence() {
  sentenceOpen.value = true
  limitStatus.value = ''
  await nextTick()
  amountInput.value?.focus()
}
function closeSentence() { sentenceOpen.value = false; limitError.value = ''; draft.value = draftFor(use.value, choices.value) }
async function apply() {
  const result = parsed.value
  if (!result.ok) { limitError.value = result.message; return }
  limitBusy.value = true; limitError.value = ''; limitStatus.value = ''
  try {
    await putLimit(props.account.id, result.body)
    limitStatus.value = `Saved: at most ${limitSummary(result.body)}.`
    emit('changed')
  } catch (e) {
    limitError.value = e instanceof Error ? e.message : 'The limit did not change. Please try again.'
  } finally { limitBusy.value = false }
}
async function clearLimit() {
  limitBusy.value = true; limitError.value = ''; limitStatus.value = ''
  try {
    await removeLimit(props.account.id)
    sentenceOpen.value = false
    limitStatus.value = 'Limit removed. Agents follow your plan.'
    emit('changed')
  } catch (e) {
    limitError.value = e instanceof Error ? e.message : 'The limit did not change. Please try again.'
  } finally { limitBusy.value = false }
}

// ---------- Set by you (before the sentence) ----------
const windowBusy = ref('')
const windowError = ref('')
const windowTip = (w: AllowanceWindow) => `${amountText(w.used, w.unit)} used of ${amountText(w.allowance, w.unit)}, ${dateRange(w.starts_at, w.ends_at, props.timezone)}`
async function repeat(w: AllowanceWindow) {
  windowBusy.value = w.id; windowError.value = ''; limitStatus.value = ''
  try {
    const rule = await repeatWindow(props.account.id, w.id)
    limitStatus.value = `Now repeats: at most ${limitSummary(rule)}.`
    emit('changed')
  } catch (e) {
    windowError.value = e instanceof Error ? e.message : 'The limit did not change. Please try again.'
  } finally { windowBusy.value = '' }
}
async function drop(w: AllowanceWindow) {
  const ok = await confirmAction({ title: `Remove ${amountText(w.allowance, w.unit)} on ${name.value}?`, body: `Agents follow your plan and the vendor's readings on ${name.value}. Its history stays.`, confirmLabel: 'Remove limit' })
  if (!ok) return
  windowBusy.value = w.id; windowError.value = ''
  try {
    await removeWindow(props.account.id, w.id)
    emit('changed')
  } catch (e) {
    windowError.value = e instanceof Error ? e.message : 'The limit did not change. Please try again.'
  } finally { windowBusy.value = '' }
}
</script>

<template>
  <div class="detail">
    <dl class="account-facts">
      <div class="fact">
        <dt>Name</dt>
        <dd v-if="!renaming" class="name-line">
          <span class="nm" :title="name">{{ name }}</span>
          <button v-if="mayManage" ref="nameButton" type="button" class="icon-btn flat sm" :aria-label="`Rename ${name}`" data-tip="Rename" @click="startRename()"><AppIcon name="edit" :size="14" /></button>
        </dd>
        <dd v-else>
          <form class="rename" @submit.prevent="saveName" @keydown.esc.prevent="cancelRename">
            <input ref="nameInput" v-model="nameDraft" class="field" maxlength="128" :aria-label="`New name for ${name}`" :aria-invalid="!!nameError" :disabled="nameBusy">
            <button type="submit" class="btn sm" :disabled="nameBusy">Save</button>
            <button type="button" class="btn sm ghost" :disabled="nameBusy" @click="cancelRename">Cancel</button>
          </form>
          <p v-if="nameError" class="problem" role="alert">{{ nameError }}</p>
        </dd>
      </div>

      <template v-if="windows.length">
        <div v-for="w in windows" :key="`${w.reading.window_kind}/${w.reading.bucket}`" class="fact window" :class="{ frozen: windowFacts(w, now, timezone).stale }">
          <dt>{{ windowLabel(w, account.harness) }}</dt>
          <dd>
            <b class="num">{{ windowFacts(w, now, timezone).left }}</b><span class="sep"> · </span>{{ windowFacts(w, now, timezone).reset }}
            <span class="src"><span class="sep"> · </span>{{ sourceText(w.reading, account.harness, host, now) }}<template v-if="offline"> · offline</template><template v-else-if="windowFacts(w, now, timezone).stale"> · stale</template></span>
          </dd>
        </div>
      </template>
      <div v-else class="fact">
        <dt>Limits</dt>
        <dd class="quiet">{{ noWindowLine(account.harness, host) }}</dd>
      </div>

      <div v-if="recent.length" class="fact">
        <dt>Readings</dt>
        <dd>
          <ol class="readings">
            <li v-for="(r, i) in recent" :key="r.read_at"><time :datetime="r.read_at">{{ when(r.read_at, now, timezone) }}</time> <span class="num">{{ pct(r.used_percent) }}{{ i === 0 ? ' used' : '' }}</span> <span class="by">{{ readingWho(r, account.harness, host) }}</span></li>
          </ol>
        </dd>
      </div>

      <div v-if="cap?.spend_month_usd" class="fact">
        <dt>Spend</dt>
        <dd>{{ spendLine(cap.spend_month_usd, use, now, timezone) }} <span class="quiet">· list prices</span></dd>
      </div>

      <div class="fact limit">
        <dt>Limit</dt>
        <dd>
          <template v-if="!mayManage">
            <span v-if="use">At most {{ limitSummary(use) }}</span>
            <span v-else class="quiet">None · agents follow your plan</span>
          </template>
          <button v-else-if="!showSentence" type="button" class="btn sm ghost set-limit" @click="openSentence"><AppIcon name="sliders" :size="14" />Set a limit by hand</button>
          <form v-else class="sentence" @submit.prevent="apply">
            <span>Let agents use at most</span>
            <span class="keep">
              <input ref="amountInput" v-model="draft.amount" class="field amount" :inputmode="draft.unit === 'dollars' ? 'decimal' : 'numeric'" aria-label="At most" :aria-invalid="!!limitError" :disabled="limitBusy">
              <select v-model="draft.unit" class="field pick" aria-label="Counted in" :disabled="limitBusy">
                <option v-for="u in choices" :key="u" :value="u">{{ UNIT_WORD[u] }}</option>
              </select>
            </span>
            <span class="keep">of this account per
              <select v-model="draft.period" class="field pick" aria-label="Per" :disabled="limitBusy">
                <option v-for="p in PERIODS" :key="p" :value="p">{{ p }}</option>
              </select>
            </span>
            <span class="actions">
              <button type="submit" class="btn sm" :disabled="limitBusy || !dirty || !draft.amount.trim()">Apply</button>
              <button v-if="use" type="button" class="btn sm ghost" :disabled="limitBusy" @click="clearLimit">Remove</button>
              <button v-else type="button" class="btn sm ghost" :disabled="limitBusy" @click="closeSentence">Cancel</button>
            </span>
          </form>
          <p v-if="use" class="use">{{ limitUseLine(use, now, timezone) }}<template v-if="limitBinds(use, primary)"> · binds before the plan today</template></p>
          <p v-if="limitError" class="problem" role="alert">{{ limitError }}</p>
          <p v-if="limitStatus" class="status" role="status">{{ limitStatus }}</p>
        </dd>
      </div>

      <div v-if="mine.length" class="fact mine-fact">
        <dt>Set by you</dt>
        <dd>
          <ul class="mine">
            <li v-for="w in mine" :key="w.id">
              <span class="what" :data-tip="windowTip(w)">{{ setByYouText(w, timezone) }}</span>
              <span v-if="mayManage" class="actions">
                <button v-if="w.unit !== 'percent'" type="button" class="btn sm ghost" :disabled="!!windowBusy" @click="repeat(w)"><AppIcon name="refresh" :size="14" />Make this repeat</button>
                <button type="button" class="btn sm ghost" :disabled="!!windowBusy" @click="drop(w)"><AppIcon name="trash" :size="14" />Remove</button>
              </span>
            </li>
          </ul>
          <p v-if="windowError" class="problem" role="alert">{{ windowError }}</p>
        </dd>
      </div>
    </dl>
  </div>
</template>

<style scoped>
.detail { margin: 0 0 10px 30px; padding: 12px 16px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.account-facts { display: grid; grid-template-columns: 96px minmax(0, 1fr); gap: 8px 18px; margin: 0; }
.fact { display: contents; }
dt { padding-top: 5px; font: 500 12px/1.5 var(--font); letter-spacing: 0; text-transform: none; color: var(--ink-3); }
dd { min-width: 0; margin: 0; padding-top: 4px; font-size: 13px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
.num { font-variant-numeric: tabular-nums; }
b.num { font-weight: 650; }
.sep, .src, .by, .quiet { color: var(--ink-2); }
.src { font-size: 12.5px; }
.window.frozen dd { color: var(--ink-2); }
.name-line { display: flex; align-items: center; gap: 4px; min-height: 28px; padding-top: 0; }
.nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.rename { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.rename .field { flex: 1 1 200px; max-width: 320px; height: 30px; }
.readings { display: flex; flex-wrap: wrap; gap: 2px 14px; margin: 0; padding: 0; list-style: none; }
.readings time { font-variant-numeric: tabular-nums; color: var(--ink-2); }
.by { font-size: 12.5px; }
.set-limit { margin: -2px 0 0 -10px; }
/* Labels sit on the text line of a 30 px control row. */
.limit:has(.sentence) dt, .mine-fact dt { padding-top: 10px; }
.sentence { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.sentence .keep { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.sentence .field { height: 30px; width: auto; padding: 0 8px; }
.sentence .amount { width: 72px; text-align: right; font-variant-numeric: tabular-nums; }
.sentence .pick { padding-right: 4px; }
.sentence .actions, .mine .actions { display: inline-flex; gap: 4px; }
.sentence .actions { margin-left: 4px; }
.use { margin-top: 4px; font-size: 12.5px; color: var(--ink-2); }
.status { margin-top: 4px; font-size: 12.5px; color: var(--teal-ink); }
.problem { margin-top: 4px; font-size: 12.5px; color: var(--danger); }
.mine { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.mine li { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 16px; min-height: 30px; }
.mine .what { font-variant-numeric: tabular-nums; }
@media (max-width: 600px) {
  .detail { margin: 0 0 10px; padding: 12px; }
  .account-facts { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  dt { padding-top: 10px; }
  .fact:first-child dt { padding-top: 0; }
  dd { padding-top: 0; }
  .src { display: block; }
  .src .sep { display: none; }
  .sentence .field, .rename .field { height: 44px; font-size: 16px; }
  .rename .field { flex-basis: 100%; max-width: none; }
  .sentence .actions, .mine .actions { margin-left: -10px; }
  .sentence .actions { flex-basis: 100%; }
  .btn.sm { min-height: 44px; }
  .icon-btn.sm { width: 44px; height: 44px; }
}
</style>
