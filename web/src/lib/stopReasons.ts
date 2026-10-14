// SPDX-License-Identifier: AGPL-3.0-only
// How a session ended (AEON-1285): whether its stop reason reports a failure, and the words the
// page says for it. Words only: nothing here decides Done, which is the server's finished flag
// alone (agentSignals.ts, AEON-437).
// Stop reasons are free text. Match error words, not arbitrary nonempty reasons:
// "user requested", "completed", "stopped", "cancelled" are normal stops.
// heartbeat_lost is the server closing a silent session ("Lost contact"): the
// session ended; losing reports is not evidence that it failed (AEON-291).
export const LOST_CONTACT = 'heartbeat_lost'
export function problemReason(reason?: string | null) {
  return !!reason && reason !== LOST_CONTACT && /\b(error|errored|failed|failure|blocked|crash(?:ed)?|ownership lost|heartbeat lost|timeout|timed out)\b/i.test(reason.replace(/[_-]+/g, ' '))
}
// AEON-1285: the page never shows a stop reason as reported. A known one reads as a label
// ("Process failed") and a short sentence with what to do; any other reads "Stopped", and
// a failure among them reads as a failure without echoing the reporter's text.
const STOP_WORDS: Record<string, readonly [label: string, sentence: string]> = {
  process_failed: ['Process failed', 'The worker process failed · Retry or check the ticket'],
  harness_crashed: ['Harness crashed', 'The harness crashed · Retry or check the ticket'],
  ownership_lost: ['Ownership lost', 'The session lost its run · Check the run before a retry'],
  process_exited: ['Process exited', 'The worker process exited'],
  [LOST_CONTACT]: ['Lost contact', 'Reports stopped arriving, so the session was closed'],
  stopped: ['Stopped', 'The session was stopped'],
  'user requested': ['Stopped on request', 'Stopped at a person’s request'],
  cancelled: ['Cancelled', 'The run was cancelled'],
  canceled: ['Cancelled', 'The run was cancelled'],
  completed: ['Completed', 'The session completed'],
  paused: ['Paused', 'Paused with a handover'],
  budget_exhausted: ['Budget exhausted', 'The run used up its budget'],
  token_budget_exhausted: ['Budget exhausted', 'The run used up its token budget'],
  turn_budget_exhausted: ['Budget exhausted', 'The run used up its turn budget'],
  archived: ['Removed', 'The session was removed'],
}
const FAILED_WORDS = ['Stopped', 'The session reported a failure · Retry or check the ticket'] as const
function stopWords(reason: string | null | undefined): readonly [string, string] | null {
  const text = reason?.trim() ?? ''
  if (!text) return null
  const known = STOP_WORDS[text] ?? STOP_WORDS[text.toLowerCase()]
  if (known) return known
  if (/^watch daemon restarted\b/i.test(text)) return ['Stopped', 'The watching daemon restarted · Process exit unconfirmed']
  if (/^attach confirmed exited\b/i.test(text)) return ['Process exited', 'The attached process exited']
  if (/^attach\b.*\bunconfirmed$/i.test(text)) return ['Stopped', 'The attachment ended · Process exit unconfirmed']
  return problemReason(text) ? FAILED_WORDS : ['Stopped', 'The session stopped']
}
/** The words for how a session ended: '' without a reason, "Stopped" for one it does not know. */
export const stopReasonLabel = (reason: string | null | undefined) => stopWords(reason)?.[0] ?? ''
/** One short sentence on how it ended and what to do; '' without a reason. */
export const stopReasonSentence = (reason: string | null | undefined) => stopWords(reason)?.[1] ?? ''
