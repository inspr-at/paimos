<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { createPolicyEditor, policyJSON, policyRequest } from '../../lib/policyEditor'
import { readLadder, writeLadder, type ModelRoute, type EditableLadder } from '../../lib/policyModels'
import { stepName, stepState, truncatedLadder, type PolicyRole, type PolicyStep } from '../../lib/policies'
import { createScope } from '../../lib/identityScope'
import PolicyEditorFrame from './PolicyEditorFrame.vue'
import PolicyEditorActions from './PolicyEditorActions.vue'
const props = defineProps<{ owner: string; role: PolicyRole; person: boolean }>()
const editor = createPolicyEditor<EditableLadder, ModelRoute[]>(() => props.owner ? `${props.owner}/${props.role}` : '', {
  read: signal => readLadder(props.role, signal), write: writeLadder, identity: value => value.role,
  writeKey: before => `${props.owner}/ladder/${before.role}`,
  compensate: before => structuredClone(before.routes),
  saved: (result, undo, wanted) => undo ? wanted.some(row => row.state !== 'available' && result.routes.find(now => now.profile_id === row.profile_id)?.state === 'available') ? 'Order restored; expired holds remain available.' : 'Order restored.' : 'Saved.',
})
const { snapshot, draft, busy, loading, needsReload, message, undo, phase } = editor
const selected = ref(0), profiles = ref<PolicyStep['profile'][]>([]), profileError = ref(''), catalogTruncated = ref(false)
const scope = createScope(() => `${props.owner}/${props.role}`)
const rows = computed(() => phase.value === 'saving' && draft.value ? draft.value : snapshot.value?.routes ?? [])
const editable = computed(() => props.person && !!snapshot.value?.can_edit && !!snapshot.value?.edit_token && !snapshot.value.truncated && snapshot.value.setup)
const route = computed(() => draft.value?.[selected.value])
const opened = ref(false)
function cancel() { if (!busy.value) { editor.cancel(); opened.value = false } }
async function load() {
  opened.value = false; editor.reset(); scope.reset(); selected.value = 0; profiles.value = []; profileError.value = ''; catalogTruncated.value = false
  await editor.load()
}
watch(() => [props.owner, props.role], load, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { editor.dispose(); scope.dispose() })
function edit() {
  if (!editable.value || !snapshot.value || busy.value) return
  opened.value = true
  editor.edit(draft.value ?? snapshot.value.routes)
  if (!profiles.value.length) void scope.run(({ after, signal }) => after(policyRequest('/models', { signal }).then(r => policyJSON(r)), value => {
    if (!Array.isArray(value)) throw new Error('Invalid model catalog')
    catalogTruncated.value = value.length > 256; profiles.value = value.slice(0, 256)
  }), { failed: () => { profileError.value = 'Model choices could not be loaded. Existing steps can still be reordered.' } })
}
function move(position: number) {
  if (!draft.value || busy.value || !route.value || !Number.isInteger(position)) return
  const target = Math.max(0, Math.min(draft.value.length - 1, position - 1))
  const next = [...draft.value], [item] = next.splice(selected.value, 1)
  next.splice(target, 0, item!); selected.value = target
  draft.value = next.map((row, index) => ({ ...row, priority: index + 1 }))
}
function update(field: 'state' | 'reason' | 'valid_until', value: string) {
  if (!draft.value || !route.value || busy.value) return
  const changed = { ...route.value, [field]: value || null }
  if (field === 'state' && value === 'available') { changed.reason = ''; changed.valid_until = null }
  draft.value = draft.value.map((row, index) => index === selected.value ? changed as ModelRoute : row)
}
function add(event: Event) {
  const select = event.target as HTMLSelectElement, id = select.value; select.value = ''
  if (!id || !draft.value || busy.value || draft.value.length >= 50 || draft.value.some(row => row.profile_id === id)) return
  draft.value = [...draft.value, { role: props.role, profile_id: id, priority: draft.value.length + 1, state: 'available', reason: '', valid_until: null }]; selected.value = draft.value.length - 1
}
function remove() {
  if (!draft.value || busy.value) return
  draft.value = draft.value.filter((_, index) => index !== selected.value).map((row,index) => ({ ...row, priority: index + 1 }))
  selected.value = Math.max(0, Math.min(selected.value, draft.value.length - 1))
}
function name(id: string) {
  const profile = profiles.value.find(row => row.id === id) ?? snapshot.value?.steps.find(row => row.profile_id === id)?.profile
  return profile ? stepName({ profile } as PolicyStep) : id
}
const preview = computed(() => (draft.value ?? rows.value).map(row => ({ ...row, profile: profiles.value.find(item => item.id === row.profile_id) ?? snapshot.value?.steps.find(item => item.profile_id === row.profile_id)?.profile })).filter((row): row is PolicyStep => !!row.profile))
function submit() { if (editable.value) void editor.submit() }
</script>
<template>
  <div class="ladder-editor">
    <PolicyEditorFrame :active="opened" title="Saved job order" @save="submit" @cancel="cancel" @undo="editor.submit(true)" @edit="edit">
      <template #actions="{ submitKey }"><PolicyEditorActions :editable="editable" :editing="!!draft" :closable="opened" :busy="busy || loading" :reload-required="needsReload" :undoable="!!undo" :submit-key="submitKey" @edit="edit" @save="submit" @cancel="cancel" @undo="editor.submit(true)" @reload="editor.load" /></template>
      <template #status>{{ message || (loading ? 'Loading the saved order…' : snapshot?.truncated ? 'The complete order is needed before it can be changed.' : !editable ? 'A person with See models and Manage models may change a complete order.' : 'Model registry owns this order. Save changes only the selected job role.') }}<span v-if="draft && !draft.length"> The empty order leaves no configured fallback for this role.</span></template>
      <div v-if="draft" class="ladder-fields">
        <label>Step<select :value="selected" aria-label="Selected ladder step" :disabled="busy" @change="selected = Number(($event.target as HTMLSelectElement).value)"><option v-for="(item,index) in draft" :key="index" :value="index">{{ index + 1 }} · {{ name(item.profile_id) }}</option></select></label>
        <label>Position<input aria-label="Ladder position" type="number" min="1" :max="draft.length || 1" :value="selected + 1" :disabled="busy || !route" @change="move(Number(($event.target as HTMLInputElement).value))" /></label>
        <label>Availability<select aria-label="Ladder availability" :value="route?.state ?? 'available'" :disabled="busy || !route" @change="update('state', ($event.target as HTMLSelectElement).value)"><option value="available">Available</option><option value="unavailable">Unavailable</option><option value="conserved">Conserved</option><option value="budget_limited">Budget limited</option></select></label>
        <label>Add a model<select aria-label="Add ladder model" :disabled="busy || draft.length >= 50" value="" @change="add"><option value="">Choose a profile</option><option v-for="profile in profiles" :key="profile.id" :value="profile.id" :disabled="draft.some(row => row.profile_id === profile.id)">{{ profile.display_name || profile.model }} · {{ profile.harness }} · {{ profile.effort }}</option></select></label>
        <label>Reason<input aria-label="Hold reason" maxlength="500" :value="route?.reason ?? ''" :disabled="busy || !route || route.state === 'available'" @input="update('reason', ($event.target as HTMLInputElement).value)" /></label>
        <label>Expiry (UTC ISO timestamp)<input aria-label="Hold expiry" placeholder="2099-10-04T12:00:00Z" :value="route?.valid_until ?? ''" :disabled="busy || !route || route.state === 'available'" @input="update('valid_until', ($event.target as HTMLInputElement).value)" /></label>
        <button class="btn" type="button" :disabled="busy || !route" @click="remove">Remove selected step</button>
        <p class="wide">New or renewed holds need a reason and a future expiry. An unchanged expired hold can stay in a reorder; Undo keeps expired holds available.</p>
        <p v-if="profileError || catalogTruncated" class="wide">{{ profileError || 'Only the first 256 model choices are shown; existing steps remain intact.' }}</p>
      </div>
      <div v-else class="read-help"><p>CLI follows this priority and harness health. Dispatch qualifies accounts, platform and preferences separately.</p><p v-if="role === 'review-gate'">Command-line order. Managed review currently retains its built-in family fallback; minimum review checks still apply.</p><p>Use a numbered position to move a step. Holds record a reason and expiry; no drag gesture is required.</p></div>
    </PolicyEditorFrame>
    <p v-if="snapshot && !snapshot.setup">The model registry is not set up yet.</p>
    <p v-if="snapshot?.truncated">{{ truncatedLadder() }}</p>
    <p class="list-status">Enforced</p>
    <ol class="order ladder" aria-label="Configured ladder">
      <li v-for="(step,index) in preview" :key="index" class="order-row" data-testid="ladder-slot"><span>{{ String(index + 1).padStart(2,'0') }}</span><div><h4>{{ stepName(step) }}</h4><p>{{ step.profile.family }} · {{ step.profile.harness }} · {{ step.profile.effort }}</p><p>{{ stepState(step, Date.now()) }}</p></div></li>
    </ol>
    <div v-if="role === 'review-gate' && snapshot" class="floors routing-notes"><p>Built-in review family order: {{ snapshot.dispatch_family_order.join(', then ') }}.</p><p v-for="floor in snapshot.review_floors" :key="floor">{{ floor }}</p></div>
  </div>
</template>
<style scoped>
.ladder-fields { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 10px; }
label { display: grid; gap: 5px; font-size: 12px; min-width: 0; color: var(--ink-2); }
input,select { width: 100%; min-width: 0; box-sizing: border-box; height: 36px; background: var(--surface); color: var(--ink); border: 1px solid var(--line-2); border-radius: 3px; padding: 6px; }
.wide { grid-column: 1/-1; }
p { font-size: 12px; color: var(--ink-2); line-height: 1.6; margin: 4px 0 8px; overflow-wrap: anywhere; }
.order { padding: 0; margin: 0; list-style: none; }
.order-row { box-sizing:border-box; height:128px; overflow:auto; display: grid; grid-template-columns: 28px minmax(0,1fr); gap: 12px; padding: 14px 0; border-top: 1px solid var(--line); min-height: 90px; }
.order-row > span { font-size: 12px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
h4 { margin: 0 0 5px; font-size: 14px; overflow-wrap: anywhere; }
.floors { border-top: 1px solid var(--line); padding-top: 14px; }
@media (max-width:600px) { .ladder-fields { grid-template-columns: repeat(2,minmax(0,1fr)); } input,select { min-height:44px; } }
</style>
