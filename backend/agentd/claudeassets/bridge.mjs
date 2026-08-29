// PAIMOS owns this bridge process and one documented Agent SDK Query handle.
// Its stdout protocol is deliberately content-free; prompts and model output
// remain only in the inherited local Claude process pipeline.
import { randomUUID } from "node:crypto";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

const [, , sdkPath, claudePath, workspace] = process.argv;
const MAX_INPUT_FRAME_BYTES = 2 * 1024 * 1024;
const MAX_PROMPT_BYTES = 256 * 1024;
const MAX_STEER_BYTES = 64 * 1024;
const MAX_PENDING_STEERS = 256;
const CORRELATION_TTL_MS = 60 * 1000;
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

class InputStream {
  constructor(first) {
    this.items = [{ message: first, acknowledged: null, rejected: null, settled: false }];
    this.waiters = [];
    this.closed = false;
    this.inFlight = null;
  }

  push(message) {
    if (this.closed) return Promise.reject(new Error("closed"));
    return new Promise((resolve, reject) => {
      const item = { message, acknowledged: resolve, rejected: reject, settled: false };
      const waiter = this.waiters.shift();
      if (waiter) waiter(item); else this.items.push(item);
    });
  }

  settle(item, error = null) {
    if (!item || item.settled) return;
    item.settled = true;
    if (error) item.rejected?.(error); else item.acknowledged?.();
  }

  close(error = new Error("closed")) {
    if (this.closed) return;
    this.closed = true;
    this.settle(this.inFlight, error);
    for (const item of this.items.splice(0)) this.settle(item, error);
    for (const waiter of this.waiters.splice(0)) waiter(null);
  }

  async *[Symbol.asyncIterator]() {
    while (!this.closed) {
      let item = this.items.shift();
      if (!item) item = await new Promise((resolve) => this.waiters.push(resolve));
      if (!item) return;
      this.inFlight = item;
      try {
        yield item.message;
        this.settle(item);
      } finally {
        this.settle(item, new Error("input stream aborted"));
        if (this.inFlight === item) this.inFlight = null;
      }
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
    !sdkPath || !claudePath || !workspace) {
  fail();
  process.exit(1);
}

let queryHandle;
let input;
let stopping = false;
let sessionStarted = false;
let sessionID = "";
let initialTurnStarted = false;
let interruptReceipt = false;
const correlations = new Map();

function deleteCorrelation(uuid) {
  const state = correlations.get(uuid);
  if (state?.timer) clearTimeout(state.timer);
  correlations.delete(uuid);
}

function addCorrelation(uuid, correlationID) {
  const state = { correlationID, reacted: false, applied: false, expiresAt: Date.now() + CORRELATION_TTL_MS };
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
  emit({ kind: "turn_started", correlation_id: state.correlationID });
  if (state.applied) deleteCorrelation(uuid);
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
  const { query } = await import(pathToFileURL(sdkPath));
  input = new InputStream(userMessage(start.prompt));
  start.prompt = "";
  queryHandle = query({
    prompt: input,
    options: {
      cwd: workspace,
      pathToClaudeCodeExecutable: claudePath,
      persistSession: false,
      settingSources: [],
      strictMcpConfig: true,
      mcpServers: {},
      plugins: [],
      includePartialMessages: true,
      permissionMode: "dontAsk",
      allowedTools: DEFAULT_TOOLS,
      tools: DEFAULT_TOOLS,
      systemPrompt: { type: "preset", preset: "claude_code" }
    }
  });
  if (!queryHandle || typeof queryHandle.interrupt !== "function" || typeof queryHandle.close !== "function" ||
      typeof queryHandle[Symbol.asyncIterator] !== "function") throw new Error("query capabilities");
} catch {
  fail("app_server_protocol", "", "sdk_query_capability_missing");
  process.exit(1);
}

let controlChain = Promise.resolve();
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
    try {
      if (request.op === "steer") {
        expireCorrelations();
        if (typeof request.text !== "string" || request.text.length === 0 ||
            Buffer.byteLength(request.text) > MAX_STEER_BYTES || request.text.includes("\0") || !interruptReceipt) {
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
        await input.push(userMessage(request.text, uuid));
        request.text = "";
        const receipt = await queryHandle.interrupt();
        if (!receipt || !Array.isArray(receipt.still_queued)) {
          fatal = true;
          throw new Error("receipt");
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
        const receipt = await queryHandle.interrupt();
        if (!receipt || !Array.isArray(receipt.still_queued)) {
          fatal = true;
          throw new Error("receipt");
        }
        emit({ kind: "control_applied", correlation_id: correlationID });
      } else if (request.op === "stop") {
        stopping = true;
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
      fail("app_server_protocol", correlationID);
      if (fatal) {
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
      interruptReceipt = true;
      if (!sessionStarted) {
        sessionStarted = true;
        sessionID = message.session_id;
        emit({ kind: "session_started", harness_session_id: message.session_id });
      } else if (message.session_id !== sessionID) {
        fail();
        queryHandle.close();
        break;
      }
      if (!initialTurnStarted) {
        initialTurnStarted = true;
        emit({ kind: "turn_started" });
      }
    }
    observeReaction(message);
    observeTool(message);
  }
  lines.close();
  input.close(new Error("Query ended"));
  queryHandle.close();
  await controlChain;
  if (!stopping) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
} catch {
  lines.close();
  input?.close(new Error("Query aborted"));
  queryHandle?.close();
  await controlChain.catch(() => {});
  if (!stopping) {
    fail("child_exit_failed");
    process.exitCode = 1;
  }
}
