<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Chat composer and live replies

AEON-1071 brings the AEON-1070 live stream into the session chat, following
the approved AEON-947 chat design.

## Live replies

When the chat tab opens for a person, the panel asks
`GET /api/chat-sessions/{sessionId}/thread` for the person's own conversation
whose current binding is that session. Other people's threads, past bindings
and unbound sessions return 404, and so does the disabled chat module. Without
a thread the chat keeps its final-history path and shows no error.

With a thread, the panel follows `GET /api/chat-threads/{id}/live`. Text
deltas, tool lines and running, idle or waiting state render in a live block
at the end of the thread. The block sits inside the scroll area and only grows
downward; a reader pinned to the bottom stays on the newest line. Frames are
applied once per animation frame. The block holds at most 256 KiB of text and
64 tool lines and says when output was cut or the relay skipped frames.

Interim output follows transcript policy A. It lives only in the open view's
memory and is never stored, logged or replayed. When the turn ends, the tool
lines fold into a "Used N tools" chip marked live-only. The block remains
until the agent's saved reply arrives in the thread, then the reply takes its
place. Message, receipt and read-marker hints reload final history. A resync,
or a refused or expired stream, drops interim content, reloads history and
looks up the thread again; refused streams retry with backoff four times per
turn.

## Sends and Stop

Claude, Codex and Pi derive their send options from the same flags as the
agentd capability snapshot. Native steering requires an advertised steer
control and enables Send now (⌘/Ctrl+Enter). Pi's steering takes effect at the
next agent step and shows At next step. Without steering, Enter sends After
this turn while the agent works. OpenCode, Gemini and the other harnesses
keep their existing presentation until their adapters report flags
(AEON-1073). Unmanaged sessions keep one Send.

The steer action and Stop stay in the composer for the whole session and are
disabled while nothing runs, so a turn starting or ending never moves a
control. A narrow composer hides the new-line hint so Stop stays on the action
row. A live turn counts as working before the session row catches up. Stop and
Esc outside a text field use the existing interrupt control. The live block
shows "Stopping…" only while an interrupt is on record; a refusal, or no
interrupt within five seconds, withdraws it. "You stopped this turn" is a
live-only line; saving the partial reply is part of the daemon slices.

## Validation

`internal/chat` `TestSessionThreadLookupFollowsOnlyTheCallersCurrentBinding`
covers the lookup's participant, handover and disabled-module refusals.
`web/tests/chatLive.test.ts` covers turn assembly, bounds, gap detection, the
capability mapping and the stream follower's resync and bounded retry.
`web/tests/aeon-1071-chat-live.spec.ts` streams a turn at 390 and 1440 px in
light and dark themes. The stability guard keeps the field, Send, Send now and
Stop within 0.5 px through streaming, Esc-to-stop and the saved reply
replacing the block. The spec also checks the composer for each capability.
