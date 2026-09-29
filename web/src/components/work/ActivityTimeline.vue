<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { commentEditable, describeChange, parseWorkerMarker, shortRole, type TimelineEntry } from '../../lib/activity'
import { brand } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import MarkdownEditor from './MarkdownEditor.vue'
import PersonAvatar from './PersonAvatar.vue'
import StatusIcon from './StatusIcon.vue'

// The ticket's history, oldest first, on one timeline: comments as single cards,
// field changes as one compact line per person and moment, agent work markers as
// system lines that expand to the full marker.
const props = defineProps<{
  entries: TimelineEntry[]; loading: boolean; loadingOlder: boolean; hasOlder: boolean; error: string
  me: string | undefined; now: number; canWrite: boolean; canDelete: boolean
  edit: (id: string, body: string) => Promise<boolean>; remove: (id: string) => Promise<boolean>
}>()
const emit = defineEmits<{ older: []; retry: [] }>()
const editingId = ref<string | null>(null)
const draft = ref('')
const saving = ref(false)
const expanded = ref(new Set<string>())
const editor = ref<InstanceType<typeof MarkdownEditor>[]>()
const historyOptions = [
  { id: 'all', label: 'All' },
  { id: 'people', label: 'People and agents' },
  { id: 'automatic', label: 'Automatic' },
] as const
type HistoryFilter = (typeof historyOptions)[number]['id']
const history = ref<HistoryFilter>('all')
const visible = computed(() => props.entries.filter(entry => {
  if (history.value === 'automatic') return entry.author.automatic === true
  if (history.value === 'people') return entry.author.automatic !== true
  return true
}))
const emptyLine = computed(() => {
  if (!props.entries.length || history.value === 'all') return 'No activity yet.'
  return history.value === 'automatic' ? 'No automatic activity.' : 'No activity from people or agents.'
})
function isAutomatic(entry: TimelineEntry) { return entry.author.automatic === true }
function changesOf(entry: TimelineEntry) { return entry.kind === 'changes' ? entry.changes : [] }
function actorLabel(entry: TimelineEntry) { return isAutomatic(entry) ? `${brand.value.short_name} (automatic)` : entry.author.name }
function actorTip(entry: TimelineEntry) {
  if (!isAutomatic(entry)) return undefined
  const parts = [entry.author.job, entry.author.reason].filter((part): part is string => !!part)
  return parts.length ? parts.join(' · ') : undefined
}
function historyKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
  event.preventDefault()
  const index = historyOptions.findIndex(option => option.id === history.value)
  const step = event.key === 'ArrowRight' ? 1 : -1
  history.value = historyOptions[(index + step + historyOptions.length) % historyOptions.length].id
}

function marker(entry: TimelineEntry) { return entry.kind === 'comment' ? parseWorkerMarker(entry.body) : null }
function toggle(id: string) { const next = new Set(expanded.value); if (next.has(id)) next.delete(id); else next.add(id); expanded.value = next }
async function startEdit(entry: Extract<TimelineEntry, { kind: 'comment' }>) {
  editingId.value = entry.id; draft.value = entry.body
  await nextTick(); editor.value?.[0]?.focus()
}
async function saveEdit() {
  if (!editingId.value) return
  saving.value = true
  const ok = await props.edit(editingId.value, draft.value)
  saving.value = false
  if (ok) editingId.value = null
}
async function cancelEdit(original: string) {
  if (draft.value !== original && !(await confirmAction({ title: 'Discard your comment changes?', confirmLabel: 'Discard', danger: true }))) return
  editingId.value = null
}
async function removeComment(id: string) {
  if (await confirmAction({ title: 'Delete this comment?', body: 'It leaves the timeline for everyone. The audit log keeps a record.', confirmLabel: 'Delete comment', danger: true })) await props.remove(id)
}
function isDirty() { return editingId.value !== null }
defineExpose({ isDirty })
</script>

<template>
  <section class="activity" aria-label="Activity">
    <div class="activity-head">
      <h3 class="eyebrow">Activity</h3>
      <div v-if="entries.length" class="seg history-filter" role="radiogroup" aria-label="History" @keydown="historyKey">
        <button v-for="option in historyOptions" :key="option.id" type="button" role="radio" :aria-checked="history === option.id" :tabindex="history === option.id ? 0 : -1" @click="history = option.id">{{ option.label }}</button>
      </div>
    </div>
    <button v-if="hasOlder" type="button" class="older" :disabled="loadingOlder" @click="emit('older')">
      <AppIcon name="chevron-up" :size="13" />{{ loadingOlder ? 'Loading older activity…' : 'Show older activity' }}
    </button>
    <div v-if="loading && !entries.length" class="activity-skeleton" aria-hidden="true">
      <span class="skeleton" /><span class="skeleton short" /><span class="skeleton" />
    </div>
    <p v-else-if="error" class="activity-error" role="alert">Activity could not be loaded. <button type="button" class="btn sm" @click="emit('retry')">Try again</button></p>
    <ol v-else class="timeline">
      <template v-for="entry in visible" :key="entry.id">
        <!-- Comment, possibly led by an agent work marker -->
        <template v-if="entry.kind === 'comment'">
          <li v-if="marker(entry) && editingId !== entry.id" class="entry marker">
            <span class="node agent" aria-hidden="true"><AppIcon name="agent" :size="12" /></span>
            <div class="marker-body">
              <p class="system-line">
                <button type="button" class="marker-toggle" :aria-expanded="expanded.has(entry.id)" :aria-label="`${marker(entry)!.session} started as ${marker(entry)!.role}. Show the full work marker`" @click="toggle(entry.id)">
                  <strong class="session">{{ marker(entry)!.session }}</strong>{{ ' ' }}<span class="role">started as {{ shortRole(marker(entry)!.role) }}</span>
                  <AppIcon name="chevron" :size="11" class="chev" />
                </button>
                {{ ' ' }}<span class="sep">·</span>{{ ' ' }}<time :datetime="entry.at" :data-tip="absoluteTime(entry.at)">{{ relativeTime(entry.at, { now }) }}</time>
                <span v-if="(canWrite || canDelete) && commentEditable(entry, me, now)" class="line-actions">
                  <button v-if="canWrite" type="button" class="icon-btn sm flat" aria-label="Edit comment" data-tip="Edit · within 15 minutes" @click="startEdit(entry)"><AppIcon name="edit" :size="12" /></button>
                  <button v-if="canDelete" type="button" class="icon-btn sm flat danger-icon" aria-label="Delete comment" data-tip="Delete" @click="removeComment(entry.id)"><AppIcon name="trash" :size="12" /></button>
                </span>
              </p>
              <dl v-if="expanded.has(entry.id)" class="marker-detail">
                <div><dt>Session</dt><dd class="mono">{{ marker(entry)!.session }}</dd></div>
                <div><dt>ID</dt><dd class="mono">{{ marker(entry)!.sessionId }}</dd></div>
                <div><dt>Role</dt><dd>{{ marker(entry)!.role }}</dd></div>
                <div v-for="extra in marker(entry)!.extras" :key="extra.key"><dt>{{ extra.key }}</dt><dd class="mono">{{ extra.value }}</dd></div>
                <div><dt>Started</dt><dd><time :datetime="marker(entry)!.started">{{ absoluteTime(marker(entry)!.started) || marker(entry)!.started }}</time></dd></div>
                <div><dt>Posted by</dt><dd>{{ entry.author.name }}</dd></div>
              </dl>
              <div v-if="marker(entry)!.rest" class="comment-card marker-rest">
                <MarkdownBody :body="marker(entry)!.rest" class="comment-body" />
              </div>
            </div>
          </li>
          <li v-else class="entry comment">
            <PersonAvatar :id="entry.author.id" :name="entry.author.name" :size="26" class="node avatar" />
            <div class="comment-card">
              <header class="comment-head">
                <strong>{{ entry.author.name }}</strong>
                <time :datetime="entry.at" :data-tip="absoluteTime(entry.at)">{{ relativeTime(entry.at, { now, long: true }) }}</time>
                <span v-if="(canWrite || canDelete) && commentEditable(entry, me, now) && editingId !== entry.id" class="line-actions">
                  <button v-if="canWrite" type="button" class="icon-btn sm flat" aria-label="Edit comment" data-tip="Edit · within 15 minutes" @click="startEdit(entry)"><AppIcon name="edit" :size="13" /></button>
                  <button v-if="canDelete" type="button" class="icon-btn sm flat danger-icon" aria-label="Delete comment" data-tip="Delete" @click="removeComment(entry.id)"><AppIcon name="trash" :size="13" /></button>
                </span>
              </header>
              <MarkdownEditor v-if="editingId === entry.id" ref="editor" v-model="draft" label="Comment" compact :min-rows="3" :saving="saving" @save="saveEdit" @cancel="cancelEdit(entry.body)" />
              <MarkdownBody v-else :body="entry.body" class="comment-body" />
            </div>
          </li>
        </template>
        <li v-else class="entry" :class="[entry.kind, { automatic: isAutomatic(entry) }]">
          <span v-if="isAutomatic(entry)" class="node auto" aria-hidden="true"><AppIcon name="sparkle" :size="12" /></span>
          <span v-else class="node dot" :class="{ 'created-dot': entry.kind === 'created' }" aria-hidden="true" />
          <p class="change-line">
            <strong :data-tip="actorTip(entry)">{{ actorLabel(entry) }}</strong>
            <template v-if="entry.kind === 'changes'">
              <template v-for="(change, index) in changesOf(entry)" :key="change.field">
                <span v-if="index > 0" class="sep">,</span>
                {{ ' ' }}{{ describeChange(change).label }}
                <template v-if="describeChange(change).from">
                  <span class="value"><StatusIcon v-if="change.field === 'status' && change.from" :state="change.from" :size="11" />{{ describeChange(change).from }}</span>
                  <AppIcon name="arrow" :size="11" class="arrow" /><span class="sr-only"> to </span>
                </template>
                <span v-if="describeChange(change).to" class="value"><StatusIcon v-if="change.field === 'status' && change.to" :state="change.to" :size="11" />{{ describeChange(change).to }}</span>
              </template>
            </template>
            <template v-else> created this</template>
            <span class="sep">·</span>
            <time :datetime="entry.at" :data-tip="absoluteTime(entry.at)">{{ relativeTime(entry.at, { now }) }}</time>
          </p>
        </li>
      </template>
      <li v-if="!visible.length && !loading" class="entry empty"><span class="node dot" aria-hidden="true" /><p class="change-line">{{ emptyLine }}</p></li>
    </ol>
  </section>
</template>

<style scoped>
.activity { min-width: 0; }
.activity-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 12px; margin: 0 0 12px; }
.activity-head .eyebrow { margin: 0; }
.history-filter { flex-wrap: wrap; max-width: 100%; }
.auto { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2), 0 0 0 3px var(--surface-raised); color: var(--teal-ink); }
.older { display: inline-flex; align-items: center; gap: 6px; height: 28px; margin: 0 0 10px -8px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.older:hover { background: var(--row-hover); }
.older:focus-visible { box-shadow: var(--focus-ring); }
/* One timeline: a hairline through 26px nodes (avatars, agent marks, dots). */
.timeline { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; margin: 0; padding: 0; list-style: none; }
.timeline::before { content: ''; position: absolute; left: 12.5px; top: 8px; bottom: 8px; width: 1px; background: var(--line-2); }
.entry { position: relative; display: grid; grid-template-columns: 26px minmax(0, 1fr); column-gap: 12px; align-items: start; }
.node { position: relative; z-index: 1; justify-self: center; }
.avatar { margin-top: 3px; box-shadow: 0 0 0 3px var(--surface-raised), 0 0 0 4px var(--glass-rim); }
.dot { display: grid; place-items: center; width: 26px; height: 22px; }
.dot::after { content: ''; width: 7px; height: 7px; border-radius: 50%; background: var(--surface-raised); box-shadow: inset 0 0 0 1.5px var(--ink-3), 0 0 0 3px var(--surface-raised); }
.created-dot::after { background: var(--aqua); box-shadow: 0 0 0 3px var(--surface-raised); }
.agent { display: grid; place-items: center; width: 22px; height: 22px; margin-top: 0; border-radius: 7px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 0 0 3px var(--surface-raised); color: var(--teal-ink); }
/* Comments: one card, a tint and no border. */
.comment-card { min-width: 0; padding: 9px 14px 2px; border-radius: 12px; background: var(--code-bg); }
.comment-head { display: flex; align-items: center; gap: 8px; min-height: 24px; font-size: 12.5px; }
.comment-head strong { color: var(--ink); font-weight: 650; }
.comment-head time { color: var(--ink-3); }
.line-actions { display: inline-flex; gap: 2px; margin-left: auto; }
.line-actions .icon-btn { width: 24px; height: 24px; color: var(--ink-3); }
.danger-icon:hover { color: var(--danger) !important; }
.comment-body { margin-top: 2px; font-size: 13.5px; overflow-wrap: anywhere; }
.comment-card .md-editor { margin: 6px 0 10px; }
.change-line, .system-line { display: flex; align-items: center; flex-wrap: wrap; gap: 0 4px; min-height: 22px; margin: 0; font-size: 12.5px; color: var(--ink-2); }
.change-line strong { color: var(--ink); font-weight: 600; }
.value { display: inline-flex; align-items: center; gap: 4px; color: var(--ink); }
.arrow, .sep, .change-line time, .system-line time { color: var(--ink-3); }
.marker-body { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; min-width: 0; }
.marker-toggle { display: inline-flex; align-items: center; gap: 5px; min-width: 0; max-width: 100%; height: 22px; margin-left: -6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font-size: 12.5px; }
@media (hover: hover) { .marker-toggle:hover { background: var(--row-hover); } }
.marker-toggle:focus-visible { box-shadow: var(--focus-ring); }
.session { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font: 600 12px/1 var(--mono); font-variant-ligatures: none; }
.role { white-space: nowrap; }
/* A work marker keeps one line in a narrow column: the session name gives way,
   its time and edit actions stay beside it (AEON-140). */
.marker .system-line { flex-wrap: nowrap; }
.marker .marker-toggle { flex: 0 1 auto; }
.marker .role, .marker .chev, .marker .sep, .marker .system-line time, .marker .line-actions { flex-shrink: 0; white-space: nowrap; }
.chev { color: var(--ink-3); }
.marker-toggle[aria-expanded="true"] .chev { transform: rotate(180deg); }
.marker-detail { display: grid; gap: 3px; margin: 0; padding: 8px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12px; }
.marker-detail div { display: grid; grid-template-columns: 76px minmax(0, 1fr); gap: 8px; }
.marker-detail dt { font: 500 10px/1.8 var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.marker-detail dd { margin: 0; color: var(--ink); overflow-wrap: anywhere; }
.marker-detail .mono { font-family: var(--mono); font-size: 11.5px; font-variant-ligatures: none; }
.marker-rest { padding-top: 8px; }
.empty .change-line { color: var(--ink-3); }
.activity-skeleton { display: grid; gap: 12px; padding: 6px 0; }
.activity-skeleton .short { width: 55%; }
.activity-error { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--danger); }
</style>
