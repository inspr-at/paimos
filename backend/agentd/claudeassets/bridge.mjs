// PAIMOS owns this bridge process and one documented Agent SDK Query handle.
// Lifecycle events are content-free. The explicit native_message frame carries
// bounded send arguments transiently to the owner; it is never journaled.
import { createRequire } from "node:module";
import { randomUUID } from "node:crypto";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

const [, , sdkPath, claudePath, workspace] = process.argv;
const MAX_INPUT_FRAME_BYTES = 2 * 1024 * 1024;
const MAX_PROMPT_BYTES = 256 * 1024;
const MAX_STEER_BYTES = 64 * 1024;
const MAX_PENDING_STEERS = 256;
const CORRELATION_TTL_MS = 60 * 1000;
const CONTROL_INPUT_TIMEOUT_MS = 30 * 1000;
const DEFAULT_TOOLS = ["Read", "Glob", "Grep", "Edit", "Write"];

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
if (start?.op !== "start" || typeof start.prompt !== "string" || start.prompt.length === 0 ||
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
let sessionStarted = false;
let sessionID = "";
let effectiveModel = "";
let modelEvidenceStatus = "";
let initialTurnStarted = false;
let turnActive = false;
let interruptReceipt = false;
const correlations = new Map();
const nativeMessages = new Map();
function nativeMessage(args) {
  const failure = { content: [{ type: "text", text: '{"error":"sender_unavailable"}' }], isError: true };
  if (stopping || nativeMessages.size >= 1 || Buffer.byteLength(JSON.stringify(args)) > 30 * 1024) return Promise.resolve(failure);
  return new Promise((resolve) => {
    const id = randomUUID();
    const timer = setTimeout(() => { nativeMessages.delete(id); resolve(failure); }, 20000);
    nativeMessages.set(id, { resolve, timer });
    emit({ kind: "native_message", correlation_id: id, arguments: args });
  });
}
function closeNativeMessages() {
  for (const pending of nativeMessages.values()) {
    clearTimeout(pending.timer);
    pending.resolve({ content: [{ type: "text", text: '{"error":"sender_unavailable"}' }], isError: true });
  }
  nativeMessages.clear();
}
function nativeMessageResult(line) {
  // An inbox control can be waiting for the model awaiting this tool. Resolve
  // receipts outside the serialized control chain to avoid that deadlock.
  let request;
  try { request = JSON.parse(line); } catch { return false; }
  if (request?.op !== "native_message_result") return false;
  const pending = nativeMessages.get(request.correlation_id);
  if (!pending) return true;
  nativeMessages.delete(request.correlation_id);
  clearTimeout(pending.timer);
  const receipt = request.receipt;
  if (!receipt || typeof receipt !== "object" || Buffer.byteLength(JSON.stringify(receipt)) > 2048) {
    pending.resolve({ content: [{ type: "text", text: '{"error":"send_failed"}' }], isError: true });
  } else {
    pending.resolve({ content: [{ type: "text", text: JSON.stringify(receipt) }], isError: !!receipt.error });
  }
  return true;
}

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

try {
  const { query, tool, createSdkMcpServer } = await import(pathToFileURL(sdkPath));
  let mcpServers = {};
  let allowedTools = DEFAULT_TOOLS;
  if (start.native_messages === "v1") {
    const { z } = createRequire(pathToFileURL(sdkPath))("zod");
    mcpServers = { paimos: createSdkMcpServer({ name: "paimos", version: "1.0.0", tools: [tool(
      "send_message",
      "Send a durable Paimos message of at most 4096 UTF-8 bytes under your owned identity. Use reply_to from the received envelope when replying; stop when complete. Requests for action must set is_action_request and are held for human review. Receipt means ledger acceptance, not receiver completion.",
      { to: z.string().max(129), body: z.string().min(1).max(4096), reply_to: z.string().max(256), is_action_request: z.boolean(), expects_reply: z.boolean() },
      nativeMessage
    )] }) };
    allowedTools = [...DEFAULT_TOOLS, "mcp__paimos__send_message"];
  }
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
    allowedTools,
    tools: DEFAULT_TOOLS,
    systemPrompt: { type: "preset", preset: "claude_code" },
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
  if (Buffer.byteLength(line) <= MAX_INPUT_FRAME_BYTES && nativeMessageResult(line)) return;
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
        closeNativeMessages();
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
          !message.capabilities.includes("interrupt_receipt_v1")) {
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
      emit({ kind: "turn_completed" });
    }
  }
  closeNativeMessages();
  queryEndedResolve();
  lines.close();
  controlInput.close();
  input.close();
  queryHandle.close();
  await controlChain;
  if (!stopping) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
} catch {
  closeNativeMessages();
  queryEndedResolve();
  lines.close();
  controlInput?.close();
  input?.close();
  queryHandle?.close();
  await controlChain.catch(() => {});
  if (!stopping) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
}
