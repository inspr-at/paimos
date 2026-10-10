<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import RuleItem from './RuleItem.vue'
import RulesDialog from './RulesDialog.vue'
import { ensurePermissions } from '../../lib/authz'
import {
  IMPORT_MAX_BYTES, draftImportProjects, importWrites, parseDraftImport, runDraftImport, scopeKey, utf8Length, validateDraft,
  type Caller, type DraftImportPlan, type DraftImportSet, type ExistingSet, type ImportReport, type RuleScope,
} from '../../lib/rules'

// Import in three calm steps: choose a file, read a compact review (per set:
// new, changed or unchanged), import as drafts. Permissions for every scope in
// the file are loaded before the review, so "checking" never reads as "denied".
const props = defineProps<{
  tenantId: string
  tenantName: string
  caller: Caller | null
  existing: { scope: RuleScope; sets: ExistingSet[] }[]
  scopeTitle: (scope: RuleScope) => string
  hold: string
}>()
const emit = defineEmits<{ close: []; imported: [report: ImportReport] }>()

type Phase = 'choose' | 'checking' | 'review' | 'importing' | 'result'
const phase = ref<Phase>('choose')
const fileName = ref('')
const fileSize = ref(0)
const error = ref('')
const unavailable = ref(false)
const plan = ref<DraftImportPlan | null>(null)
const report = ref<ImportReport | null>(null)
const dragging = ref(false)
let lastFile: File | null = null

const sets = computed(() => plan.value?.layers.reduce((n, layer) => n + layer.sets.length, 0) ?? 0)
const rules = computed(() => plan.value?.layers.reduce((n, layer) => n + layer.sets.reduce((m, set) => m + set.rules.length, 0), 0) ?? 0)
const writes = computed(() => plan.value ? importWrites(plan.value) : 0)
const skipped = computed(() => sets.value - writes.value)
const duplicateWording = (set: DraftImportSet) => validateDraft(set.name, set.rules).warning ?? ''
const size = computed(() => fileSize.value < 1024 ? `${fileSize.value} bytes` : `${(fileSize.value / 1024).toFixed(fileSize.value < 10240 ? 1 : 0)} KB`)
const confirmLabel = computed(() => {
  if (phase.value === 'importing') return 'Importing…'
  if (!plan.value) return 'Import as drafts'
  if (!writes.value) return 'Nothing new to import'
  return `Import ${writes.value} ${writes.value === 1 ? 'set' : 'sets'} as drafts`
})

function counts(set: DraftImportSet): string {
  const c = set.counts
  const n = set.rules.length
  if (set.action === 'skip') return 'Unchanged, skipped'
  if (set.action !== 'update' || !c) return `New · ${n} ${n === 1 ? 'rule' : 'rules'}`
  const parts: string[] = []
  if (c.added) parts.push(`${c.added} new`)
  if (c.changed) parts.push(`${c.changed} changed`)
  if (c.removed) parts.push(`${c.removed} removed`)
  if (c.unchanged) parts.push(`${c.unchanged} unchanged`)
  return parts.join(' · ')
}

async function take(file: File | undefined) {
  if (!file || phase.value === 'importing') return
  lastFile = file
  plan.value = null
  report.value = null
  error.value = ''
  unavailable.value = false
  fileName.value = file.name
  fileSize.value = file.size
  if (file.size > IMPORT_MAX_BYTES) { error.value = 'The file must be 2 MB or smaller.'; phase.value = 'choose'; return }
  let text: string
  try { text = await file.text() } catch { error.value = 'The file could not be read.'; phase.value = 'choose'; return }
  if (lastFile !== file) return
  phase.value = 'checking'
  // Wait for the server's answer for the workspace and every project the file
  // names. A missing answer is "could not check", never a denial.
  const answers = await Promise.all([ensurePermissions(), ...draftImportProjects(text).map(project => ensurePermissions(project))])
  if (lastFile !== file) return
  if (answers.includes('unavailable')) {
    unavailable.value = true
    error.value = 'Your permissions could not be checked just now. Nothing was imported.'
    phase.value = 'choose'
    return
  }
  const parsed = parseDraftImport(text, Math.max(file.size, utf8Length(text)), props.tenantId, props.caller, props.existing)
  if ('error' in parsed) { error.value = parsed.error; phase.value = 'choose'; return }
  plan.value = parsed.plan
  phase.value = 'review'
}
function picked(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  void take(file)
}
function dropped(event: DragEvent) {
  dragging.value = false
  void take(event.dataTransfer?.files?.[0])
}
async function confirm() {
  if (!plan.value || !writes.value || phase.value !== 'review') return
  if (props.hold) { error.value = props.hold; return }
  phase.value = 'importing'
  error.value = ''
  const outcome = await runDraftImport(plan.value, props.tenantId, props.caller)
  report.value = outcome
  if (outcome.status === 'imported') { emit('imported', outcome); return }
  if (outcome.status === 'rejected') { error.value = outcome.message; phase.value = 'review'; return }
  phase.value = 'result'
  emit('imported', outcome)
}

const saved = computed(() => report.value?.results.filter(result => result.status === 'saved').length ?? 0)
const resultLine = computed(() => {
  const outcome = report.value
  if (!outcome) return ''
  const total = outcome.results.filter(result => result.action !== 'skip').length
  if (outcome.status === 'uncertain') return `${saved.value} of ${total} sets were confirmed as drafts. The server did not answer for one set, so the import stopped there. Nothing was published or rolled back.`
  return `${saved.value} of ${total} sets were saved as drafts. The import stopped at the set that failed. Nothing was published or rolled back.`
})
const STATUS = { saved: 'Saved as draft', unchanged: 'Unchanged', failed: 'Not saved', unknown: 'Not confirmed: check before importing again', 'not-started': 'Not started' } as const
const byScope = computed(() => {
  const groups = new Map<string, { scope: RuleScope; results: ImportReport['results'] }>()
  for (const result of report.value?.results ?? []) {
    const key = scopeKey(result.scope)
    if (!groups.has(key)) groups.set(key, { scope: result.scope, results: [] })
    groups.get(key)!.results.push(result)
  }
  return [...groups.values()]
})
</script>

<template>
  <RulesDialog title="Import rules" lede="A rules file (JSON) holds sets of rules prepared from your doctrine. Import saves them as drafts; nothing reaches agents until you publish." size="wide" :busy="phase === 'importing'" @close="emit('close')">
    <template v-if="phase !== 'result'">
      <label
        class="drop" :class="{ dragging, chosen: !!fileName }" @dragover.prevent="dragging = true" @dragleave="dragging = false" @drop.prevent="dropped"
      >
        <input id="draft-import-file" class="file-input" type="file" accept="application/json,.json" :disabled="phase === 'importing'" data-autofocus @change="picked">
        <span class="drop-icon" aria-hidden="true"><BizIcon :name="fileName ? 'document' : 'upload'" :size="18" /></span>
        <span v-if="!fileName" class="drop-text"><strong>Choose a rules file</strong><span>or drop it here · JSON, up to 2 MB</span></span>
        <span v-else class="drop-text"><strong class="file-name" :title="fileName">{{ fileName }}</strong><span>{{ size }} · choose another file</span></span>
      </label>

      <p v-if="phase === 'checking'" class="checking" role="status"><svg class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="5.8" class="track" /><path d="M8 2.2a5.8 5.8 0 0 1 5.8 5.8" class="arc" /></svg>Checking permissions…</p>
      <p v-if="error" class="error" role="alert"><BizIcon name="alert" :size="14" /><span>{{ error }}</span><button v-if="unavailable && lastFile" type="button" class="btn sm" @click="take(lastFile ?? undefined)">Try again</button></p>

      <div v-if="plan && phase !== 'checking'" class="review">
        <p class="summary"><strong>{{ sets }} {{ sets === 1 ? 'set' : 'sets' }} · {{ rules }} {{ rules === 1 ? 'rule' : 'rules' }}</strong> for {{ tenantName }}<template v-if="skipped"> · {{ skipped }} unchanged {{ skipped === 1 ? 'set is' : 'sets are' }} skipped</template></p>
        <section v-for="layer in plan.layers" :key="scopeKey(layer.scope)" class="scope" :aria-label="scopeTitle(layer.scope)">
          <h3>{{ scopeTitle(layer.scope) }}</h3>
          <details v-for="set in layer.sets" :key="set.name" class="set" :class="{ skip: set.action === 'skip' }">
            <summary>
              <BizIcon name="chevron-right" :size="13" class="chev" />
              <span v-clip-tip="set.name" class="set-name">{{ set.name }}</span>
              <span class="set-counts">{{ counts(set) }}</span>
            </summary>
            <ul class="rules"><RuleItem v-for="rule in set.rules" :key="rule.identity" :set-name="set.name" :rule="rule" /></ul>
            <p v-if="duplicateWording(set)" class="wording" role="status">{{ duplicateWording(set) }}</p>
          </details>
        </section>
      </div>
    </template>

    <div v-else class="result">
      <p class="result-line" role="alert">{{ resultLine }}</p>
      <section v-for="group in byScope" :key="scopeKey(group.scope)" class="scope" :aria-label="scopeTitle(group.scope)">
        <h3>{{ scopeTitle(group.scope) }}</h3>
        <ul class="outcomes">
          <li v-for="result in group.results" :key="result.name" :class="result.status">
            <BizIcon :name="result.status === 'saved' || result.status === 'unchanged' ? 'check' : result.status === 'failed' ? 'close' : result.status === 'unknown' ? 'alert' : 'minus'" :size="14" />
            <span v-clip-tip="result.name" class="set-name">{{ result.name }}</span>
            <span class="set-counts">{{ STATUS[result.status] }}<template v-if="result.reason">: {{ result.reason }}</template></span>
          </li>
        </ul>
      </section>
      <details class="tech"><summary><BizIcon name="chevron-right" :size="12" class="chev" />Technical details</summary><p>{{ report?.message }}</p></details>
    </div>

    <template #footer>
      <template v-if="phase === 'result'">
        <button type="button" class="btn primary" @click="emit('close')">Close</button>
      </template>
      <template v-else>
        <button type="button" class="btn ghost" :disabled="phase === 'importing'" @click="emit('close')">Cancel</button>
        <button type="button" class="btn primary" :disabled="phase !== 'review' || !writes" @click="confirm">{{ confirmLabel }}</button>
      </template>
    </template>
  </RulesDialog>
</template>

<style scoped>
.drop { position: relative; display: flex; align-items: center; gap: 14px; padding: 16px 18px; border-radius: 14px; border: 1.5px dashed var(--line-2); background: var(--surface-2); cursor: pointer; min-width: 0; }
.drop.chosen { border-style: solid; border-color: var(--line); }
.drop.dragging { border-color: var(--teal); background: var(--row-selected); }
.drop:has(.file-input:focus-visible) { box-shadow: var(--focus-ring); }
@media (hover: hover) { .drop:hover { border-color: var(--teal); } }
.file-input { position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.drop-icon { flex: none; display: grid; place-items: center; width: 40px; height: 40px; border-radius: 12px; background: var(--surface); color: var(--teal-ink); box-shadow: 0 0 0 1px var(--line); }
.drop-text { display: grid; gap: 2px; min-width: 0; font-size: 13px; color: var(--ink-3); }
.drop-text strong { color: var(--ink); font-size: 14px; font-weight: 650; }
.file-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.checking { display: flex; align-items: center; gap: 8px; margin: 0; color: var(--ink-2); font-size: 13px; }
.spinner { flex: none; }
.spinner .track { stroke: var(--line-2); }
.spinner .arc { stroke: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: spin .8s linear infinite; } }
@keyframes spin { to { transform: rotate(360deg); } }
.error { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.error span { flex: 1 1 200px; }
.review, .result { display: flex; flex-direction: column; gap: 14px; }
.summary { margin: 0; color: var(--ink-2); font-size: 13px; }
.summary strong { color: var(--ink); }
.scope { display: flex; flex-direction: column; gap: 4px; }
.scope h3 { margin: 0 0 2px; color: var(--ink-3); font-size: 12px; font-weight: 650; letter-spacing: .02em; }
.set { border-radius: 10px; }
.set summary { display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 0 8px; border-radius: 10px; cursor: pointer; list-style: none; }
.set summary::-webkit-details-marker { display: none; }
@media (hover: hover) { .set summary:hover { background: var(--row-hover); } }
.set summary:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.chev { flex: none; color: var(--ink-3); }
.set[open] .chev { transform: rotate(90deg); }
.set-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; font-weight: 600; }
.set-counts { margin-left: auto; flex: none; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
.skip .set-name { color: var(--ink-3); font-weight: 500; }
.rules { list-style: none; margin: 0 0 6px; padding: 0 0 0 18px; }
.wording { margin: 0 8px 8px 26px; color: var(--ink-2); font-size: 12.5px; line-height: 1.45; }
.result-line { margin: 0; font-size: 14px; line-height: 1.5; }
.outcomes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.outcomes li { display: flex; align-items: center; gap: 8px; min-height: 32px; padding: 0 8px; border-radius: 8px; }
.outcomes li svg { flex: none; }
.outcomes .saved svg, .outcomes .unchanged svg { color: var(--st-ok); }
.outcomes .failed svg { color: var(--danger); }
.outcomes .unknown svg { color: var(--warn); }
.outcomes .not-started { color: var(--ink-3); }
.outcomes .set-counts { white-space: normal; text-align: right; }
.tech summary { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12.5px; cursor: pointer; list-style: none; }
.tech summary::-webkit-details-marker { display: none; }
.tech[open] .chev { transform: rotate(90deg); }
.tech p { margin: 6px 0 0; color: var(--ink-2); font-size: 12px; overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .set-name { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .set summary, .outcomes li { align-items: flex-start; padding-block: 6px; }
}
@media (max-width: 600px) {
  .set summary { flex-wrap: wrap; row-gap: 0; padding: 6px 8px; }
  .set-counts { margin-left: 21px; width: 100%; }
  .outcomes li { flex-wrap: wrap; padding: 6px 8px; }
  .outcomes .set-counts { margin-left: 22px; width: 100%; text-align: left; }
}
</style>
