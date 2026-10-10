# Live agent chat events

AEON-1069 adds daemon-local, owned-session streaming groundwork for the
AEON-618 conversation identity. It does not enable thread SSE or chat delivery.
The `session_chat_v1` section of `api/agent-pairing-contract.json` defines the
binding, event shapes, bounds and retention policy.

Claude Agent SDK partial messages, Codex app-server notifications and Pi RPC
events project to `agent_message_chunk`, `tool_call` and `state` updates.
Only text deltas, tool identity/status, and running/idle/requires_action state
are exposed. Tool arguments/results, reasoning and subagent output are excluded.
Permission requests are informational and grant no approval authority.

`Supervisor.SubscribeChat` is an internal seam for a future authenticated relay.
It requires the current tenant, principal, run, daemon generation and registered
session, and only accepts a running owned process. It adds no public endpoint.
The initial capability snapshot distinguishes native Claude/Codex steering,
Pi steering at the next agent step, and queued input when steering is disabled.
Interrupt reflects the exact session controls; deltas are qualified only for
these three adapters. Review and verification runs have no chat subscription.
Existing control leases, claims and process ownership checks remain authoritative.

Each session permits four subscribers with 32 queued events each. Slow readers
receive sequence gaps and a cumulative dropped-event count. Cancel, archive and
process exit discard queued content. Reconnection receives content-free capability
and state snapshots, never prior text. Unknown or invalid chat shapes increment
the owned connection's `DroppedChatFrames` counter without logging payloads.
Malformed JSON and transport-size failures retain the existing fail-closed behavior.

All interim events remain in memory and bypass `Record`, `Telemetry`, checkpoint,
inbox and audit writes. Final-message persistence and remote thread streaming are
the responsibility of the following chat slices.

The fixture tests exercise native JSONL framing, matching per-harness projections,
unknown-frame counts, capability flags, ownership rejection, bounded queues and
journal isolation. A mock Agent SDK drives the actual Claude bridge to verify
text/tool projection and exclusion of tool payloads, reasoning and subagent output.
Protocol references: [Claude SDK partial messages](https://code.claude.com/docs/en/agent-sdk/streaming-output),
[Codex app-server events and runtime status](https://learn.chatgpt.com/docs/app-server),
and [Pi RPC records](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md).
