<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { displayHeadline, groupChanges, releasedAt, shortCommit, span, ticketsOf, type Release } from '../../lib/releases'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import CalendarVersion from '../CalendarVersion.vue'
import ReleaseChanges from './ReleaseChanges.vue'
import TicketChips from './TicketChips.vue'
import TicketLink from './TicketLink.vue'
const noteLanguage = ref<'en' | 'de'>('en')

// One release: when it shipped, what changed, and the evidence behind it.
const props = defineProps<{
  release: Release; repository: string; current: boolean; rollback: boolean; fresh: boolean
  liveSince: string | null; now: number; query: string; evidence: boolean
}>()
const emit = defineEmits<{ evidence: [open: boolean] }>()

const at = computed(() => releasedAt(props.release))
const reserved = computed(() => props.release.state === 'reserved')
const groups = computed(() => groupChanges(props.release.changes))
const tickets = computed(() => ticketsOf(props.release))
const counted = computed(() => groups.value.features.length + groups.value.fixes.length + groups.value.other.length)
const live = computed(() => {
  if (!props.current || !props.liveSince) return ''
  const t = Date.parse(props.liveSince)
  return Number.isNaN(t) ? '' : `Live on this server since ${absoluteTime(props.liveSince)} · ${span(Math.max(60_000, props.now - t))}`
})

// ---------- Evidence ----------
const ev = computed(() => props.release.evidence)
const RUN_WORD: Record<string, string> = { success: 'passed', failure: 'failed', cancelled: 'cancelled', skipped: 'skipped', timed_out: 'timed out' }
const runWord = (run: { status: string; conclusion: string }) => run.conclusion ? (RUN_WORD[run.conclusion] ?? run.conclusion.replace(/_/g, ' ')) : run.status.replace(/_/g, ' ')
const summary = computed(() => {
  const bits: string[] = []
  if (ev.value.ci) bits.push(`CI ${runWord(ev.value.ci)}`)
  if (ev.value.image?.digest) bits.push('image digest')
  if (ev.value.source_commit) bits.push(`commit ${shortCommit(ev.value.source_commit)}`)
  return bits.join(' · ') || (ev.value.unavailable.length ? 'Partly unavailable' : 'None recorded')
})
const digestShort = (d: string) => d.length > 30 ? `${d.slice(0, 19)}…${d.slice(-8)}` : d
const copied = ref('')
let copyTimer: ReturnType<typeof setTimeout> | undefined
async function copy(what: string, text: string) {
  try { await navigator.clipboard.writeText(text); copied.value = what }
  catch { copied.value = `${what}:failed` }
  clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copied.value = '' }, 1600)
}
const COPIED: Record<string, string> = { version: 'Version copied', commit: 'Commit copied', digest: 'Image digest copied' }
const copyStatus = computed(() => copied.value.endsWith(':failed') ? 'Copying is not available here; select the text instead.' : COPIED[copied.value] ?? '')
const copyLabel = (what: string, label: string) => copied.value === what ? 'Copied' : copied.value === `${what}:failed` ? 'Select to copy' : label
const heading = ref<HTMLElement>()
defineExpose({ focus: () => heading.value?.focus({ preventScroll: false }) })
</script>

<template>
  <article class="detail" :class="{ reserved }" aria-labelledby="release-detail-title">
    <p class="eyebrow top">
      <span>{{ reserved ? 'Reserved version' : `Release ${release.release_sequence}` }}</span>
      <span v-if="!reserved && release.release_channel" class="dot-sep">{{ release.release_channel }}</span>
    </p>
    <h2 id="release-detail-title" ref="heading" class="version" tabindex="-1"><CalendarVersion :value="release.version" /></h2>
    <div class="badges">
      <span v-if="current" class="chip teal"><span class="live-dot" aria-hidden="true" />Current</span>
      <span v-if="fresh" class="chip new">New since your last visit</span>
      <span v-if="rollback" class="chip"><AppIcon name="rollback" :size="11" />Rollback target</span>
      <span v-if="reserved" class="chip">Reserved, never published</span>
    </div>
    <p v-if="live" class="when live-line">{{ live }}</p>
    <p v-if="at" class="when">
      <template v-if="reserved">Reserved {{ absoluteTime(at) }} · {{ relativeTime(at, { now, long: true }) }}. The version was taken{{ release.tag ? ' and tagged' : '' }}, but no release was published under it.</template>
      <template v-else>{{ release.published_at ? 'Published' : 'Tagged' }} {{ absoluteTime(at) }} · {{ relativeTime(at, { now, long: true }) }}</template>
    </p>
    <p v-if="!release.notes && release.headline" class="headline">{{ displayHeadline(release) }}</p>

    <TicketChips v-if="tickets.length" :tickets="tickets" class="tickets" />

    <section v-if="release.notes" class="changes-block" aria-label="Release notes">
      <div class="badges" role="group" aria-label="Release note language">
        <button type="button" class="chip" :aria-pressed="noteLanguage === 'en'" @click="noteLanguage = 'en'">English</button>
        <button type="button" class="chip" :aria-pressed="noteLanguage === 'de'" @click="noteLanguage = 'de'">Deutsch</button>
      </div>
      <div v-for="note in release.notes.items" :key="note.id" :lang="noteLanguage" class="note-item">
        <p><span class="chip">{{ noteLanguage === 'en' ? note.pill_en : note.pill_de }}</span> <TicketLink :ticket-key="note.key" /></p>
        <p>{{ noteLanguage === 'en' ? note.benefit_en : note.benefit_de }}</p>
      </div>
      <p v-for="gap in release.notes.gaps" :key="gap" class="none">{{ gap }}</p>
      <p v-if="!release.notes.items.length && !release.notes.gaps.length" class="none">{{ noteLanguage === 'en' ? 'No public release notes.' : 'Keine öffentlichen Release Notes.' }}</p>
      <p v-if="release.notes.hidden" class="none">{{ release.notes.hidden }} {{ noteLanguage === 'en' ? 'ticket(s) hidden from release notes.' : 'Ticket(s) in den Release Notes ausgeblendet.' }}</p>
    </section>
    <div v-else class="changes-block">
      <ReleaseChanges v-if="counted" :groups="groups" :repository="repository" :query="query" />
      <p v-else class="none">{{ reserved ? 'Nothing shipped under this version.' : 'No changes are recorded between this release and the one before it.' }}</p>
      <p v-if="release.changes_omitted" class="none">And {{ release.changes_omitted }} more {{ release.changes_omitted === 1 ? 'change' : 'changes' }} not listed here.</p>
    </div>

    <!-- A reservation that was tagged still has evidence: often why it never published. -->
    <section v-if="release.tag" class="evidence" :class="{ open: evidence }">
      <button type="button" class="ev-toggle" :aria-expanded="evidence" aria-controls="release-evidence" aria-keyshortcuts="e" @click="emit('evidence', !evidence)">
        <span class="ev-icon"><AppIcon name="shield" :size="14" /></span>
        <span class="ev-title">Evidence</span>
        <span class="ev-summary">{{ summary }}</span>
        <AppIcon name="chevron" :size="14" class="ev-chev" />
      </button>
      <div v-if="evidence" id="release-evidence" class="ev-body">
        <p class="sr" role="status">{{ copyStatus }}</p>
        <template v-if="release.notes">
          <p v-if="release.headline" class="none">Git headline: {{ release.headline }}</p>
          <p class="none">Note source: {{ release.notes.source }}</p>
          <p v-if="release.notes.snapshot_sha256" class="mono wrap">Snapshot SHA-256: {{ release.notes.snapshot_sha256 }}</p>
          <ReleaseChanges v-if="counted" :groups="groups" :repository="repository" :query="query" />
        </template>
        <dl>
          <div>
            <dt>Version</dt>
            <dd><span class="mono">{{ release.version }}</span>
              <button type="button" class="copy" :aria-label="`Copy version ${release.version}`" @click="copy('version', release.version)"><AppIcon :name="copied === 'version' ? 'check' : 'copy'" :size="12" />{{ copyLabel('version', 'Copy') }}</button></dd>
          </div>
          <div v-if="ev.source_commit">
            <dt>Source commit</dt>
            <dd><a v-if="ev.source_url" class="mono ext" :href="ev.source_url" target="_blank" rel="noopener">{{ shortCommit(ev.source_commit) }}<AppIcon name="external" :size="11" /></a><span v-else class="mono">{{ shortCommit(ev.source_commit) }}</span>
              <button type="button" class="copy" :aria-label="`Copy commit ${ev.source_commit}`" @click="copy('commit', ev.source_commit)"><AppIcon :name="copied === 'commit' ? 'check' : 'copy'" :size="12" />{{ copyLabel('commit', 'Copy') }}</button></dd>
          </div>
          <div v-if="ev.ci">
            <dt>CI run</dt>
            <dd><a class="ext" :href="ev.ci.url" target="_blank" rel="noopener"><span class="run" :class="ev.ci.conclusion">{{ runWord(ev.ci) }}</span>{{ ev.ci.name }}<AppIcon name="external" :size="11" /></a></dd>
          </div>
          <div v-if="ev.release_run">
            <dt>Release run</dt>
            <dd><a class="ext" :href="ev.release_run.url" target="_blank" rel="noopener"><span class="run" :class="ev.release_run.conclusion">{{ runWord(ev.release_run) }}</span>{{ ev.release_run.name }}<AppIcon name="external" :size="11" /></a></dd>
          </div>
          <div v-if="ev.image?.reference">
            <dt>Image</dt>
            <dd><span class="mono wrap">{{ ev.image.reference }}</span></dd>
          </div>
          <div v-if="ev.image?.digest">
            <dt>OCI digest</dt>
            <dd><span class="mono" :data-tip="ev.image.digest">{{ digestShort(ev.image.digest) }}</span>
              <button type="button" class="copy" aria-label="Copy the image digest" @click="copy('digest', ev.image.digest)"><AppIcon :name="copied === 'digest' ? 'check' : 'copy'" :size="12" />{{ copyLabel('digest', 'Copy') }}</button></dd>
          </div>
          <div v-if="ev.release_url">
            <dt>GitHub release</dt>
            <dd><a class="ext" :href="ev.release_url" target="_blank" rel="noopener">{{ release.tag }}<AppIcon name="external" :size="11" /></a></dd>
          </div>
        </dl>
        <ul v-if="ev.unavailable.length" class="unavailable" aria-label="Not available">
          <li v-for="note in ev.unavailable" :key="note"><AppIcon name="info" :size="12" />{{ note }}</li>
        </ul>
        <p v-if="rollback" class="rollback">
          <AppIcon name="rollback" :size="14" />
          <span><b>Rollback target.</b> This is the published release before the current one. Rolling back deploys {{ ev.image?.digest ? 'the image digest above' : 'this version' }}.</span>
        </p>
      </div>
    </section>
  </article>
</template>

<style scoped>
.detail { display: grid; align-content: start; gap: 10px; max-width: 820px; }
.top { display: flex; gap: 8px; margin: 0; }
.dot-sep::before { content: '·'; margin-right: 8px; }
.version { font: 500 clamp(24px, 2.2vw, 30px)/1.25 var(--mono); letter-spacing: 0; color: var(--ink); outline: none; }
.version:focus-visible { box-shadow: var(--focus-ring); border-radius: 8px; }
.badges { display: flex; flex-wrap: wrap; gap: 6px; }
.badges:empty { display: none; }
.chip { height: 24px; font-size: 11.5px; letter-spacing: .02em; }
.chip.new { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: color-mix(in oklab, var(--gold-ink), var(--ink) 35%); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); box-shadow: 0 0 0 3px rgba(47, 122, 90, .16); }
.when { font-size: 13px; color: var(--ink-2); }
.live-line { color: var(--ink); }
.headline { margin-top: 4px; font: 500 19px/1.4 var(--serif); color: var(--ink); letter-spacing: -.01em; text-wrap: pretty; }
.reserved .headline, .reserved .version { color: var(--ink-2); }
.tickets { margin-top: 2px; }
.changes-block { display: grid; gap: 8px; margin-top: 10px; }
.note-item { display: grid; gap: 6px; font-size: 14px; }
.chip[aria-pressed="true"] { background: var(--row-selected); font-weight: 650; }
.none { font-size: 13px; color: var(--ink-3); }
.evidence { margin-top: 14px; border-radius: 14px; background: var(--glass); border: 1px solid var(--glass-edge); box-shadow: 0 0 0 1px var(--line); overflow: hidden; }
.ev-toggle { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 48px; padding: 8px 14px; border: 0; background: transparent; color: var(--ink); text-align: left; }
@media (hover: hover) { .ev-toggle:hover { background: var(--row-hover); } }
.ev-toggle:focus-visible { box-shadow: inset 0 0 0 2px var(--aqua); }
.ev-icon { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 8px; background: var(--surface-2); color: var(--teal-ink); }
.ev-title { font-weight: 650; font-size: 13.5px; }
.ev-summary { flex: 1; min-width: 0; font-size: 12.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ev-chev { color: var(--ink-3); transition: transform .18s ease; }
.open .ev-chev { transform: rotate(180deg); }
.ev-body { padding: 4px 14px 14px; border-top: 1px solid var(--line); }
dl { margin: 0; display: grid; }
dl > div { display: grid; grid-template-columns: 130px minmax(0, 1fr); align-items: center; gap: 12px; min-height: 40px; padding: 4px 0; border-bottom: 1px solid var(--line); }
dl > div:last-child { border-bottom: 0; }
dt { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
dd { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 10px; margin: 0; font-size: 13px; color: var(--ink); min-width: 0; }
.wrap { overflow-wrap: anywhere; font-size: 12px; }
.ext { display: inline-flex; align-items: center; gap: 6px; color: var(--teal-ink); border-radius: 6px; }
@media (hover: hover) { .ext:hover { text-decoration: underline; text-underline-offset: 3px; } }
.run { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 11.5px; font-weight: 600; }
.run.success { background: rgba(47, 122, 90, .12); color: color-mix(in oklab, var(--ok), var(--ink) 35%); }
.run.failure, .run.timed_out { background: var(--danger-bg); color: var(--danger); }
.copy { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 9px; border: 0; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 11.5px; font-weight: 600; }
@media (hover: hover) { .copy:hover { background: var(--row-selected); color: var(--teal-ink); } }
.copy:focus-visible { box-shadow: var(--focus-ring); }
.unavailable { display: grid; gap: 4px; margin: 10px 0 0; padding: 0; list-style: none; }
.unavailable li { display: flex; gap: 8px; align-items: flex-start; font-size: 12.5px; color: var(--ink-2); }
.unavailable svg { margin-top: 3px; color: var(--ink-3); }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
.rollback { display: flex; gap: 10px; align-items: flex-start; margin-top: 12px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); color: var(--ink); font-size: 13px; }
.rollback svg { margin-top: 2px; color: var(--teal-ink); }
@media (max-width: 760px) {
  dl > div { grid-template-columns: minmax(0, 1fr); gap: 2px; padding: 8px 0; }
  .copy { height: 44px; padding: 0 14px; }
  .ev-toggle { flex-wrap: wrap; row-gap: 2px; }
  .ev-summary { flex-basis: 100%; order: 3; padding-left: 36px; white-space: normal; overflow: visible; }
  .ext { min-height: 44px; }
  .headline { font-size: 17px; }
}
@media (prefers-reduced-motion: reduce) { .ev-chev { transition: none; } }
</style>
