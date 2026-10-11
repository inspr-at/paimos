<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getRoutineGuardrails, listRoutines } from '../../lib/api'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { ruleOutcome, ruleWords, type RoutineGuardEntry, type RoutineGuardPolicy } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import RecurringWorkCard from '../recurrences/RecurringWorkCard.vue'

// Settings › Routines (AEON-1182): workspace routines, and the rules every routine
// follows, read-only (a rule editor is a later design). The hard floor, the recommended
// rules and the workspace's own rules are the same for every routine, so they are read
// from the first routine this person can see until a routine-free read exists (R0).
const route = useRoute()
const policy = ref<RoutineGuardPolicy | null>(null), state = ref<'loading' | 'ready' | 'none' | 'failed'>('loading')
const scope = useIdentityScope(), reads = scope.lane()
function load() {
  state.value = 'loading'; policy.value = null
  void reads.run(({ after, signal }) => after(listRoutines({ scope: 'workspace' }, signal), page => {
    const first = page.items[0]
    const next = (id: string) => after(getRoutineGuardrails(id, signal), result => { policy.value = result; state.value = 'ready' })
    if (first) return next(first.id)
    return after(listRoutines({}, signal), any => { if (any.items[0]) return next(any.items[0].id); state.value = 'none' })
  }), { failed: () => { state.value = 'failed' } })
}
watch(() => scope.owner.value, load, { immediate: true })
const shared = (entry: RoutineGuardEntry) => !entry.superseded && (entry.scope.kind === 'installation' || entry.scope.kind === 'tenant')
const groups = computed(() => {
  const rules = (policy.value?.rules ?? []).filter(shared)
  return [
    { label: 'Hard floor · in PAIMOS itself, can’t be changed', level: 'hard', items: rules.filter(entry => entry.level === 'hard') },
    { label: `Recommended${policy.value?.preset_version ? ` · version ${policy.value.preset_version}` : ''}`, level: 'rec', items: rules.filter(entry => entry.level === 'recommended') },
    { label: 'Workspace', level: 'ws', items: rules.filter(entry => entry.level === 'custom') },
  ].filter(group => group.items.length)
})
</script>

<template>
  <div class="routines-section">
    <RecurringWorkCard class="routines-box" scope="workspace" :selected-id="typeof route.query.recurrence === 'string' ? route.query.recurrence : undefined" :consent="route.query.consent === '1'" />
    <section id="rules" class="settings-card glass-card rules-card" aria-labelledby="routine-rules-title">
      <header class="card-head"><span class="card-icon"><AppIcon name="shield" :size="16" /></span><div class="card-titles"><h2 id="routine-rules-title">Routine rules</h2><p>Every action a routine takes is checked against all of these; the strictest result wins. Projects and people can only add or tighten. Read-only for now.</p></div></header>
      <div class="card-body" aria-live="polite">
        <p v-if="state === 'loading'" class="empty" role="status">Reading the rules…</p>
        <p v-else-if="state === 'failed'" class="empty" role="alert">The rules couldn’t be read. They still apply. <button type="button" class="btn sm" @click="load">Try again</button></p>
        <p v-else-if="state === 'none'" class="empty">The rules show here once you can see a routine. Each routine’s editor lists the rules that apply to it after its first save.</p>
        <template v-else>
          <template v-for="group in groups" :key="group.level">
            <p class="eh">{{ group.label }}</p>
            <ul class="checks"><li v-for="entry in group.items" :key="`${entry.scope.kind}/${entry.rule.id}`"><span class="ck-t" :title="ruleWords(entry.rule).full">{{ ruleWords(entry.rule).text }}</span><span class="out" :class="ruleOutcome(entry.rule)[0]">{{ ruleOutcome(entry.rule)[1] }}</span><span class="lv" :class="group.level"><AppIcon v-if="group.level === 'hard'" name="lock" :size="11" />{{ group.level === 'hard' ? 'Hard floor' : group.level === 'rec' ? 'Recommended' : 'Workspace' }}</span></li></ul>
          </template>
          <p class="foot"><AppIcon name="info" :size="13" /><span>Project rules apply to each project’s routines; your own rules apply to your personal routines. Loosening a recommended rule needs an admin and a reason, and is logged.</span></p>
        </template>
      </div>
    </section>
  </div>
</template>

<style scoped>
.routines-section { display: grid; gap: 16px; }
.routines-box { padding: 0; }
.rules-card { padding: 20px; }
.card-head { display: flex; align-items: center; gap: 12px; }
.card-icon { display: grid; place-items: center; flex: none; align-self: flex-start; width: 32px; height: 32px; border-radius: 10px; background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.card-titles { flex: 1; min-width: 0; }
.card-titles h2 { font: 600 15px/1.35 var(--font); }
.card-titles p { margin-top: 2px; font-size: 13px; }
.card-body { display: grid; gap: 8px; margin-top: 14px; }
.empty { padding: 16px 8px; font-size: 13px; color: var(--ink-3); }
.eh { margin-top: 6px; font: 500 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.checks { display: grid; padding: 0; list-style: none; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); }
.checks li { display: grid; grid-template-columns: minmax(0, 1fr) 110px minmax(110px, auto); align-items: center; gap: 10px; min-height: 44px; padding: 6px 12px; font-size: 12.5px; color: var(--ink); }
.checks li + li { border-top: 1px solid var(--line); }
.out { justify-self: start; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font-size: 11.5px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-sunken); }
.out.ok { color: var(--teal-ink); background: var(--chip-teal-bg); }
.out.ask { color: var(--queue-wait-ink); background: var(--queue-wait-bg); }
.out.bad { color: var(--danger); background: var(--danger-bg); }
.lv { justify-self: start; display: inline-flex; align-items: center; gap: 5px; height: 20px; padding: 0 7px; border-radius: 6px; font-size: 11px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.lv::before { content: ''; flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--level-default); }
.lv.hard::before { display: none; }
.lv.hard { color: var(--ink); background: var(--surface-sunken); }
.lv.rec::before { background: transparent; box-shadow: inset 0 0 0 1.6px var(--level-default); }
.foot { display: flex; align-items: flex-start; gap: 8px; margin-top: 6px; font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.foot > svg { flex: none; margin-top: 2px; }
@media (max-width: 600px) { .rules-card { padding: 16px; } .checks li { grid-template-columns: minmax(0, 1fr); gap: 4px; } }
</style>
