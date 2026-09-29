// SPDX-License-Identifier: AGPL-3.0-only
// AEON owns this bridge process and one documented Agent SDK Query handle.
// Lifecycle events are content-free. The explicit native_message frame carries
// bounded send arguments transiently to the owner; it is never journaled.
import { randomUUID } from "node:crypto";
import { realpathSync } from "node:fs";
import { isAbsolute } from "node:path";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

const [, , sdkPath, claudePath, workspace] = process.argv;
const MAX_INPUT_FRAME_BYTES = 2 * 1024 * 1024;
const MAX_PROMPT_BYTES = 256 * 1024;
const MAX_STEER_BYTES = 64 * 1024;
const MAX_PENDING_STEERS = 256;
const CORRELATION_TTL_MS = 60 * 1000;
const CONTROL_INPUT_TIMEOUT_MS = 30 * 1000;
// Built-in file tools reopen paths after hooks and cannot enforce descriptor
// ownership. All file access goes through the run-bound daemon MCP proxy.
const AEON_TOOLS = ["aeon_comment", "aeon_status", "aeon_check_criterion", "aeon_evidence",
  "aeon_request_approval", "aeon_reply", "aeon_terminal", "aeon_read", "aeon_write",
  "aeon_edit", "aeon_glob", "aeon_grep"];

function managedToolHook(allowedTools) {
  return async (input) => allowedTools.includes(input?.tool_name) ? {} : {
    hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "deny",
      permissionDecisionReason: "Only run-bound daemon tools are available" }
  };
}

function emit(frame) {
  process.stdout.write(JSON.stringify(frame) + "\n");
}

function fail(errorCode = "app_server_protocol", correlationID = "", reason = "") {
  emit({ kind: "control_failed", correlation_id: correlationID, error_code: errorCode, reason });
}

function validID(value, maximum = 256) {
  return typeof value === "string" && value.length > 0 && value.length <= maximum &&
    value.trim() === value && !/[\0\r\n]/u.test(value);
}

function validDispatchValue(value, maximum = 128) {
  return typeof value === "string" && value.length > 0 && value.length <= maximum &&
    /^[A-Za-z0-9][A-Za-z0-9._:-]*$/u.test(value);
}

class InputStream {
  constructor(first) {
    this.first = first;
    this.closed = false;
    this.closedPromise = new Promise((resolve) => { this.resolveClosed = resolve; });
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.first = null;
    this.resolveClosed();
  }

  async *[Symbol.asyncIterator]() {
    if (this.closed || !this.first) return;
    const first = this.first;
    this.first = null;
    yield first;
    if (!this.closed) await this.closedPromise;
  }
}

class ControlStream {
  constructor() {
    this.pending = null;
    this.receiver = null;
    this.closed = false;
  }

  send(message) {
    if (this.closed || this.pending) return Promise.reject(new Error("closed"));
    return new Promise((resolve, reject) => {
      const item = { message, resolve, reject };
      if (this.receiver) {
        const receiver = this.receiver;
        this.receiver = null;
        receiver({ value: item, done: false });
      } else {
        this.pending = item;
      }
    });
  }

  close() {
    this.abort(new Error("closed"));
  }

  abort(error) {
    if (this.closed) return;
    this.closed = true;
    this.pending?.reject(error);
    this.pending = null;
    if (this.receiver) {
      this.receiver({ done: true });
      this.receiver = null;
    }
  }

  async next() {
    if (this.pending) {
      const item = this.pending;
      this.pending = null;
      return { value: item, done: false };
    }
    if (this.closed) return { done: true };
    return await new Promise((resolve) => { this.receiver = resolve; });
  }

  async *[Symbol.asyncIterator]() {
    while (!this.closed) {
      const next = await this.next();
      if (next.done) return;
      next.value.resolve();
      yield next.value.message;
    }
  }
}

function userMessage(text, uuid = undefined) {
  return {
    type: "user",
    message: { role: "user", content: [{ type: "text", text }] },
    parent_tool_use_id: null,
    origin: { kind: "human" },
    ...(uuid ? { uuid } : {})
  };
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
const bufferedLines = [];
let firstLineWaiter = null;
let controlHandler = null;
let inputClosed = false;
lines.on("line", (line) => {
  if (firstLineWaiter) {
    const waiter = firstLineWaiter;
    firstLineWaiter = null;
    waiter.resolve(line);
  } else if (controlHandler) {
    controlHandler(line);
  } else {
    bufferedLines.push(line);
  }
});
lines.on("close", () => {
  inputClosed = true;
  if (firstLineWaiter) {
    firstLineWaiter.reject(new Error("closed"));
    firstLineWaiter = null;
  }
});
let firstLine;
try {
  if (bufferedLines.length) firstLine = bufferedLines.shift();
  else if (inputClosed) throw new Error("closed");
  else firstLine = await new Promise((resolve, reject) => { firstLineWaiter = { resolve, reject }; });
} catch {
  fail();
  process.exit(1);
}
if (Buffer.byteLength(firstLine) > MAX_INPUT_FRAME_BYTES) {
  fail("event_stream_bound");
  process.exit(1);
}
let start;
try {
  start = JSON.parse(firstLine);
} catch {
  fail();
  process.exit(1);
}
if ((start?.rules !== undefined && (typeof start.rules !== "string" || Buffer.byteLength(start.rules) > 12000)) ||
    (start?.max_turns !== undefined && (!Number.isSafeInteger(start.max_turns) || start.max_turns < 0)) ||
    (start?.max_tokens !== undefined && (!Number.isSafeInteger(start.max_tokens) || start.max_tokens < 0)) ||
    start?.op !== "start" || typeof start.prompt !== "string" || start.prompt.length === 0 ||
    Buffer.byteLength(start.prompt) > MAX_PROMPT_BYTES || start.prompt.includes("\0") ||
    ((start.model !== undefined || start.effort !== undefined) &&
     (!validDispatchValue(start.model) || !["low", "medium", "high", "xhigh", "max"].includes(start.effort))) ||
    !sdkPath || !claudePath || !workspace) {
  fail();
  process.exit(1);
}

let queryHandle;
let input;
let controlInput;
let stopping = false;
let verificationSucceeded = false;
let sessionStarted = false;
let sessionID = "";
let effectiveModel = "";
let effectiveEffort = start.effort || "";
let modelEvidenceStatus = "";
let initialTurnStarted = false;
let completedTurns = 0;
let turnActive = false;
let interruptReceipt = false;
const correlations = new Map();
function deleteCorrelation(uuid) {
  const state = correlations.get(uuid);
  if (state?.timer) clearTimeout(state.timer);
  correlations.delete(uuid);
}

function addCorrelation(uuid, correlationID) {
  const state = { correlationID, reacted: false, applied: false, expiresAt: Date.now() + CORRELATION_TTL_MS };
  state.reaction = new Promise((resolve) => { state.resolveReaction = resolve; });
  state.timer = setTimeout(() => deleteCorrelation(uuid), CORRELATION_TTL_MS);
  state.timer.unref?.();
  correlations.set(uuid, state);
  return state;
}

function expireCorrelations() {
  const now = Date.now();
  for (const [uuid, state] of correlations) if (state.expiresAt <= now) deleteCorrelation(uuid);
}

function observeReaction(message) {
  if (message?.type !== "assistant" && message?.type !== "stream_event") return;
  const uuid = message?.user_message_uuid;
  const state = correlations.get(uuid);
  if (!state || state.reacted) return;
  state.reacted = true;
  state.resolveReaction();
  turnActive = true;
  emit({ kind: "turn_started", correlation_id: state.correlationID });
  if (state.applied) deleteCorrelation(uuid);
}

function observeTurnActivity(message) {
  if (message?.type === "assistant" || message?.type === "stream_event") turnActive = true;
}

function observeTool(message) {
  if (message?.type === "assistant" && Array.isArray(message.message?.content) &&
      message.message.content.some((block) => block?.type === "tool_use")) {
    emit({ kind: "tool_started" });
    return;
  }
  if (message?.type === "stream_event" && message.event?.type === "content_block_start" &&
      message.event?.content_block?.type === "tool_use") emit({ kind: "tool_started" });
}

function observeUsage(message) {
  // modelUsage includes subagents and sidechains and is cumulative for Query.
  // Missing, malformed or overflowing usage cannot prove remaining budget.
  if (!message.modelUsage || typeof message.modelUsage !== "object" ||
      Array.isArray(message.modelUsage) || Object.keys(message.modelUsage).length === 0) return null;
  let input = 0, output = 0;
  for (const usage of Object.values(message.modelUsage)) {
    if (!usage || typeof usage !== "object") return null;
    const values = [usage.inputTokens, usage.outputTokens,
      usage.cacheCreationInputTokens ?? 0, usage.cacheReadInputTokens ?? 0];
    if (values.some((value) => !Number.isSafeInteger(value) || value < 0)) return null;
    input += values[0] + values[2] + values[3];
    output += values[1];
  }
  if (!Number.isSafeInteger(input + output)) return null;
  const frame = { kind: "usage", input_tokens_total: input, output_tokens_total: output };
  if (typeof message.total_cost_usd === "number" && Number.isFinite(message.total_cost_usd) &&
      message.total_cost_usd >= 0) frame.cost_usd_total = message.total_cost_usd;
  emit(frame);
  return input + output;
}

try {
  if (start.native_messages !== undefined) throw new Error("direct message targets are unavailable in AEON");
  const physicalWorkspace = realpathSync(workspace);
  if (!isAbsolute(workspace) || physicalWorkspace !== workspace) throw new Error("workspace is not physical");
  const { query } = await import(pathToFileURL(sdkPath));
  const verification = start.purpose === "pairing_verification";
  if (verification && start.tools != null) throw new Error("verification cannot have tools");
  const toolBinding = start.tools ?? undefined;
  if (toolBinding !== undefined &&
      (typeof toolBinding?.url !== "string" || !/^http:\/\/127\.0\.0\.1:[0-9]+$/u.test(toolBinding.url) ||
       typeof toolBinding?.token !== "string" || !/^[0-9a-f]{64}$/u.test(toolBinding.token))) {
    throw new Error("invalid managed tool binding");
  }
  const mcpServers = toolBinding ? { aeon: { type: "http", url: toolBinding.url,
    headers: { Authorization: `Bearer ${toolBinding.token}` } } } : {};
  const allowedTools = !verification && toolBinding ? AEON_TOOLS.map((name) => `mcp__aeon__${name}`) : [];
  start.tools = undefined;
  input = new InputStream(userMessage(start.prompt));
  start.prompt = "";
  const queryOptions = {
    cwd: workspace,
    pathToClaudeCodeExecutable: claudePath,
    persistSession: false,
    settingSources: [],
    strictMcpConfig: true,
    mcpServers,
    plugins: [],
    includePartialMessages: true,
    permissionMode: "dontAsk",
    additionalDirectories: [],
    hooks: { PreToolUse: [{ matcher: ".*", hooks: [managedToolHook(allowedTools)] }] },
    allowedTools,
    tools: [],
    ...(verification ? { maxTurns: 1, canUseTool: async () => ({ behavior: "deny", message: "Verification has no tools" }) } : {}),
    systemPrompt: { type: "preset", preset: "claude_code", ...(start.rules ? { append: start.rules } : {}) },
    ...(!verification && Number.isSafeInteger(start.max_turns) && start.max_turns > 0 ? { maxTurns: start.max_turns } : {}),
    ...(start.model ? { model: start.model, effort: start.effort } : {})
  };
  queryHandle = query({
    prompt: input,
    options: queryOptions
  });
  if (!queryHandle || typeof queryHandle.streamInput !== "function" ||
      typeof queryHandle.interrupt !== "function" || typeof queryHandle.close !== "function" ||
      typeof queryHandle[Symbol.asyncIterator] !== "function") throw new Error("query capabilities");
  controlInput = new ControlStream();
  queryHandle.streamInput(controlInput).catch(() => { controlInput.abort(new Error("stream input failed")); });
} catch {
  fail("app_server_protocol", "", "sdk_query_capability_missing");
  process.exit(1);
}

let controlChain = Promise.resolve();
let queryEndedResolve;
const queryEnded = new Promise((resolve) => { queryEndedResolve = resolve; });
async function streamInputBound(message) {
  let timer;
  try {
    return await Promise.race([
      controlInput.send(message),
      queryEnded.then(() => { throw new Error("Query ended before input acknowledgement"); }),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("Query input acknowledgement timed out")), CONTROL_INPUT_TIMEOUT_MS);
      })
    ]);
  } finally {
    clearTimeout(timer);
  }
}
const handleControlLine = (line) => {
  controlChain = controlChain.then(async () => {
    if (Buffer.byteLength(line) > MAX_INPUT_FRAME_BYTES) {
      fail("event_stream_bound");
      queryHandle.close();
      return;
    }
    let request;
    try {
      request = JSON.parse(line);
    } catch {
      fail();
      return;
    }
    const correlationID = request?.correlation_id;
    if (!validID(correlationID, 128)) {
      fail();
      return;
    }
    let controlUUID = "";
    let fatal = false;
    let failureReason = "control_failed";
    try {
      if (start.purpose === "pairing_verification" && request.op !== "stop") {
        fail("app_server_protocol", correlationID, "verification_control_forbidden");
        return;
      }
      if (request.op === "steer" || request.op === "inbox") {
        expireCorrelations();
        if (typeof request.text !== "string" || request.text.length === 0 ||
            Buffer.byteLength(request.text) > MAX_STEER_BYTES || request.text.includes("\0") || (request.op === "steer" && !interruptReceipt)) {
          fail("app_server_protocol", correlationID);
          return;
        }
        if (correlations.size >= MAX_PENDING_STEERS) {
          fail("event_stream_bound", correlationID);
          return;
        }
        const uuid = randomUUID();
        controlUUID = uuid;
        const state = addCorrelation(uuid, correlationID);
        failureReason = "stream_input_failed";
        await streamInputBound(userMessage(request.text, uuid));
        request.text = "";
        if (request.op === "steer") {
          failureReason = "interrupt_receipt_failed";
          const receipt = await queryHandle.interrupt();
          if (!receipt || !Array.isArray(receipt.still_queued)) {
            fatal = true;
            throw new Error("receipt");
          }
        }
        if (request.op === "inbox") {
          // Consuming our iterator is not external acceptance. Require the
          // live Query's matching input UUID reaction before reporting handoff.
          failureReason = "input_reaction_unconfirmed";
          let reactionTimer;
          try {
            await Promise.race([
              state.reaction,
              queryEnded.then(() => { throw new Error("Query ended"); }),
              new Promise((_, reject) => { reactionTimer = setTimeout(() => reject(new Error("reaction timeout")), 20000); })
            ]);
          } finally { clearTimeout(reactionTimer); }
        }
        state.applied = true;
        emit({ kind: "control_applied", correlation_id: correlationID, vendor_message_id: uuid });
        if (state.reacted) deleteCorrelation(uuid);
        controlUUID = "";
      } else if (request.op === "model" || request.op === "effort") {
        failureReason = "setting_rejected";
        if (!sessionStarted || typeof queryHandle.supportedModels !== "function") throw new Error("unavailable");
        const value = request.value;
        if (!validDispatchValue(value)) throw new Error("invalid setting");
        const models = await queryHandle.supportedModels();
        const model = models.find(m => m.value === (request.op === "model" ? value : effectiveModel));
        const effort = request.op === "effort" ? value : effectiveEffort;
        if (!model || !["low", "medium", "high", "xhigh", "max"].includes(effort) ||
            model.supportsEffort === false ||
            (Array.isArray(model.supportedEffortLevels) && !model.supportedEffortLevels.includes(effort))) throw new Error("unsupported setting");
        if (request.op === "model") {
          if (typeof queryHandle.setModel !== "function") throw new Error("unsupported setter");
          await queryHandle.setModel(value);
          effectiveModel = value;
          emit({ kind: "settings_changed", effective_model: value });
        } else {
          if (typeof queryHandle.applyFlagSettings !== "function") throw new Error("unsupported setter");
          // Exact allowlist: no caller-provided settings object reaches the SDK.
          await queryHandle.applyFlagSettings({ effortLevel: value });
          effectiveEffort = value;
          emit({ kind: "settings_changed", effective_effort: value });
        }
        emit({ kind: "control_applied", correlation_id: correlationID });
      } else if (request.op === "interrupt") {
        if (!interruptReceipt) {
          fail("app_server_protocol", correlationID);
          return;
        }
        if (!turnActive) {
          fail("app_server_protocol", correlationID, "not_running");
          return;
        }
        const receipt = await queryHandle.interrupt();
        if (!receipt || !Array.isArray(receipt.still_queued)) {
          fatal = true;
          throw new Error("receipt");
        }
        if (!turnActive) {
          fail("app_server_protocol", correlationID, "not_running");
          return;
        }
        emit({ kind: "control_applied", correlation_id: correlationID });
      } else if (request.op === "stop") {
        stopping = true;
        controlInput.close();
        input.close();
        queryHandle.close();
        emit({ kind: "control_applied", correlation_id: correlationID });
        lines.close();
      } else {
        fail("app_server_protocol", correlationID);
        return;
      }
    } catch {
      if (controlUUID) deleteCorrelation(controlUUID);
      fail("app_server_protocol", correlationID, failureReason);
      if (fatal) {
        controlInput.close();
        input.close();
        queryHandle.close();
      }
    }
  }).catch(() => fail());
};
controlHandler = handleControlLine;
for (const buffered of bufferedLines.splice(0)) handleControlLine(buffered);

try {
  for await (const message of queryHandle) {
    if (message?.type === "system" && message.subtype === "init") {
      if (!validID(message.session_id) || !Array.isArray(message.capabilities) ||
          (start.purpose !== "pairing_verification" && !message.capabilities.includes("interrupt_receipt_v1"))) {
        fail("app_server_protocol", "", "interrupt_receipt_v1_missing");
        queryHandle.close();
        break;
      }
      const initModelMissing = message.model === undefined;
      if (!initModelMissing && !validDispatchValue(message.model)) {
        fail();
        queryHandle.close();
        break;
      }
      const initModel = initModelMissing ? "" : message.model;
      const initModelEvidenceStatus = initModelMissing ? "unverified" : "vendor_reported";
      interruptReceipt = true;
      if (!sessionStarted) {
        sessionStarted = true;
        sessionID = message.session_id;
        effectiveModel = initModel;
        modelEvidenceStatus = initModelEvidenceStatus;
        // Agent SDK system/init is emitted only after Query has accepted its
        // first streamed user input. This evidence annotates that owned
        // session; it cannot truthfully validate the first input in advance.
        emit({ kind: "session_started", harness_session_id: message.session_id,
          effective_model: effectiveModel, model_evidence_status: modelEvidenceStatus });
      } else if (message.session_id !== sessionID || initModel !== effectiveModel ||
          initModelEvidenceStatus !== modelEvidenceStatus) {
        fail();
        queryHandle.close();
        break;
      }
      if (!initialTurnStarted) {
        initialTurnStarted = true;
        turnActive = true;
        emit({ kind: "turn_started" });
      }
    }
    observeTurnActivity(message);
    observeReaction(message);
    observeTool(message);
    if (message?.type === "result") {
      turnActive = false;
      const tokens = observeUsage(message);
      completedTurns++;
      const reason = start.max_tokens > 0 && (tokens === null || tokens >= start.max_tokens) ? "token_budget_exhausted"
        : message.subtype === "error_max_turns" || (start.max_turns > 0 && completedTurns >= start.max_turns) ? "turn_budget_exhausted" : "";
      if (reason && start.purpose !== "pairing_verification") {
        // Enforce inside the Query owner too: remote telemetry cannot delay close.
        emit({ kind: "budget_exhausted", reason });
        stopping = true;
        controlInput.close(); input.close(); queryHandle.close();
        emit({ kind: "turn_completed" });
        break;
      }
      emit({ kind: "turn_completed" });
      if (start.purpose === "pairing_verification") {
        verificationSucceeded = message.subtype === "success" && message.is_error !== true;
        controlInput.close(); input.close(); queryHandle.close();
        break;
      }
    }
  }
  queryEndedResolve();
  lines.close();
  controlInput.close();
  input.close();
  queryHandle.close();
  await controlChain;
  if (!stopping && !verificationSucceeded) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
} catch {
  queryEndedResolve();
  lines.close();
  controlInput?.close();
  input?.close();
  queryHandle?.close();
  await controlChain.catch(() => {});
  if (!stopping && !verificationSucceeded) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
}
