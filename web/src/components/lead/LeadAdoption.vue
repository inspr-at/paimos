<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { readLeadCandidates, type LeadCandidate } from '../../lib/lead'
import { vClipTip } from '../../directives/clipTip'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'

const props = defineProps<{ projectId: string; revision: number }>()
const leads = useProjectLeads(), session = useSession()
const open = ref(false), loading = ref(false), writing = ref(false)
const items = ref<LeadCandidate[]>([]), selected = ref(''), cursor = ref<string | null>(null), feedback = ref('')
const owner = computed(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`)
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('harness.control', props.projectId) && can('run.create', props.projectId))
let epoch = 0, confirmedRevision = props.revision
watch(() => [props.projectId, owner.value, allowed.value], () => { epoch++; open.value = false; items.value = []; selected.value = ''; cursor.value = null; feedback.value = ''; loading.value = false; writing.value = false }, { flush: 'sync' })
onBeforeUnmount(() => { epoch++ })
async function load(more = false) {
  if (loading.value || !allowed.value) return
  const started = epoch, project = props.projectId, next = more ? cursor.value : null
  loading.value = true; feedback.value = ''
  try {
    const result = await readLeadCandidates(project, next ?? undefined)
    if (started !== epoch) return
    if (result.items.length > 50) throw new Error('The session list exceeded its limit.')
    items.value = more ? [...items.value, ...result.items] : result.items
    cursor.value = result.next_cursor
  } catch (e) { if (started === epoch) feedback.value = e instanceof Error ? e.message : 'Running sessions could not be read.' }
  finally { if (started === epoch) loading.value = false }
}
function toggle() {
  if (writing.value) return
  open.value = !open.value
  if (open.value) { confirmedRevision = props.revision; selected.value = ''; items.value = []; void load() }
  else { epoch++; loading.value = false }
}
async function confirm() {
  if (!allowed.value || loading.value || writing.value || !selected.value || !items.value.some(item => item.id === selected.value)) return
  if (props.revision !== confirmedRevision) { feedback.value = 'The lead changed. Reopen adoption and confirm again.'; return }
  const started = epoch, project = props.projectId, id = selected.value, revision = confirmedRevision
  writing.value = true; feedback.value = ''
  try {
    const next = await leads.adopt(project, revision, id)
    if (started !== epoch) return
    if (!next) throw new Error('The lead changed. Reopen adoption and confirm again.')
    feedback.value = 'Adoption confirmed. The session must now prove its existing lease with harness lead claim.'
  } catch (e) { if (started === epoch) feedback.value = e instanceof Error ? e.message : 'The session was not adopted.' }
  finally { if (started === epoch) writing.value = false }
}
</script>

<template>
  <div class="adoption">
    <button type="button" class="btn sm ghost" data-act="adopt-open" :aria-expanded="open" :aria-disabled="writing || !allowed" @click="allowed && toggle()"><AppIcon name="agent" :size="15" />Adopt a running session</button>
    <div v-if="open" class="adopt-content">
      <div class="adopt-actions">
        <button type="button" class="btn primary" data-act="adopt-confirm" :aria-disabled="!selected || loading || writing || !allowed" @click="confirm">Confirm adoption</button>
        <button type="button" class="btn" data-act="adopt-cancel" :aria-disabled="writing" @click="toggle">Cancel</button>
      </div>
      <p class="adopt-help">Only your running root coordinators in this project are eligible. The session keeps its context and proves its existing lease after confirmation.</p>
      <div class="adopt-options" role="radiogroup" aria-label="Running coordinator sessions">
        <button v-for="item in items" :key="item.id" type="button" class="adopt-option" role="radio" :aria-checked="selected === item.id" :aria-disabled="writing" @click="!writing && (selected = item.id)">
          <HarnessMark :harness="item.harness" :size="18" />
          <span class="candidate"><strong v-clip-tip>{{ item.display_label || item.harness }}</strong><span v-clip-tip>{{ item.host }} · {{ item.harness }} · {{ item.management_mode }}</span><span v-clip-tip>Fresh heartbeat · {{ new Date(item.reported_at).toLocaleTimeString() }}</span></span>
          <AppIcon :name="selected === item.id ? 'check' : 'v-none'" :size="16" />
        </button>
      </div>
      <p v-if="loading" class="adopt-help" role="status">Reading running sessions…</p>
      <p v-else-if="!items.length && !feedback" class="adopt-help">No eligible running coordinator is reporting for this person and project.</p>
      <p class="adopt-feedback" role="status">{{ feedback }}</p>
      <button v-if="cursor" type="button" class="btn sm" :aria-disabled="loading || items.length >= 200" @click="items.length < 200 && load(true)">More sessions</button>
      <p v-if="cursor" class="adopt-help">More sessions exist{{ items.length >= 200 ? '; reopen to refresh the first page' : '' }}.</p>
    </div>
  </div>
</template>

<style scoped>
.adoption { margin-top: 12px; }
.adopt-content { border-top: 1px solid var(--line); margin-top: 10px; padding-top: 12px; }
.adopt-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.adoption .btn[aria-disabled="true"] { opacity: .55; cursor: default; }
.adopt-help, .adopt-feedback { margin: 10px 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.adopt-feedback { min-height: 2.8em; color: var(--warn-ink); }
.adopt-options { display: grid; max-height: 24rem; overflow: auto; overscroll-behavior: contain; }
.adopt-option { display: grid; grid-template-columns: 22px minmax(0, 1fr) 18px; align-items: center; gap: 10px; height: 84px; width: 100%; border: 0; border-bottom: 1px solid var(--line); padding: 8px 10px; background: transparent; color: var(--ink); text-align: left; cursor: pointer; }
.adopt-option[aria-checked="true"] { background: var(--row-hover); box-shadow: inset 0 0 0 1px var(--line-2); }
.adopt-option:focus-visible { outline: 1px solid var(--teal); outline-offset: -2px; }
.adopt-option:hover { background: var(--row-hover); }
.candidate { display: grid; min-width: 0; font-size: 12px; color: var(--ink-2); }
.candidate strong { color: var(--ink); font-size: 13.5px; }
.candidate > * { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
@media (max-width: 720px) { .adoption > .btn, .adopt-actions .btn { min-height: 44px; } }
</style>
