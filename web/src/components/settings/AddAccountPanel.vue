<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import {
  AGENTD_INSTALLS, addHarnessCommand, harnessChoices, isAgentdInstall, signInStep,
  type AddMachine, type AgentdInstall,
} from '../../lib/addAccount'
import { brand } from '../../lib/brand'
import AppIcon from '../AppIcon.vue'

// Two copyable steps on the machine the person picks. The vendor password stays there.
const props = defineProps<{ machines: AddMachine[] }>()

const INSTALL_KEY = 'aeon.addAccountInstall'
const machineId = ref('')
const choiceId = ref('')
const install = ref<AgentdInstall>(readInstall())
const copied = ref('')
const fallback = ref('')
let copyTimer: ReturnType<typeof setTimeout> | undefined

const machine = computed(() => props.machines.find(item => item.id === machineId.value) ?? props.machines[0] ?? null)
const choices = computed(() => machine.value ? harnessChoices(machine.value.harnesses) : [])
const choice = computed(() => choices.value.find(item => item.id === choiceId.value) ?? choices.value[0] ?? null)
const signIn = computed(() => choice.value ? signInStep(choice.value.harness) : null)
const enroll = computed(() => choice.value ? addHarnessCommand(choice.value.harness, install.value) : null)

function readInstall(): AgentdInstall {
  try {
    const stored = sessionStorage.getItem(INSTALL_KEY) ?? ''
    if (isAgentdInstall(stored)) return stored
  } catch { /* Storage may be disabled. */ }
  return 'homebrew'
}
function setInstall(value: string) {
  if (!isAgentdInstall(value)) return
  install.value = value
  try { sessionStorage.setItem(INSTALL_KEY, value) } catch { /* Storage may be disabled. */ }
}
function onMachine(event: Event) { machineId.value = (event.target as HTMLSelectElement).value }
function onChoice(event: Event) { choiceId.value = (event.target as HTMLSelectElement).value }
function onInstall(event: Event) { setInstall((event.target as HTMLSelectElement).value) }

async function copy(kind: 'sign-in' | 'enroll', value: string, event: Event) {
  const field = (event.currentTarget as HTMLElement).closest('.step')?.querySelector('input') ?? null
  try {
    await navigator.clipboard.writeText(value)
    copied.value = kind
    fallback.value = ''
  } catch {
    field?.focus()
    field?.select()
    copied.value = ''
    fallback.value = kind
  }
  clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { if (copied.value === kind) copied.value = '' }, 2000)
}
onBeforeUnmount(() => clearTimeout(copyTimer))
</script>

<template>
  <div id="add-account-panel" class="panel" role="region" aria-label="Add an account">
    <div class="picks">
      <label>Machine
        <select class="field" :value="machine?.id ?? ''" aria-label="Machine" @change="onMachine">
          <option v-for="item in machines" :key="item.id" :value="item.id">{{ item.name }}</option>
        </select>
      </label>
      <label>Harness
        <select class="field" :value="choice?.id ?? ''" aria-label="Harness" :disabled="!choices.length" @change="onChoice">
          <option v-for="item in choices" :key="item.id" :value="item.id">{{ item.label }}</option>
        </select>
      </label>
      <label>Installed with
        <select class="field" :value="install" aria-label="Installed with" @change="onInstall">
          <option v-for="item in AGENTD_INSTALLS" :key="item.id" :value="item.id">{{ item.label }}</option>
        </select>
      </label>
    </div>
    <p class="calm">Sign-in happens on the machine; the password never reaches {{ brand.short_name }}.</p>
    <p v-if="!choices.length" class="calm">Every harness on this machine is already enrolled.</p>
    <ol v-else class="steps">
      <li class="step">
        <input class="field mono" :class="{ sentence: !signIn?.command }" readonly :value="signIn?.text ?? ''" :title="signIn?.text" :aria-label="signIn?.command ? 'Sign-in command' : 'Sign-in step'" @focus="($event.target as HTMLInputElement).select()" />
        <button type="button" class="btn sm" :disabled="!signIn" @click="signIn && copy('sign-in', signIn.text, $event)"><AppIcon :name="copied === 'sign-in' ? 'check' : 'copy'" :size="13" />{{ copied === 'sign-in' ? 'Copied' : 'Copy' }}</button>
      </li>
      <li v-if="enroll" class="step">
        <input class="field mono" readonly :value="enroll" :title="enroll" aria-label="Enroll command" @focus="($event.target as HTMLInputElement).select()" />
        <button type="button" class="btn sm" @click="copy('enroll', enroll, $event)"><AppIcon :name="copied === 'enroll' ? 'check' : 'copy'" :size="13" />{{ copied === 'enroll' ? 'Copied' : 'Copy' }}</button>
      </li>
    </ol>
    <p v-if="fallback" class="calm" role="status">Clipboard unavailable. The step is selected; copy it from the field.</p>
  </div>
</template>

<style scoped>
.panel { display: grid; gap: 12px; margin-bottom: 14px; min-width: 0; }
.picks { display: flex; flex-wrap: wrap; gap: 10px 14px; }
.picks label { display: grid; gap: 4px; flex: 1 1 160px; min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 600; }
.calm { margin: 0; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.steps { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; counter-reset: step; }
.step { display: grid; grid-template-columns: 1.25rem minmax(0, 1fr) auto; gap: 8px; align-items: center; min-width: 0; counter-increment: step; }
.step::before { content: counter(step); color: var(--ink-3); font-size: 12px; font-weight: 650; font-variant-numeric: tabular-nums; text-align: center; }
.step .field { height: 34px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; }
.step .sentence { font-family: var(--font); }
.step .btn { flex: none; }
@media (max-width: 600px) {
  .picks label { flex-basis: 100%; }
}
</style>
