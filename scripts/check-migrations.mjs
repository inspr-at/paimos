// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';

// Strip SQL comments and literals, including nested block comments. Dollar
// bodies remain visible so DDL in DO blocks is checked too. Quoted identifiers
// become placeholders; words in identifiers must not be mistaken for SQL.
export function sqlWords(sql) {
  let result = '', depth = 0;
  for (let i = 0; i < sql.length;) {
    if (sql.startsWith('/*', i)) { depth++; result += ' '; i += 2; continue; }
    if (depth && sql.startsWith('*/', i)) { depth--; i += 2; continue; }
    if (depth) { i++; continue; }
    if (sql.startsWith('--', i)) {
      const end = sql.indexOf('\n', i);
      i = end < 0 ? sql.length : end;
      result += ' ';
      continue;
    }
    if (sql[i] === "'" || sql[i] === '"') {
      const escaped = sql[i] === "'" && /[eE]/.test(sql[i - 1] ?? '') && !/\w/.test(sql[i - 2] ?? '');
      const quote = sql[i++], start = i;
      while (i < sql.length) {
        if (escaped && sql[i] === '\\') { i += 2; continue; }
        if (sql[i++] !== quote) continue;
        if (sql[i] === quote) { i++; continue; }
        break;
      }
      // Literal SQL passed to EXECUTE (including format()) is executable DDL.
      // Ordinary data literals stay invisible to avoid flagging prose/backfills.
      const dynamic = quote === "'" && /\bEXECUTE\b[^;]*$/i.test(result);
      result += quote === '"' ? ' identifier ' : dynamic ? ` ${sqlWords(sql.slice(start, i - 1).replaceAll("''", "'"))} ` : ' ';
      continue;
    }
    result += sql[i++];
  }
  return result;
}

export function destructive(sql) {
  const statements = sqlWords(sql);
  return /\bDROP\s+(?:TABLE|COLUMN)\b/i.test(statements)
    || /\bALTER\s+TABLE\b[^;]*\b(?:DROP\s+(?!(?:CONSTRAINT|NOT\s+NULL|DEFAULT)\b)(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?\w+|RENAME\s+(?:COLUMN\b|TO\b|\w+\s+TO\b))/i.test(statements);
}

// The marker is a standalone header comment, before the first SQL statement.
// The ticket owns the evidence that expansion shipped in an earlier release.
export function contractMarker(sql, previousVersion = null) {
  const lines = [];
  for (const line of sql.split('\n')) {
    if (line.trim() && !line.trim().startsWith('--')) break;
    lines.push(line);
  }
  const prefix = lines.join('\n');
  const marker = /^-- aeon:contract-phase [A-Z][A-Z0-9]*-\d+ expanded-in=v(\d{12}\.0\.0)[ \t\r]*$/m.exec(prefix);
  return !!marker && validCalendarVersion(marker[1]) && (!previousVersion || marker[1] <= previousVersion);
}

export function checkMigrations(files, published = new Map(), previousVersion = null) {
  const problems = [], numbers = new Map();
  for (const [name, sql] of files) {
    const match = /^(\d{4})_[a-z0-9_]+\.sql$/.exec(name);
    if (!match) { problems.push(`${name}: expected NNNN_name.sql`); continue; }
    const number = Number(match[1]);
    if (numbers.has(number)) problems.push(`${name}: duplicate migration number ${match[1]} (also ${numbers.get(number)})`);
    numbers.set(number, name);
    if (published.has(name)) {
      if (published.get(name) !== sql) problems.push(`${name}: published migration changed; add a new migration instead`);
      continue;
    }
    if (destructive(sql) && !contractMarker(sql, previousVersion)) problems.push(`${name}: destructive DDL requires -- aeon:contract-phase TICKET-N expanded-in=vYYMMDDhhmmss.0.0 (expansion no later than the previous published release)`);
  }
  for (const name of published.keys()) if (!files.has(name)) problems.push(`${name}: published migration removed`);
  return problems;
}

export function publishedMigrations(ref, directory = 'internal/db/migrations') {
  const names = execFileSync('git', ['ls-tree', '--name-only', `${ref}:${directory}`], { encoding: 'utf8' }).trim().split('\n').filter(name => name.endsWith('.sql'));
  if (!names.length) throw new Error('Published release has no migrations');
  return new Map(names.map(name => [name, execFileSync('git', ['show', `${ref}:${directory}/${name}`], { encoding: 'utf8', maxBuffer: 10 << 20 })]));
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    if (args.length !== 2 || args[0] !== '--base-ref' || !args[1].startsWith('v') || !validCalendarVersion(args[1].slice(1))) throw new Error('usage: node scripts/check-migrations.mjs --base-ref vYYMMDDhhmmss.0.0');
    const directory = 'internal/db/migrations';
    const files = new Map(readdirSync(directory).filter(name => name.endsWith('.sql')).sort().map(name => [name, readFileSync(`${directory}/${name}`, 'utf8')]));
    const problems = checkMigrations(files, publishedMigrations(args[1]), args[1].slice(1));
    if (problems.length) { console.error(problems.join('\n')); process.exitCode = 1; }
    else console.log(`migration guard: ${files.size} unique numbers; published files unchanged; new destructive DDL marked`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
