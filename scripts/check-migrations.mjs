// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';

// Port internal/db/sqlsplit.go exactly: the runner removes comments (without
// inserting whitespace), ends block comments at the first */, ignores backslash
// escapes in quoted strings, and accepts digits in dollar tags. Classify what
// pgx will execute rather than using the tokenizer to choose statement borders.
// Go strings.TrimSpace uses Unicode White_Space, which differs from JS trim().
export function splitSQL(sql) {
  const statements = [];
  let buffer = '';
  const flush = () => {
    const statement = buffer.replace(/^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g, '');
    buffer = '';
    if (statement !== '') statements.push(statement);
  };
  for (let i = 0; i < sql.length;) {
    if (sql.startsWith('--', i)) {
      i += 2;
      while (i < sql.length && sql[i] !== '\n') i++;
      continue;
    }
    if (sql.startsWith('/*', i)) {
      i += 2;
      while (i + 1 < sql.length && !sql.startsWith('*/', i)) i++;
      i = i + 1 < sql.length ? i + 2 : sql.length;
      continue;
    }
    if (sql[i] === "'" || sql[i] === '"') {
      const quote = sql[i];
      buffer += sql[i++];
      while (i < sql.length) {
        buffer += sql[i];
        if (sql[i] === quote) {
          if (i + 1 < sql.length && sql[i + 1] === quote) {
            buffer += quote; i += 2; continue;
          }
          i++; break;
        }
        i++;
      }
      continue;
    }
    if (sql[i] === '$') {
      const tag = /^\$[A-Za-z0-9_]*\$/.exec(sql.slice(i))?.[0];
      if (tag) {
        const end = sql.indexOf(tag, i + tag.length);
        const next = end < 0 ? sql.length : end + tag.length;
        buffer += sql.slice(i, next); i = next; continue;
      }
    }
    if (sql[i] === ';') { flush(); i++; continue; }
    buffer += sql[i++];
  }
  flush();
  return statements;
}

// Tokenize without interpreting words or semicolons in comments, identifiers or
// literals as SQL. Opaque DO bodies never enter the expand-safe allowlist.
function tokens(sql) {
  const result = [];
  for (let i = 0; i < sql.length;) {
    if (/\s/.test(sql[i])) { i++; continue; }
    if (sql.startsWith('/*', i)) {
      let depth = 1; i += 2;
      while (i < sql.length && depth) {
        if (sql.startsWith('/*', i)) { depth++; i += 2; }
        else if (sql.startsWith('*/', i)) { depth--; i += 2; }
        else i++;
      }
      if (depth) throw new Error('Unterminated SQL comment');
      continue;
    }
    if (sql.startsWith('--', i)) {
      const end = sql.indexOf('\n', i);
      i = end < 0 ? sql.length : end;
      continue;
    }
    const escaped = /^[eE]'/.test(sql.slice(i));
    if (escaped) i++;
    if (sql[i] === "'" || sql[i] === '"') {
      const quote = sql[i++];
      let value = '', closed = false;
      while (i < sql.length) {
        if (escaped && sql[i] === '\\') { value += sql.slice(i, i + 2); i += 2; continue; }
        if (sql[i] !== quote) { value += sql[i++]; continue; }
        i++;
        if (sql[i] === quote) { value += quote; i++; continue; }
        closed = true; break;
      }
      if (!closed) throw new Error('Unterminated SQL quote');
      result.push({kind: quote === '"' ? 'identifier' : 'literal', value, escaped});
      continue;
    }
    const dollar = /^(\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$)/.exec(sql.slice(i));
    if (dollar) {
      const start = i + dollar[0].length, end = sql.indexOf(dollar[0], start);
      if (end < 0) throw new Error('Unterminated dollar literal');
      result.push({kind: 'literal', value: sql.slice(start, end)});
      i = end + dollar[0].length; continue;
    }
    const word = /^[A-Za-z_][A-Za-z0-9_$]*/.exec(sql.slice(i));
    if (word) { result.push({kind: 'word', value: word[0].toUpperCase()}); i += word[0].length; continue; }
    const number = /^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/.exec(sql.slice(i));
    if (number) { result.push({kind: 'number', value: number[0]}); i += number[0].length; continue; }
    result.push({kind: 'symbol', value: sql[i++]});
  }
  return result;
}

function starts(list, ...words) {
  return words.every((word, i) => list[i]?.kind === 'word' && list[i].value === word);
}
function identifier(token) { return token && ['word', 'identifier'].includes(token.kind); }
function nameEnd(list, start) {
  if (!identifier(list[start])) return start;
  let end = start + 1;
  while (list[end]?.value === '.' && identifier(list[end + 1])) end += 2;
  return end;
}
function nameKey(list, start, end) { return list.slice(start, end).map(t => t.value).join('.'); }
function split(list, separator) {
  const groups = [[]]; let depth = 0;
  for (const token of list) {
    if (token.kind === 'symbol') {
      if (token.value === '(') depth++;
      if (token.value === ')') depth--;
      if (depth < 0) throw new Error('Unbalanced SQL parentheses');
      if (!depth && token.value === separator) { groups.push([]); continue; }
    }
    groups.at(-1).push(token);
  }
  if (depth) throw new Error('Unbalanced SQL parentheses');
  return groups;
}
function constantDefault(list) {
  let i = ['+', '-'].includes(list[0]?.value) ? 1 : 0;
  const value = list[i++];
  if (!value || !(value.kind === 'literal' || value.kind === 'number' || starts([value], 'TRUE') || starts([value], 'FALSE') || starts([value], 'NULL'))) return null;
  if (i > 1 && value.kind !== 'number') return null;
  // Only literal casts, never expressions or calls, are constant defaults.
  if (list[i]?.value === ':' && list[i + 1]?.value === ':') {
    const end = nameEnd(list, i + 2);
    if (end === i + 2) return null;
    i = end;
    if (starts(list.slice(i), 'PRECISION') || starts(list.slice(i), 'VARYING')) i++;
    while (list[i]?.value === '[' && list[i + 1]?.value === ']') i += 2;
  }
  if (list[i] && !['COLLATE', 'CONSTRAINT', 'CHECK', 'NOT', 'NULL'].some(w => starts(list.slice(i), w))) return null;
  return {isNull: starts([value], 'NULL')};
}
function safeAlter(action) {
  if (starts(action, 'VALIDATE', 'CONSTRAINT')) return identifier(action[2]) && action.length === 3;
  if (!starts(action, 'ADD')) return false;
  if (starts(action.slice(1), 'CONSTRAINT')) {
    return identifier(action[2]) && ['CHECK', 'FOREIGN'].some(w => starts(action.slice(3), w)) && starts(action.slice(-2), 'NOT', 'VALID');
  }
  let i = starts(action.slice(1), 'COLUMN') ? 2 : 1;
  if (starts(action.slice(i), 'IF', 'NOT', 'EXISTS')) i += 3;
  if (!identifier(action[i++]) || !identifier(action[i])) return false;
  const definition = action.slice(i);
  if (definition.some(t => t.kind === 'word' && ['GENERATED', 'IDENTITY', 'PRIMARY', 'UNIQUE', 'REFERENCES'].includes(t.value))) return false;
  const defaults = definition.map((t, index) => starts([t], 'DEFAULT') ? index : -1).filter(index => index >= 0);
  if (defaults.length > 1) return false;
  const value = defaults.length ? constantDefault(definition.slice(defaults[0] + 1)) : null;
  if (defaults.length && !value) return false;
  const notNull = definition.some((_, index) => starts(definition.slice(index), 'NOT', 'NULL'));
  return !notNull || (!!value && !value.isNull);
}
function expandSafe(statement, createdTables) {
  if (starts(statement, 'CREATE')) {
    const kind = statement[1]?.value;
    if (kind === 'TABLE') {
      const ifNotExists = starts(statement.slice(2), 'IF', 'NOT', 'EXISTS');
      const i = ifNotExists ? 5 : 2;
      const end = nameEnd(statement, i);
      if (end === i || statement[end]?.value !== '(') return false;
      // IF NOT EXISTS may refer to a table that predates this migration.
      if (!ifNotExists) createdTables.add(nameKey(statement, i, end));
      return true;
    }
    if (['INDEX', 'TYPE', 'POLICY', 'TRIGGER'].includes(kind) || starts(statement.slice(1), 'UNIQUE', 'INDEX')) return true;
    if (kind === 'FUNCTION') {
      // A function declaration is allowed, but dynamic SQL in any body fails
      // closed. Trigger EXECUTE FUNCTION is a declaration, not dynamic SQL.
      return statement.filter(t => t.kind === 'literal').every(t => {
        // Encoded bodies (E/U& strings) can spell executable keywords through
        // escapes. Opaque escapes are never expand-safe function evidence.
        if (t.value.includes('\\')) return false;
        return !tokens(t.value).some(word => word.kind === 'word' && ['EXECUTE', 'DO', 'DROP', 'ALTER', 'TRUNCATE', 'DELETE'].includes(word.value));
      });
    }
    return false;
  }
  if (starts(statement, 'ALTER', 'TABLE')) {
    const i = starts(statement.slice(2), 'ONLY') ? 3 : 2;
    const end = nameEnd(statement, i);
    if (end === i) return false;
    const action = statement.slice(end);
    if (createdTables.has(nameKey(statement, i, end)) && (starts(action, 'ENABLE', 'ROW', 'LEVEL', 'SECURITY') || starts(action, 'FORCE', 'ROW', 'LEVEL', 'SECURITY')) && action.length === 4) return true;
    return split(action, ',').every(safeAlter);
  }
  return starts(statement, 'COMMENT', 'ON') || starts(statement, 'GRANT') || starts(statement, 'INSERT', 'INTO') || starts(statement, 'UPDATE') || starts(statement, 'SET', 'LOCAL');
}

export function destructive(sql) {
  try {
    // Keep malformed source fail-closed, but choose statement boundaries only
    // with the runner's splitter. Nested-looking comments can expose real SQL.
    tokens(sql);
    const createdTables = new Set();
    return splitSQL(sql).map(tokens).filter(statement => statement.length).some(statement => !expandSafe(statement, createdTables));
  } catch { return true; }
}

// The marker is a standalone header comment, before the first SQL statement.
// The ticket owns the evidence that expansion shipped in an earlier release.
function markerEvidence(sql, previousVersion) {
  const lines = [];
  for (const line of sql.split('\n')) {
    if (line.trim() && !line.trim().startsWith('--')) break;
    lines.push(line);
  }
  const prefix = lines.join('\n');
  const marker = /^-- aeon:contract-phase [A-Z][A-Z0-9]*-\d+ expanded-in=v(\d{12}\.0\.0) expansion-migration=(\d{4}_[a-z0-9_]+\.sql)[ \t\r]*$/m.exec(prefix);
  if (!marker || !validCalendarVersion(marker[1]) || (previousVersion && (!validCalendarVersion(previousVersion) || marker[1] > previousVersion))) return null;
  return {tag: `v${marker[1]}`, migration: marker[2]};
}
export function contractMarker(sql, previousVersion = null) {
  return !!markerEvidence(sql, previousVersion);
}
function expansionReleased(evidence, previousTag, repository = '.') {
  if (!evidence || !previousTag) return false;
  const git = args => execFileSync('git', args, {cwd: repository, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']}).trim();
  try {
    // Fully qualified refs prevent a branch from impersonating a release tag.
    const tags = new Set(git(['tag', '--list', 'v*']).split('\n'));
    if (!tags.has(evidence.tag) || !tags.has(previousTag)) return false;
    git(['merge-base', '--is-ancestor', `refs/tags/${evidence.tag}`, `refs/tags/${previousTag}`]);
    return git(['cat-file', '-t', `refs/tags/${evidence.tag}:internal/db/migrations/${evidence.migration}`]) === 'blob';
  } catch { return false; }
}

export function checkMigrations(files, published = new Map(), previousVersion = null, options = {}) {
  const problems = [], numbers = new Map();
  const grandfathered = options.baseline?.grandfatheredFiles ?? {};
  const sha256 = sql => createHash('sha256').update(sql).digest('hex');
  for (const [name, sql] of files) {
    const match = /^(\d{4})_[a-z0-9_]+\.sql$/.exec(name);
    if (!match) { problems.push(`${name}: expected NNNN_name.sql`); continue; }
    const number = Number(match[1]);
    if (numbers.has(number)) problems.push(`${name}: duplicate migration number ${match[1]} (also ${numbers.get(number)})`);
    numbers.set(number, name);
    if (published.has(name) && published.get(name) !== sql) problems.push(`${name}: published migration changed; add a new migration instead`);
    const legacy = grandfathered[name];
    if (legacy && legacy !== sha256(sql)) problems.push(`${name}: pre-policy migration changed; add a new migration instead`);
    // Classify all SQL; grandfather explicit published filenames and exact legacy
    // content, while still checking names, duplicates and immutability above.
    const requiresContract = destructive(sql);
    if (requiresContract && !published.has(name) && !legacy) {
      const evidence = markerEvidence(sql, previousVersion);
      if (!expansionReleased(evidence, options.previousTag ?? (previousVersion ? `v${previousVersion}` : evidence?.tag), options.repository)) problems.push(`${name}: non-allowlisted SQL requires -- aeon:contract-phase TICKET-N expanded-in=vYYMMDDhhmmss.0.0 expansion-migration=NNNN_name.sql (existing expansion release tag, at or before and ancestral to the previous release, containing the expansion migration)`);
    }
  }
  for (const name of published.keys()) if (!files.has(name)) problems.push(`${name}: published migration removed`);
  for (const name of Object.keys(grandfathered)) if (!files.has(name)) problems.push(`${name}: pre-policy migration removed`);
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
    const baseline = JSON.parse(readFileSync(new URL('./migration-policy-baseline.json', import.meta.url), 'utf8'));
    const published = publishedMigrations(`refs/tags/${args[1]}`);
    const problems = checkMigrations(files, published, args[1].slice(1), {baseline, previousTag: args[1]});
    if (problems.length) { console.error(problems.join('\n')); process.exitCode = 1; }
    else console.log(`migration guard: ${files.size} unique numbers; published and grandfathered files unchanged; expand-safe allowlist enforced for every new filename`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
