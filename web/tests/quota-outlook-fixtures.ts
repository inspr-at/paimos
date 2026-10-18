// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: the quota outlook the dial reads, as the Go projection produced it from simulated
// readings (internal/agentaccounts TestQuotaOutlookWebFixtureIsCurrent pins this file).
// "fast": Claude runs dry before a refill, Codex is on pace, Cursor reports no quota.
// "slow": Claude refills with quota unused and one account is held.
import { readFileSync } from 'node:fs'
import type { QuotaOutlook } from '../src/lib/quotaOutlook'

export const OUTLOOK = JSON.parse(readFileSync(new URL('./fixtures/quota-outlook.json', import.meta.url), 'utf8')) as Record<'fast' | 'slow', QuotaOutlook>
