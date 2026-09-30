<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { ACTION_LONG, gateApprovals, isImported, offeredApproval, PROFILES, STAGE_LABEL, type ActionKey, type Profile } from '../../lib/journey'
import { useJourneyContext } from '../../lib/journeyContext'
import { toast } from '../../lib/toast'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import GateApprovals from './GateApprovals.vue'
import GateCard from './GateCard.vue'
import HistoryFold from './HistoryFold.vue'
import ImportedOrigin from './ImportedOrigin.vue'
import LaterCard from './LaterCard.vue'
import ExtensionData from './ExtensionData.vue'

// Shape: the brief Aithema drafted and the person accepted, the profile, and the
// decision: go, reduce scope, park or drop. The decision needs the shape gate.
const ctx = useJourneyContext()
const store = useJourney()
const journey = computed(() => ctx.journey.value)
const state = computed(() => journey.value.stages.find(s => s.key === 'shape')?.state ?? 'later')
const next = computed(() => journey.value.next_action)
const brief = computed(() => ctx.data.intake.value.value.drafts.filter(d => d.kind === 'brief' && d.status === 'accepted').sort((a, b) => Date.parse(b.accepted_at ?? b.proposed_at) - Date.parse(a.accepted_at ?? a.proposed_at))[0] ?? null)
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'shape', journey.value.project_node_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'shape', ctx.now.value))
const deciding = computed(() => next.value.key === 'decide')
const reopening = computed(() => next.value.key === 'reopen')
const reasonFor = ref<'park' | 'drop' | null>(null)
const reason = ref('')
const reasonField = ref<HTMLInputElement>()
async function decide(action: ActionKey) {
  if ((action === 'park' || action === 'drop') && reasonFor.value !== action) { reasonFor.value = action; reason.value = ''; await nextTick(); reasonField.value?.focus(); return }
  if ((action === 'park' || action === 'drop') && !reason.value.trim()) { reasonField.value?.focus(); return }
  const done: Partial<Record<ActionKey, string>> = { go: 'Decided: go. Aithema drafts the requirements next.', reduce_scope: 'Decided: go with a reduced scope.', park: 'Parked, with its reason.', drop: 'Dropped, with its reason.', reopen: 'Reopened. Decide again.' }
  if (await ctx.act(action, { approval: approval.value, reason: reason.value.trim() || undefined, done: done[action] })) reasonFor.value = null
}
const profileSaving = ref(false)
async function setProfile(value: Profile) {
  profileSaving.value = true
  try { await store.profile(ctx.project.value.id, value); toast(`Profile: ${PROFILES[value].label}`) } catch (e) { toast(e instanceof Error ? e.message : 'The profile was not saved.', { tone: 'error' }) } finally { profileSaving.value = false }
}
const noGate = computed(() => !approval.value)
// Past Shape, the stage is history: one line, folded.
const past = computed(() => ['done', 'skipped'].includes(state.value) && journey.value.stage !== 'shape')
const summary = computed(() => state.value === 'skipped' ? 'Not needed on Personal: the conversation went straight to requirements.'
  : `Decided${brief.value ? `: ${brief.value.title}` : ''}. The decision is recorded.`)
// An imported project was decided before it came here: its description is the brief of record.
const imported = computed(() => isImported(journey.value) && past.value && !brief.value)
</script>

<template>
  <div v-if="imported" class="j-grid">
    <div class="j-col">
      <ImportedOrigin eyebrow="Brief · the project’s description" mode="brief" />
      <section class="j-card" aria-labelledby="shape-profile">
        <label class="profile"><span id="shape-profile" class="eyebrow">Profile</span>
          <select class="field" :value="journey.profile" :disabled="!ctx.canAct.value || profileSaving" @change="setProfile(($event.target as HTMLSelectElement).value as Profile)">
            <option v-for="(p, key) in PROFILES" :key="key" :value="key">{{ p.label }} · {{ p.line }}</option>
          </select>
        </label>
        <p class="j-note">The profile sets the gates ahead: budget and scope on Professional, an independent reviewer on Enterprise.</p>
      </section>
    </div>
    <div class="j-col">
      <GateCard eyebrow="Decided" title="Go, before it came here" tone="record">
        <p>The decision to build this project was made in Paimos, so no gate was recorded here. A parked or dropped project would show its reason on this card.</p>
        <button type="button" class="btn sm go" @click="ctx.view(journey.stage)">Go to {{ STAGE_LABEL[journey.stage] }}<AppIcon name="arrow" :size="13" /></button>
      </GateCard>
    </div>
  </div>
  <component :is="past ? HistoryFold : 'div'" v-else v-bind="past ? { title: 'The brief and the decision', summary, label: 'Show the brief' } : {}">
  <div v-if="state === 'skipped'" class="j-grid one">
    <section class="j-card">
      <h3>Not needed on Personal</h3>
      <p class="j-note">Own time, own machine. The conversation goes straight to requirements.</p>
      <label class="profile"><span class="eyebrow">Profile</span>
        <select class="field" :value="journey.profile" :disabled="!ctx.canAct.value || profileSaving" @change="setProfile(($event.target as HTMLSelectElement).value as Profile)">
          <option v-for="(p, key) in PROFILES" :key="key" :value="key">{{ p.label }} · {{ p.line }}</option>
        </select>
      </label>
    </section>
  </div>
  <div v-else class="j-grid">
    <div class="j-col">
      <section class="j-card" aria-labelledby="brief-title">
        <header class="j-card-head"><p id="brief-title" class="eyebrow">Brief · drafted by Aithema</p></header>
        <template v-if="brief">
          <h3>{{ brief.title }}</h3>
          <MarkdownBody :body="brief.body" />
          <ExtensionData :extensions="brief.extensions" />
        </template>
        <div v-else class="j-empty"><strong>No brief yet</strong><span>Aithema drafts the brief from the conversation and sources; accept it on Inspire.</span>
          <button type="button" class="btn sm" @click="ctx.view('inspire')"><AppIcon name="arrow" :size="13" />Inspire</button>
        </div>
        <label class="profile"><span class="eyebrow">Profile</span>
          <select class="field" :value="journey.profile" :disabled="!ctx.canAct.value || profileSaving" @change="setProfile(($event.target as HTMLSelectElement).value as Profile)">
            <option v-for="(p, key) in PROFILES" :key="key" :value="key">{{ p.label }} · {{ p.line }}</option>
          </select>
        </label>
      </section>
    </div>
    <div class="j-col">
      <GateCard v-if="deciding" eyebrow="Decision" title="Go, reduce scope, park or drop">
        <p>{{ ACTION_LONG.decide }}</p>
        <GateApprovals gate="shape" :approvals="approvals" on="the project" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="noGate" class="j-note">An agent asks for the shape gate; it appears here for you to approve. Until then the decision stays open.</p>
        <div class="decide">
          <button type="button" class="btn primary decide-btn" :disabled="noGate || store.busy || !ctx.canAct.value" @click="decide('go')">Go<span class="sub">to requirements</span><AppIcon name="arrow" :size="14" /></button>
          <button type="button" class="btn decide-btn" :disabled="noGate || store.busy || !ctx.canAct.value" @click="decide('reduce_scope')">Reduce scope<span class="sub">go with less</span></button>
          <button type="button" class="btn decide-btn" :disabled="noGate || store.busy || !ctx.canAct.value" :aria-expanded="reasonFor === 'park'" @click="decide('park')">Park<span class="sub">resume later</span></button>
          <button type="button" class="btn decide-btn" :disabled="noGate || store.busy || !ctx.canAct.value" :aria-expanded="reasonFor === 'drop'" @click="decide('drop')">Drop<span class="sub">keep the reason</span></button>
        </div>
        <form v-if="reasonFor" class="reason" @submit.prevent="decide(reasonFor)">
          <label for="shape-reason">{{ reasonFor === 'park' ? 'Why park it?' : 'Why drop it?' }}</label>
          <input id="shape-reason" ref="reasonField" v-model="reason" class="field" placeholder="Required" maxlength="2048" @keydown.esc.stop="reasonFor = null" />
          <div class="reason-actions"><button type="button" class="btn sm ghost" @click="reasonFor = null">Cancel</button><button type="submit" class="btn sm primary" :disabled="!reason.trim()">Record</button></div>
        </form>
      </GateCard>
      <GateCard v-else-if="reopening" eyebrow="Parked or dropped" title="Reopen to decide again" :action="{ label: 'Reopen', disabled: noGate || !ctx.canAct.value, busy: store.busy, tip: noGate ? 'Reopening needs the shape gate' : undefined }" @act="decide('reopen')">
        <p>{{ ACTION_LONG.reopen }}</p>
        <GateApprovals gate="shape" :approvals="approvals" on="the project" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
      </GateCard>
      <GateCard v-else-if="state === 'done'" eyebrow="Decided" title="Go" tone="record">
        <p>The decision is recorded. <button type="button" class="linkish" @click="ctx.view('requirements')">Requirements <AppIcon name="arrow" :size="12" /></button></p>
        <GateApprovals v-if="approvals.length" gate="shape" :approvals="approvals" on="the project" :can-decide="false" :now="ctx.now.value" :me="ctx.me.value" />
      </GateCard>
      <LaterCard v-else stage="shape" :detail="journey.stage === 'inspire' ? 'Once the brief is confirmed, the decision is made here.' : ''" />
    </div>
  </div>
  </component>
</template>

<style scoped>
.profile { display: grid; gap: 6px; margin-top: 6px; }
.go { justify-self: start; }
.profile .field { height: 36px; }
.decide { display: grid; gap: 6px; }
.decide-btn { justify-content: flex-start; gap: 8px; width: 100%; height: 40px; }
.decide-btn .sub { margin-left: auto; font-size: 12px; font-weight: 500; opacity: .75; }
/* Keep helper text aligned with Go's helper, which has a trailing arrow. */
.decide-btn:not(:has(svg))::after { content: ''; flex: 0 0 14px; }
.reason { display: grid; gap: 6px; }
.reason label { font-size: 12.5px; color: var(--ink-2); }
.reason-actions { display: flex; justify-content: flex-end; gap: 6px; }
.linkish { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
</style>
