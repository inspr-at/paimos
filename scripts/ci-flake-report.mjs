// SPDX-License-Identifier: AGPL-3.0-only
// node scripts/ci-flake-report.mjs <summary-directory>
// Recursively reads archived GITHUB_STEP_SUMMARY files. Counts RETRIED records
// by UTC Monday week, kind, and test id; no network or external dependencies.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';

export function weeklyReport(directory) {
  const counts = new Map();
  let files = 0;
  function visit(path, depth = 0) {
    if (depth > 20) throw new Error('Summary directory nesting exceeds limit');
    for (const entry of readdirSync(path, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = join(path, entry.name);
      if (entry.isDirectory()) visit(file, depth + 1);
      else if (entry.isFile()) {
        if (++files > 10000 || statSync(file).size > 32 * 1024 * 1024) throw new Error('Summary input exceeds limit');
        const text = readFileSync(file, 'utf8');
        for (const line of text.split('\n')) {
          // Only metadata comments are counted; the stdout copy is not counted
          // again if a log and Markdown happen to share the same file.
          const match = line.match(/^<!-- CI_FLAKE (.+) -->$/);
          if (!match) continue;
          const record = JSON.parse(match[1]);
          if (record.label !== 'RETRIED') continue;
          if (!['go', 'playwright'].includes(record.kind) || typeof record.id !== 'string' || record.attempt !== 2 ||
              typeof record.date !== 'string' || !Number.isFinite(Date.parse(record.date))) throw new Error('Invalid flake summary record');
          const date = new Date(record.date);
          date.setUTCHours(0, 0, 0, 0);
          date.setUTCDate(date.getUTCDate() - (date.getUTCDay() + 6) % 7);
          const key = JSON.stringify([date.toISOString().slice(0, 10), record.kind, record.id]);
          counts.set(key, (counts.get(key) ?? 0) + 1);
        }
      }
    }
  }
  visit(resolve(directory));
  const safe = value => value.replaceAll('|', '&#124;').replace(/[\r\n]/g, ' ').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  const rows = [...counts].sort(([a], [b]) => a.localeCompare(b)).map(([key, count]) => {
    const [week, kind, id] = JSON.parse(key);
    return `| ${week} | ${kind} | ${safe(id)} | ${count} |`;
  });
  return ['| Week (UTC Monday) | Kind | Test id | Retries |', '| --- | --- | --- | ---: |', ...rows,
    ...(rows.length ? [] : ['| — | — | No retries recorded | 0 |'])].join('\n') + '\n';
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 3) throw new Error('Usage: node scripts/ci-flake-report.mjs <summary-directory>');
    process.stdout.write(weeklyReport(process.argv[2]));
  } catch (error) { console.error(`ci-flake-report: ${error.message}`); process.exitCode = 2; }
}
