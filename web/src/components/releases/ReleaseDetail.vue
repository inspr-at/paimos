<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { emptyNotesLine, hasUsableNotes, hiddenNoteLine, localizedPresentation, markParts, presentRelease, releaseCopy, releasedAt, runWord, shortCommit, span, ticketsOf, writtenAfterLine, writtenAfterRelease, type Release, type ReleaseLang, type ReleaseView } from '../../lib/releases'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import CalendarVersion from '../CalendarVersion.vue'
import LangBadge from './LangBadge.vue'
import ReleaseChanges from './ReleaseChanges.vue'
import TicketChips from './TicketChips.vue'

// One release: when it shipped, its codename and its name when it has one,
// what it brings, and the evidence behind it. Every release reads the same (AEON-305): backfilled
// notes and linked tickets both become blocks under Features and Fixes.
// Highlights tells the benefits; Details lists the commits and shows the
// evidence open (AEON-323).
const props = defineProps<{
  release: Release; repository: string; current: boolean; rollback: boolean; fresh: boolean
  liveSince: string | null; now: number; query: string; evidence: boolean
  lang: ReleaseLang; view: ReleaseView
}>()
const locale = computed(() => props.lang)
const details = computed(() => props.view === 'details')
const evidenceOpen = computed(() => details.value || props.evidence)
const emit = defineEmits<{ evidence: [open: boolean] }>()

const at = computed(() => releasedAt(props.release))
const reserved = computed(() => props.release.state === 'reserved')
const lines = computed(() => presentRelease(props.release, locale.value))
const tickets = computed(() => ticketsOf(props.release))
const counted = computed(() => lines.value.features.length + lines.value.fixes.length + lines.value.other.length)
// The header: theme, headline and intro when the release has them. The pills
// and benefits are the blocks below; Git tag messages are evidence only.
const presented = computed(() => localizedPresentation(props.release, locale.value))
// One badge for the header when the headline fell back; else on the part that did.
const badge = (lang: ReleaseLang, own = false) => lang !== props.lang && (own || presented.value?.headlineLang === props.lang)
const noted = computed(() => hasUsableNotes(props.release) ? props.release.notes : null)
const parts = (text: string) => markParts(text, props.query)
const copyText = computed(() => releaseCopy(locale.value))
// A ticket already heading a block needs no chip.
const lined = computed(() => new Set([...lines.value.features, ...lines.value.fixes].map(line => line.key)))
const chipTickets = computed(() => tickets.value.filter(key => !lined.value.has(key)))
const live = computed(() => {
  if (!props.current || !props.liveSince) return ''
  const t = Date.parse(props.liveSince)
  return Number.isNaN(t) ? '' : `Live on this server since ${absoluteTime(props.liveSince)}`
})
const liveFor = computed(() => props.liveSince ? span(Math.max(60_000, props.now - Date.parse(props.liveSince))) : '')
// Changes name their ticket only when the release has more than one.
const soleTicket = computed(() => tickets.value.length === 1 ? tickets.value[0] : '')

// ---------- Evidence ----------
const ev = computed(() => props.release.evidence)
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
      <span v-if="release.codename" class="dot-sep codename" lang="en"><template v-for="(p, i) in parts(release.codename)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span>
      <span v-if="!reserved && release.release_channel" class="dot-sep">{{ release.release_channel }}</span>
    </p>
    <h2 id="release-detail-title" ref="heading" class="version" tabindex="-1"><CalendarVersion :value="release.version" /></h2>
    <div class="badges">
      <span v-if="current && !live" class="chip teal"><span class="live-dot" aria-hidden="true" />Current</span>
      <span v-if="fresh" class="chip new">New since your last visit</span>
      <span v-if="rollback" class="chip"><AppIcon name="rollback" :size="11" />Rollback target</span>
      <span v-if="reserved" class="chip">Reserved, never published</span>
    </div>
    <p v-if="live" class="when live-line"><span class="live-dot" aria-hidden="true" />{{ live }}<span class="for"> · {{ liveFor }}</span></p>
    <p v-if="at" class="when">
      <template v-if="reserved">Reserved {{ absoluteTime(at) }} · {{ relativeTime(at, { now, long: true }) }}. The version was taken{{ release.tag ? ' and tagged' : '' }}, but no release was published under it.</template>
      <template v-else>{{ release.published_at ? 'Published' : 'Tagged' }} {{ absoluteTime(at) }} · {{ relativeTime(at, { now, long: true }) }}</template>
    </p>
    <section class="notes" aria-label="Release notes">
      <div v-if="!reserved && presented" class="summary">
        <p v-if="presented.theme" class="kicker" :lang="presented.themeLang"><template v-for="(p, i) in parts(presented.theme)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template><LangBadge v-if="badge(presented.themeLang)" :lang="presented.themeLang" /></p>
        <p class="headline" :lang="presented.headlineLang"><template v-for="(p, i) in parts(presented.headline)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template><LangBadge v-if="badge(presented.headlineLang, true)" :lang="presented.headlineLang" /></p>
        <p v-if="presented.intro" class="intro" :lang="presented.introLang"><template v-for="(p, i) in parts(presented.intro)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template><LangBadge v-if="badge(presented.introLang)" :lang="presented.introLang" /></p>
      </div>
      <p v-if="noted && !(noted.public_items ?? noted.items).length && !noted.gaps.length" class="none">{{ emptyNotesLine(locale) }}</p>
      <TicketChips v-if="chipTickets.length" :tickets="chipTickets" class="tickets" />
      <ReleaseChanges v-if="counted" :presented="lines" :repository="repository" :query="query" :sole-ticket="soleTicket" :view="view" :lang="lang" />
      <p v-else-if="!noted" class="none" :lang="lang">{{ reserved ? copyText.nothingShipped : copyText.noChanges }}</p>
      <template v-if="noted">
        <p v-for="gap in noted.gaps" :key="gap" class="none">{{ gap }}</p>
        <p v-if="noted.hidden" class="none">{{ hiddenNoteLine(noted.hidden, locale) }}</p>
      </template>
      <p v-if="release.changes_omitted" class="none" :lang="lang">{{ copyText.omitted(release.changes_omitted) }}</p>
      <p v-if="writtenAfterRelease(release)" class="none written-after">{{ writtenAfterLine(locale) }}</p>
    </section>

    <!-- A reservation that was tagged still has evidence: often why it never published. -->
    <!-- Details shows it open; Highlights keeps it behind a toggle. -->
    <section v-if="release.tag" class="evidence" :class="{ open: evidenceOpen }" aria-labelledby="release-evidence-title">
      <h3 v-if="details" id="release-evidence-title" class="ev-toggle ev-head">
        <span class="ev-icon"><AppIcon name="shield" :size="14" /></span>
        <span class="ev-title">Evidence</span>
      </h3>
      <button v-else type="button" class="ev-toggle" :aria-expanded="evidence" aria-controls="release-evidence" aria-keyshortcuts="e" @click="emit('evidence', !evidence)">
        <span class="ev-icon"><AppIcon name="shield" :size="14" /></span>
        <span id="release-evidence-title" class="ev-title">Evidence</span>
        <span class="ev-summary">{{ summary }}</span>
        <AppIcon name="chevron" :size="14" class="ev-chev" />
      </button>
      <div v-if="evidenceOpen" id="release-evidence" class="ev-body">
        <p class="sr" role="status">{{ copyStatus }}</p>
        <p v-if="release.headline" class="none">Tag message: {{ release.headline }}</p>
        <template v-if="release.notes">
          <p class="none">Note source: {{ release.notes.source }}</p>
          <p v-if="release.notes.snapshot_sha256" class="mono wrap">Snapshot SHA-256: {{ release.notes.snapshot_sha256 }}</p>
          <ul v-if="release.notes.corrections?.length" class="unavailable" :aria-label="lang === 'de' ? 'Geprüfte Korrekturen' : 'Reviewed corrections'">
            <li v-for="correction in release.notes.corrections" :key="correction.key"><span><b>{{ correction.key }}</b>: {{ correction.reason }}</span></li>
          </ul>
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
.top { display: flex; flex-wrap: wrap; gap: 2px 8px; margin: 0; }
.dot-sep::before { content: '·'; margin-right: 8px; }
/* The codename (AEON-430) reads as part of the release's name, a step above the label. */
.codename { color: var(--ink-2); }
.version { font: 500 clamp(24px, 2.2vw, 30px)/1.25 var(--mono); letter-spacing: 0; color: var(--ink); outline: none; }
.version:focus-visible { box-shadow: var(--focus-ring); border-radius: 8px; }
.badges { display: flex; flex-wrap: wrap; gap: 6px; }
.badges:empty { display: none; }
.chip { height: 24px; font-size: 11.5px; letter-spacing: .02em; }
.chip.new { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: color-mix(in oklab, var(--gold-ink), var(--ink) 35%); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); box-shadow: 0 0 0 3px rgba(47, 122, 90, .16); }
.when { font-size: 13px; color: var(--ink-2); }
.live-line { color: var(--ink); }
.live-line .live-dot { display: inline-block; margin-right: 7px; vertical-align: 1px; }
.live-line .for { white-space: nowrap; color: var(--ink-2); }
.reserved .version { color: var(--ink-2); }
/* AEON-305: a compact header for a named release; the blocks below stay the main content. */
.notes { display: grid; gap: 8px; margin-top: 10px; min-width: 0; }
.summary { display: grid; gap: 4px; margin-bottom: 8px; padding: 14px 16px; border-radius: 12px; background: color-mix(in oklab, var(--surface-2), transparent 40%); box-shadow: inset 0 0 0 1px var(--line); min-width: 0; }
.kicker { margin: 0; font: 700 11px/1.35 var(--font); letter-spacing: .08em; text-transform: uppercase; color: var(--teal-ink); overflow-wrap: anywhere; }
.summary .headline { margin: 0; font: 650 19px/1.3 var(--font); letter-spacing: -.01em; color: var(--ink); text-wrap: balance; overflow-wrap: anywhere; }
.intro { margin: 0; font-size: 14px; line-height: 1.45; color: var(--ink-2); text-wrap: pretty; overflow-wrap: anywhere; }
mark { background: var(--mark-hl); color: inherit; border-radius: 3px; padding: 0 1px; }
.tickets { margin-top: 2px; }
.none { font-size: 13px; color: var(--ink-3); }
.written-after { font-size: 12.5px; }
.evidence { margin-top: 14px; border-radius: 14px; background: var(--glass); border: 1px solid var(--glass-edge); box-shadow: 0 0 0 1px var(--line); overflow: hidden; }
.ev-toggle { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 48px; padding: 8px 14px; border: 0; background: transparent; color: var(--ink); text-align: left; }
@media (hover: hover) { .ev-toggle:hover { background: var(--row-hover); } }
.ev-toggle:focus-visible { box-shadow: inset 0 0 0 2px var(--aqua); }
.ev-head { margin: 0; font: inherit; letter-spacing: 0; }
@media (hover: hover) { .ev-head:hover { background: transparent; } }
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
  .summary { padding: 12px; }
  .summary .headline { font-size: 17px; }
}
@media (prefers-reduced-motion: reduce) { .ev-chev { transition: none; } }
</style>
