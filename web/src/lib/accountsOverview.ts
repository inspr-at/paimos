// SPDX-License-Identifier: AGPL-3.0-only
import type { AgentAccount } from './agents.ts'
import type { PairingEnrollment, PairingView } from './agentPairing.ts'
import { HARNESS_NAME, type AccountRow, type CapacityWindow } from './capacity.ts'
export interface SignInReference { computer: PairingView; enrollment: PairingEnrollment }
export interface OverviewAccount { id: string; vendor: string; harness: string; identity: string; records: AgentAccount[]; rows: AccountRow[]; signins: SignInReference[]; windows: CapacityWindow[] }
/** The server's canonical quota identity is the only grouping authority. A
 * display label, local account key or matching email never merges accounts. */
export function overviewAccounts(records: AgentAccount[], rows: AccountRow[], computers: PairingView[]): OverviewAccount[] {
  const result = new Map<string, OverviewAccount>()
  for (const record of records) {
    const key = record.quota_pool_fingerprint ? `${record.harness}:${record.quota_pool_fingerprint}` : record.id
    let account = result.get(key)
    if (!account) { account = { id: key, vendor: HARNESS_NAME[record.harness] ?? record.harness, harness: record.harness, identity: record.label, records: [], rows: [], signins: [], windows: [] }; result.set(key, account) }
    account.records.push(record)
    const row = rows.find(r => r.id === record.id)
    if (row) account.rows.push(row)
    for (const computer of computers) for (const enrollment of computer.enrollments) if (enrollment.account_id === record.id) account.signins.push({ computer, enrollment })
  }
  // One reader per shared window, choosing the most recent real measurement.
  for (const account of result.values()) {
    const windows = new Map<string, CapacityWindow>()
    for (const row of account.rows) for (const window of [row.primary, row.five]) {
      if (!window) continue
      const key = `${window.reading.window_kind}/${window.reading.bucket ?? ''}`
      const prior = windows.get(key)
      if (!prior || Date.parse(window.reading.read_at) > Date.parse(prior.reading.read_at)) windows.set(key, window)
    }
    account.windows = [...windows.values()]
  }
  return [...result.values()]
}
