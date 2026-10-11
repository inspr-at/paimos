<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive } from 'vue'
import { ROUTINE_ACTIONS, ROUTINE_NEVER, actionOutcome, budgetWords, budgetFrom, mayWords, ruleLevel, ruleOutcome, ruleWords, type RoutineAction, type RoutineDraft, type RoutineExecutionPolicy, type RoutineGuardEntry } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'

// The three folds under "Who does it" (spec "Folds"): one row each with a one-line
// summary, growing in place below the row. Values that change keep fixed slots
// (the budget fields, the budget note and the route line), so nothing above moves.
const draft = defineModel<RoutineDraft>({ required: true })
const props = defineProps<{ policy: RoutineExecutionPolicy | null; rules: RoutineGuardEntry[] | null; rulesState: 'unsaved' | 'loading' | 'ready' | 'failed'; owner: string; mine: boolean; project: string; fresh: boolean; idPrefix: string }>()
const wait = computed(() => props.policy?.wait_reason ?? '')
const budgetUnqualified = computed(() => wait.value === 'budget_mode_unqualified')
const browserUnqualified = computed(() => wait.value === 'capability_unqualified')
// Capability is known once the policy got past it (its checks run in order).
const capabilityKnown = computed(() => ['budget_mode_unqualified', 'execution_consent_required', ''].includes(wait.value) && !!props.policy)
const open = reactive({ rules: props.fresh, budget: props.fresh || budgetUnqualified.value, needs: props.fresh || browserUnqualified.value })
const budget = computed(() => budgetFrom(draft.value.mode, draft.value.tokens, draft.value.dollars))
const budgetSummary = computed(() => budgetUnqualified.value ? 'This limit can’t be enforced yet' : budget.value ? budgetWords(budget.value) : 'Enter the limit')
const shownRules = computed(() => (props.rules ?? []).filter(entry => !entry.superseded))
const groups = [{ label: 'Usually safe', reach: false }, { label: 'Reaches code or people', reach: true }]
function toggle(action: RoutineAction, on: boolean) { draft.value.actions = on ? [...new Set([...draft.value.actions, action])] : draft.value.actions.filter(item => item !== action) }
const id = (name: string) => `${props.idPrefix}-${name}`
</script>

<template>
  <div class="folds">
    <button :id="id('f-rules')" type="button" class="fold-row" :aria-expanded="open.rules" :aria-controls="id('fb-rules')" @click="open.rules = !open.rules">
      <AppIcon name="chevron-right" :size="13" /><span class="fr-ic"><AppIcon name="shield" :size="15" /></span><span class="fr-t">What it may do</span><span class="fr-s">{{ mayWords(draft.actions) }}</span>
    </button>
    <div :id="id('fb-rules')" class="fold" :class="{ open: open.rules }" :inert="!open.rules"><div class="fold-in"><div class="fb">
      <p class="fb-lead">In its own copy of the code it may read, change and run tests freely. Everything that leaves that copy is one of these actions, and each is checked first.</p>
      <template v-for="group in groups" :key="group.label">
        <p class="eh">{{ group.label }}</p>
        <div class="acts">
          <label v-for="item in ROUTINE_ACTIONS.filter(entry => entry.reach === group.reach)" :key="item.action" class="act">
            <span class="switch"><input type="checkbox" role="switch" :data-act="item.action" :checked="draft.actions.includes(item.action)" :aria-describedby="id(`ad-${item.action}`)" @change="toggle(item.action, ($event.target as HTMLInputElement).checked)" /></span>
            <span class="act-t"><b>{{ item.title }}</b><small :id="id(`ad-${item.action}`)">{{ item.detail }}</small></span>
            <span class="out" :class="actionOutcome(item.action, draft.actions.includes(item.action))[0]">{{ actionOutcome(item.action, draft.actions.includes(item.action))[1] }}</span>
          </label>
        </div>
      </template>
      <p class="eh">Never, for any routine</p>
      <div class="never"><span v-for="line in ROUTINE_NEVER" :key="line"><AppIcon name="lock" :size="12" />{{ line }}</span><span class="lv hard">Hard floor · can’t be changed</span></div>
      <p class="eh">Checked before each action <span class="count">all apply · the strictest wins</span></p>
      <ul v-if="rulesState === 'ready' && shownRules.length" class="checks">
        <li v-for="entry in shownRules" :key="`${entry.scope.kind}/${entry.rule.id}`"><span class="ck-t" :title="ruleWords(entry.rule).full">{{ ruleWords(entry.rule).text }}</span><span class="out" :class="ruleOutcome(entry.rule)[0]">{{ ruleOutcome(entry.rule)[1] }}</span><span class="lv" :class="ruleLevel(entry, { project, mine })[0]"><AppIcon v-if="entry.level === 'hard'" name="lock" :size="11" />{{ ruleLevel(entry, { project, mine })[1] }}</span></li>
      </ul>
      <p v-else-if="rulesState === 'loading'" class="hint" role="status">Reading the rules that apply…</p>
      <p v-else-if="rulesState === 'failed'" class="hint" role="alert">The rules that apply couldn’t be read. They still apply.</p>
      <p v-else class="hint">Listed here after the first save: the hard floor, the recommended rules, and the workspace, project and personal rules that apply to it.</p>
      <p class="fb-foot"><AppIcon name="info" :size="13" /><span>“Asks a person” depends on what an action contains, so each action is checked when it happens. Projects and people can only add or tighten rules; loosening a recommended rule needs an admin and a reason. A routine can’t change any of them.</span></p>
    </div></div></div>

    <button :id="id('f-budget')" type="button" class="fold-row" :class="{ warn: budgetUnqualified }" :aria-expanded="open.budget" :aria-controls="id('fb-budget')" @click="open.budget = !open.budget">
      <AppIcon name="chevron-right" :size="13" /><span class="fr-ic"><AppIcon name="gauge" :size="15" /></span><span class="fr-t">Budget</span><span class="fr-s"><AppIcon v-if="budgetUnqualified" name="alert" :size="12" />{{ budgetSummary }}</span>
    </button>
    <div :id="id('fb-budget')" class="fold" :class="{ open: open.budget }" :inert="!open.budget"><div class="fold-in"><div class="fb">
      <div class="fr"><span class="fl">Per run</span><div class="seg bud" role="radiogroup" aria-label="Limit per run"><button v-for="[mode, label] in ([['off', 'No limit'], ['tokens', 'Tokens'], ['money', 'Money'], ['both', 'Both']] as const)" :key="mode" type="button" role="radio" :data-bud="mode" :aria-checked="draft.mode === mode" @click="draft.mode = mode">{{ label }}</button></div></div>
      <div class="fr"><span class="fl">Stops at</span><div class="bud-slot">
        <div class="fline" :aria-hidden="draft.mode === 'off'" :inert="draft.mode === 'off'">
          <span class="bud-f" :aria-hidden="draft.mode === 'money'" :inert="draft.mode === 'money'"><input v-model="draft.tokens" class="field n" inputmode="decimal" aria-label="Tokens per run, in thousands" /><span>k tokens</span></span>
          <span class="bud-f" :aria-hidden="draft.mode === 'tokens'" :inert="draft.mode === 'tokens'"><span>$</span><input v-model="draft.dollars" class="field n" inputmode="decimal" aria-label="Paid US dollars per run" /></span>
          <span class="hint" :aria-hidden="draft.mode !== 'both'">whichever comes first</span>
        </div>
        <p class="hint" :aria-hidden="draft.mode !== 'off'">No token or money limit. Daily caps, time limits and what is kept for people still apply.</p>
      </div></div>
      <p class="hint">Counts every agent in the run: the builder, the checks, review and fixes. Money means what a paid API actually charges; subscriptions count as $0.</p>
      <div class="note-slot">
        <p class="warn-line" role="note" :aria-hidden="!budgetUnqualified"><AppIcon name="alert" :size="13" /><span><b>This limit can’t be enforced on this route yet,</b> so a run with it won’t start. Choose another limit, or wait until OPS qualifies it for {{ project }}. <span class="code">budget_mode_unqualified</span></span></p>
        <p class="room" :aria-hidden="budgetUnqualified"><AppIcon name="pulse" :size="13" /><span><b>Room:</b> {{ fresh ? 'no runs yet, so no estimate.' : 'how much a run uses and how many fit isn’t measured here yet.' }} A run starts only when an account has room; see <a href="/settings/capacity" target="_blank" rel="noopener" class="link-btn quiet">Settings › Capacity</a>.</span></p>
      </div>
    </div></div></div>

    <button :id="id('f-needs')" type="button" class="fold-row" :class="{ warn: browserUnqualified }" :aria-expanded="open.needs" :aria-controls="id('fb-needs')" @click="open.needs = !open.needs">
      <AppIcon name="chevron-right" :size="13" /><span class="fr-ic"><AppIcon name="monitor" :size="15" /></span><span class="fr-t">Where it runs</span><span class="fr-s"><AppIcon v-if="browserUnqualified" name="alert" :size="12" />{{ draft.browser ? 'Browser' : 'Coding' }} · {{ browserUnqualified ? 'not qualified yet' : `a computer ${mine ? 'of yours' : `${owner} allowed`}` }}</span>
    </button>
    <div :id="id('fb-needs')" class="fold" :class="{ open: open.needs }" :inert="!open.needs"><div class="fold-in"><div class="fb">
      <p class="fb-lead">It runs on a computer {{ mine ? 'you' : owner }} allowed in Settings › Computers, in its own copy of the repository.</p>
      <label class="act single"><span class="switch"><input :id="id('needs-browser')" v-model="draft.browser" type="checkbox" role="switch" /></span><span class="act-t"><b>Needs a browser</b><small>For pages it must open, like a link check.</small></span><span class="out" :class="draft.browser ? 'ok' : 'off'">{{ draft.browser ? 'On' : 'Off' }}</span></label>
      <div class="route-slot">
        <p v-if="browserUnqualified" class="warn-line" role="note"><AppIcon name="alert" :size="13" /><span><b>Not qualified yet:</b> no computer has passed the check for this kind of work, so runs wait and say why. It stays saved. <span class="code">capability_unqualified</span></span></p>
        <p v-else-if="capabilityKnown" class="ok-line"><AppIcon name="check" :size="13" /><span><b>Ready:</b> a qualified computer in {{ project }} can take {{ draft.browser ? 'browser' : 'coding' }} work.</span></p>
        <p v-else class="calm-line"><AppIcon name="info" :size="13" /><span>Which computer qualifies is checked once agents may run routines in {{ project }}.</span></p>
      </div>
      <p class="hint">Which computers: <a href="/settings/computers" target="_blank" rel="noopener" class="link-btn quiet">Settings › Computers</a>. A routine never falls back to a computer that isn’t qualified.</p>
    </div></div></div>
  </div>
</template>

<style scoped>
.folds { display: grid; margin-top: 6px; border: 1px solid var(--line); border-radius: 14px; background: var(--surface-raised-2); overflow: hidden; }
.fold-row { display: grid; grid-template-columns: 14px 28px auto minmax(0, 1fr); align-items: center; gap: 10px; width: 100%; min-height: 52px; padding: 8px 14px; border: 0; background: transparent; color: var(--ink); text-align: left; }
.fold-row + .fold, .fold + .fold-row { border-top: 1px solid var(--line); }
@media (hover: hover) { .fold-row:hover { background: var(--row-hover); } }
.fold-row[aria-expanded="true"] { background: var(--row-selected); }
.fold-row:focus-visible { box-shadow: inset var(--focus-ring); }
.fold-row > svg:first-child { color: var(--ink-3); transition: transform .2s ease; }
.fold-row[aria-expanded="true"] > svg:first-child { transform: rotate(90deg); }
.fr-ic { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
.fr-t { font-size: 13.5px; font-weight: 650; white-space: nowrap; }
.fr-s { display: flex; align-items: center; justify-content: flex-end; gap: 6px; min-width: 0; font-size: 12.5px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fold-row.warn .fr-s { color: var(--warn-ink); font-weight: 600; }
.fold { display: grid; grid-template-rows: 0fr; transition: grid-template-rows .24s ease; }
.fold.open { grid-template-rows: 1fr; }
.fold-in { min-height: 0; overflow: hidden; }
.fold:not(.open) .fold-in { visibility: hidden; }
@media (prefers-reduced-motion: reduce) { .fold, .fold-row > svg:first-child { transition: none; } }
.fb { display: grid; gap: 10px; padding: 14px 16px 16px; border-top: 1px solid var(--line); }
.fb-lead { font-size: 13px; line-height: 1.55; color: var(--ink-2); }
.eh { margin-top: 4px; font: 500 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.eh .count { margin-left: 6px; font: 500 11.5px/1 var(--font); letter-spacing: 0; text-transform: none; }
.acts { display: grid; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); }
.act { display: grid; grid-template-columns: 44px minmax(0, 1fr) auto; align-items: center; gap: 8px; min-height: 52px; padding: 2px 12px 2px 6px; cursor: pointer; }
.acts .act + .act { border-top: 1px solid var(--line); }
.act.single { border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); }
.act .switch { justify-content: center; min-width: 44px; min-height: 44px; }
.act-t { display: grid; min-width: 0; }
.act-t b { font-size: 13px; font-weight: 600; }
.act-t small { font-size: 12px; color: var(--ink-3); }
.out { justify-self: end; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font-size: 11.5px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-sunken); }
.out.ok { color: var(--teal-ink); background: var(--chip-teal-bg); }
.out.ask { color: var(--queue-wait-ink); background: var(--queue-wait-bg); }
.out.bad { color: var(--danger); background: var(--danger-bg); }
.out.off { color: var(--ink-3); }
.never { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); font-size: 12.5px; color: var(--ink-2); }
.never > span:not(.lv) { display: inline-flex; align-items: center; gap: 6px; }
.never svg { color: var(--ink-3); }
.never .lv { margin-left: auto; }
.lv { display: inline-flex; align-items: center; gap: 5px; height: 20px; padding: 0 7px; border-radius: 6px; font-size: 11px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.lv::before { content: ''; flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--level-default); }
.lv.hard::before { display: none; }
.lv.hard { color: var(--ink); background: var(--surface-sunken); }
.lv.rec::before { background: transparent; box-shadow: inset 0 0 0 1.6px var(--level-default); }
.lv.proj::before { background: var(--level-project); }
.lv.you::before { background: var(--level-person); }
.checks { display: grid; padding: 0; list-style: none; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); }
.checks li { display: grid; grid-template-columns: minmax(0, 1fr) 110px minmax(120px, auto); align-items: center; gap: 10px; min-height: 44px; padding: 6px 12px; font-size: 12.5px; color: var(--ink); }
.checks li + li { border-top: 1px solid var(--line); }
.checks .out, .checks .lv { justify-self: start; }
.fb-foot { display: flex; align-items: flex-start; gap: 8px; font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.fb-foot > svg { flex: none; margin-top: 2px; }
.fr { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 34px; }
.fl { font-size: 12.5px; color: var(--ink-3); }
.fr > .seg { justify-self: start; }
.fline { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-width: 0; }
.bud-slot { display: grid; min-height: 34px; align-items: center; }
.bud-slot > * { grid-area: 1 / 1; }
.bud-slot [aria-hidden="true"], .bud-f[aria-hidden="true"] { visibility: hidden; }
.bud-f { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--ink-2); }
.bud-f .field.n { width: 9ch; height: 34px; }
.hint { font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.hint[aria-hidden="true"] { visibility: hidden; }
.note-slot { display: grid; }
.note-slot > * { grid-area: 1 / 1; }
.note-slot > [aria-hidden="true"] { visibility: hidden; }
.route-slot { min-height: 62px; display: grid; align-content: start; }
.room, .warn-line, .ok-line, .calm-line { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 10px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); background: var(--surface-sunken); }
.room > svg, .warn-line > svg, .ok-line > svg, .calm-line > svg { flex: none; margin-top: 2px; }
.room b, .ok-line b { color: var(--ink); font-weight: 600; }
.ok-line > svg { color: var(--teal-ink); }
.warn-line { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.warn-line b { font-weight: 650; }
.code { font: 500 11px/1 var(--mono); color: inherit; opacity: .8; white-space: nowrap; }
.link-btn.quiet { color: var(--teal-ink); font-weight: 600; text-decoration: none; }
.link-btn.quiet:hover { text-decoration: underline; text-underline-offset: 2px; }
@media (max-width: 600px) {
  .fold-row { grid-template-columns: 14px 28px minmax(0, 1fr); }
  .fr-s { grid-column: 3; justify-content: flex-start; white-space: normal; }
  .act { grid-template-columns: 44px minmax(0, 1fr); }
  .act .out { grid-column: 2; justify-self: start; }
  .checks li { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  .checks .lv { white-space: normal; height: auto; min-height: 20px; }
  .never .lv { margin-left: 0; }
  .fr { grid-template-columns: minmax(0, 1fr); gap: 6px; }
  .seg.bud { flex-wrap: wrap; border-radius: 14px; }
  .bud-f .field.n { width: 8ch; }
}
@media (pointer: coarse) {
  .seg.bud button { min-width: 44px; min-height: 44px; }
  .bud-f .field.n { height: 44px; }
}
</style>
