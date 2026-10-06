// SPDX-License-Identifier: AGPL-3.0-only
// Offline upper-bound proxy; this does not establish green PR execution evidence.
import { execFileSync, spawnSync } from 'node:child_process';
const git = (...args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 4 * 1024 * 1024 }).trim();
const merges = git('log', '--first-parent', 'origin/main', '--merges', '--format=%H %s').split('\n')
  .filter(line => /^[a-f0-9]{40} Merge pull request #[0-9]+/.test(line)).slice(0, 80);
const rows = merges.map(line => {
  const sha = line.slice(0, 40), pr = Number(line.match(/#([0-9]+)/)[1]);
  const parents = git('rev-list', '--parents', '-n', '1', sha).split(' ').slice(1);
  if (parents.length !== 2) throw new Error(`PR ${pr} has no two-parent merge`);
  const equalTree = git('rev-parse', `${sha}^{tree}`) === git('rev-parse', `${parents[1]}^{tree}`);
  const ancestor = spawnSync('git', ['merge-base', '--is-ancestor', parents[0], parents[1]]);
  if (![0, 1].includes(ancestor.status)) throw new Error(`PR ${pr} ancestry unavailable`);
  return { pr, sha, equalTree, containsPreviousMain: ancestor.status === 0 };
});
const count = predicate => rows.filter(predicate).length;
console.log(JSON.stringify({ sample: rows.length, equalTree: count(r => r.equalTree),
  equalTreePercent: rows.length ? 100 * count(r => r.equalTree) / rows.length : 0,
  equalTreeAndContainsPreviousMain: count(r => r.equalTree && r.containsPreviousMain),
  strictPercent: rows.length ? 100 * count(r => r.equalTree && r.containsPreviousMain) / rows.length : 0,
  caveat: 'Final merge vs second-parent head; no green-run, queue grouping or historical PR merge-ref evidence. Requested upper-bound proxy only; does not bound reuse of actual PR merge-checkout trees.', rows }, null, 2));
