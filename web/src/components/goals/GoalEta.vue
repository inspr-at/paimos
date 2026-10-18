<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// LANDS (AEON-1457 decision 7): the ETA its agent set. Hover or keyboard focus opens
// its history — who set it when, what it was before — right-aligned to the ETA,
// flipping above when there is no room. A passed ETA (the server says so: more than
// ten minutes and not done) shows a gold clock and "passed · update pending". The
// goal's popover adds where the recent pace would land it; LANDS stays the agent's.
import { computed, onBeforeUnmount, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import GoalTip, { type TipLine } from './GoalTip.vue'
import { useSession } from '../../stores/session'
import { useLiveAgents } from '../../stores/liveAgents'
import { who as agentName } from '../../lib/liveAgents'
import { etaChanges, etaText, goalHistory, passedMinutes, whenText, type GoalETA, type GoalOutcome, type EtaChange } from '../../lib/goals'

const props = defineProps<{ goalId: string; outcomeId?: string; eta?: GoalETA; passed?: boolean; done?: boolean; now: number; projection?: string | null; items?: readonly GoalOutcome[] }>()
const session = useSession()
const live = useLiveAgents()

const text = computed(() => etaText(props.eta, props.now))
const passed = computed(() => !!props.passed && !props.done && !!text.value)
const hasHistory = computed(() => props.eta?.source === 'agent' || props.eta?.source === 'worker')
// The popover has something to say: a set ETA, or (for a goal) a projection.
const opens = computed(() => !props.done && (!!text.value || !!props.projection))

// Names this browser already knows: you, or an agent at work right now. Otherwise the time alone.
function name(id: string | undefined): string {
  if (!id) return ''
  if (id === session.identity?.principal.id) return 'you'
  const agent = live.items.find(item => item.principal_id === id)
  return agent ? agentName(agent) : ''
}

// One read per ETA value; the popover waits briefly for it, then shows what it has.
const histories = new Map<string, Promise<EtaChange[]>>()
function history(): Promise<EtaChange[]> {
  if (!hasHistory.value) return Promise.resolve([])
  const key = `${props.goalId}:${props.outcomeId ?? ''}:${props.eta?.text ?? ''}:${props.eta?.at ?? ''}`
  let known = histories.get(key)
  if (!known) {
    known = goalHistory(props.goalId, props.outcomeId).then(page => etaChanges(page.items, props.outcomeId ?? props.goalId), () => { histories.delete(key); return [] })
    histories.set(key, known)
  }
  return known
}
const same = (a: Pick<GoalETA, 'text' | 'at'> | null, b: GoalETA | undefined) => !!a && (a.text ?? '') === (b?.text ?? '') && (a.at ?? '') === (b?.at ?? '')

function linesFor(changes: EtaChange[]): TipLine[] {
  const eta = props.eta, lines: TipLine[] = []
  if (eta && text.value) {
    if (eta.source === 'outcomes') {
      const latest = (props.items ?? []).filter(item => item.state !== 'done' && item.eta.at === eta.at)[0]
      lines.push({ text: latest ? `from its items, latest: ${latest.label}` : 'from its items', muted: eta.partial ? ' · some items have no ETA yet' : undefined })
    } else {
      const current = changes.find(change => same(change.value, eta)), at = current ? changes.indexOf(current) : -1
      const by = name(current?.by ?? eta.by), verb = eta.source === 'worker' ? 'reported' : 'set'
      const head = [`${verb}${by ? ` by ${by}` : ''}`, current ? whenText(current.at, props.now) : ''].filter(Boolean).join(' ')
      const before = current?.before ? etaText(current.before, props.now) : ''
      const earlier = at >= 0 ? changes[at + 1] : undefined
      lines.push({ text: head, muted: before ? ` · was ${before}${earlier ? ` (set ${whenText(earlier.at, props.now)})` : ''}` : undefined })
    }
  }
  if (passed.value) {
    const minutes = passedMinutes(props.eta, props.now)
    lines.push({ text: `${minutes < 90 ? `${minutes} min` : `${Math.round(minutes / 60)} h`} past · update pending`, attention: true })
  }
  if (props.projection) lines.push({ text: props.projection })
  return lines
}

// ---------- Showing ----------
const button = ref<HTMLElement>()
const shown = ref<TipLine[] | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined
let turn = 0
function show(delay: number) {
  if (!opens.value) return
  clearTimeout(timer)
  const mine = ++turn, started = Date.now()
  const reading = history()
  // At most a moment's wait for the history; the lines are fixed once shown.
  const wait = new Promise<EtaChange[] | null>(resolve => setTimeout(() => resolve(null), Math.max(delay, 1200)))
  void Promise.race([reading, wait]).then(changes => {
    if (mine !== turn) return
    const rest = Math.max(0, delay - (Date.now() - started))
    timer = setTimeout(() => { if (mine !== turn) return; shown.value = linesFor(changes ?? []); window.addEventListener('scroll', hide, true) }, rest)
  })
}
function hide() { turn++; clearTimeout(timer); shown.value = null; window.removeEventListener('scroll', hide, true) }
function enter(event: PointerEvent) { if (event.pointerType !== 'touch') show(320) }
function focus(event: FocusEvent) { if ((event.target as HTMLElement).matches(':focus-visible')) show(0) }
// Touch: a tap shows it (there is no hover); a second tap hides it.
function tap(event: MouseEvent) { if (matchMedia('(hover: none)').matches) { event.stopPropagation(); if (shown.value) hide(); else show(0) } }
function key(event: KeyboardEvent) { if (event.key === 'Escape' && shown.value) { event.preventDefault(); event.stopPropagation(); hide() } }
onBeforeUnmount(hide)
const label = computed(() => `${text.value || 'No ETA'}${passed.value ? ', passed, update pending' : ''}${opens.value ? '. ETA history' : ''}`)
</script>

<template>
  <button
    v-if="opens" ref="button" type="button" class="eta-btn" :class="{ passed }" :aria-label="label"
    @pointerenter="enter" @pointerleave="hide" @focus="focus" @blur="hide" @click="tap" @keydown="key"
  >
    <span class="t"><AppIcon v-if="passed" name="clock" :size="13" />{{ text || '—' }}</span>
    <span v-if="passed" class="note">passed · update pending</span>
    <GoalTip v-if="shown && button" :anchor="button" :lines="shown" prefer="below" align="end" />
  </button>
  <span v-else class="eta-btn none" :aria-label="text || 'No ETA'">{{ text || '—' }}</span>
</template>

<style scoped>
.eta-btn { display: grid; justify-items: end; align-content: center; gap: 1px; min-width: 0; padding: 3px 4px; margin: -3px -4px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font-size: 12.5px; line-height: 1.35; text-align: right; }
.eta-btn .t { display: inline-flex; align-items: center; gap: 5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.eta-btn .note { font-size: 11.5px; white-space: nowrap; }
.eta-btn.passed { color: var(--warn-ink); }
.eta-btn.passed .t { font-weight: 600; }
.eta-btn.none { color: var(--ink-3); }
button.eta-btn:hover { background: var(--row-hover); }
.eta-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (max-width: 760px) {
  .eta-btn { position: relative; }
  .eta-btn::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
  .eta-btn.none::before { display: none; }
}
</style>
