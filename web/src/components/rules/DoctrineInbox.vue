<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The doctrine inbox (AEON-444): rule changes agents proposed from their work,
// waiting for a person. Each shows its word diff against the pinned rule, who
// proposed it, the ticket it came from and why. Propose PR sends it to git
// through the normal proposal path; Edit changes it first; Dismiss sends a
// reason back to the proposer.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import DoctrineProposalDialog from './DoctrineProposalDialog.vue'
import { can, onAccessChange } from '../../lib/authz'
import { doctrineInbox, inboxChanged } from '../../lib/doctrineInbox'
import { toast } from '../../lib/toast'
import { absoluteTime, relativeTime } from '../../lib/work'
import {
  dismissDoctrineInbox, doctrineMessage, fileName, getDoctrineInbox, inboxLabel, repoName, submitDoctrineInbox,
  type DoctrineFile, type DoctrineInboxItem, type DoctrineProposal, type DoctrineRule, type DoctrineSource,
} from '../../lib/doctrine'

const props = defineProps<{ sources: DoctrineSource[] }>()
const emit = defineEmits<{ proposed: [proposal: DoctrineProposal]; count: [pending: number] }>()

const items = ref<DoctrineInboxItem[]>([])
const error = ref('')
const busy = ref('')
const dismissing = ref('')
const reason = ref('')
const editing = ref<{ item: DoctrineInboxItem; source: DoctrineSource; file: DoctrineFile; rule: DoctrineRule }>()
const canAct = computed(() => can('rules.write'))
let generation = 0

async function load() {
  const turn = ++generation
  try {
    const inbox = await getDoctrineInbox()
    if (turn !== generation) return
    items.value = inbox.items
    error.value = ''
    publish()
  } catch (cause) { if (turn === generation) error.value = doctrineMessage(cause) }
}
function publish() { emit('count', items.value.length); inboxChanged(items.value.length) }
function drop(id: string) { items.value = items.value.filter(item => item.id !== id); publish() }

// The pinned rule the proposal is diffed against, for Edit.
function pinned(item: DoctrineInboxItem) {
  const source = props.sources.find(s => s.id === item.source_id)
  const file = source?.files.find(f => f.path === item.path)
  const rule = file?.rules.find(r => r.sha256 === item.base_sha256)
  return source && file && rule ? { source, file, rule } : undefined
}
// The accessible name of a proposal: its label, without a final full stop.
const name = (item: DoctrineInboxItem) => inboxLabel(item.label, 120)
// A changed run is marked without its surrounding spaces.
function runs(text: string): [string, string, string] {
  const match = /^(\s*)([\s\S]*?)(\s*)$/.exec(text)!
  return [match[1]!, match[2]!, match[3]!]
}

async function send(item: DoctrineInboxItem) {
  if (busy.value) return
  busy.value = item.id
  try {
    const proposal = await submitDoctrineInbox(item.id)
    drop(item.id)
    emit('proposed', proposal)
    toast('Pull request created', proposal.pr_url ? { action: { label: 'Open', run: () => window.open(proposal.pr_url, '_blank', 'noopener') } } : undefined)
  } catch (cause) { toast(doctrineMessage(cause), { tone: 'error' }) }
  finally { busy.value = '' }
}
function edit(item: DoctrineInboxItem) {
  const at = pinned(item)
  if (at) editing.value = { item, ...at }
}
function edited(proposal: DoctrineProposal) {
  if (editing.value) drop(editing.value.item.id)
  editing.value = undefined
  emit('proposed', proposal)
  toast('Pull request created')
}
async function startDismiss(item: DoctrineInboxItem) {
  dismissing.value = item.id
  reason.value = ''
  await nextTick()
  document.getElementById(`dismiss-${item.id}`)?.focus()
}
async function dismiss(item: DoctrineInboxItem) {
  const text = reason.value.trim()
  if (!text || busy.value) return
  busy.value = item.id
  try {
    await dismissDoctrineInbox(item.id, text)
    dismissing.value = ''
    drop(item.id)
    toast('Proposal dismissed')
  } catch (cause) { toast(doctrineMessage(cause), { tone: 'error' }) }
  finally { busy.value = '' }
}

// A new proposal elsewhere changes the count the header polls: read the list again.
watch(() => doctrineInbox.pending, pending => { if (pending !== items.value.length && !busy.value) void load() })
const stopAccess = onAccessChange(() => { items.value = []; void load() })
onMounted(load)
onBeforeUnmount(() => { generation++; stopAccess() })
</script>

<template>
  <section v-if="items.length || error" id="doctrine-inbox" class="inbox" aria-labelledby="doctrine-inbox-title">
    <h4 id="doctrine-inbox-title">Proposed changes<span v-if="items.length" class="count"> · {{ items.length }}</span></h4>
    <p v-if="error" class="quiet" role="status">{{ error }} <button type="button" class="link" @click="load">Try again</button></p>
    <div v-if="items.length" class="list">
      <article v-for="item in items" :key="item.id" class="item" :aria-label="name(item)">
        <div class="item-head">
          <strong class="label" :title="item.label">{{ item.label }}</strong>
          <span class="where" :title="`${item.repository}/${item.path}`">{{ item.heading }} · {{ repoName(item.repository) }}/{{ fileName(item.path) }}</span>
        </div>
        <pre class="diff" :aria-label="`Change to ${item.heading}`"><template v-for="(part, index) in item.diff" :key="index"><template v-if="part.op === 'eq'">{{ part.text }}</template><template v-else>{{ runs(part.text)[0] }}<del v-if="part.op === 'del'"><span class="sr-only">removed: </span>{{ runs(part.text)[1] }}</del><ins v-else><span class="sr-only">added: </span>{{ runs(part.text)[1] }}</ins>{{ runs(part.text)[2] }}</template></template></pre>
        <p v-if="item.why" class="why" :title="item.why">{{ item.why }}</p>
        <p v-if="item.outdated" class="note">The rule changed since this was proposed. Edit it against the current rule to propose it.</p>
        <div class="foot">
          <span class="meta">
            <span v-if="item.proposer" class="who"><AppIcon :name="item.proposer_kind === 'agent' ? 'agent' : 'user'" :size="13" />{{ item.proposer }}</span>
            <RouterLink v-if="item.ticket && item.ticket_href" :to="item.ticket_href" class="ticket">{{ item.ticket }}</RouterLink>
            <span v-else-if="item.ticket" class="ticket plain">{{ item.ticket }}</span>
            <time :datetime="item.created_at" :title="absoluteTime(item.created_at)">{{ relativeTime(item.created_at) }}</time>
          </span>
          <div v-if="canAct && dismissing !== item.id" class="actions">
            <button type="button" class="btn sm ghost" :disabled="!!busy" :aria-label="`Dismiss ${name(item)}`" @click="startDismiss(item)">Dismiss</button>
            <button type="button" class="btn sm ghost" :disabled="!!busy || !pinned(item)" :aria-label="`Edit ${name(item)}, then propose`" data-tip="Edit, then propose" @click="edit(item)"><AppIcon name="edit" :size="14" />Edit</button>
            <button type="button" class="btn sm primary" :disabled="!!busy || item.outdated" :aria-label="`Propose PR for ${name(item)}`" @click="send(item)">{{ busy === item.id ? 'Creating PR…' : 'Propose PR' }}</button>
          </div>
        </div>
        <form v-if="dismissing === item.id" class="dismiss" @submit.prevent="dismiss(item)">
          <label class="sr-only" :for="`dismiss-${item.id}`">Why dismiss it</label>
          <input :id="`dismiss-${item.id}`" v-model="reason" class="field" maxlength="500" required placeholder="Why not? The proposer sees this." @keydown.esc="dismissing = ''">
          <button type="button" class="btn sm ghost" :disabled="!!busy" @click="dismissing = ''">Cancel</button>
          <button type="submit" class="btn sm primary" :disabled="!!busy || !reason.trim()">{{ busy === item.id ? 'Dismissing…' : 'Dismiss' }}</button>
        </form>
      </article>
    </div>
    <DoctrineProposalDialog v-if="editing" :source="editing.source" :file="editing.file" :rule="editing.rule" :draft="editing.item" @close="editing = undefined" @saved="edited" />
  </section>
</template>

<style scoped>
.inbox { display: flex; flex-direction: column; gap: 8px; margin-left: 36px; min-width: 0; }
h4 { margin: 0; color: var(--ink-2); font-size: 13px; font-weight: 650; }
.count { color: var(--ink-3); font-weight: 600; font-variant-numeric: tabular-nums; }
.list { display: flex; flex-direction: column; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); min-width: 0; }
.list > * + * { border-top: 1px solid var(--line); }
.item { display: flex; flex-direction: column; gap: 8px; padding: 14px 16px; min-width: 0; }
.item-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 12px; min-width: 0; }
.label { flex: 1 1 240px; min-width: 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; color: var(--ink); font-size: 14px; font-weight: 650; line-height: 1.4; overflow-wrap: anywhere; }
.where { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; }
.diff { margin: 0; padding: 10px 12px; border-radius: 9px; background: var(--surface-2); color: var(--ink-2); font-family: var(--mono); font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.diff ins { padding: 0 1px; border-radius: 3px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); text-decoration: underline; text-decoration-thickness: 1px; text-underline-offset: 3px; }
.diff del + ins { margin-left: 3px; }
.diff del { color: var(--ink-3); text-decoration: line-through; text-decoration-thickness: 1px; }
.why, .note { margin: 0; font-size: 13px; line-height: 1.5; overflow-wrap: anywhere; }
.why { display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; color: var(--ink); }
.note { color: var(--ink-2); }
.foot { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 12px; min-width: 0; }
.meta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; min-width: 0; color: var(--ink-3); font-size: 12px; }
.who { display: inline-flex; align-items: center; gap: 5px; color: var(--ink-2); font-weight: 600; }
.who :deep(svg) { flex: none; color: var(--ink-3); }
.ticket { color: var(--teal-ink); font-weight: 600; text-decoration: none; font-variant-numeric: tabular-nums; }
.ticket.plain { color: var(--ink-2); }
@media (hover: hover) { a.ticket:hover { text-decoration: underline; text-underline-offset: 2px; } }
.ticket:focus-visible { box-shadow: var(--focus-ring); outline: none; border-radius: 4px; }
.actions { display: flex; flex-wrap: wrap; gap: 6px; margin-left: auto; }
.actions .btn :deep(svg) { flex: none; }
.dismiss { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.dismiss .field { flex: 1 1 240px; box-sizing: border-box; min-width: 0; height: 32px; padding: 0 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-2); color: var(--ink); font: inherit; font-size: 13px; }
.dismiss .field:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.quiet { margin: 0; color: var(--ink-3); font-size: 13px; }
.link { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 600px) {
  .inbox { margin-left: 0; }
  .item { padding: 12px; }
  .actions { width: 100%; margin-left: 0; }
  .actions .btn { flex: 1 1 auto; justify-content: center; }
}
</style>
