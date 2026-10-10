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
applied once per animation frame; when 256 frames wait, for example in a
hidden tab, they are applied at once rather than dropped, so a turn's end and
its saved-reply hint are never lost. The block holds at most 256 KiB of text and
64 tool lines and says when output was cut or the relay skipped frames.

Interim output follows transcript policy A. It lives only in the open view's
memory and is never stored, logged or replayed. When the turn ends, the tool
lines fold into a "Used N tools" chip marked live-only; its toggle sits above
the list it opens. The block remains until the agent's saved reply arrives in
the thread, then the reply takes its place. Chat-native replies are stored
only in the chat thread, so the panel also reads the thread's newest page
(`GET /api/chat-threads/{id}/messages`, whose items now name
`sender_principal_id` and `created_at`) and shows the agent's messages from it
in the session thread, together with the person's own chat inputs. A turn ends
on screen when its final is there: a message the stream announced during the
turn, or, once the thread's history has been read, a newer agent message than
the one before the turn. A history read that lands after a turn began
therefore never dismisses it with an older reply. Message, receipt and
read-marker hints reload both. A resync,
or a refused or expired stream, drops interim content, reloads history and
looks up the thread again; refused streams retry with backoff four times per
turn.

## Chat-native messages

Messages from the chat thread keep their thread. Reply on one sends through
`POST /api/chat-threads/{id}/outbox` with the message as `reply_to`, because
the project message route only accepts project messages as parents; the
client message ID keeps a retry idempotent. Such a reply has no project
receipt and shows none. The daemon relay reads the chat outbox only at turn
boundaries, so while a chat-native message is the reply target the composer
offers After this turn and Stop only: Send now and At next step leave the
action row, and a short note in their place says that chat replies arrive
between turns; ⌘/Ctrl+Enter then sends after the turn as well. Once the reply
is sent or cancelled, a managed send may steer again. Steering a chat-native
reply mid-turn is a follow-up.

Read state follows the same split. Every chat-native message on screen goes
to `PUT /api/chat-threads/{id}/read-marker` as an exact visible ID, including
each post folded into a collapsed group. The session read marker receives
only a project message this view has loaded: the watermark's own message
when it is one, otherwise the newest loaded project message at or before it.
A watermark cached on this browser from an earlier visit, whose message is
not loaded yet, therefore waits for the thread to load and never reaches the
session marker as an unknown ID. Another device takes the furthest message
the history page reports as `seen` as its read watermark.

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
shows "Stopping…" only while an interrupt is on record. "You stopped this
turn" appears only once the harness applied the interrupt. A refusal, or no
interrupt within five seconds, withdraws the claim, also when the turn has
meanwhile ended on its own. The line is live-only; saving the partial reply is
part of the daemon slices.

## Validation

`internal/chat` `TestSessionThreadLookupFollowsOnlyTheCallersCurrentBinding`
covers the lookup's participant, handover and disabled-module refusals;
`TestChatLiveViewersReplayFinalOnlyAndReceipts` checks that history names each message's author and time
and reports a read-marker union as `seen`.
`web/tests/chatLive.test.ts` covers turn assembly, bounds, gap detection, the
capability mapping, the stream follower's resync and bounded retry, the full
frame queue, the stop claim and the mapping of saved chat replies.
`web/tests/aeon-1071-chat-live.spec.ts` streams a turn at 390 and 1440 px in
light and dark themes. The stability guard keeps the field, Send, Send now and
Stop within 0.5 px through streaming, Esc-to-stop, the applied interrupt, the
tool fold and a chat-native saved reply replacing the block. The spec also
checks the composer for each capability and that a refused stop after the
turn ended claims nothing. Against mocks that refuse chat-native IDs the way
the project routes do, it checks that a reply to a chat-native final goes to
its chat thread, that seeing one is recorded on the thread and not sent again
on the next visit, and that a delayed history read keeps the ended turn until
its own final arrives. It also reopens with a watermark cached on a
chat-native final and checks that only a project message reaches the session
marker, that every visible chat-native post and every post of a collapsed
group is recorded, and, at 390 and 1440 px, that a chat-native reply target
removes the steer action without moving the field, Send or Stop.
