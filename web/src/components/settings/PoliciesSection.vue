<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Policies, rules only (AEON-1143/1165): one line, then three cards.
// Policies say who may do what; which model runs is decided on Models, so this
// page names no model. Nothing here writes except the cross-family rule.
import { computed, ref } from 'vue'
import { can } from '../../lib/authz'
import { AGENT_ASKS, ASK_RISKS, askPreview, ELSEWHERE_RULES, ruleLink, type AskRisk } from '../../lib/policies'
import AppIcon from '../AppIcon.vue'
import CrossFamilyReviewCard from './CrossFamilyReviewCard.vue'
import SettingsCard from './SettingsCard.vue'

// Settings remounts the section for another person, so open groups never carry over.
const open = ref<AskRisk[]>([])
const isOpen = (risk: AskRisk) => open.value.includes(risk)
function toggle(risk: AskRisk) { open.value = isOpen(risk) ? open.value.filter(item => item !== risk) : [...open.value, risk] }
const desk = computed(() => can('approvals.read'))
const rules = computed(() => ELSEWHERE_RULES.map(rule => ({ rule, to: ruleLink(rule, permission => can(permission)) })))
</script>

<template>
  <div class="policies">
    <p class="intro"><AppIcon name="info" :size="14" /><span>Policies say who may do what. Which model runs is decided on <RouterLink class="quiet" to="/settings/models">Models</RouterLink>.</span></p>
    <CrossFamilyReviewCard />
    <SettingsCard title="What agents must ask you for" icon="key" anchor="agent-asks">
      <template #lead>Agent keys never carry these. When an agent needs one for a single action, it asks, and you approve on the Decision Desk. Each approval covers that one action, once.</template>
      <template v-if="desk" #aside><RouterLink class="btn sm desk" to="/decision-desk">Decision Desk<AppIcon name="chevron-right" :size="13" /></RouterLink></template>
      <div class="risks">
        <div v-for="group in ASK_RISKS" :key="group.risk" class="risk" :class="{ open: isOpen(group.risk) }">
          <button type="button" class="risk-head" :data-risk="group.risk" :aria-expanded="isOpen(group.risk)" :aria-controls="`agent-asks-${group.risk}`" @click="toggle(group.risk)">
            <AppIcon name="chevron-right" :size="13" class="chev" />
            <span v-clip-tip="isOpen(group.risk) ? undefined : `${group.label} · ${AGENT_ASKS[group.risk].length} · ${askPreview(AGENT_ASKS[group.risk])}`" class="risk-text"><b>{{ group.label }}</b> · {{ AGENT_ASKS[group.risk].length }}<span class="preview"> · {{ askPreview(AGENT_ASKS[group.risk]) }}</span></span>
          </button>
          <div :id="`agent-asks-${group.risk}`" class="fold" :inert="!isOpen(group.risk) || undefined">
            <div class="fold-in">
              <ul class="asks">
                <li v-for="item in AGENT_ASKS[group.risk]" :key="item.title" class="ask"><span class="ask-t"><b>{{ item.title }}</b><code>{{ item.keys.join(' · ') }}</code></span><span class="ask-s"><AppIcon name="key" :size="12" />Asks you</span></li>
              </ul>
            </div>
          </div>
        </div>
      </div>
      <p class="foot"><AppIcon name="lock" :size="13" /><span>Also person-only, checked in code: release planning and membership, and changes to locked doctrine rules.</span></p>
    </SettingsCard>
    <SettingsCard title="Rules that live elsewhere" icon="link" anchor="rules-elsewhere">
      <template #lead>Each is set and enforced where it lives. This page only points there.</template>
      <ul class="rules">
        <li v-for="{ rule, to } in rules" :key="rule.title" class="rule">
          <AppIcon :name="rule.icon" :size="15" class="rule-i" />
          <span class="rule-t"><b>{{ rule.title }}</b><span>{{ rule.text }}</span></span>
          <span class="rule-s" :class="rule.status === 'Enforced' ? 'enforced' : 'advisory'"><AppIcon :name="rule.status === 'Enforced' ? 'check' : 'info'" :size="12" />{{ rule.status }}</span>
          <RouterLink v-if="to" class="rule-go quiet" :to="to">{{ rule.owner }}<AppIcon name="chevron-right" :size="12" /></RouterLink>
          <span v-else class="rule-go none">{{ rule.owner }}</span>
        </li>
      </ul>
    </SettingsCard>
  </div>
</template>

<style scoped>
/* One column that never outgrows the page: the folded previews are a single clipped line. */
.policies { display: grid; grid-template-columns: minmax(0, 1fr); gap: 20px; min-width: 0; color: var(--ink); container: policies / inline-size; }
.intro { display: flex; align-items: flex-start; gap: 8px; margin: 0; padding: 2px 4px 0; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.intro > svg { flex: none; margin-top: 3px; color: var(--ink-3); }
/* Inline links keep the sentence's line height but a 24 px target (44 px on touch). */
.quiet { display: inline-flex; align-items: center; min-height: 24px; color: var(--ink-2); font-weight: 500; text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; }
.intro .quiet { margin: -2px 0; }
.quiet:hover { color: var(--teal-ink); text-decoration-color: currentColor; }
.quiet:focus-visible, .risk-head:focus-visible { outline: none; border-radius: 6px; box-shadow: var(--focus-ring); }
.desk { gap: 2px; white-space: nowrap; }

/* What agents must ask you for: three folded risk groups between hairlines. */
.risks { border-top: 1px solid var(--line); }
.risk { border-bottom: 1px solid var(--line); }
.risk-head { display: flex; align-items: center; gap: 6px; width: 100%; min-height: 44px; padding: 0 8px 0 6px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 13px; text-align: left; cursor: pointer; }
.risk-head b { color: var(--ink); font-weight: 600; }
.chev { flex: none; color: var(--ink-3); transition: transform .2s ease; }
.risk.open .chev { transform: rotate(90deg); }
.risk-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview { color: var(--ink-3); }
.risk.open .preview { display: none; }
@media (hover: hover) { .risk-head:hover { background: var(--row-hover); } }
/* The group grows below its head; nothing above it moves. */
.fold { display: grid; grid-template-rows: 0fr; visibility: hidden; transition: grid-template-rows .24s ease, visibility 0s .24s; }
.risk.open .fold { grid-template-rows: 1fr; visibility: visible; transition: grid-template-rows .24s ease, visibility 0s 0s; }
.fold-in { min-height: 0; overflow: hidden; }
.asks { display: grid; margin: 0; padding: 0 0 8px 22px; list-style: none; }
.ask { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 12px; min-height: 48px; padding: 6px 4px; }
.ask + .ask { border-top: 1px solid var(--line); }
.ask-t { display: grid; min-width: 0; }
.ask-t b { font-size: 13px; font-weight: 600; color: var(--ink); }
.ask-t code { font: 500 11px/1.45 var(--mono); color: var(--ink-3); overflow-wrap: anywhere; }
.ask-s { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.foot { display: flex; align-items: flex-start; gap: 8px; margin: 0; padding-top: 12px; font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.foot > svg { flex: none; margin-top: 2px; }

/* Rules that live elsewhere: what, owner, Enforced or Advisory, one link. The
   rows share one set of columns, so status and owner line up down the card. */
.rules { display: grid; grid-template-columns: 22px minmax(0, 1fr) max-content max-content; column-gap: 12px; margin: 0; padding: 0; border-top: 1px solid var(--line); list-style: none; }
.rule { display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; align-items: center; min-height: 56px; padding: 8px 4px; }
.rule + .rule { border-top: 1px solid var(--line); }
.rule-i { justify-self: center; color: var(--ink-3); }
.rule-t { display: grid; min-width: 0; }
.rule-t b { font-size: 13px; font-weight: 600; color: var(--ink); }
.rule-t span { font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.rule-s { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; font-weight: 500; color: var(--ink-2); white-space: nowrap; }
.rule-s.enforced > svg { color: var(--ok); }
.rule-s.advisory { color: var(--ink-3); }
.rule-go { display: inline-flex; align-items: center; justify-self: end; gap: 2px; min-height: 24px; font-size: 12.5px; white-space: nowrap; }
.rule-go.none { color: var(--ink-3); }

@media (prefers-reduced-motion: reduce) { .chev, .fold, .risk.open .fold { transition: none; } }
@container policies (max-width: 600px) {
  .intro > svg { margin-top: 3px; }
  .preview { display: none; }
  .asks { padding-left: 4px; }
  .ask { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  .rules { grid-template-columns: 22px minmax(0, 1fr) auto; }
  .rule { align-items: start; row-gap: 2px; }
  .rule-i { padding-top: 2px; }
  .rule-t { grid-column: 2 / -1; }
  .rule-s { grid-column: 2; }
  .rule-go { grid-column: 3; grid-row: 2; }
}
@media (pointer: coarse) {
  .desk { min-height: 44px; }
  .quiet, .rule-go { min-height: 44px; }
  .intro .quiet { margin: -12px 0; }
}
</style>
