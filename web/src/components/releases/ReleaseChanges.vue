<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { plainSubject, shortCommit, ticketBlocks, type ChangeGroup, type ReleaseChange } from '../../lib/releases'
import { useProfile } from '../../stores/profile'
import AppIcon, { type IconName } from '../AppIcon.vue'
import TicketLink from './TicketLink.vue'

// A release's changes, grouped: features, fixes, and everything else. Inside
// features and fixes, a ticket's changes sit under its pill (AEON-305); the
// pill names the ticket, so its rows do not repeat the key.
// A release with one ticket names it once above the changes, not on every line.
const props = defineProps<{ groups: Record<ChangeGroup, ReleaseChange[]>; repository: string; query?: string; soleTicket?: string }>()
const profile = useProfile()
const locale = computed(() => profile.profile?.locale ?? null)
const blocks = computed(() => ({
  features: ticketBlocks(props.groups.features, locale.value),
  fixes: ticketBlocks(props.groups.fixes, locale.value),
  other: props.groups.other.length ? [{ key: '', pill: '', pillLang: 'en' as const, changes: props.groups.other }] : [],
}))
const ticketsShown = (c: ReleaseChange, under = '') => c.tickets.filter(t => t !== under && !(props.soleTicket && c.tickets.length === 1 && t === props.soleTicket))
const GROUPS: { key: ChangeGroup; label: string; icon: IconName }[] = [
  { key: 'features', label: 'Features', icon: 'sparkle' },
  { key: 'fixes', label: 'Fixes', icon: 'bug' },
  { key: 'other', label: 'Other changes', icon: 'gear' },
]
const TYPE_LABEL: Record<ReleaseChange['type'], string> = { feat: 'feature', fix: 'fix', test: 'tests', docs: 'docs', refactor: 'refactor', chore: 'chore', release: 'release', other: '' }
const shown = computed(() => GROUPS.filter(g => props.groups[g.key].length))
const commitUrl = (sha: string) => props.repository ? `https://github.com/${props.repository}/commit/${sha}` : ''
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
    <section v-for="g in shown" :key="g.key" class="group" :class="g.key" :aria-label="`${g.label}, ${groups[g.key].length}`">
      <h3 class="group-h"><span class="g-icon"><AppIcon :name="g.icon" :size="13" /></span>{{ g.label }}<span class="count mono">{{ groups[g.key].length }}</span></h3>
      <ul>
        <template v-for="b in blocks[g.key]" :key="b.key || '-'">
          <li v-if="b.key" class="ticket-h">
            <span class="chip pill" :lang="b.pillLang" :data-tip="b.pill"><template v-for="(p, i) in parts(b.pill)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span>
            <TicketLink :ticket-key="b.key" variant="inline" />
          </li>
          <li v-for="c in b.changes" :key="c.commit" class="change">
            <p class="subject"><template v-for="(p, i) in parts(plainSubject(c.subject, c.tickets))" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></p>
            <p class="meta">
              <span v-if="g.key === 'other' && TYPE_LABEL[c.type]" class="type">{{ TYPE_LABEL[c.type] }}</span>
              <TicketLink v-for="t in ticketsShown(c, b.key)" :key="t" :ticket-key="t" variant="inline" />
              <a v-if="commitUrl(c.commit)" class="mono commit" :href="commitUrl(c.commit)" target="_blank" rel="noopener" :aria-label="`Commit ${shortCommit(c.commit)} on GitHub`">{{ shortCommit(c.commit) }}</a>
              <span v-else class="mono commit">{{ shortCommit(c.commit) }}</span>
            </p>
          </li>
        </template>
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
ul { margin: 0; padding: 0 0 0 33px; list-style: none; display: grid; gap: 1px; }
li { padding: 6px 10px 7px; margin-left: -10px; border-radius: 9px; }
@media (hover: hover) { li.change:hover { background: var(--row-hover); } }
.ticket-h { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; padding: 8px 10px 2px; min-width: 0; }
.ticket-h:not(:first-child) { margin-top: 6px; }
.pill { height: 22px; max-width: 100%; padding: 0 9px; font: 600 12px/22px var(--font); letter-spacing: 0; text-transform: none; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.subject { color: var(--ink); font-size: 13.5px; line-height: 1.45; overflow-wrap: anywhere; }
.meta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; margin-top: 1px; font-size: 11.5px; color: var(--ink-3); }
.type { font-size: 11px; text-transform: lowercase; }
.commit { font-size: 11px; color: var(--ink-3); border-radius: 4px; }
@media (max-width: 600px) { a.commit { display: inline-flex; align-items: center; min-height: 44px; } }
@media (hover: hover) { a.commit:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; } }
@media (max-width: 760px) { ul { padding-left: 0; } li { margin-left: 0; padding: 6px 4px 7px; } .ticket-h { padding: 8px 4px 2px; } }
/* Phones: the commit links' touch height already spaces the groups. */
@media (max-width: 600px) { .changes { gap: 6px; } li { padding-bottom: 0; } .meta { margin-top: 0; } }
</style>
