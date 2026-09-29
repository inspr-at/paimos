<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { idMatches, plainSubject, shortCommit, type ChangeGroup, type PresentedChanges, type ReleaseChange, type ReleaseLang, type ReleaseView, type TicketChangeLine } from '../../lib/releases'
import AppIcon, { type IconName } from '../AppIcon.vue'
import LangBadge from './LangBadge.vue'
import TicketLink from './TicketLink.vue'

// A release's changes, the same for every release (AEON-305): features and fixes
// are one block per ticket. Highlights: the pill is the heading, the benefit the
// sentence, and the ticket's commits sit behind a small disclosure. Details: the
// pill heads the ticket's commits, listed open, and the benefit steps aside
// (AEON-323). Anything no ticket tells about stays under Other, with the package
// prefix and leading key removed. A text in the other language carries a badge.
const props = defineProps<{ presented: PresentedChanges; repository: string; query?: string; soleTicket?: string; view?: ReleaseView; lang?: ReleaseLang }>()
const presented = computed(() => props.presented)
const details = computed(() => props.view === 'details')
const fellBack = (lang: ReleaseLang) => !!props.lang && lang !== props.lang
const titleLang = (line: TicketChangeLine) => line.pill ? line.pillLang : line.benefitLang
const ticketsShown = (c: ReleaseChange) => props.soleTicket && c.tickets.length === 1 && c.tickets[0] === props.soleTicket ? [] : c.tickets
const GROUPS: { key: ChangeGroup; label: string; icon: IconName }[] = [
  { key: 'features', label: 'Features', icon: 'sparkle' },
  { key: 'fixes', label: 'Fixes', icon: 'bug' },
  { key: 'other', label: 'Other changes', icon: 'gear' },
]
const TYPE_LABEL: Record<ReleaseChange['type'], string> = { feat: 'feature', fix: 'fix', test: 'tests', docs: 'docs', refactor: 'refactor', chore: 'chore', release: 'release', other: '' }
const countOf = (key: ChangeGroup) => key === 'other' ? presented.value.other.length : presented.value[key].length
const shown = computed(() => GROUPS.filter(g => countOf(g.key)))
const commitUrl = (sha: string) => props.repository ? `https://github.com/${props.repository}/commit/${sha}` : ''
const titleId = (group: string, key: string) => `change-${group}-${key}`
const commitWord = (n: number) => n === 1 ? '1 commit' : `${n} commits`
// A subject or SHA hit is painted inside the disclosure. Open it for this query, then
// leave it alone so a later render does not slam a reader-closed disclosure shut.
const revealed = new WeakMap<HTMLDetailsElement, string>()
function commitsMatch(line: TicketChangeLine) {
  const q = props.query?.trim().toLowerCase()
  return !!q && line.commits.some(c => plainSubject(c.subject, c.tickets).toLowerCase().includes(q) || idMatches(c.commit, q))
}
function revealCommits(el: unknown, line: TicketChangeLine) {
  if (!(el instanceof HTMLDetailsElement)) return
  const q = props.query?.trim().toLowerCase() ?? ''
  const hit = commitsMatch(line)
  const stamp = hit ? q : ''
  if (revealed.get(el) === stamp) return
  revealed.set(el, stamp)
  if (hit) el.open = true
}
const showBenefit = (line: TicketChangeLine) => {
  if (details.value) return false
  const benefit = line.benefit.trim()
  return !!benefit && benefit.toLowerCase() !== line.pill.trim().toLowerCase()
}
// A SHA search marks the start of the short SHA it matched.
function shaParts(sha: string) {
  const short = shortCommit(sha), q = props.query?.trim().toLowerCase() ?? ''
  if (!idMatches(sha, q)) return [{ text: short, hit: false }]
  const n = Math.min(q.length, short.length)
  return [{ text: short.slice(0, n), hit: true }, { text: short.slice(n), hit: false }].filter(p => p.text)
}
// Search terms stay marked in the list of changes.
function parts(text: string) {
  const q = props.query?.trim()
  if (!q) return [{ text, hit: false }]
  const out: { text: string; hit: boolean }[] = []
  const lower = text.toLowerCase(), needle = q.toLowerCase()
  let at = 0
  for (let i = lower.indexOf(needle); i !== -1; i = lower.indexOf(needle, at)) {
    if (i > at) out.push({ text: text.slice(at, i), hit: false })
    out.push({ text: text.slice(i, i + needle.length), hit: true })
    at = i + needle.length
  }
  if (at < text.length) out.push({ text: text.slice(at), hit: false })
  return out
}
</script>

<template>
  <div class="changes">
    <section v-for="g in shown" :key="g.key" class="group" :class="g.key" :aria-label="`${g.label}, ${countOf(g.key)}`">
      <h3 class="group-h"><span class="g-icon"><AppIcon :name="g.icon" :size="13" /></span>{{ g.label }}<span class="count mono">{{ countOf(g.key) }}</span></h3>
      <ul v-if="g.key !== 'other'" class="ticket-lines">
        <li v-for="line in presented[g.key]" :key="line.key">
          <article class="ticket-line" :aria-labelledby="titleId(g.key, line.key)">
            <div class="line-head">
              <h4 :id="titleId(g.key, line.key)" class="pill-title" :lang="titleLang(line)"><span class="change-glyph" aria-hidden="true"><AppIcon :name="g.icon" :size="13" /></span><template v-for="(p, i) in parts(line.pill || line.benefit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template><LangBadge v-if="fellBack(titleLang(line))" :lang="titleLang(line)" /></h4>
              <TicketLink :ticket-key="line.key" variant="inline" />
            </div>
            <p v-if="line.pill && showBenefit(line)" class="benefit" :lang="line.benefitLang"><template v-for="(p, i) in parts(line.benefit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template><LangBadge v-if="fellBack(line.benefitLang) && !fellBack(titleLang(line))" :lang="line.benefitLang" /></p>
            <ul v-if="details && line.commits.length" class="commit-list open" :aria-label="`${commitWord(line.commits.length)}`">
              <li v-for="c in line.commits" :key="c.commit">
                <p class="subject"><template v-for="(p, i) in parts(plainSubject(c.subject, c.tickets))" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></p>
                <a v-if="commitUrl(c.commit)" class="mono commit" :href="commitUrl(c.commit)" target="_blank" rel="noopener" :aria-label="`Commit ${shortCommit(c.commit)} on GitHub`"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></a>
                <span v-else class="mono commit"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span>
              </li>
            </ul>
            <details v-else-if="line.commits.length" :ref="el => revealCommits(el, line)" class="commits">
              <summary><AppIcon name="chevron-right" :size="12" class="chev" />{{ commitWord(line.commits.length) }}</summary>
              <ul class="commit-list">
                <li v-for="c in line.commits" :key="c.commit">
                  <p class="subject"><template v-for="(p, i) in parts(plainSubject(c.subject, c.tickets))" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></p>
                  <a v-if="commitUrl(c.commit)" class="mono commit" :href="commitUrl(c.commit)" target="_blank" rel="noopener" :aria-label="`Commit ${shortCommit(c.commit)} on GitHub`"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></a>
                  <span v-else class="mono commit"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span>
                </li>
              </ul>
            </details>
          </article>
        </li>
      </ul>
      <ul v-else>
        <li v-for="c in presented.other" :key="c.commit">
          <p class="subject"><span class="change-glyph" aria-hidden="true"><AppIcon :name="g.icon" :size="13" /></span><template v-for="(p, i) in parts(plainSubject(c.subject, c.tickets))" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></p>
          <p class="meta">
            <span v-if="TYPE_LABEL[c.type]" class="type">{{ TYPE_LABEL[c.type] }}</span>
            <TicketLink v-for="t in ticketsShown(c)" :key="t" :ticket-key="t" variant="inline" />
            <a v-if="commitUrl(c.commit)" class="mono commit" :href="commitUrl(c.commit)" target="_blank" rel="noopener" :aria-label="`Commit ${shortCommit(c.commit)} on GitHub`"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></a>
            <span v-else class="mono commit"><template v-for="(p, i) in shaParts(c.commit)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span>
          </p>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.changes { display: grid; gap: 18px; }
.group-h { display: flex; align-items: center; gap: 9px; margin-bottom: 6px; font: 650 13px/1.4 var(--font); color: var(--ink); letter-spacing: 0; }
.g-icon { display: grid; place-items: center; width: 24px; height: 24px; border-radius: 8px; background: var(--surface-2); color: var(--ink-2); }
.features .g-icon { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.fixes .g-icon { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); color: var(--gold-ink); }
.count { margin-left: 2px; font-size: 11px; font-weight: 500; color: var(--ink-3); }
ul { margin: 0; padding: 0; list-style: none; display: grid; gap: 1px; }
.group > ul { padding-left: 33px; }
.ticket-lines { gap: 4px; }
.ticket-lines > li, .group > ul:not(.ticket-lines) > li { padding: 6px 10px 7px 28px; margin-left: -28px; border-radius: 9px; }
.ticket-line { display: grid; gap: 3px; min-width: 0; }
.line-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; min-width: 0; }
.pill-title { margin: 0; min-width: 0; font: 650 14.5px/1.35 var(--font); color: var(--ink); letter-spacing: 0; overflow-wrap: anywhere; }
/* Hanging kind mark. Its box is the first line's cap, bottom on the baseline,
   and the icon is centred in that box — so the mark meets the cap centre
   without a nudge, and a wrapped title keeps it on the first line. Where cap
   units are missing, the box is the line and the icon centres there. The
   benefit and commit count keep the title's text edge. */
.change-glyph {
  --glyph-gap: 8px;
  position: relative; display: inline-block; width: 0; height: 1lh; vertical-align: top;
  opacity: .7; pointer-events: none;
}
.change-glyph :deep(svg) { position: absolute; right: var(--glyph-gap); top: 0; bottom: 0; margin-block: auto; }
@supports (height: 1cap) {
  .change-glyph { height: 1cap; vertical-align: baseline; }
}
.features .change-glyph { color: var(--teal-ink); }
.fixes .change-glyph { color: var(--gold-ink); }
.other .change-glyph { color: var(--ink-3); }
.benefit { margin: 0; font-size: 14px; line-height: 1.45; color: var(--ink); text-wrap: pretty; overflow-wrap: anywhere; }
.commits { min-width: 0; }
.commits summary { display: inline-flex; align-items: center; gap: 4px; margin: 1px 0 0; padding: 2px 6px 2px 0; border-radius: 6px; color: var(--ink-3); font: 500 12px/1.3 var(--font); cursor: pointer; }
.commits summary::-webkit-details-marker { display: none; }
.commits summary:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.chev { color: var(--ink-3); transition: transform .16s ease; }
.commits[open] .chev { transform: rotate(90deg); }
.commit-list { margin: 6px 0 2px; padding: 0; gap: 6px; }
.commit-list li { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; padding: 0 0 0 16px; }
/* Details: the commits are the content, listed open under the pill. */
.commit-list.open { margin: 3px 0 2px; gap: 4px; }
.commit-list.open li { padding-left: 0; }
.commit-list.open .subject { color: var(--ink-2); }
@media (hover: hover) {
  .ticket-lines > li:hover, .group > ul:not(.ticket-lines) > li:hover { background: var(--row-hover); }
  .commits summary:hover { color: var(--ink-2); }
}
.subject { color: var(--ink); font-size: 13.5px; line-height: 1.45; overflow-wrap: anywhere; }
.meta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; margin-top: 1px; font-size: 11.5px; color: var(--ink-3); }
.type { font-size: 11px; text-transform: lowercase; }
.commit { font-size: 11px; color: var(--ink-3); border-radius: 4px; }
@media (max-width: 600px) { a.commit, .commits summary { display: inline-flex; align-items: center; min-height: 44px; } }
@media (hover: hover) { a.commit:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; } }
@media (max-width: 760px) {
  .group > ul { padding-left: 0; }
  .ticket-lines > li, .group > ul:not(.ticket-lines) > li { margin-left: 0; padding: 6px 4px 7px; }
  .commit-list li { padding-left: 8px; }
  /* The detail pane scrolls, so a mark in the page padding is clipped. The
     13px mark stays in the row with an 8px gap, and the benefit, commits
     and meta share the title's text edge. */
  .pill-title, .group > ul:not(.ticket-lines) > li > .subject { padding-left: 21px; }
  .benefit, .commits, .commit-list.open, .group > ul:not(.ticket-lines) > li > .meta { margin-left: 21px; }
  .commit-list.open li { padding-left: 0; }
}
/* Phones: the subject wraps on the left and the SHA keeps its 44 px target on the right. */
@media (max-width: 600px) {
  .commit-list.open { gap: 0; }
  .commit-list.open li { flex-wrap: nowrap; align-items: center; justify-content: space-between; gap: 12px; }
  .commit-list.open .subject { flex: 1 1 auto; min-width: 0; }
  .commit-list.open .commit { flex: 0 0 auto; }
}
@media (max-width: 600px) { .changes { gap: 6px; } ul:not(.ticket-lines):not(.commit-list) > li { padding-bottom: 0; } .meta { margin-top: 0; } }
@media (prefers-reduced-motion: reduce) { .chev { transition: none; } }
</style>
