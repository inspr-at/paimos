<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { budgetWords, mayWords, waitReasonWords, type Recurrence, type RoutineExecutionSettings } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import RunsAsPicker, { type RunsAsState } from './RunsAsPicker.vue'
import SettingsPlace from './SettingsPlace.vue'

// Step 2 of the same sheet (spec "Save, then consent"): what agents would be allowed,
// as whom, and whether the project lets them. Only the owner decides; everyone else
// sees who does. Nothing here writes; the sheet's primary action does.
const props = defineProps<{ record: Recurrence; mine: boolean; ownerName: string; project: string; settings: RoutineExecutionSettings | null; settingsFailed: boolean; principal: string | null; runsAs: RunsAsState; failure: string; failureCode: string; pausing: boolean }>()
const emit = defineEmits<{ 'update:principal': [id: string | null]; 'update:runsAs': [state: RunsAsState]; pauseAgain: [] }>()
const SWITCH = ['automatic_launch_disabled', 'project_unavailable', 'qualification_unavailable', 'qualification_runtime_changed', 'runtime_unavailable']
const assignment = computed(() => props.record.definition?.assignment)
const personal = computed(() => props.record.definition?.scope.kind === 'personal')
const scopeLabel = computed(() => props.record.definition?.scope.kind === 'workspace' ? 'the workspace' : props.project)
const wait = computed(() => props.record.execution_policy?.wait_reason ?? '')
// The project switch: the settings read when it answered, otherwise the saved policy.
const switchOn = computed(() => props.settings ? props.settings.automatic_launch_enabled : !!props.record.execution_policy && !SWITCH.includes(wait.value))
const switchReason = computed(() => props.settings && !props.settings.automatic_launch_enabled ? props.settings.wait_reason : SWITCH.includes(wait.value) ? wait.value : '')
// A reason consent can't fix, past the project switch (owner, budget mode, capability, …).
const blocker = computed(() => wait.value && wait.value !== 'execution_consent_required' && !SWITCH.includes(wait.value) ? wait.value : '')
const allow = computed(() => `${mayWords(assignment.value?.allowed_actions ?? [])}; ${budgetWords(assignment.value?.budget).toLowerCase()}; on a qualified computer ${props.mine ? 'you' : props.ownerName} allowed`)
const pausedLine = computed(() => props.record.paused ? `It is paused; nothing runs ${props.mine ? 'until you say so' : 'yet'}.` : 'No agent works on it yet.')
</script>

<template>
  <section v-if="!mine" id="consent" class="consent wait" aria-labelledby="consent-heading">
    <p id="consent-heading" class="eyebrow">Let agents run it</p>
    <p class="cs-ok"><AppIcon name="check" :size="13" /><span>Saved as revision {{ record.revision }}. {{ pausedLine }}</span></p>
    <div class="cs-row"><AppIcon name="person-wait" :size="16" /><div><b>{{ ownerName }} decides.</b> {{ personal ? 'Personal routines act with their owner’s rights, so only the owner lets agents run them.' : record.definition?.scope.kind === 'workspace' ? 'Workspace routines belong to a workspace admin, so they let agents run it.' : `Project routines in ${project} belong to a ${project} admin, so they let agents run it.` }} You’ll see it start in its row.</div></div>
    <p class="hint">Until then it can still create its tickets if it is resumed; no agent works on them.</p>
  </section>
  <section v-else-if="blocker" id="consent" class="consent wait" aria-labelledby="consent-heading">
    <p id="consent-heading" class="eyebrow">Let agents run it</p>
    <p class="cs-ok"><AppIcon name="check" :size="13" /><span>Saved as revision {{ record.revision }}. {{ pausedLine }}</span></p>
    <div class="cs-row"><AppIcon name="person-wait" :size="16" /><div><b>{{ blocker === 'owner_policy_mismatch' && personal ? `Not yet possible for your routines in ${project}.` : 'Agents can’t run it yet.' }}</b> {{ waitReasonWords(blocker, { owner: ownerName, project }) }} {{ blocker === 'owner_policy_mismatch' ? 'Yours needs its own check first; until then it can create its tickets and they wait for a person.' : 'It stays saved and paused.' }} <span class="code">{{ blocker }}</span></div></div>
    <p class="hint">{{ blocker === 'owner_policy_mismatch' ? `Ask a ${project} admin or OPS to qualify your routines in ${project}.` : 'Change what the routine needs below, or ask OPS.' }} The button shows here as soon as it can work.</p>
  </section>
  <section v-else id="consent" class="consent" aria-labelledby="consent-heading">
    <p id="consent-heading" class="eyebrow">Let agents run it</p>
    <p class="cs-ok"><AppIcon name="check" :size="13" /><span>Saved as revision {{ record.revision }}. {{ pausedLine }}</span></p>
    <ul class="cs-list">
      <li v-if="switchOn"><AppIcon name="check" :size="13" /><span>Agents may run routines in <b>{{ project }}</b>: on.</span></li>
      <li v-else-if="settingsFailed && !record.execution_policy" class="warn"><AppIcon name="alert" :size="13" /><span>Whether agents may run routines in <b>{{ project }}</b> couldn’t be read.</span></li>
      <li v-else class="warn"><AppIcon name="alert" :size="13" /><span>{{ waitReasonWords(switchReason || 'automatic_launch_disabled', { project }) }} A {{ project }} owner or admin turns it on in {{ project }}’s Routines box. <span class="code">{{ switchReason || 'automatic_launch_disabled' }}</span></span></li>
      <li v-if="personal"><AppIcon name="agent" :size="13" /><span>It runs as <b>you</b>, with exactly your rights.</span></li>
      <li v-else :class="{ warn: runsAs === 'none' || runsAs === 'unavailable' }"><AppIcon name="agent" :size="13" /><span><template v-if="runsAs === 'none'">No restricted agent for {{ scopeLabel }} yet. An admin adds one in <SettingsPlace page="Access" />.</template><template v-else-if="runsAs === 'unavailable'">Runs as: unavailable. The restricted agents for {{ scopeLabel }} can’t be read yet, so agents can’t be let run it.</template><template v-else>It runs as </template><RunsAsPicker :recurrence-id="record.id" :revision="record.revision" :scope-label="scopeLabel" :model-value="principal" @update:model-value="id => emit('update:principal', id)" @state="state => emit('update:runsAs', state)" /><template v-if="runsAs === 'ready' || runsAs === 'loading'">, a restricted agent allowed only this routine’s actions in {{ scopeLabel }}.</template></span></li>
      <li><AppIcon name="shield" :size="13" /><span>You allow: {{ allow }}.</span></li>
    </ul>
    <p v-if="failure" class="warn-line" role="alert"><AppIcon name="alert" :size="13" /><span><b>Agents may not run it:</b> {{ failure }} <span v-if="failureCode" class="code">{{ failureCode }}</span> <template v-if="!record.paused">It is resumed, so its tickets would be created and wait. <button type="button" class="link-btn" :disabled="pausing" @click="emit('pauseAgain')">{{ pausing ? 'Pausing…' : 'Pause again' }}</button></template></span></p>
    <p class="hint">You can stop it any time. Any edit, pause or resume asks you again. Recorded with revision {{ record.revision }}.</p>
  </section>
</template>

<style scoped>
.consent { display: grid; gap: 10px; margin: 12px 0 14px; padding: 16px 18px; border-radius: 14px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.consent.wait { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.eyebrow { margin: 0; }
.cs-ok { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; color: var(--ink); }
.cs-ok svg { flex: none; color: var(--teal-ink); }
.cs-list { display: grid; gap: 8px; padding: 0; list-style: none; }
.cs-list li, .cs-row { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 10px; align-items: start; font-size: 13px; line-height: 1.55; color: var(--ink-2); }
.cs-list li > svg, .cs-row > svg { margin-top: 4px; color: var(--teal-ink); }
.consent.wait .cs-row > svg, .cs-list li.warn > svg { color: var(--queue-wait-ink); }
.cs-list b, .cs-row b { color: var(--ink); font-weight: 600; }
.cs-list li.warn { color: var(--queue-wait-ink); }
.hint { font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.code { font: 500 11px/1 var(--mono); color: var(--ink-3); white-space: nowrap; }
.warn-line { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 10px; font-size: 12.5px; line-height: 1.5; color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.warn-line > svg { flex: none; margin-top: 2px; }
.warn-line .code { color: inherit; opacity: .8; }
.link-btn { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn:disabled { color: var(--ink-3); cursor: default; }
@media (pointer: coarse) { .link-btn { min-height: 44px; } }
</style>
