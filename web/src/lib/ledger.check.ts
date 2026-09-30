// SPDX-License-Identifier: AGPL-3.0-only
// Compile-time guard for the ledger (AEON-449). Nothing imports this file and it ships
// nothing: `vue-tsc -b` (npm run typecheck, npm run build) fails here when the brand
// weakens, because each @ts-expect-error below stops being an error. What it pins down:
// a session or run can be shown or kept only after the ledger admitted it, and nothing
// else (raw API JSON, a spread copy, a wire row) can stand in for one.
import { ref } from 'vue'
import type { AgentRun, HarnessSession } from './agents'
import type { AgentRunRow, HarnessSessionRow } from './agentRows'
import type { Wire } from './wire'
import type { useAgents } from '../stores/agents'

declare const raw: HarnessSessionRow
declare const rawRun: AgentRunRow
declare const admitted: HarnessSession
declare const wire: Wire<HarnessSessionRow>
declare const agents: ReturnType<typeof useAgents>

// @ts-expect-error raw API JSON is not an admitted session
export const fromJson: HarnessSession = raw
// @ts-expect-error a run needs the ledger too
export const runFromJson: AgentRun = rawRun
// @ts-expect-error a copy made by spreading is a new object the ledger never saw
export const copied: HarnessSession = { ...admitted, phase: 'stopped' }
// @ts-expect-error a wire row has no fields to read
export const peek: string = wire.id
// @ts-expect-error and is not a session to show
export const shown: HarnessSession = wire
// @ts-expect-error nor can one be stored in a list of sessions
export const listed: HarnessSession[] = [raw]

// The brand survives Vue's ref and the store's unwrapping, so components keep it.
export const held = ref<HarnessSession[]>([])
export const fromRef: HarnessSession | undefined = held.value[0]
export const fromStore: HarnessSession | undefined = agents.views[0]?.session
export const runFromStore: AgentRun | undefined = agents.runs.any
// @ts-expect-error a raw row cannot be put into what the store holds
held.value.push(raw)
