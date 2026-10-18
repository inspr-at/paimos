// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, writeFileSync } from 'node:fs';
import { browserGroups, combine, readResult, maxResultBytes } from './ci-preflight-result.mjs';

try {
  if (Buffer.byteLength(process.env.PREFLIGHT_LOCAL ?? '') > maxResultBytes) throw new Error('oversized local result');
  const result = combine(JSON.parse(process.env.PREFLIGHT_LOCAL), browserGroups.map(group => readResult(`receipts/${group}.json`)),
    { sha: process.env.PREFLIGHT_SHA, run: Number(process.env.GITHUB_RUN_ID), attempt: Number(process.env.GITHUB_RUN_ATTEMPT) });
  mkdirSync('result', { recursive: true });
  writeFileSync('result/preflight.json', JSON.stringify(result) + '\n');
  console.log(JSON.stringify({ metric: 'preflight', sha: result.sha, status: result.status, local: result.local.status,
    browser_failed: result.browsers.filter(row => row.status === 'failed').length }));
  process.exitCode = result.status === 'passed' ? 0 : 1;
} catch { console.error('preflight_evidence_invalid_or_missing'); process.exitCode = 1; }
