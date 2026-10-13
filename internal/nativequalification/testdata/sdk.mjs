// SPDX-License-Identifier: AGPL-3.0-only
// Pinned, offline SDK fixture. It never starts the vendor binary or contacts a
// subscription. The trace below exercises the C0 qualification observer.
import { writeFileSync } from 'node:fs';

export const fixtureProtocol = 'aeon-lead-sdk-fixture/1';

if (process.argv[2] === 'qualification-trace') {
  process.stdout.write(JSON.stringify([
    { kind: 'consumed', input: 'first', turn: 'turn-1' },
    { kind: 'background_task', task: 'task-1', state: 'started' },
    { kind: 'final', input: 'first', turn: 'turn-1', text: 'Explicit first final.' },
    { kind: 'compaction', thread: 'thread-compacted', cursor: 'cursor-compacted' },
    { kind: 'background_task', task: 'task-1', state: 'progress' },
    { kind: 'background_task', task: 'task-1', state: 'completed' },
    { kind: 'consumed', input: 'second', turn: 'turn-2' },
    { kind: 'final', input: 'second', turn: 'turn-2', text: 'Explicit second final.' },
  ]));
}

export function query({ prompt, options }) {
  const denied = ['Bash', 'Read', 'Write', 'Edit', 'Task', 'mcp__foreign__send'];
  const session = options.resume;
  const queue = [];
  let wake, closed = false;
  const push = message => { queue.push(message); wake?.(); wake = null; };
  const consume = message => {
    push({ type: 'assistant', session_id: session, user_message_uuid: message.uuid,
      message: { content: [{ type: 'text', text: 'A partial answer, never the final.' }] } });
    push({ type: 'result', subtype: 'success', session_id: session,
      user_message_uuid: message.message.content[0].text === 'wrong-result' ? 'not-consumed' : message.uuid,
      result: 'Explicit SDK final.' });
  };
  if (options.settingSources.length || options.persistSession !== true || options.strictMcpConfig !== true ||
      options.permissionMode !== 'dontAsk' || options.tools.length || options.plugins.length || options.additionalDirectories.length ||
      options.allowedTools.join(',') !== 'mcp__aeon__aeon_read' || Object.keys(options.mcpServers).join(',') !== 'aeon' ||
      options.mcpServers.aeon.url !== 'http://127.0.0.1:49152' ||
      Object.keys(options.hooks).join(',') !== 'PreToolUse' ||
      JSON.parse(options.extraArgs.settings).fastMode !== false ||
      ['AEON_API_KEY', 'ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN', 'OPS_TEST_SECRET'].some(name => process.env[name] !== undefined)) {
    throw Error('fixture boundary refused');
  }
  (async () => {
    for (const name of denied) {
      if ((await options.canUseTool(name, {})).behavior !== 'deny' ||
          (await options.hooks.PreToolUse[0].hooks[0]({ tool_name: name })).hookSpecificOutput.permissionDecision !== 'deny') {
        throw Error('fixture ambient tool refused');
      }
    }
    if ((await options.canUseTool('mcp__aeon__aeon_read', {})).behavior !== 'allow') throw Error('fixture bound tool refused');
    writeFileSync(`${options.cwd}/boundary.json`, JSON.stringify({
      protocol: fixtureProtocol, legacyHooksAbsent: true, ambientToolsDenied: true,
      boundToolAllowed: true, daemonCredentialAbsent: true, persistentNativeOnly: true,
      accountHomePreserved: process.env.CLAUDE_CONFIG_DIR !== process.env.HOME,
    }), { mode: 0o600 });
    const initial = await prompt[Symbol.asyncIterator]().next();
    push({ type: 'system', subtype: 'init', session_id: session, model: 'fixture-model' });
    consume(initial.value);
  })().catch(() => { closed = true; wake?.(); wake = null; });
  return {
    async streamInput(input) { for await (const message of input) consume(message); },
    async interrupt() {},
    close() { closed = true; wake?.(); wake = null; },
    async *[Symbol.asyncIterator]() {
      while (!closed || queue.length) {
        if (queue.length) yield queue.shift();
        else await new Promise(resolve => { wake = resolve; });
      }
    },
  };
}
