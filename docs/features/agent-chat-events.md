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
AEON-1073 extends the same projections to owned Cursor, OpenCode, Gemini and
native Grok ACP sessions. These adapters report `steer: queue`, never native
mid-turn steering. Interrupt reflects the exact session controls; native Grok
has no qualified interrupt and refuses it explicitly. Its pinned binaries,
assets, profile, account checks, network confinement and tool refusal stay in
force. Grok support is validated with fixtures, without a live model call.
Review and verification runs have no chat subscription.
Existing control leases, claims and process ownership checks remain authoritative.

Cursor, OpenCode, Gemini and Grok keep ordinary chat sessions available after a
clean turn end. Queued input retains its existing inbox lease or authorized steer
control until the session is idle, then starts one prompt on the same session.
Content-free replay receipts prevent duplicate injection after a lost completion
or uncertain vendor response. Waiting steer controls let interrupt and Stop pass;
expired authorization is refused. Verification and review stay single-turn.
Gemini steering never cancels a turn implicitly. No lossy interrupt-and-replace
action is exposed; requesting that unsupported action returns a refusal.

ACP tool projections use static category names rather than vendor display titles
that can contain commands or paths. At most 128 tool identities are held per turn,
and status-only tool updates reuse only those owned identities. User echoes,
nontext content, tool arguments/results and reasoning have no chat projection.

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
journal isolation. Barrier tests cover ACP turn-end pickup, same-session
continuation, cancellation
acknowledgements, clear refusals, authorization expiry and delivery replay without
duplicate input. A fixture executable drives persistent Cursor ACP turns.
A mock Agent SDK drives the actual Claude bridge to verify
text/tool projection and exclusion of tool payloads, reasoning and subagent output.
Protocol references: [Claude SDK partial messages](https://code.claude.com/docs/en/agent-sdk/streaming-output),
[Codex app-server events and runtime status](https://learn.chatgpt.com/docs/app-server),
and [Pi RPC records](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md).

ACP projection reference: [Tool calls and status updates](https://agentclientprotocol.com/protocol/v1/tool-calls).
