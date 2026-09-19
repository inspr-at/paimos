// Offline check against the exact operator-installed SDK; no query or model call.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { nativeMessageShape } from "../backend/agentd/claudeassets/native-message-schema.mjs";

const sdkPath = process.argv[2];
assert(sdkPath, "usage: node scripts/check-claude-message-schema.mjs /absolute/sdk.mjs");
assert.equal(createHash("sha256").update(await readFile(sdkPath)).digest("hex"),
  "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93");
const { tool, createSdkMcpServer } = await import(pathToFileURL(sdkPath));
let handlerCalls = 0;
const server = createSdkMcpServer({ name: "paimos", version: "1.0.0", tools: [tool(
  "send_message", "bounded schema fixture", nativeMessageShape,
  async () => { handlerCalls++; return { content: [{ type: "text", text: "fixture accepted" }] }; },
)] });
const pending = new Map();
const transport = {
  start: async () => {}, close: async () => {},
  send: async (message) => pending.get(message.id)?.(message),
};
await server.instance.connect(transport);
let sequence = 0;
async function request(method, params) {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => { pending.delete(id); reject(Error("SDK protocol timeout")); }, 3000);
    pending.set(id, (message) => { clearTimeout(timeout); pending.delete(id); resolve(message); });
    transport.onmessage({ jsonrpc: "2.0", id, method, params });
  });
}
try {
  const list = await request("tools/list", {});
  assert(!list.error);
  assert.equal(list.result.tools.length, 1);
  const schema = list.result.tools[0].inputSchema;
  assert.equal(schema.properties.to.maxLength, 129);
  assert.equal(schema.properties.body.minLength, 1);
  assert.equal(schema.properties.body.maxLength, 4096);
  assert.equal(schema.properties.reply_to.maxLength, 256);
  assert.deepEqual(schema.required, ["to", "body", "reply_to", "is_action_request", "expects_reply"]);
  const args = { to: "codex:fixture", body: "fixture", reply_to: "", is_action_request: false, expects_reply: false };
  const accepted = await request("tools/call", { name: "send_message", arguments: args });
  assert(!accepted.error && !accepted.result.isError);
  for (const invalid of [{ ...args, body: "" }, { ...args, body: "x".repeat(4097) },
    { ...args, to: "x".repeat(130) }, { ...args, expects_reply: "false" }]) {
    const rejected = await request("tools/call", { name: "send_message", arguments: invalid });
    assert(rejected.error || rejected.result.isError);
  }
  assert.equal(handlerCalls, 1);
  console.log("Claude SDK 0.3.251: bundled schema discovery, valid call, and invalid argument rejection passed; no model called.");
} finally {
  await server.instance.close();
}
