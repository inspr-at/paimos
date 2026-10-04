// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

export function checkFull(report, env=process.env) {
  if(report.version!==1||report.full!==true||report.exitCode!==0)throw new Error('Full execution was not successful')
  for(const field of ['runId','attempt','sha']) {
    const value=env[{runId:'GITHUB_RUN_ID',attempt:'GITHUB_RUN_ATTEMPT',sha:'GITHUB_SHA'}[field]]
    if(value===undefined||report[field]!==value)throw new Error('Full execution report has a different run identity')
  }
  if(!report.classes?.ESSENTIAL||!report.classes?.NIGHTLY||Object.values(report.classes).some(row=>row.failed||row.notRun))throw new Error('Full execution report is incomplete')
  return true
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { checkFull(JSON.parse(readFileSync(process.env.TIER_REPORT,'utf8')));console.log('Full tier execution verified for this run, attempt and SHA') }
  catch(error){console.error(error.message);process.exitCode=1}
}
