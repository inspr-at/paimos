<script setup lang="ts">
import LoadingText from "@/components/LoadingText.vue";
import { ref, watch, computed, nextTick, onUnmounted } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";

const { t } = useI18n();
import AppIcon from "@/components/AppIcon.vue";
import StatusDot from "@/components/StatusDot.vue";
import NumericInput from "@/components/NumericInput.vue";
import MetaSelect from "@/components/MetaSelect.vue";
import type { MetaOption } from "@/components/MetaSelect.vue";
import IssueStatusSelect from "@/components/issue/IssueStatusSelect.vue";
import IssueAssigneeSelect from "@/components/issue/IssueAssigneeSelect.vue";
import MarkdownToolbar from "@/components/MarkdownToolbar.vue";
import AutocompleteInput from "@/components/AutocompleteInput.vue";
import TagSelector from "@/components/TagSelector.vue";
import TagChip from "@/components/TagChip.vue";
import SprintChips from "@/components/issue/SprintChips.vue";
import AttachmentSidebar from "@/components/issue/AttachmentSidebar.vue";
import IssueComments from "@/components/issue/IssueComments.vue";
import IssueAiActivity from "@/components/issue/IssueAiActivity.vue";
import { ApiError, api, errMsg, isSessionExpiredError } from "@/api/client";
import { attachmentsEnabled } from "@/api/instance";
import type { Issue, User, Tag, Sprint, TimeEntry, Attachment } from "@/types";
import { useAuthStore } from "@/stores/auth";
import { useTimerStore } from "@/stores/timer";
import {
  useIssueDisplay,
  STATUS_LABEL,
  PRIORITY_LABEL,
  PRIORITY_COLOR,
  TYPE_SVGS,
} from "@/composables/useIssueDisplay";
import { useMarkdown } from "@/composables/useMarkdown";
import { useTimeUnit } from "@/composables/useTimeUnit";
import { useDirtyGuard } from "@/composables/useDirtyGuard";
import { useSearchStore } from "@/stores/search";
import { highlightDom } from "@/composables/useHighlight";
import { formatDuration } from "@/composables/useDurationInput";
import { useConfirm } from "@/composables/useConfirm";
import { useIssueContext } from "@/composables/useIssueContext";
import {
  useAttachmentUploads,
  type AttachmentJob,
} from "@/composables/useAttachmentUploads";
import {
  useSidePanelWidth,
  resetSidePanelWidth,
  SIDE_PANEL_DEFAULT_WIDTH,
  SIDE_PANEL_MIN_WIDTH,
  SIDE_PANEL_MAX_WIDTH_RATIO,
} from "@/composables/useSidePanelWidth";
// PAI-146: AI text optimization on multiline editors. Same composable
// + overlay singleton as the detail view; once mounted here, the side
// panel surfaces the AI action for description / acceptance / notes.
import AiActionMenu from "@/components/ai/AiActionMenu.vue";
import AiSurfaceFeedback from "@/components/ai/AiSurfaceFeedback.vue";
import {
  aiMutationHeaders,
  applyIssueTextMutations,
  type AiApplyInfo,
} from "@/services/aiActionApply";
import { undoMutationByRequestId } from "@/services/aiPaperTrail";
import { useUndoStore } from "@/stores/undo";
import { addIssueRelation } from "@/services/issueRelations";
import {
  issueIfMatch,
  loadIssueEditorMetadata,
  saveIssueDetail,
} from "@/services/issueDetail";

const ctx = useIssueContext(true);

const props = defineProps<{
  issueId: number | null;
  users?: User[];
  allTags?: Tag[];
  costUnits?: string[];
  releases?: string[];
  sprints?: Sprint[];
  issueIds?: number[]; // ordered list of visible issue IDs for prev/next
  startInEdit?: boolean;
  pinned?: boolean;
  readonly?: boolean; // no edit controls, fields rendered as text/markdown (portal mode)
  /** Embedded in Agent Mode's layout instead of painting over the app. */
  embedded?: boolean;
  /** Fail-closed attachment capability supplied by the embedding. */
  allowAttachments?: boolean;
  allowComments?: boolean;
  /** Force the comment composer to create internal notes only. */
  internalCommentsOnly?: boolean;
  /** Truthful warning for active one-shot runs without live-note support. */
  noteAffectsNextRun?: boolean;
}>();

const loadedUsers = ref<User[]>([]);
const loadedTags = ref<Tag[]>([]);
const loadedCostUnits = ref<string[]>([]);
const loadedReleases = ref<string[]>([]);
const loadedSprints = ref<Sprint[]>([]);

// Prefer context, fall back to props for backward compatibility
const users = computed(() =>
  props.users?.length ? props.users : ctx.users.value.length ? ctx.users.value : loadedUsers.value,
);
const allTags = computed(() =>
  props.allTags?.length ? props.allTags : ctx.allTags.value.length ? ctx.allTags.value : loadedTags.value,
);
const costUnits = computed(() =>
  props.costUnits?.length ? props.costUnits : ctx.costUnits.value.length ? ctx.costUnits.value : loadedCostUnits.value,
);
const releases = computed(() =>
  props.releases?.length ? props.releases : ctx.releases.value.length ? ctx.releases.value : loadedReleases.value,
);
const sprints = computed(() =>
  props.sprints?.length ? props.sprints : ctx.sprints.value.length ? ctx.sprints.value : loadedSprints.value,
);

const emit = defineEmits<{
  close: [];
  updated: [issue: Issue];
  deleted: [id: number];
  navigate: [id: number];
  "update:pinned": [pinned: boolean];
  "guard-state": [state: { dirty: boolean; inFlight: boolean }];
}>();

const router = useRouter();
const undoStore = useUndoStore();
const { confirm } = useConfirm();
const authStore = useAuthStore();
const timerStore = useTimerStore();
const { showTypeIcon } = useIssueDisplay();
const {
  formatHours,
  label: timeLabel,
  toggle: toggleTimeUnit,
  unit: timeUnit,
  toDisplay,
  toHours,
} = useTimeUnit();

const issue = ref<Issue | null>(null);
const loading = ref(false);
const editing = ref(false);
const saving = ref(false);
const saveError = ref("");
const panelGuardError = ref("");
const commentDirty = ref(false);
const commentInFlight = ref(false);
const savedSnapshot = ref("");
const editorMetadataError = ref("");
const mdMode = ref(authStore.user?.markdown_default ?? false);
const issueMutationAllowed = computed(() => !props.readonly);
let issueMutationAuthorityEpoch = 0;
let issueMutationOperationSequence = 0;
let editorMetadataSequence = 0;

// Full edit form
const form = ref({
  title: "",
  description: "",
  acceptance_criteria: "",
  notes: "",
  report_summary: "",
  status: "",
  priority: "",
  type: "",
  assignee_id: "" as string,
  parent_id: "" as string,
  cost_unit: "",
  release: "",
  estimate_hours: null as number | null,
  estimate_lp: null as number | null,
  ar_hours: null as number | null,
  ar_lp: null as number | null,
  time_override: null as number | null,
});

const PRIORITY_OPTIONS: MetaOption[] = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
];
// Attachments — scoped to the currently-loaded issue.
const attachments = useAttachmentUploads({
  endpoint: () =>
    issue.value ? `/issues/${issue.value.id}/attachments` : "/attachments",
});
const attachmentsAllowed = computed(() =>
  !props.readonly &&
  attachmentsEnabled.value &&
  (props.embedded ? props.allowAttachments === true : props.allowAttachments !== false),
);
const commentsAllowed = computed(() =>
  !props.readonly && (props.embedded ? props.allowComments === true : props.allowComments !== false),
);
const hasInFlight = computed(() =>
  attachments.hasInFlight.value ||
  commentInFlight.value ||
  saving.value ||
  quickSavingField.value !== "",
);

function clearEditorMetadata() {
  editorMetadataSequence += 1;
  loadedUsers.value = [];
  loadedTags.value = [];
  loadedCostUnits.value = [];
  loadedReleases.value = [];
  loadedSprints.value = [];
  editorMetadataError.value = "";
}

async function loadEmbeddedEditorMetadata(forIssue: Issue) {
  if (!props.embedded || !issueMutationAllowed.value) return;
  const sequence = ++editorMetadataSequence;
  const authorityEpoch = issueMutationAuthorityEpoch;
  const expectedIssueId = forIssue.id;
  const expectedProjectId = forIssue.project_id;
  editorMetadataError.value = "";
  try {
    const metadata = await loadIssueEditorMetadata(expectedProjectId);
    if (
      sequence !== editorMetadataSequence ||
      authorityEpoch !== issueMutationAuthorityEpoch ||
      !issueMutationAllowed.value ||
      issue.value?.id !== expectedIssueId ||
      issue.value.project_id !== expectedProjectId ||
      props.issueId !== expectedIssueId
    ) return;
    loadedUsers.value = metadata.users;
    loadedTags.value = metadata.allTags;
    loadedCostUnits.value = metadata.costUnits;
    loadedReleases.value = metadata.releases;
    loadedSprints.value = metadata.allSprints;
  } catch (e: unknown) {
    if (
      sequence !== editorMetadataSequence ||
      authorityEpoch !== issueMutationAuthorityEpoch ||
      !issueMutationAllowed.value ||
      issue.value?.id !== expectedIssueId
    ) return;
    if (!isSessionExpiredError(e)) {
      editorMetadataError.value = "Editor options could not be loaded. Reopen the ticket or retry after access is restored.";
    }
  }
}

async function loadAttachments(forIssue: Issue | null = issue.value) {
  if (!forIssue) {
    attachments.reset();
    return;
  }
  const expectedId = forIssue.id;
  try {
    const list = await api.get<Attachment[]>(
      `/issues/${expectedId}/attachments`,
    );
    if (issue.value?.id !== expectedId) return;
    attachments.seedExisting(list);
  } catch {
    if (issue.value?.id !== expectedId) return;
    attachments.reset();
  }
}

// Parent picker for the quick-edit form.
// Hierarchy: ticket → parent must be epic; task → parent must be ticket.
// Fetched lazily on edit, scoped to the currently-displayed issue so
// switching issues never shows stale candidates from the previous project.
const parentCandidates = ref<Issue[]>([]);
const parentCandidatesForIssueId = ref<number | null>(null);
const relationCandidates = ref<Issue[]>([]);
const relationCandidatesForProjectId = ref<number | null>(null);
async function loadParentCandidates() {
  if (!issue.value) return;
  if (parentCandidatesForIssueId.value === issue.value.id) return;
  const t = issue.value.type;
  if (t !== "ticket" && t !== "task") {
    parentCandidates.value = [];
    parentCandidatesForIssueId.value = issue.value.id;
    return;
  }
  const parentType = t === "ticket" ? "epic" : "ticket";
  const fetchingForId = issue.value.id;
  try {
    const list = await api.get<Issue[]>(
      `/projects/${issue.value.project_id}/issues?type=${parentType}`,
    );
    // Guard against races: ignore the result if the user has since switched issues.
    if (issue.value?.id !== fetchingForId) return;
    parentCandidates.value = list;
    parentCandidatesForIssueId.value = fetchingForId;
  } catch {
    if (issue.value?.id !== fetchingForId) return;
    parentCandidates.value = [];
    parentCandidatesForIssueId.value = fetchingForId;
  }
}
async function loadRelationCandidates() {
  if (!issue.value) return;
  if (relationCandidatesForProjectId.value === issue.value.project_id) return;
  try {
    relationCandidates.value = await api.get<Issue[]>(
      `/projects/${issue.value.project_id}/issues`,
    );
    relationCandidatesForProjectId.value = issue.value.project_id;
  } catch {
    relationCandidates.value = [];
    relationCandidatesForProjectId.value = issue.value.project_id;
  }
}
const parentOptions = computed<MetaOption[]>(() => {
  if (!issue.value) return [];
  const opts: MetaOption[] = [{ value: "", label: "— None —" }];
  for (const p of parentCandidates.value) {
    const truncated =
      p.title.length > 40 ? p.title.slice(0, 40) + "..." : p.title;
    opts.push({ value: String(p.id), label: `${p.issue_key} — ${truncated}` });
  }
  return opts;
});
const showParentPicker = computed(
  () => issue.value?.type === "ticket" || issue.value?.type === "task",
);

// Markdown rendering for view mode
const descRef = computed(() => issue.value?.description ?? "");
const acRef = computed(() => issue.value?.acceptance_criteria ?? "");
const notesRef = computed(() => issue.value?.notes ?? "");
const reportSummaryRef = computed(() => issue.value?.report_summary ?? "");
const { html: descHtml } = useMarkdown(descRef, mdMode);
const { html: acHtml } = useMarkdown(acRef, mdMode);
const { html: notesHtml } = useMarkdown(notesRef, mdMode);
const { html: reportSummaryHtml } = useMarkdown(reportSummaryRef, mdMode);

// PAI-146: AI optimization. The form's id matches issue.value.id when
// editing an existing issue (this panel never opens without one), so
// pass it through for context assembly.
function onAiAccept(
  field: "description" | "acceptance_criteria" | "notes" | "report_summary",
) {
  return (text: string) => {
    form.value[field] = text;
  };
}
async function applyAiPanelResult(info: AiApplyInfo) {
  if (!issue.value || props.embedded || !issueMutationAllowed.value) return;
  if (info.action === "estimate_effort") {
    const prevHours = form.value.estimate_hours;
    const prevLp = form.value.estimate_lp;
    form.value.estimate_hours = Number(
      info.values?.hours ?? (info.body as any)?.hours ?? 0,
    );
    form.value.estimate_lp = Number(
      info.values?.lp ?? (info.body as any)?.lp ?? 0,
    );
    return {
      undoLabel: `Estimate ${form.value.estimate_hours}h / ${form.value.estimate_lp} LP applied`,
      undo: () => {
        form.value.estimate_hours = prevHours;
        form.value.estimate_lp = prevLp;
      },
    };
  }
  if (info.action === "find_parent") {
    await loadParentCandidates();
    const issueKey = String(info.values?.issue_key ?? "");
    const parent = parentCandidates.value.find((i) => i.issue_key === issueKey);
    if (!parent) return;
    const prevParent = form.value.parent_id;
    form.value.parent_id = String(parent.id);
    return {
      undoLabel: `Parent set to ${parent.issue_key}`,
      undo: () => {
        form.value.parent_id = prevParent;
      },
    };
  }
  if (info.action === "detect_duplicates") {
    const issueKey = String(info.values?.issue_key ?? "");
    const relationType = String(info.values?.relation_type ?? "related") as
      | "depends_on"
      | "impacts"
      | "follows_from"
      | "blocks"
      | "related";
    const requestId = info.requestId;
    await loadRelationCandidates();
    const target = relationCandidates.value.find(
      (i) => i.issue_key === issueKey,
    );
    if (!target) return;
    await addIssueRelation(issue.value.id, target.id, relationType, {
      headers: aiMutationHeaders(info),
    });
    if (requestId) {
      undoStore.showSyntheticToast(
        {
          id: Date.now(),
          title: issue.value.issue_key,
          detail: `${relationType.replace(/_/g, " ")} link to ${target.issue_key} added`,
        },
        "undo",
      );
      void undoStore.refresh();
    }
    return requestId
      ? {
          undoLabel: `${relationType.replace(/_/g, " ")} link to ${target.issue_key} added`,
          undo: async () => {
            await undoMutationByRequestId(requestId);
            await loadRelationCandidates();
          },
          undoAutoDismissMs: 15000,
        }
      : undefined;
  }
  const next = applyIssueTextMutations(info, {
    description: form.value.description,
    acceptance_criteria: form.value.acceptance_criteria,
    notes: form.value.notes,
  });
  form.value.description = next.description;
  form.value.acceptance_criteria = next.acceptance_criteria;
  form.value.notes = next.notes;
}
const search = useSearchStore();

// DOM-based search highlighting — applied after v-html renders, walks text nodes only
const descEl = ref<HTMLElement | null>(null);
const acEl = ref<HTMLElement | null>(null);
const notesEl = ref<HTMLElement | null>(null);
// PAI-433. The report-summary block joined the watched dep array
// but was missing its DOM ref + highlightDom call, so matches in
// the new field rendered without yellow highlighting.
const reportSummaryEl = ref<HTMLElement | null>(null);

watch([() => search.query, descHtml, acHtml, notesHtml, reportSummaryHtml], () => {
  nextTick(() => {
    if (descEl.value) highlightDom(descEl.value, search.query);
    if (acEl.value) highlightDom(acEl.value, search.query);
    if (notesEl.value) highlightDom(notesEl.value, search.query);
    if (reportSummaryEl.value) highlightDom(reportSummaryEl.value, search.query);
  });
});

// Prev/next navigation
const currentIdx = computed(() => {
  if (!props.issueIds || !props.issueId) return -1;
  return props.issueIds.indexOf(props.issueId);
});
const canPrev = computed(() => currentIdx.value > 0);
const canNext = computed(() =>
  props.issueIds ? currentIdx.value < props.issueIds.length - 1 : false,
);
async function goPrev() {
  if (canPrev.value && props.issueIds && await requestLeave())
    emit("navigate", props.issueIds[currentIdx.value - 1]);
}
async function goNext() {
  if (canNext.value && props.issueIds && await requestLeave())
    emit("navigate", props.issueIds[currentIdx.value + 1]);
}

// Tag management
const issueTagIds = computed(() => issue.value?.tags?.map((t) => t.id) ?? []);
async function addTag(tagId: number) {
  if (!issue.value || props.embedded) return;
  await api.post(`/issues/${issue.value.id}/tags`, { tag_id: tagId });
  issue.value = await api.get<Issue>(`/issues/${issue.value.id}`);
  emit("updated", issue.value);
}
async function removeTag(tagId: number) {
  if (!issue.value || props.embedded) return;
  await api.delete(`/issues/${issue.value.id}/tags/${tagId}`);
  issue.value = await api.get<Issue>(`/issues/${issue.value.id}`);
  emit("updated", issue.value);
}

const quickSavingField = ref<"" | "status" | "assignee_id">("");
const quickError = ref("");

async function quickUpdateIssueField(
  field: "status" | "assignee_id",
  value: string,
) {
  if (!issue.value || !issueMutationAllowed.value || quickSavingField.value) return;
  const payload =
    field === "assignee_id"
      ? { assignee_id: value ? Number(value) : null }
      : { status: value };
  const loaded = issue.value;
  const authorityEpoch = issueMutationAuthorityEpoch;
  const operation = ++issueMutationOperationSequence;
  quickSavingField.value = field;
  quickError.value = "";
  try {
    const updated = await saveIssueDetail(
      loaded.id,
      payload,
      issueIfMatch(loaded.id, loaded.updated_at),
    );
    if (
      props.issueId !== loaded.id ||
      issue.value?.id !== loaded.id ||
      !issueMutationAllowed.value ||
      authorityEpoch !== issueMutationAuthorityEpoch
    ) return;
    issue.value = updated;
    resetForm();
    emit("updated", updated);
  } catch (e: unknown) {
    if (
      props.issueId !== loaded.id ||
      !issueMutationAllowed.value ||
      authorityEpoch !== issueMutationAuthorityEpoch
    ) return;
    if (e instanceof ApiError && e.status === 412) {
      const expectedLoadSequence = issueLoadSequence + 1;
      const expectedAuthorityEpoch = issueMutationAuthorityEpoch + 1;
      const reloaded = await loadIssue(props.issueId);
      if (
        props.issueId !== loaded.id ||
        !issueMutationAllowed.value ||
        issueLoadSequence !== expectedLoadSequence ||
        issueMutationAuthorityEpoch !== expectedAuthorityEpoch
      ) return;
      quickError.value = reloaded
        ? "This ticket changed elsewhere. Latest values were reloaded; review and try again."
        : "This ticket changed elsewhere and the latest values could not be reloaded. Reopen the ticket and try again.";
    } else if (!isSessionExpiredError(e)) {
      quickError.value = errMsg(e, "Update failed.");
    }
  } finally {
    if (operation === issueMutationOperationSequence) quickSavingField.value = "";
  }
}

let issueLoadSequence = 0;
async function loadIssue(id: number | null): Promise<boolean> {
  const sequence = ++issueLoadSequence;
  issueMutationAuthorityEpoch += 1;
  issueMutationOperationSequence += 1;
  // This identity/reload now owns the editor state. A mutation invalidated by
  // the generation bumps above must not leave its busy state attached here
  // when that older promise eventually settles.
  saving.value = false;
  quickSavingField.value = "";
  clearEditorMetadata();
  if (!id) {
    issue.value = null;
    editing.value = false;
    commentDirty.value = false;
    savedSnapshot.value = "";
    attachments.reset();
    loading.value = false;
    return false;
  }
  // Never retain the previous ticket while a new selection is loading.
  issue.value = null;
  editing.value = false;
  commentDirty.value = false;
  savedSnapshot.value = "";
  quickError.value = "";
  saveError.value = "";
  panelGuardError.value = "";
  attachments.reset();
  loading.value = true;
  try {
    const loaded = await api.get<Issue>(`/issues/${id}`);
    if (sequence !== issueLoadSequence || props.issueId !== id) return false;
    issue.value = loaded;
    resetForm();
    savedSnapshot.value = "";
    editing.value = !props.readonly && !!props.startInEdit;
    if (editing.value) savedSnapshot.value = JSON.stringify(form.value);
    commentDirty.value = false;
    void loadAttachments(loaded);
    void loadEmbeddedEditorMetadata(loaded);
    return true;
  } catch (e: unknown) {
    if (sequence !== issueLoadSequence || props.issueId !== id) return false;
    if (!isSessionExpiredError(e)) issue.value = null;
    return false;
  } finally {
    if (sequence === issueLoadSequence) loading.value = false;
  }
}

watch(
  () => props.issueId,
  (id) => void loadIssue(id),
  { immediate: true },
);

function resetForm() {
  if (!issue.value) return;
  const i = issue.value;
  form.value = {
    title: i.title,
    description: i.description,
    acceptance_criteria: i.acceptance_criteria,
    notes: i.notes,
    report_summary: i.report_summary,
    status: i.status,
    priority: i.priority,
    type: i.type,
    assignee_id: i.assignee_id != null ? String(i.assignee_id) : "",
    parent_id: i.parent_id != null ? String(i.parent_id) : "",
    cost_unit: i.cost_unit?.label ?? "",
    release: i.release?.label ?? "",
    estimate_hours: i.estimate_hours,
    estimate_lp: i.estimate_lp,
    ar_hours: i.ar_hours,
    ar_lp: i.ar_lp,
    time_override: i.time_override,
  };
}

watch(issueMutationAllowed, (allowed) => {
  issueMutationAuthorityEpoch += 1;
  if (!allowed) {
    // Security outranks preserving a local draft: once edit authority is
    // revoked, remove the writable surface and invalidate every response
    // that started under the previous authority epoch.
    editing.value = false;
    saving.value = false;
    quickSavingField.value = "";
    issueMutationOperationSequence += 1;
    commentDirty.value = false;
    savedSnapshot.value = "";
    quickError.value = "";
    saveError.value = "";
    panelGuardError.value = "";
    resetForm();
    clearEditorMetadata();
    attachments.reset();
    if (issue.value) void loadAttachments(issue.value);
    return;
  }
  if (issue.value) void loadEmbeddedEditorMetadata(issue.value);
});

watch(attachmentsAllowed, (allowed, wasAllowed) => {
  if (allowed || !wasAllowed) return;
  // A revoked attachment capability must remove pending/failed upload jobs.
  // Existing attachments are reloaded read-only under the still-authorized
  // issue identity; late upload callbacks mutate only detached job objects.
  attachments.reset();
  if (issue.value) void loadAttachments(issue.value);
});

function startEdit() {
  if (!issueMutationAllowed.value || commentInFlight.value) {
    if (commentInFlight.value) {
      panelGuardError.value = "An internal note is still posting. Wait for it to finish before editing this ticket.";
    }
    return;
  }
  resetForm();
  savedSnapshot.value = JSON.stringify(form.value);
  editing.value = true;
  loadParentCandidates();
}
function cancelEdit() {
  editing.value = false;
  resetForm();
  resetDirty();
}

const currentSnapshot = computed(() =>
  editing.value ? JSON.stringify(form.value) : "",
);
const {
  isDirty,
  reset: resetDirty,
} = useDirtyGuard(currentSnapshot, savedSnapshot);
const hasUnsavedChanges = computed(() => isDirty.value || commentDirty.value);

watch(
  [hasUnsavedChanges, hasInFlight],
  ([dirty, inFlight]) => emit("guard-state", { dirty, inFlight }),
  { immediate: true },
);

/** Shared handshake for parent selection changes, close, and panel-local
 * navigation. Uploads cannot be reassigned mid-flight; dirty text requires
 * explicit discard. */
async function requestLeave(): Promise<boolean> {
  panelGuardError.value = "";
  if (commentInFlight.value) {
    panelGuardError.value = "An internal note is still posting. Wait for it to finish before leaving this ticket.";
    return false;
  }
  if (saving.value || quickSavingField.value !== "") {
    panelGuardError.value = "A ticket update is still saving. Wait for it to finish before leaving this ticket.";
    return false;
  }
  if (attachments.hasInFlight.value) {
    panelGuardError.value = "An attachment upload is still in progress. Wait for it to finish or remove it before leaving this ticket.";
    return false;
  }
  if (!hasUnsavedChanges.value) return true;
  const allowed = await confirm({
    message: "You have unsaved ticket edits or an unposted note. Discard and continue?",
    confirmLabel: "Discard",
    danger: true,
  });
  return allowed;
}

async function requestClose() {
  if (await requestLeave()) emit("close");
}

function onPanelKeydown(event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  const target = event.target as HTMLElement | null;
  if (target?.closest("input, textarea, select, button, a, [contenteditable='true']")) return;
  event.preventDefault();
  void requestClose();
}

function onPanelPaste(event: ClipboardEvent) {
  if (!attachmentsAllowed.value) return;
  const files = Array.from(event.clipboardData?.files ?? []);
  if (files.length === 0) return; // ordinary text paste keeps native behaviour
  event.preventDefault();
  attachments.addFiles(files);
}

function onPanelDragover(event: DragEvent) {
  if (!attachmentsAllowed.value) return;
  if (Array.from(event.dataTransfer?.types ?? []).includes("Files")) event.preventDefault();
}

function onPanelDrop(event: DragEvent) {
  // The existing AttachmentSidebar drop zone handles its own event first.
  // Avoid adding those files twice when that event bubbles to the panel.
  if (event.defaultPrevented || !attachmentsAllowed.value) return;
  const files = Array.from(event.dataTransfer?.files ?? []);
  if (files.length === 0) return;
  event.preventDefault();
  attachments.addFiles(files);
}

function addAttachmentFiles(files: FileList) {
  if (!attachmentsAllowed.value) return;
  attachments.addFiles(files);
}

async function removeAttachment(job: AttachmentJob) {
  if (!attachmentsAllowed.value) return;
  await attachments.removeJob(job);
}

function retryAttachment(job: AttachmentJob) {
  if (!attachmentsAllowed.value) return;
  attachments.retryJob(job);
}

defineExpose({ requestLeave, hasUnsavedChanges, hasInFlight });

async function save() {
  if (!issue.value || !issueMutationAllowed.value || !editing.value) return;
  const loaded = issue.value;
  const authorityEpoch = issueMutationAuthorityEpoch;
  const operation = ++issueMutationOperationSequence;
  saving.value = true;
  saveError.value = "";
  try {
    const payload = {
      ...form.value,
      assignee_id: form.value.assignee_id
        ? Number(form.value.assignee_id)
        : null,
      parent_id: form.value.parent_id ? Number(form.value.parent_id) : null,
    };
    const updated = await saveIssueDetail(
      loaded.id,
      payload,
      issueIfMatch(loaded.id, loaded.updated_at),
    );
    if (
      props.issueId !== loaded.id ||
      issue.value?.id !== loaded.id ||
      !issueMutationAllowed.value ||
      authorityEpoch !== issueMutationAuthorityEpoch
    ) return;
    issue.value = updated;
    editing.value = false;
    savedSnapshot.value = ""; // reset dirty guard
    emit("updated", updated);
  } catch (e: unknown) {
    if (
      props.issueId !== loaded.id ||
      !issueMutationAllowed.value ||
      authorityEpoch !== issueMutationAuthorityEpoch
    ) return;
    if (e instanceof ApiError && e.status === 412) {
      const expectedLoadSequence = issueLoadSequence + 1;
      const expectedAuthorityEpoch = issueMutationAuthorityEpoch + 1;
      const reloaded = await loadIssue(props.issueId);
      if (
        props.issueId !== loaded.id ||
        !issueMutationAllowed.value ||
        issueLoadSequence !== expectedLoadSequence ||
        issueMutationAuthorityEpoch !== expectedAuthorityEpoch
      ) return;
      saveError.value = reloaded
        ? "This ticket changed elsewhere. Latest values were reloaded; review and re-apply your edit."
        : "This ticket changed elsewhere and the latest values could not be reloaded. Reopen the ticket and try again.";
    } else if (!isSessionExpiredError(e)) {
      saveError.value = errMsg(e, "Save failed.");
    }
  } finally {
    if (operation === issueMutationOperationSequence) saving.value = false;
  }
}

async function openFull() {
  if (!issue.value) return;
  const editParam = editing.value ? "?edit=1" : "";
  if (!await requestLeave()) return;
  router.push(
    `/projects/${issue.value.project_id}/issues/${issue.value.id}${editParam}`,
  );
}

const cloning = ref(false);
async function cloneIssue() {
  if (!issue.value || props.embedded || cloning.value) return;
  cloning.value = true;
  try {
    const clone = await api.post<Issue>(`/issues/${issue.value.id}/clone`, {});
    // Navigate to the cloned issue in full view for editing
    router.push(`/projects/${clone.project_id}/issues/${clone.id}?edit=1`);
  } catch (e: unknown) {
    saveError.value = errMsg(e, "Clone failed.");
  } finally {
    cloning.value = false;
  }
}

function togglePin() {
  emit("update:pinned", !props.pinned);
}

// Forward wheel events from the transparent full-viewport backdrop to whatever
// scroll container sits beneath, so the list stays scrollable while the panel
// is open in unpinned mode. Without this, wheel events land on the backdrop
// (position: fixed), the scroll chain walks the containing-block chain to the
// viewport (overflow: visible), and nothing scrolls. PAI-16.
function onBackdropWheel(e: WheelEvent) {
  if (e.ctrlKey) return; // let browser zoom work
  const backdrop = e.currentTarget as HTMLElement;
  // Temporarily disable backdrop's hit-testing so elementFromPoint returns the
  // element visually underneath it. Restored immediately, synchronously.
  const prevPE = backdrop.style.pointerEvents;
  backdrop.style.pointerEvents = "none";
  const hit = document.elementFromPoint(
    e.clientX,
    e.clientY,
  ) as HTMLElement | null;
  backdrop.style.pointerEvents = prevPE;
  if (!hit) return;
  let el: HTMLElement | null = hit;
  while (el && el !== document.body) {
    const cs = getComputedStyle(el);
    const scrollableY =
      (cs.overflowY === "auto" || cs.overflowY === "scroll") &&
      el.scrollHeight > el.clientHeight;
    const scrollableX =
      (cs.overflowX === "auto" || cs.overflowX === "scroll") &&
      el.scrollWidth > el.clientWidth;
    if (scrollableY && e.deltaY) {
      el.scrollTop += e.deltaY;
      return;
    }
    if (scrollableX && e.deltaX) {
      el.scrollLeft += e.deltaX;
      return;
    }
    el = el.parentElement;
  }
}

// Type icon SVG lookup
const typeIcon = computed(() => {
  if (!issue.value) return "";
  return TYPE_SVGS[issue.value.type] ?? "";
});

// ── Resizable sidebar ────────────────────────────────────────────────────────
// `width` is the committed (persisted, layout-affecting) value shared with
// IssueList via useSidePanelWidth. During an active drag we use a local
// `draftWidth` for smooth visual feedback so the IssueList offset doesn't
// reflow on every mousemove — the new value lands in `width` only at drag-end.
const { width } = useSidePanelWidth();
const draftWidth = ref(width.value);
const resizing = ref(false);

watch(width, (v) => {
  if (!resizing.value) draftWidth.value = v;
});

function onResizeStart(e: MouseEvent) {
  e.preventDefault();
  resizing.value = true;
  const startX = e.clientX;
  const startW = draftWidth.value;
  const maxW = Math.round(window.innerWidth * SIDE_PANEL_MAX_WIDTH_RATIO);

  function onMove(ev: MouseEvent) {
    const delta = startX - ev.clientX; // moving left = wider
    draftWidth.value = Math.min(
      maxW,
      Math.max(SIDE_PANEL_MIN_WIDTH, startW + delta),
    );
  }
  function onUp() {
    resizing.value = false;
    width.value = draftWidth.value;
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
  }
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

function resetWidth() {
  resetSidePanelWidth();
  draftWidth.value = SIDE_PANEL_DEFAULT_WIDTH;
}

// ── Time entries (view mode) ────────────────────────────────────────────────
const timeEntries = ref<TimeEntry[]>([]);
const showTimeEntries = ref(false);
let timeEntryLoadSequence = 0;

const isTimerIssue = computed(
  () => issue.value != null && timerStore.isRunning(issue.value.id),
);
const isTicketOrTask = computed(
  () => issue.value?.type === "ticket" || issue.value?.type === "task",
);
const totalHours = computed(() =>
  timeEntries.value.reduce((sum, e) => sum + (e.hours ?? 0), 0),
);

watch(
  () => props.issueId,
  async (id) => {
    const sequence = ++timeEntryLoadSequence;
    timeEntries.value = [];
    if (id && !props.embedded) {
      try {
        const loaded = await api.get<TimeEntry[]>(
          `/issues/${id}/time-entries`,
        );
        if (sequence !== timeEntryLoadSequence || props.issueId !== id) return;
        timeEntries.value = loaded;
      } catch {
        /* ignore */
      }
    }
    if (sequence !== timeEntryLoadSequence) return;
    // Auto-expand if there are entries or a running timer; collapse if empty
    showTimeEntries.value =
      timeEntries.value.length > 0 ||
      (issue.value != null && timerStore.isRunning(issue.value.id));
  },
);

async function toggleTimer() {
  if (!issue.value || props.embedded) return;
  if (isTimerIssue.value) {
    const entry = timerStore.getRunningEntry(issue.value.id);
    if (entry) await timerStore.stop(entry.id);
  } else {
    await timerStore.start(issue.value.id);
  }
  // Reload entries after timer action
  if (issue.value) {
    try {
      timeEntries.value = await api.get<TimeEntry[]>(
        `/issues/${issue.value.id}/time-entries`,
      );
    } catch {
      /* ignore */
    }
  }
}

// Move to trash (soft-delete — recoverable from Settings → Trash)
async function deleteIssue() {
  if (!issue.value || props.embedded) return;
  if (
    !(await confirm({
      message: `Move ${issue.value.issue_key} "${issue.value.title}" to Trash? Any child tasks will be moved too. You can restore from Settings → Trash.`,
      confirmLabel: "Move to trash",
      danger: true,
    }))
  )
    return;
  try {
    await api.delete(`/issues/${issue.value.id}`);
    emit("deleted", issue.value.id);
    emit("close");
  } catch (e: unknown) {
    /* error swallowed — panel stays open as feedback */
  }
}

// Sprint management
const sprintDropOpen = ref(false);

async function addSprint(sprintId: number) {
  if (!issue.value || props.embedded) return;
  await api.post(`/issues/${issue.value.id}/relations`, {
    target_id: sprintId,
    type: "sprint",
  });
  issue.value = {
    ...issue.value,
    sprint_ids: [...(issue.value.sprint_ids ?? []), sprintId],
  };
  emit("updated", issue.value);
  sprintDropOpen.value = false;
}
async function removeSprint(sprintId: number) {
  if (!issue.value || props.embedded) return;
  await api.delete(`/issues/${issue.value.id}/relations`, {
    target_id: sprintId,
    type: "sprint",
  });
  issue.value = {
    ...issue.value,
    sprint_ids: (issue.value.sprint_ids ?? []).filter((id) => id !== sprintId),
  };
  emit("updated", issue.value);
}

async function deleteTimeEntry(entry: TimeEntry) {
  if (props.embedded) return;
  const isOther = entry.user_id !== authStore.user?.id;
  const msg = isOther
    ? `You are deleting ${entry.username}'s time entry. You can undo this from Recent activity.`
    : "Delete this time entry? You can undo it from Recent activity.";
  if (!(await confirm({ message: msg, confirmLabel: "Delete", danger: true })))
    return;
  await api.delete(`/time-entries/${entry.id}`);
  timeEntries.value = timeEntries.value.filter((e) => e.id !== entry.id);
}
</script>

<template>
  <Transition name="sidepanel">
    <aside
      v-if="issueId || pinned"
      :class="[
        'side-panel',
        {
          'side-panel--pinned': pinned && !embedded,
          'side-panel--embedded': embedded,
          'side-panel--resizing': resizing,
        },
      ]"
      :style="embedded ? { width: '100%' } : { width: draftWidth + 'px' }"
      @keydown="onPanelKeydown"
      @paste="onPanelPaste"
      @dragover="onPanelDragover"
      @drop="onPanelDrop"
    >
      <div
        v-if="!pinned && !embedded"
        class="sp-backdrop"
        @click="requestClose"
        @wheel.passive="onBackdropWheel"
      />
      <div
        v-if="!embedded"
        class="sp-resize-handle"
        @mousedown="onResizeStart"
        @dblclick="resetWidth"
        title="Drag to resize · double-click to reset"
      />
      <div class="sp-content">
        <!-- Header -->
        <div class="sp-header">
          <button
            v-if="!embedded"
            class="sp-pin"
            :class="{ 'sp-pin--active': pinned }"
            @click="togglePin"
            :title="pinned ? 'Unpin sidebar' : 'Pin sidebar'"
          >
            <AppIcon :name="pinned ? 'pin' : 'pin-off'" :size="14" />
          </button>
          <span v-if="issue" class="sp-key">
            <span v-if="typeIcon" class="sp-type-icon" v-html="typeIcon" />
            {{ issue.issue_key }}
          </span>
          <span class="sp-spacer" />
          <!-- PAI-179: issue-level AI menu (find parent, generate
               sub-tasks, estimate, detect duplicates). Only mounts
               when an issue is loaded. -->
          <AiActionMenu
            v-if="issue && !readonly && !embedded"
            surface="issue"
            placement="issue"
            :host-key="`issue-side:${issue.id}:record`"
            field=""
            field-label="Issue"
            :issue-id="issue.id"
            :text="() => issue?.title ?? ''"
            :on-accept="
              () => {
                /* issue actions don't rewrite text */
              }
            "
          />
          <button
            v-if="issueIds && issueIds.length > 1"
            class="sp-action-btn"
            :disabled="!canPrev"
            @click="goPrev"
            title="Previous issue"
          >
            <AppIcon name="chevron-up" :size="15" />
          </button>
          <button
            v-if="issueIds && issueIds.length > 1"
            class="sp-action-btn"
            :disabled="!canNext"
            @click="goNext"
            title="Next issue"
          >
            <AppIcon name="chevron-down" :size="15" />
          </button>
          <button
            v-if="issue && !readonly && !embedded && authStore.isAdmin"
            class="sp-action-btn sp-action-btn--danger"
            @click="deleteIssue"
            title="Delete issue"
          >
            <AppIcon name="trash-2" :size="15" />
          </button>
          <button
            v-if="issue && !readonly && !embedded"
            class="sp-action-btn"
            @click="cloneIssue"
            :disabled="cloning"
            title="Clone issue"
          >
            <AppIcon name="copy" :size="15" />
          </button>
          <button
            v-if="issue && !readonly"
            class="sp-action-btn"
            :class="{ 'sp-action-btn--disabled': editing }"
            :disabled="editing || commentInFlight"
            @click="startEdit"
            title="Quick Edit"
          >
            <AppIcon name="pencil" :size="15" />
          </button>
          <button
            v-if="issue"
            class="sp-action-btn"
            @click="openFull"
            title="Open full view"
          >
            <AppIcon name="maximize-2" :size="15" />
          </button>
          <button
            class="sp-action-btn"
            @click="requestClose"
            title="Close"
            aria-label="Close ticket"
          >
            <AppIcon name="x" :size="15" />
          </button>
        </div>

        <AiSurfaceFeedback
          v-if="issue && !embedded"
          :host-key="`issue-side:${issue.id}:record`"
          :apply="applyAiPanelResult"
        />

        <div v-if="panelGuardError" class="sp-guard-error" role="alert">{{ panelGuardError }}</div>
        <div v-if="quickError" class="sp-quick-error" role="alert">{{ quickError }}</div>
        <div v-if="saveError" class="sp-quick-error" role="alert">{{ saveError }}</div>
        <div v-if="editorMetadataError" class="sp-quick-error" role="alert">{{ editorMetadataError }}</div>
        <LoadingText v-if="loading" class="sp-loading" label="Loading…" />

        <!-- View mode -->
        <template v-else-if="issue && !editing">
          <h2 class="sp-title">{{ issue.title }}</h2>
          <div v-if="allTags && !readonly && !embedded" class="sp-tags sp-tags--interactive">
            <TagSelector
              :all-tags="allTags"
              :selected-ids="issueTagIds"
              variant="pills"
              add-label="Add tag"
              @add="addTag"
              @remove="removeTag"
            />
          </div>
          <div v-else-if="issue.tags?.length" class="sp-tags">
            <TagChip v-for="t in issue.tags" :key="t.id" :tag="t" />
          </div>
          <div class="sp-meta">
            <IssueStatusSelect
              v-if="!readonly"
              :model-value="issue.status"
              :loading="quickSavingField === 'status'"
              size="sm"
              @update:model-value="quickUpdateIssueField('status', $event)"
            />
            <span v-else class="sp-meta-item">
              <StatusDot :status="issue.status" />
              {{ STATUS_LABEL[issue.status] ?? issue.status }}
            </span>
            <span
              class="sp-meta-item"
              :style="{ color: PRIORITY_COLOR[issue.priority] }"
              >{{ PRIORITY_LABEL[issue.priority] ?? issue.priority }}</span
            >
            <IssueAssigneeSelect
              v-if="!readonly"
              :model-value="issue.assignee_id !== null ? String(issue.assignee_id) : ''"
              :users="users"
              :fallback-user="issue.assignee"
              :loading="quickSavingField === 'assignee_id'"
              size="sm"
              @update:model-value="quickUpdateIssueField('assignee_id', $event)"
            />
            <span v-else-if="issue.assignee" class="sp-meta-item">{{
              issue.assignee.username
            }}</span>
            <span v-else class="sp-meta-item sp-muted">Unassigned</span>
            <!-- PAI-474: cost_unit, release, sprint membership, and the
                 estimate / AR rows are internal-only. In readonly mode
                 (Customer Portal) they are hidden unconditionally — the
                 backend also strips them from /api/portal/* responses
                 so they shouldn't be present, but the guard is a
                 second line of defence. -->
            <span
              v-if="issue.cost_unit && !readonly"
              class="sp-meta-item sp-meta-item--dim"
              >{{ issue.cost_unit?.label }}</span
            >
            <span v-if="issue.release && !readonly" class="sp-meta-item sp-meta-item--dim">{{
              issue.release?.label
            }}</span>
          </div>
          <!-- Sprints (click to edit) -->
          <div
            v-if="sprints?.length && !readonly"
            class="sp-sprints sp-sprints--clickable"
            @click="!readonly && startEdit()"
          >
            <AppIcon name="repeat" :size="12" class="sp-sprint-icon" />
            <SprintChips
              v-if="issue.sprint_ids?.length"
              :sprint-ids="issue.sprint_ids"
              :sprints="sprints"
              compact
            />
            <span v-else class="sp-muted">No sprints</span>
          </div>

          <!-- Estimate / AR — click to toggle h / PT — internal only. -->
          <div
            v-if="
              !readonly &&
              (issue.estimate_hours != null ||
                issue.estimate_lp != null ||
                issue.ar_hours != null ||
                issue.ar_lp != null)
            "
            class="sp-estimates"
          >
            <span
              v-if="issue.estimate_hours != null"
              class="sp-est-item sp-est-item--toggle"
              @click="toggleTimeUnit"
              title="Toggle h / PT"
              >Est.
              <span class="unit-toggle">{{
                formatHours(issue.estimate_hours, "detail")
              }}</span></span
            >
            <span v-if="issue.estimate_lp != null" class="sp-est-item"
              >Est. LP {{ issue.estimate_lp }}</span
            >
            <span
              v-if="issue.ar_hours != null"
              class="sp-est-item sp-est-item--toggle"
              @click="toggleTimeUnit"
              title="Toggle h / PT"
              >AR
              <span class="unit-toggle">{{
                formatHours(issue.ar_hours, "detail")
              }}</span></span
            >
            <span v-if="issue.ar_lp != null" class="sp-est-item"
              >AR LP {{ issue.ar_lp }}</span
            >
          </div>

          <!-- Time tracking (view mode, ticket/task only) — before description -->
          <div v-if="!readonly && !embedded" class="sp-time-section">
            <div
              class="sp-time-header"
              @click="showTimeEntries = !showTimeEntries"
            >
              <span class="sp-time-title">
                <AppIcon name="clock" :size="12" class="sp-time-clock" />
                Time
                <span v-if="isTimerIssue && issue" class="sp-timer-badge">{{
                  timerStore.formattedElapsed(
                    timerStore.getRunningEntry(issue.id)?.id ?? 0,
                  )
                }}</span>
                <span v-else-if="totalHours > 0" class="sp-time-badge"
                  >Total: {{ formatDuration(totalHours) }}</span
                >
              </span>
              <div class="sp-time-right">
                <button
                  v-if="isTimerIssue"
                  class="sp-te-action sp-te-action--stop"
                  @click.stop="toggleTimer"
                  title="Stop timer"
                >
                  <AppIcon name="square" :size="10" /> Stop
                </button>
                <button
                  v-else
                  class="sp-te-action"
                  @click.stop="toggleTimer"
                  title="Start timer"
                >
                  <AppIcon name="play" :size="10" /> Start
                </button>
                <AppIcon
                  :name="showTimeEntries ? 'chevron-up' : 'chevron-down'"
                  :size="12"
                />
              </div>
            </div>
            <div
              v-if="showTimeEntries && timeEntries.length"
              class="sp-time-entries"
            >
              <div v-for="e in timeEntries" :key="e.id" class="sp-te-row">
                <span class="sp-te-date">{{ e.started_at.slice(0, 10) }}</span>
                <span class="sp-te-hours">
                  <template v-if="e.stopped_at">{{
                    formatDuration(e.hours)
                  }}</template>
                  <AppIcon
                    v-else
                    name="clock"
                    :size="11"
                    class="sp-te-running-icon"
                  />
                </span>
                <span class="sp-te-comment">{{ e.comment || "—" }}</span>
                <button
                  v-if="
                    authStore.isAdmin ||
                    e.user_id === authStore.user?.id
                  "
                  class="sp-te-del"
                  @click="deleteTimeEntry(e)"
                  title="Delete"
                >
                  <AppIcon name="x" :size="10" />
                </button>
              </div>
            </div>
            <div
              v-else-if="showTimeEntries && !timeEntries.length"
              class="sp-muted"
              style="font-size: 12px"
            >
              No entries yet.
            </div>
          </div>

          <!-- Long text fields -->
          <div class="sp-body">
            <div class="sp-body-block" v-if="issue.description">
              <p class="sp-body-label">Description</p>
              <div
                ref="descEl"
                class="sp-body-text"
                :class="{ 'md-rendered': mdMode }"
                v-html="descHtml"
              />
            </div>
            <div class="sp-body-block" v-if="issue.acceptance_criteria">
              <p class="sp-body-label">Acceptance Criteria</p>
              <div
                ref="acEl"
                class="sp-body-text"
                :class="{ 'md-rendered': mdMode }"
                v-html="acHtml"
              />
            </div>
            <div class="sp-body-block" v-if="issue.notes">
              <p class="sp-body-label">Notes</p>
              <div
                ref="notesEl"
                class="sp-body-text"
                :class="{ 'md-rendered': mdMode }"
                v-html="notesHtml"
              />
            </div>
            <!-- PAI-433 / PAI-434. Always-render shape matches
                 IssueDetailBody: same em-dash placeholder when
                 empty, and the DOM node is referenced so the
                 search-highlight watcher can find it. Hiding the
                 block on empty made the field undiscoverable. -->
            <div
              class="sp-body-block"
              v-if="['epic', 'cost_unit', 'ticket'].includes(issue.type)"
            >
              <p class="sp-body-label">
                {{ t('reportSummary.label') }}
                <span class="sp-body-label-hint">{{ t('reportSummary.labelHint') }}</span>
              </p>
              <div
                v-if="issue.report_summary"
                ref="reportSummaryEl"
                class="sp-body-text"
                :class="{ 'md-rendered': mdMode }"
                v-html="reportSummaryHtml"
              />
              <span v-else class="sp-muted" style="font-size: 12px">—</span>
            </div>
            <div
              v-if="
                !issue.description &&
                !issue.acceptance_criteria &&
                !issue.notes &&
                !issue.report_summary
              "
              class="sp-muted"
            >
              No content.
            </div>
          </div>

          <MarkdownToolbar v-model="mdMode" :subtle="true" />

          <IssueAiActivity v-if="issue && !embedded" :issue-id="issue.id" />

          <!-- Attachments (view mode — read-only chip list, clickable thumbnails) -->
          <AttachmentSidebar
            v-if="attachments.jobs.value.length"
            class="sp-attach-sidebar"
            title="Attachments"
            :jobs="attachments.jobs.value"
            readonly
          />

          <IssueComments
            v-if="issue"
            :issue-id="issue.id"
            :md-mode="mdMode"
            :is-monospace="authStore.user?.monospace_fields ?? false"
            :can-edit="commentsAllowed"
            :internal-only="internalCommentsOnly"
            :compact="embedded"
            :composer-notice="noteAffectsNextRun ? t('agentMode.detail.nextRunNote') : null"
            @dirty-change="commentDirty = $event"
            @in-flight-change="commentInFlight = $event"
          />
        </template>

        <!-- Edit mode -->
        <template v-else-if="issue && editing">
          <div class="sp-form">
            <div class="field">
              <label>Title</label>
              <input v-model="form.title" type="text" />
            </div>
            <div class="sp-form-row">
              <div class="field" style="flex: 1">
                <label>Status</label>
                <IssueStatusSelect v-model="form.status" />
              </div>
              <div class="field" style="flex: 1">
                <label>Priority</label>
                <MetaSelect
                  v-model="form.priority"
                  :options="PRIORITY_OPTIONS"
                />
              </div>
            </div>
            <div class="field">
              <label>Assignee</label>
              <IssueAssigneeSelect v-model="form.assignee_id" :users="users" />
            </div>
            <div class="field" v-if="showParentPicker">
              <label>Parent</label>
              <MetaSelect
                v-model="form.parent_id"
                :options="parentOptions"
                placeholder="— None —"
                searchable
              />
            </div>
            <div class="sp-form-row">
              <div class="field" style="flex: 1" v-if="costUnits">
                <label>Cost Unit</label>
                <AutocompleteInput
                  v-model="form.cost_unit"
                  :suggestions="costUnits"
                  placeholder="e.g. CU-1"
                />
              </div>
              <div class="field" style="flex: 1" v-if="releases">
                <label>Release</label>
                <select v-model="form.release" class="v2-select">
                  <option value="">— None —</option>
                  <option v-for="r in releases" :key="r" :value="r">{{ r }}</option>
                </select>
              </div>
            </div>
            <!-- Sprint assignment -->
            <div v-if="sprints?.length && !embedded" class="field">
              <label>Sprints</label>
              <div class="sp-sprint-edit">
                <SprintChips
                  v-if="issue?.sprint_ids?.length"
                  :sprint-ids="issue.sprint_ids"
                  :sprints="sprints"
                  removable
                  compact
                  @remove="removeSprint"
                />
                <div class="sp-sprint-add-wrap">
                  <button
                    type="button"
                    class="sp-sprint-add"
                    @click="sprintDropOpen = !sprintDropOpen"
                  >
                    + Sprint
                  </button>
                  <div v-if="sprintDropOpen" class="sp-sprint-dropdown">
                    <button
                      v-for="s in sprints.filter(
                        (s) => !(issue?.sprint_ids ?? []).includes(s.id),
                      )"
                      :key="s.id"
                      type="button"
                      class="sp-sprint-opt"
                      @click="addSprint(s.id)"
                    >
                      {{ s.title }}
                    </button>
                    <div
                      v-if="
                        !sprints.filter(
                          (s) => !(issue?.sprint_ids ?? []).includes(s.id),
                        ).length
                      "
                      class="sp-sprint-empty"
                    >
                      All sprints assigned
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div class="sp-form-row">
              <div class="field" style="flex: 1">
                <label class="sp-label-toggle" @click="toggleTimeUnit"
                  >Est.
                  <span class="unit-toggle">{{ timeLabel() }}</span></label
                >
                <NumericInput v-model="form.estimate_hours" />
              </div>
              <div class="field" style="flex: 1">
                <label>Est. LP</label>
                <NumericInput v-model="form.estimate_lp" />
              </div>
            </div>
            <div class="sp-form-row">
              <div class="field" style="flex: 1">
                <label class="sp-label-toggle" @click="toggleTimeUnit"
                  >AR <span class="unit-toggle">{{ timeLabel() }}</span></label
                >
                <NumericInput v-model="form.ar_hours" />
              </div>
              <div class="field" style="flex: 1">
                <label>AR LP</label>
                <NumericInput v-model="form.ar_lp" />
              </div>
            </div>
            <!-- Time tracking in edit mode -->
            <div v-if="!readonly && !embedded" class="sp-time-section sp-time-section--edit">
              <div
                class="sp-time-header"
                @click="showTimeEntries = !showTimeEntries"
              >
                <span class="sp-time-title">
                  <AppIcon name="clock" :size="12" class="sp-time-clock" />
                  Time
                  <span v-if="isTimerIssue && issue" class="sp-timer-badge">{{
                    timerStore.formattedElapsed(
                      timerStore.getRunningEntry(issue.id)?.id ?? 0,
                    )
                  }}</span>
                  <span v-else-if="totalHours > 0" class="sp-time-badge">{{
                    formatDuration(totalHours)
                  }}</span>
                </span>
                <div class="sp-time-right">
                  <button
                    v-if="isTimerIssue"
                    class="sp-te-action sp-te-action--stop"
                    @click.stop="toggleTimer"
                    title="Stop timer"
                  >
                    <AppIcon name="square" :size="10" /> Stop
                  </button>
                  <button
                    v-else
                    class="sp-te-action"
                    @click.stop="toggleTimer"
                    title="Start timer"
                  >
                    <AppIcon name="play" :size="10" /> Start
                  </button>
                  <AppIcon
                    :name="showTimeEntries ? 'chevron-up' : 'chevron-down'"
                    :size="12"
                  />
                </div>
              </div>
              <div
                v-if="showTimeEntries && timeEntries.length"
                class="sp-time-entries"
              >
                <div v-for="e in timeEntries" :key="e.id" class="sp-te-row">
                  <span class="sp-te-date">{{
                    e.started_at.slice(0, 10)
                  }}</span>
                  <span class="sp-te-hours">
                    <template v-if="e.stopped_at">{{
                      formatDuration(e.hours)
                    }}</template>
                    <AppIcon
                      v-else
                      name="clock"
                      :size="11"
                      class="sp-te-running-icon"
                    />
                  </span>
                  <span class="sp-te-comment">{{ e.comment || "—" }}</span>
                  <button
                    v-if="
                      authStore.isAdmin ||
                      e.user_id === authStore.user?.id
                    "
                    class="sp-te-del"
                    @click="deleteTimeEntry(e)"
                    title="Delete"
                  >
                    <AppIcon name="x" :size="10" />
                  </button>
                </div>
              </div>
            </div>

            <div class="field">
              <div class="field-label-row">
                <label>Description</label>
                <AiActionMenu
                  v-if="!embedded"
                  surface="issue"
                  :host-key="`issue-side:${issue?.id ?? 0}:description`"
                  field="description"
                  field-label="Description"
                  :issue-id="issue?.id ?? 0"
                  :text="() => form.description"
                  :on-accept="onAiAccept('description')"
                />
              </div>
              <textarea v-model="form.description" rows="5" />
              <AiSurfaceFeedback
                v-if="!embedded"
                :host-key="`issue-side:${issue?.id ?? 0}:description`"
                :apply="applyAiPanelResult"
              />
            </div>
            <div class="field">
              <div class="field-label-row">
                <label>Acceptance Criteria</label>
                <AiActionMenu
                  v-if="!embedded"
                  surface="issue"
                  :host-key="`issue-side:${issue?.id ?? 0}:acceptance_criteria`"
                  field="acceptance_criteria"
                  field-label="Acceptance Criteria"
                  :issue-id="issue?.id ?? 0"
                  :text="() => form.acceptance_criteria"
                  :on-accept="onAiAccept('acceptance_criteria')"
                />
              </div>
              <textarea v-model="form.acceptance_criteria" rows="4" />
              <AiSurfaceFeedback
                v-if="!embedded"
                :host-key="`issue-side:${issue?.id ?? 0}:acceptance_criteria`"
                :apply="applyAiPanelResult"
              />
            </div>
            <div class="field">
              <div class="field-label-row">
                <label>Notes</label>
                <AiActionMenu
                  v-if="!embedded"
                  surface="issue"
                  :host-key="`issue-side:${issue?.id ?? 0}:notes`"
                  field="notes"
                  field-label="Notes"
                  :issue-id="issue?.id ?? 0"
                  :text="() => form.notes"
                  :on-accept="onAiAccept('notes')"
                />
              </div>
              <textarea v-model="form.notes" rows="3" />
              <AiSurfaceFeedback
                v-if="!embedded"
                :host-key="`issue-side:${issue?.id ?? 0}:notes`"
                :apply="applyAiPanelResult"
              />
            </div>
            <div
              class="field"
              v-if="['epic', 'cost_unit', 'ticket'].includes(form.type)"
            >
              <div class="field-label-row">
                <label>{{ t('reportSummary.label') }}</label>
                <AiActionMenu
                  v-if="!embedded"
                  surface="customer"
                  :host-key="`issue-side:${issue?.id ?? 0}:report_summary`"
                  field="report_summary"
                  :field-label="t('reportSummary.label')"
                  :issue-id="issue?.id ?? 0"
                  :text="() => form.report_summary"
                  :on-accept="onAiAccept('report_summary')"
                />
              </div>
              <textarea
                v-model="form.report_summary"
                rows="3"
                :placeholder="t('reportSummary.placeholder')"
              />
              <AiSurfaceFeedback
                v-if="!embedded"
                :host-key="`issue-side:${issue?.id ?? 0}:report_summary`"
                :apply="applyAiPanelResult"
              />
            </div>
            <!-- Attachments (edit mode — drop, upload, remove) -->
            <AttachmentSidebar
              v-if="attachmentsAllowed"
              class="sp-attach-sidebar sp-attach-sidebar--edit"
              title="Attachments"
              :jobs="attachments.jobs.value"
              @add-files="addAttachmentFiles"
              @remove="removeAttachment"
              @retry="retryAttachment"
            />
            <div class="sp-form-actions">
              <button class="btn btn-ghost btn-sm" @click="cancelEdit">
                Cancel
              </button>
              <button
                class="btn btn-primary btn-sm"
                :disabled="!issueMutationAllowed || saving || attachments.hasInFlight.value || commentInFlight"
                @click="save"
              >
                {{
                  saving
                    ? "Saving…"
                    : attachments.hasInFlight.value
                      ? `Uploading ${attachments.inFlightCount.value}…`
                      : "Save"
                }}
              </button>
            </div>

            <IssueAiActivity v-if="issue && !embedded" :issue-id="issue.id" />
          </div>
        </template>
        <div v-else class="sp-empty-state">
          <AppIcon name="inbox" :size="18" />
          <span>No issue selected</span>
        </div>
      </div>
    </aside>
  </Transition>

  <!-- PAI-146: AI optimize preview overlay. Single mount per panel
       instance; the composable is a singleton so opening from a
       textarea here uses the same slot as the detail view. -->
</template>

<style scoped>
/* PAI-146: per-field label row holds the label + the AI optimize
   button on the right. Mirrors the IssueDetailView treatment. */
.field-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}
.field-label-row > label {
  margin-bottom: 0;
}

.side-panel {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 200;
}
.side-panel--embedded {
  position: relative;
  inset: auto;
  z-index: 1;
  min-width: 0;
  min-height: 0;
  align-self: stretch;
}
.side-panel--embedded .sp-content {
  max-width: none;
  box-shadow: none;
  border-left: 1px solid var(--border);
  padding: 1rem;
}
.sp-backdrop {
  position: fixed;
  top: 0;
  left: 0;
  bottom: 0;
  right: 0;
  z-index: -1;
}
.side-panel--pinned {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  left: auto;
  z-index: 150;
  min-width: 300px;
}
.side-panel--resizing {
  user-select: none;
}
.sp-resize-handle {
  position: absolute;
  top: 0;
  left: -3px;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  z-index: 5;
}
.sp-resize-handle::after {
  content: "";
  position: absolute;
  top: 50%;
  left: 2px;
  width: 2px;
  height: 32px;
  transform: translateY(-50%);
  border-radius: 1px;
  background: var(--border);
  opacity: 0;
  transition: opacity 0.15s;
}
.sp-resize-handle:hover::after,
.side-panel--resizing .sp-resize-handle::after {
  opacity: 1;
  background: var(--brand-blue);
}
.sp-content {
  width: 100%;
  max-width: 90vw;
  height: 100%;
  background: var(--bg-card);
  border-left: 1px solid var(--border);
  box-shadow: -4px 0 24px rgba(0, 0, 0, 0.1);
  overflow-y: auto;
  padding: 1.25rem 1.5rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.side-panel--pinned .sp-content {
  max-width: none;
  box-shadow: none;
  border-left: 2px solid var(--border);
  padding-left: 1.25rem;
}
.sp-header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0.75rem;
  flex-shrink: 0;
}
.sp-pin {
  background: none;
  border: 1px solid transparent;
  cursor: pointer;
  padding: 3px;
  color: var(--text-muted);
  border-radius: 4px;
  display: flex;
  align-items: center;
}
.sp-pin:hover {
  background: var(--bg);
  color: var(--text);
}
.sp-pin--active {
  color: var(--brand-blue);
  border-color: var(--brand-blue);
  background: var(--brand-blue-pale);
}
.sp-key {
  font-size: 13px;
  font-weight: 700;
  color: var(--brand-blue-dark);
  background: var(--brand-blue-pale);
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}
.sp-type-icon {
  display: inline-flex;
  align-items: center;
}
.sp-type-icon :deep(svg) {
  width: 14px;
  height: 14px;
}
/* Nav buttons now use sp-action-btn style (right side of header) */
.sp-spacer {
  flex: 1;
}
.sp-action-btn {
  background: none;
  border: none;
  cursor: pointer;
  padding: 5px;
  color: var(--text-muted);
  border-radius: 50%;
  display: flex;
  align-items: center;
  transition:
    background 0.15s,
    color 0.15s;
}
.sp-action-btn:hover:not(:disabled) {
  background: var(--bg);
  color: var(--text);
}
.sp-action-btn:disabled {
  opacity: 0.3;
  cursor: default;
}
.sp-action-btn--danger {
  color: #dc2626;
}
.sp-action-btn--danger:hover:not(:disabled) {
  background: #fef2f2;
  color: #dc2626;
}
.sp-loading {
  color: var(--text-muted);
  font-size: 13px;
  padding: 2rem 0;
}
.sp-title {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  line-height: 1.3;
}
.sp-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  font-size: 13px;
  align-items: center;
}
.sp-meta-item {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}
.sp-meta-item--dim {
  color: var(--text-muted);
}
/* Status dot now rendered by StatusDot.vue component */
.sp-muted {
  color: var(--text-muted);
  font-style: italic;
  font-size: 13px;
}
.sp-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
}
.sp-tags--interactive {
  margin-top: -0.25rem;
}
.sp-quick-error {
  color: #b91c1c;
  font-size: 12px;
}
.sp-guard-error {
  padding: .5rem .6rem;
  border: 1px solid color-mix(in srgb, #b91c1c 35%, var(--border));
  border-radius: 6px;
  background: color-mix(in srgb, #b91c1c 6%, var(--bg-card));
  color: #b91c1c;
  font-size: 12px;
}
.sp-estimates {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  font-size: 12px;
  color: var(--text-muted);
  padding: 0.4rem 0;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.sp-est-item {
  white-space: nowrap;
}
.sp-est-item--toggle {
  cursor: pointer;
}
.sp-est-item--toggle:hover .unit-toggle {
  filter: brightness(0.85);
}
.sp-body {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.sp-body-block {
}
.sp-body-label {
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
  margin: 0 0 0.25rem;
}
.sp-body-label-hint {
  font-weight: 500;
  text-transform: none;
  letter-spacing: 0;
  margin-left: 0.4em;
  opacity: 0.75;
}
.sp-body-text {
  font-size: 13px;
  line-height: 1.55;
  color: var(--text);
  white-space: pre-wrap;
}
/* Markdown styles now in global .md-rendered class (App.vue) */
.sp-tag-selector {
  margin-top: auto;
  padding-top: 0.5rem;
  border-top: 1px solid var(--border);
}
.sp-empty-state {
  min-height: 220px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 0.55rem;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
}
.sp-empty-state :deep(svg) {
  opacity: .65;
}
.sp-attach-sidebar {
  /* Reset the inline-sidebar border/margin when mounted in the flow of the panel. */
  margin: 0.5rem -1.5rem 0;
  border-left: none;
  border-top: 1px solid var(--border);
  max-width: none;
  padding: 0.75rem 1.5rem;
}
.sp-attach-sidebar--edit {
  margin-top: 0.25rem;
}
.sp-form {
  display: flex;
  flex-direction: column;
  gap: 0.65rem;
  overflow-y: auto;
  flex: 1;
}
.sp-form .field {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
}
.sp-form .field label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.sp-form textarea {
  resize: vertical;
  max-width: 100%;
}
.sp-form-row {
  display: flex;
  gap: 0.75rem;
}
.sp-form-actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  margin-top: 0.5rem;
  position: sticky;
  bottom: 0;
  background: var(--bg-card);
  padding: 0.5rem 0;
}
.sp-label-toggle {
  cursor: pointer;
}
.unit-toggle {
  color: var(--brand-blue);
  font-weight: 600;
  text-decoration: underline;
  text-decoration-style: dotted;
}

/* Timer button */
.sp-timer-active {
  color: #22c55e !important;
  background: rgba(34, 197, 94, 0.1) !important;
}
.sp-timer-active:hover {
  background: rgba(34, 197, 94, 0.2) !important;
}

/* Sprints — chip styling lives in SprintChips.vue */
.sp-sprints {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
  align-items: center;
}
.sp-sprints--clickable {
  cursor: pointer;
  padding: 0.25rem 0.4rem;
  border-radius: var(--radius);
  transition: background 0.12s;
}
.sp-sprints--clickable:hover {
  background: var(--bg-hover, rgba(0, 0, 0, 0.04));
}
.sp-sprint-icon {
  color: var(--text-muted);
  flex-shrink: 0;
}
.sp-sprint-edit {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
  align-items: center;
}
.sp-sprint-add-wrap {
  position: relative;
}
.sp-sprint-add {
  background: none;
  border: 1px dashed var(--border);
  border-radius: 20px;
  padding: 0.1rem 0.5rem;
  font-size: 11px;
  color: var(--text-muted);
  cursor: pointer;
  font-family: inherit;
}
.sp-sprint-add:hover {
  border-color: var(--brand-blue);
  color: var(--brand-blue);
}
.sp-sprint-dropdown {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  z-index: 300;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  min-width: 180px;
  max-height: 200px;
  overflow-y: auto;
}
.sp-sprint-opt {
  display: block;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  padding: 0.4rem 0.65rem;
  font-size: 12px;
  cursor: pointer;
  font-family: inherit;
  color: var(--text);
}
.sp-sprint-opt:hover {
  background: var(--surface-2);
}
.sp-sprint-empty {
  padding: 0.4rem 0.65rem;
  font-size: 12px;
  color: var(--text-muted);
}

/* Time section */
.sp-time-section {
  border-top: 1px solid var(--border);
  padding: 0.65rem 1.5rem;
  margin: 0 -1.5rem;
}
.sp-time-section--edit {
  margin: 0;
  padding: 0.5rem 0;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.sp-action-label {
  font-size: 11px;
  font-weight: 600;
}
.sp-time-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  cursor: pointer;
  padding: 0.2rem 0;
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
}
.sp-time-title {
  display: flex;
  align-items: center;
  gap: 0.4rem;
}
.sp-time-right {
  display: flex;
  align-items: center;
  gap: 0.4rem;
}
.sp-time-clock {
  color: var(--text-muted);
}
.sp-te-action {
  background: none;
  border: none;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 0.2rem;
  font-size: 10px;
  font-weight: 600;
  color: var(--text-muted);
  padding: 0.15rem 0.4rem;
  border-radius: 4px;
  font-family: inherit;
  transition:
    color 0.1s,
    background 0.1s;
}
.sp-te-action:hover {
  color: var(--brand-green, #16a34a);
  background: color-mix(in srgb, var(--brand-green) 8%, transparent);
}
.sp-te-action--stop {
  color: var(--brand-green, #16a34a);
}
.sp-timer-badge {
  font-size: 10px;
  font-weight: 600;
  padding: 0.1rem 0.4rem;
  border-radius: 8px;
  background: rgba(34, 197, 94, 0.15);
  color: #22c55e;
  animation: timer-pulse 2s ease-in-out infinite;
}
@keyframes timer-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.6;
  }
}
.sp-time-badge {
  font-size: 10px;
  font-weight: 600;
  padding: 0.1rem 0.4rem;
  border-radius: 8px;
  background: var(--brand-blue-pale);
  color: var(--brand-blue);
}
.sp-time-entries {
  display: flex;
  flex-direction: column;
  gap: 1px;
  margin-top: 0.35rem;
}
.sp-te-row {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 11px;
  padding: 0.2rem 0.25rem;
  border-radius: 3px;
}
.sp-te-row:hover {
  background: var(--bg);
}
.sp-te-date {
  color: var(--text-muted);
  white-space: nowrap;
  min-width: 65px;
}
.sp-te-hours {
  font-weight: 600;
  white-space: nowrap;
  min-width: 40px;
  display: inline-flex;
  align-items: center;
}
.sp-te-running-icon {
  color: var(--brand-green, #16a34a);
}
.sp-te-comment {
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}
.sp-te-del {
  background: none;
  border: none;
  cursor: pointer;
  padding: 2px;
  color: var(--text-muted);
  border-radius: 3px;
  display: flex;
  opacity: 0;
  transition: opacity 0.1s;
}
.sp-te-row:hover .sp-te-del {
  opacity: 1;
}
.sp-te-del:hover {
  color: var(--danger);
  background: var(--bg);
}

/* Slide transition */
.sidepanel-enter-active,
.sidepanel-leave-active {
  transition:
    opacity 0.2s,
    transform 0.2s;
}
.sidepanel-enter-active .sp-content,
.sidepanel-leave-active .sp-content {
  transition: transform 0.2s;
}
.sidepanel-enter-from {
  opacity: 0;
}
.sidepanel-enter-from .sp-content {
  transform: translateX(100%);
}
.sidepanel-leave-to {
  opacity: 0;
}
.sidepanel-leave-to .sp-content {
  transform: translateX(100%);
}
</style>
