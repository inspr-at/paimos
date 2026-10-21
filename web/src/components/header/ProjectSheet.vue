<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The project's sheet (AEON-1459, design AEON-1279 r2 D11): on phones and short screens the header's
// project crumb opens what the project head shows on wider screens: its name and description, its
// progress and counts, its sections and its lead. A sheet from the bottom on phones; anchored to the
// crumb on short landscape screens. Esc, a tap outside or a swipe down closes it.
import { computed, toRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { LEAD_WORDS } from '../../lib/lead'
import { openLeadPanel } from '../../lib/leadOverlay'
import { useLeadSummary } from '../../lib/useLeadSummary'
import { useProjectHeader } from '../../lib/useProjectHeader'
import type { Project } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import LeadBot from '../lead/LeadBot.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import StatusIcon from '../work/StatusIcon.vue'
import { PROJECT_SECTIONS, projectSection, type ProjectSection } from '../work/projectNavigation'

const props = defineProps<{ anchor: HTMLElement; project: Project }>()
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const route = useRoute(), router = useRouter()
const { compactHeadShown, toggleHeader } = useProjectHeader()
const key = computed(() => props.project.routeKey)
const label = computed(() => `${key.value} · ${props.project.title}`)
const sections = computed(() => PROJECT_SECTIONS.filter(item => item.id !== 'delivery' || can('delivery.read', props.project.id)))
const fullTicket = computed(() => typeof route.params.ticketKey === 'string' && route.query.view === 'full')
// The section on screen; a full ticket or an entry page belongs to none of them.
const here = computed<ProjectSection | null>(() => fullTicket.value || (typeof route.params.slug === 'string') ? null : projectSection(route))
const percent = computed(() => Math.max(0, Math.min(100, Math.round(props.project.percent || 0))))
const counts = computed(() => [
  { state: 'open', n: props.project.open, word: 'open' },
  { state: 'in_progress', n: props.project.in_progress, word: 'in progress' },
  { state: 'done', n: props.project.done, word: 'done' },
])
const countWords = computed(() => counts.value.map(count => `${count.n.toLocaleString('en-GB')} ${count.word}`).join(', '))
// The project head on the page folds on the ticket list; the crumb used to fold it on phones.
const ticketList = computed(() => here.value === 'tickets' && route.query.panel !== 'full')
const summary = useLeadSummary(toRef(() => props.project.id), toRef(() => props.project.routeKey))
const { band } = summary
const path = (section: ProjectSection) => `/p/${encodeURIComponent(key.value)}/${section}`
function go(section: ProjectSection) {
  emit('close', false)
  if (section !== here.value) void router.push(path(section))
}
function lead(event: MouseEvent) {
  emit('close', false)
  openLeadPanel(props.project.id, event.currentTarget as HTMLElement)
}
function fold() { toggleHeader(); emit('close', true) }
function keys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role^="menuitem"]')]
  if (!items.length) return
  event.preventDefault()
  const index = items.indexOf(document.activeElement as HTMLElement)
  items[event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
</script>

<template>
  <FloatingPanel :anchor="anchor" align="start" :width="340" :tallest="560" :label="label" phone-sheet :sheet-below="719" class="project-sheet" @close="emit('close', $event)">
    <div class="prj" @keydown="keys">
      <h2 class="prj-h" tabindex="-1" data-autofocus><span class="key-badge">{{ key }}</span><span class="prj-name">{{ project.title }}</span></h2>
      <p v-if="project.description" class="prj-d">{{ project.description }}</p>
      <div class="prj-p" role="img" :aria-label="`${percent} percent done`">
        <span class="bar" aria-hidden="true"><i :style="{ width: `${percent}%` }" /></span><span class="mono" aria-hidden="true">{{ percent }}&nbsp;%</span>
      </div>
      <p class="prj-c" :aria-label="countWords">
        <span v-for="count in counts" :key="count.state" class="prj-n" aria-hidden="true"><StatusIcon :state="count.state" :size="13" /><b>{{ count.n.toLocaleString('en-GB') }}</b>&nbsp;{{ count.word }}</span>
      </p>
      <hr class="prj-sep" role="separator" />
      <div role="menu" aria-label="Sections">
        <button
          v-for="section in sections" :key="section.id" type="button" class="prj-item" role="menuitemradio" :aria-checked="here === section.id" @click="go(section.id)"
        ><AppIcon :name="section.icon" :size="16" /><span class="prj-t">{{ section.label }}</span><AppIcon v-if="here === section.id" name="check" :size="14" class="prj-on" /></button>
        <button v-if="ticketList" type="button" class="prj-item" role="menuitemcheckbox" :aria-checked="compactHeadShown" @click="fold">
          <AppIcon :name="compactHeadShown ? 'chevron-up' : 'chevron'" :size="16" /><span class="prj-t">Show the project head</span><span class="prj-d2">above the tickets</span>
        </button>
      </div>
      <template v-if="band.state !== 'none'">
        <hr class="prj-sep" role="separator" />
        <div role="menu" :aria-label="`The ${LEAD_WORDS.l}`">
          <button type="button" class="prj-item prj-lead" role="menuitem" @click="lead">
            <span class="prj-bot" aria-hidden="true"><LeadBot :busy="band.busy" small /></span>
            <span class="prj-lt"><span class="prj-t">{{ band.title }}</span><span class="prj-d2">{{ band.status }}</span></span>
          </button>
        </div>
      </template>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.prj { display: grid; gap: 8px; padding: 6px 4px; }
.prj-h { display: flex; align-items: center; gap: 10px; margin: 0; padding: 4px 6px 0; color: var(--ink); font: 650 16px/1.3 var(--font); outline: none; }
.prj-name { min-width: 0; overflow-wrap: anywhere; }
.prj-d { margin: 0; padding: 0 6px; color: var(--ink-2); font-size: 13px; line-height: 1.45; overflow-wrap: anywhere; white-space: pre-line; }
.prj-p { display: flex; align-items: center; gap: 10px; padding: 0 6px; }
.prj-p .bar { position: relative; flex: 1; height: 6px; border-radius: 999px; background: var(--surface-sunken); overflow: hidden; }
.prj-p .bar i { position: absolute; inset: 0 auto 0 0; border-radius: inherit; background: var(--teal); }
.prj-p .mono { font: 500 12px/1 var(--mono); color: var(--ink-2); font-variant-numeric: tabular-nums; }
.prj-c { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 14px; margin: 0; padding: 0 6px; color: var(--ink-2); font-size: 12.5px; }
.prj-n { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }
.prj-n b { color: var(--ink); font-weight: 650; font-variant-numeric: tabular-nums; }
.prj-sep { height: 1px; margin: 2px 4px; border: 0; background: var(--line); }
.prj-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 44px; padding: 4px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
.prj-item > svg { flex: none; color: var(--ink-2); }
@media (hover: hover) { .prj-item:hover { background: var(--row-hover); } }
.prj-item:focus-visible { outline: none; background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.prj-item[aria-checked="true"][role="menuitemradio"] .prj-t { color: var(--teal-ink); font-weight: 600; }
.prj-t { flex: 1 1 auto; min-width: 0; }
.prj-on { margin-left: auto; color: var(--teal-ink) !important; }
.prj-d2 { color: var(--ink-3); font-size: 12px; }
.prj-item > .prj-d2 { flex: none; }
.prj-lead { align-items: center; }
.prj-bot { display: grid; place-items: center; flex: none; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-sunken); }
.prj-lt { display: grid; gap: 1px; min-width: 0; }
.prj-lt .prj-d2 { overflow-wrap: anywhere; }
</style>
