// SPDX-License-Identifier: AGPL-3.0-only
// OPS-257: node scripts/ci-flake-guard.mjs [--kind playwright|go] -- command ...
// PRs never retry. merge_group retries each non-quarantined failure once, in
// sequence. Native Playwright retries are disabled on BOTH attempts.
//
// Direct `go test` and `playwright test` commands retain their execution flags
// but replace test/package selectors. Go wrappers retry via `go test` (GOFLAGS
// still applies); CI_FLAKE_GO_RETRY_ARGS may supply a JSON array of extra flags.
// Playwright wrappers (including ci:web:shard) must consume
// CI_FLAKE_PLAYWRIGHT_TESTS: JSON [{id,file,line,column,title,project}], selecting
// only that test in its original config group. PW_GREP and PW_TEST_FILES (JSON
// file:line selectors) are also supplied. PW_RETRIES is always 0. A wrapper that
// cannot implement these filters must fail rather than rerun its whole suite.
// Use line or JSON reporter output on stdout; file-only reports are not read.
// With CI_FLAKE_REQUIRE_JSON=1 a failing wrapper must print its merged JSON
// report (ci:web:shard --reporter=line,json); infrastructure errors in it fail.
// Export GITHUB_STEP_SUMMARY; archive those files for ci-flake-report.mjs.
import { spawn } from 'node:child_process';
import { appendFileSync, readFileSync } from 'node:fs';
import { basename } from 'node:path';
import { StringDecoder } from 'node:string_decoder';
import { pathToFileURL } from 'node:url';

export const quarantineSchema = {
  type: 'object', additionalProperties: false, required: ['version', 'entries', 'template'],
  properties: {
    version: { const: 1 },
    entries: { type: 'array', maxItems: 100, items: { $ref: '#/$defs/entry' } },
    template: {
      type: 'object', additionalProperties: false,
      required: ['id', 'kind', 'owner', 'added', 'expires', 'note'],
      properties: {
        id: { type: 'string', minLength: 1 }, kind: { enum: ['go', 'playwright'] },
        owner: { type: 'string', minLength: 1 }, added: { const: 'YYYY-MM-DD' },
        expires: { const: 'YYYY-MM-DD' }, note: { type: 'string', minLength: 1 },
      },
    },
  },
  $defs: {
    entry: {
      type: 'object', additionalProperties: false,
      required: ['id', 'kind', 'owner', 'added', 'expires', 'note'],
      properties: {
        id: { type: 'string', minLength: 1, maxLength: 2000 },
        kind: { enum: ['go', 'playwright'] },
        owner: { type: 'string', pattern: '^[A-Z][A-Z0-9]*-[1-9][0-9]*$' },
        added: { type: 'string', format: 'date' },
        expires: { type: 'string', format: 'date' },
        note: { type: 'string', minLength: 1, maxLength: 2000 },
      },
    },
  },
};

function validDate(value) {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) &&
    Number.isFinite(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value;
}
function keysExactly(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
}
// Dependency-free validation of the exported JSON schema plus date ordering and
// duplicate identities. Invalid config never silently suppresses failures.
export function validateQuarantine(value) {
  if (!keysExactly(value, ['version', 'entries', 'template']) || value.version !== 1 ||
      !Array.isArray(value.entries) || value.entries.length > 100) throw new Error('Invalid quarantine document');
  const keys = ['id', 'kind', 'owner', 'added', 'expires', 'note'];
  const identities = new Set();
  for (const entry of [...value.entries, value.template]) {
    if (!keysExactly(entry, keys) || !['go', 'playwright'].includes(entry.kind) ||
        !['id', 'owner', 'note'].every(key => typeof entry[key] === 'string' && entry[key].trim().length > 0 && entry[key].length <= 2000)) {
      throw new Error('Invalid quarantine entry');
    }
    if (entry === value.template) {
      if (entry.added !== 'YYYY-MM-DD' || entry.expires !== 'YYYY-MM-DD') throw new Error('Invalid quarantine template');
      continue;
    }
    if (!/^[A-Z][A-Z0-9]*-[1-9][0-9]*$/.test(entry.owner) || !validDate(entry.added) ||
        !validDate(entry.expires) || entry.expires <= entry.added) throw new Error('Invalid quarantine owner/dates');
    const key = `${entry.kind}\0${entry.id}`;
    if (identities.has(key)) throw new Error('Duplicate quarantine identity');
    identities.add(key);
  }
  return value;
}

const stripAnsi = text => text.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replaceAll('\r', '\n');
export const escapeRegex = text => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const unique = values => [...new Map(values.map(value => [value.id, value])).values()];
const packageID = (pkg, module) => pkg.startsWith(`${module}/`) ? pkg.slice(module.length + 1) : pkg;

export function parseGo(output, module = 'github.com/inspr-at/paimos') {
  const records = [], packages = new Map();
  const unfinished = new Map();
  const open = pkg => { if (!unfinished.has(pkg)) unfinished.set(pkg, 1); };
  const close = pkg => {
    const remaining = (unfinished.get(pkg) ?? 1) - 1;
    if (remaining > 0) unfinished.set(pkg, remaining); else unfinished.delete(pkg);
  };
  const packageResult = (pkg, state) => {
    const previous = packages.get(pkg);
    packages.set(pkg, previous?.status === 'fail' ? { ...previous, build: previous.build || state.build } : state);
  };
  let pending = [];
  const addTest = (pkg, name, status) => records.push({
    kind: 'go', id: `${packageID(pkg, module)} ${name}`, package: pkg, name, status,
  });
  for (const line of stripAnsi(output).split('\n')) {
    let event;
    if (line.startsWith('{')) { try { event = JSON.parse(line); } catch { /* text */ } }
    if (event?.Package && ['pass', 'fail', 'skip'].includes(event.Action)) {
      if (event.Test) {
        open(event.Package);
        if (event.Action !== 'skip') addTest(event.Package, event.Test, event.Action);
      } else {
        close(event.Package);
        packageResult(event.Package, { status: event.Action, build: event.FailedBuild });
      }
      continue;
    }
    if (event?.Package && event.Action === 'start') unfinished.set(event.Package, (unfinished.get(event.Package) ?? 0) + 1);
    if (event?.Package && event.Action === 'run') open(event.Package);
    // Ignore output events: their text is repeated by the authoritative JSON
    // terminal events and otherwise confuses text-package attribution.
    if (event?.Action) continue;
    const terminal = line.match(/^\s*--- (FAIL|PASS): (\S+)\s+\(/);
    if (terminal) pending.push({ name: terminal[2], status: terminal[1] === 'FAIL' ? 'fail' : 'pass' });
    const pkg = line.match(/^(FAIL|ok)\s+(\S+)(?:\s+(.*))?$/);
    if (pkg) {
      for (const { name, status } of pending) addTest(pkg[2], name, status);
      pending = [];
      packageResult(pkg[2], { status: pkg[1] === 'FAIL' ? 'fail' : 'pass', build: pkg[3]?.includes('[build failed]') });
    }
  }
  const failedTests = unique(records.filter(value => value.status === 'fail'));
  // Parent FAIL events duplicate leaf subtests; retry the leaves, not the
  // passing siblings in their parent. A parent quarantine covers its subtests.
  const parents = new Set();
  for (const value of failedTests) {
    for (let slash = value.id.lastIndexOf('/'); slash >= 0; slash = value.id.lastIndexOf('/', slash - 1)) {
      parents.add(value.id.slice(0, slash));
      if (slash === 0) break;
    }
  }
  const failures = failedTests.filter(value => !parents.has(value.id));
  const testPackages = new Set(failedTests.map(value => value.package));
  for (const [pkg, state] of packages) {
    if (state.status === 'fail' && (state.build || !testPackages.has(pkg))) {
      failures.push({ kind: 'go', id: packageID(pkg, module), package: pkg, status: 'fail', infrastructure: !!state.build });
    }
  }
  return { failures, records, packages, incomplete: pending.length > 0 || unfinished.size > 0 };
}

// JSON reporters can be interleaved with npm banners or several config groups.
// Scan complete top-level JSON objects without a quadratic substring search.
function jsonObjects(output) {
  const values = [];
  let start = -1, depth = 0, quoted = false, escaped = false;
  for (let index = 0; index < output.length; index++) {
    const char = output[index];
    if (start < 0) {
      if (char === '{' && (index === 0 || output[index - 1] === '\n')) { start = index; depth = 1; }
      continue;
    }
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') quoted = false;
    } else if (char === '"') quoted = true;
    else if (char === '{' || char === '[') depth++;
    else if (char === '}' || char === ']') {
      if (--depth === 0) {
        try { values.push(JSON.parse(output.slice(start, index + 1))); } catch { /* non-reporter JSON */ }
        start = -1;
      }
    }
  }
  return values;
}
function pwRecord(file, line, column, title, project = '', status = 'fail') {
  const location = `${file}:${line}:${column}`;
  return { kind: 'playwright', id: `${project ? `[${project}] › ` : ''}${location} › ${title}`,
    file, line: Number(line), column: Number(column), title, project, status };
}
export function parsePlaywright(output) {
  const clean = stripAnsi(output), records = [];
  let infrastructure = false, reports = 0;
  for (const report of jsonObjects(clean)) {
    if (!Array.isArray(report.suites)) continue;
    reports++;
    infrastructure ||= Array.isArray(report.errors) && report.errors.length > 0;
    function visit(suite, ancestors = [], depth = 0) {
      if (depth > 100) throw new Error('Playwright report nesting exceeds limit');
      const title = suite.title && suite.title !== suite.file && suite.title !== basename(suite.file ?? '') ? [...ancestors, suite.title] : ancestors;
      for (const spec of suite.specs ?? []) {
        for (const test of spec.tests ?? []) {
          const last = test.results?.at(-1);
          const failed = test.status === 'unexpected' || (!test.status && last &&
            !['skipped', test.expectedStatus ?? 'passed'].includes(last.status));
          const status = failed ? 'fail' : test.status === 'skipped' || last?.status === 'skipped' ? 'skip' : 'pass';
          records.push(pwRecord(spec.file ?? suite.file, spec.line, spec.column,
            [...title, spec.title].join(' › '), test.projectName ?? '', status));
        }
      }
      for (const child of suite.suites ?? []) visit(child, title, depth + 1);
    }
    for (const suite of report.suites) visit(suite);
  }
  // Both failure details and the final failed-test list use this heading.
  // Progress lines provide execution evidence for a successful narrowed retry.
  // Valid structured reports are authoritative: terminal headings are cwd-relative
  // (tests/a.spec.ts) while JSON paths are testDir-relative (a.spec.ts), so mixing
  // both would give one test two identities. Headings are the fallback only.
  for (const line of reports > 0 ? [] : clean.split('\n')) {
    const heading = line.match(/^\s*(\d+\)|\[\d+\/\d+\])\s+(?:\[([^\]]+)\]\s+›\s+)?(.+?):(\d+):(\d+)\s+›\s+(.*?)\s*(?:={2,})?\s*$/);
    if (heading) records.push(pwRecord(heading[3], heading[4], heading[5], heading[6], heading[2] ?? '', heading[1].endsWith(')') ? 'fail' : 'observed'));
  }
  return { failures: unique(records.filter(value => value.status === 'fail')),
    records: unique(records), infrastructure, reports };
}

export function isQuarantined(failure, entries, now = new Date()) {
  if (failure.infrastructure) return false;
  const date = now.toISOString().slice(0, 10);
  return entries.some(entry => entry.kind === failure.kind && entry.added <= date && date < entry.expires &&
    (entry.id === failure.id || (failure.kind === 'go' && failure.id.startsWith(`${entry.id}/`))));
}

const goValueFlags = new Set(['-run', '-skip', '-count', '-timeout', '-p', '-parallel', '-cpu', '-benchtime', '-bench', '-fuzz', '-fuzztime', '-fuzzminimizetime', '-tags', '-coverprofile', '-coverpkg', '-covermode', '-mod', '-modfile', '-overlay', '-vet', '-exec', '-gcflags', '-ldflags', '-asmflags', '-buildmode', '-pkgdir', '-shuffle', '-o', '-list', '-blockprofile', '-blockprofilerate', '-cpuprofile', '-memprofile', '-memprofilerate', '-mutexprofile', '-mutexprofilefraction', '-trace', '-outputdir']);
const pwValueFlags = new Set(['--config', '-c', '--grep', '-g', '--grep-invert', '--project', '--workers', '-j', '--retries', '--reporter', '--timeout', '--global-timeout', '--max-failures', '--repeat-each', '--shard', '--output', '--browser']);
function retainedOptions(args, valueFlags, removed) {
  const options = [];
  for (let index = 0; index < args.length; index++) {
    const arg = args[index];
    if (arg === '--' || arg === '-args') throw new Error('Cannot safely narrow command after --/-args');
    if (!arg.startsWith('-')) continue; // original file/package selectors
    const rawKey = arg.split('=')[0];
    const key = valueFlags === goValueFlags ? rawKey.replace(/^-test\./, '-') : rawKey;
    const value = !arg.includes('=') && valueFlags.has(key) ? args[++index] : undefined;
    if (valueFlags.has(key) && !arg.includes('=') && value === undefined) throw new Error(`Missing value for ${key}`);
    if (!removed.has(key)) options.push(arg, ...(value === undefined ? [] : [value]));
  }
  return options;
}
function goExpression(name) {
  // Go splits -run on / before compiling each RE2 expression.
  return name.split('/').map(part => `^${escapeRegex(part)}$`).join('/');
}
export function retryCommand(command, failure, env = {}) {
  if (failure.kind === 'go') {
    const direct = basename(command[0]) === 'go' && command[1] === 'test';
    const extra = env.CI_FLAKE_GO_RETRY_ARGS ? JSON.parse(env.CI_FLAKE_GO_RETRY_ARGS) : [];
    if (!Array.isArray(extra) || extra.length > 100 || !extra.every(value => typeof value === 'string' && value.startsWith('-') && value.length <= 2000)) {
      throw new Error('CI_FLAKE_GO_RETRY_ARGS must be a JSON array of -flag=value strings');
    }
    const args = retainedOptions(direct ? command.slice(2) : extra, goValueFlags,
      new Set(['-run', '-skip', '-count', '-bench', '-list', '-failfast', '-fuzz', '-fuzztime', '-fuzzminimizetime']));
    return { command: [direct ? command[0] : 'go', 'test', ...args, '-count=1', '-json',
      ...(failure.name ? ['-run', goExpression(failure.name)] : []), failure.package], env: {} };
  }
  if (!failure.file || !Number.isInteger(failure.line) || failure.line < 1 || !failure.title) throw new Error('Missing Playwright retry identity');
  const grep = `(?:^| )${escapeRegex(failure.title.split(' › ').join(' '))}$`;
  const selector = `${failure.file}:${failure.line}`;
  const retryEnv = {
    CI_FLAKE_PLAYWRIGHT_TESTS: JSON.stringify([failure]), PW_RETRIES: '0',
    PW_GREP: grep, PW_TEST_FILES: JSON.stringify([selector]),
  };
  const index = command.findIndex((part, i) => part === 'test' && i > 0 && /(?:^|\/)playwright(?:\.cmd)?$/.test(command[i - 1]));
  if (index < 0) return { command: [...command], env: retryEnv };
  const options = retainedOptions(command.slice(index + 1), pwValueFlags,
    new Set(['--grep', '-g', '--grep-invert', '--project', '--retries', '--repeat-each', '--shard', '--last-failed', '--list', '--ui', '--debug', '--ui-host', '--ui-port']));
  return { command: [...command.slice(0, index + 1), ...options, selector, '--grep', grep, '--retries=0',
    '--no-deps', ...(failure.project ? ['--project', failure.project] : [])], env: retryEnv };
}

function initialCommand(command) {
  const result = [...command];
  if (result.some((part, index) => part === 'test' && /(?:^|\/)playwright(?:\.cmd)?$/.test(result[index - 1] ?? ''))) {
    // Last option wins; also override config retries for direct invocation.
    result.push('--retries=0');
  }
  return result;
}
function markdown(value) { return String(value).replace(/[|\r\n]/g, char => char === '|' ? '&#124;' : ' ').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); }
export function writeEvidence(record, { summary, print = text => process.stdout.write(text), now = new Date() } = {}) {
  const evidence = { date: now.toISOString(), ...record };
  // JSON makes archived Markdown summaries unambiguous to the weekly reader.
  const line = `CI_FLAKE ${JSON.stringify(evidence)}\n`;
  print(line);
  if (summary) appendFileSync(summary, `\n| Label | Kind | Test id | Attempt |\n| --- | --- | --- | ---: |\n| ${markdown(record.label)} | ${markdown(record.kind)} | ${markdown(record.id)} | ${record.attempt} |\n<!-- ${line.trim().replaceAll('--', '\\u002d\\u002d')} -->\n`);
}

export function execute(command, { env, cwd, print = (text, stream) => process[stream].write(text), timeoutMs = 20 * 60 * 1000, maxOutputBytes = 32 * 1024 * 1024 } = {}) {
  return new Promise(resolve => {
    const child = spawn(command[0], command.slice(1), { cwd, env, stdio: ['ignore', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
    const captured = { stdout: '', stderr: '' };
    let bytes = 0, overflow = false, interrupted = false, timedOut = false;
    const kill = signal => {
      try { if (process.platform === 'win32') child.kill(signal); else process.kill(-child.pid, signal); } catch { /* already exited */ }
    };
    let escalation;
    const stop = signal => { interrupted = true; kill(signal); escalation ??= setTimeout(() => kill('SIGKILL'), 5000); };
    const signals = ['SIGINT', 'SIGTERM'];
    const handlers = signals.map(signal => () => stop(signal));
    signals.forEach((signal, index) => process.on(signal, handlers[index]));
    const timer = setTimeout(() => { timedOut = true; stop('SIGTERM'); }, timeoutMs);
    for (const stream of ['stdout', 'stderr']) {
      const decoder = new StringDecoder('utf8');
      child[stream].on('data', data => {
        print(data, stream);
        bytes += data.length;
        if (bytes <= maxOutputBytes) captured[stream] += decoder.write(data);
        else overflow = true;
      });
      child[stream].on('end', () => { if (!overflow) captured[stream] += decoder.end(); });
    }
    let settled = false;
    const finish = result => {
      if (settled) return;
      settled = true;
      clearTimeout(timer); clearTimeout(escalation);
      signals.forEach((signal, index) => process.off(signal, handlers[index]));
      const output = [captured.stdout, captured.stderr].filter(Boolean).join('\n');
      resolve({ output, overflow, interrupted, timedOut, ...result });
    };
    child.once('error', error => finish({ code: 127, error: error.message }));
    child.once('close', (code, signal) => finish({ code: code ?? 1, signal }));
  });
}

export async function runGuard({ command, kind, env = process.env, quarantine, now = new Date(), run = execute,
  print = text => process.stdout.write(text), module = 'github.com/inspr-at/paimos' }) {
  if (!command?.length || !command.every(value => typeof value === 'string') || (kind && !['go', 'playwright'].includes(kind))) throw new Error('Invalid command/kind');
  validateQuarantine(quarantine);
  const evidence = record => writeEvidence(record, { summary: env.GITHUB_STEP_SUMMARY, print, now });
  const childEnv = { ...env, PW_RETRIES: '0' };
  const result = await run(initialCommand(command), { env: childEnv });
  if (result.interrupted || result.timedOut || result.error || result.overflow) {
    evidence({ id: '<command>', kind: kind ?? 'unknown', attempt: 1, label: 'FAILED', reason: 'interrupted, timed out, output truncated, or launch failed' });
    return result.code || 1;
  }
  const go = kind !== 'playwright' ? parseGo(result.output, module) : undefined;
  const pw = kind !== 'go' ? parsePlaywright(result.output) : undefined;
  const failures = [...(go?.failures ?? []), ...(pw?.failures ?? [])];
  const active = [];
  for (const failure of failures) {
    if (isQuarantined(failure, quarantine.entries, now)) evidence({ id: failure.id, kind: failure.kind, attempt: 1, label: 'QUARANTINED' });
    else active.push(failure);
  }
  // Test failures with a zero wrapper exit code still fail. Conversely, crashes,
  // incomplete output and setup failures cannot be waived by a quarantine.
  // CI_FLAKE_REQUIRE_JSON=1 (set for the sharded web job): a wrapper that runs
  // several config groups must hand over its complete merged JSON report. Line
  // output cannot show that another group failed to start, so a red exit without
  // a structured report is never retried.
  const lacksReport = !!pw && env.CI_FLAKE_REQUIRE_JSON === '1' && result.code !== 0 && pw.reports === 0;
  if (go?.incomplete || pw?.infrastructure || lacksReport || (result.code !== 0 && failures.length === 0)) {
    evidence({ id: '<command>', kind: kind ?? 'unknown', attempt: 1, label: 'FAILED', reason: 'unidentified or incomplete failure' });
    return result.code || 1;
  }
  if (!active.length) return 0;
  if (env.GITHUB_EVENT_NAME !== 'merge_group') {
    for (const failure of active) evidence({ id: failure.id, kind: failure.kind, attempt: 1, label: 'FAILED' });
    return result.code || 1;
  }
  let failed = false;
  for (const failure of active) {
    let plan;
    try { plan = retryCommand(command, failure, env); }
    catch (error) {
      evidence({ id: failure.id, kind: failure.kind, attempt: 1, label: 'FAILED', reason: error.message });
      failed = true; continue;
    }
    evidence({ id: failure.id, kind: failure.kind, attempt: 2, label: 'RETRIED' });
    const retry = await run(plan.command, { env: { ...childEnv, ...plan.env } });
    const parsed = failure.kind === 'go' ? parseGo(retry.output, module) : parsePlaywright(retry.output);
    let realFailures = false;
    for (const repeated of parsed.failures) {
      if (isQuarantined(repeated, quarantine.entries, now)) evidence({ id: repeated.id, kind: repeated.kind, attempt: 2, label: 'QUARANTINED' });
      else {
        realFailures = true;
        evidence({ id: repeated.id, kind: repeated.kind, attempt: 2, label: 'FAILED AFTER RETRY' });
      }
    }
    const observed = failure.kind === 'go'
      ? failure.name ? parsed.records.some(record => record.id === failure.id) : parsed.packages.has(failure.package)
      : parsed.records.some(record => record.id === failure.id && record.status !== 'skip');
    // A wrapper ignoring the filter cannot quietly clear a flake by running the
    // full shard. Skipped/discovered cases are harmless, executed extra tests
    // are an integration failure. Go leaf retries must run their parent setup.
    const extraTests = failure.kind === 'playwright' && parsed.records.some(record => record.id !== failure.id && record.status !== 'skip');
    const retryLacksReport = failure.kind === 'playwright' && env.CI_FLAKE_REQUIRE_JSON === '1' && parsed.reports === 0;
    if (retry.interrupted || retry.error || retry.timedOut || retry.overflow || parsed.incomplete || parsed.infrastructure || retryLacksReport || !observed || extraTests ||
        (retry.code !== 0 && parsed.failures.length === 0)) {
      failed = true;
      evidence({ id: failure.id, kind: failure.kind, attempt: 2, label: 'FAILED AFTER RETRY',
        reason: extraTests ? 'wrapper did not narrow retry to the failed test' : 'retry failed or execution evidence is missing' });
    }
    failed ||= realFailures;
    if (retry.interrupted || retry.timedOut) return 1;
  }
  return failed ? 1 : 0;
}

export async function main(args = process.argv.slice(2), env = process.env) {
  const separator = args.indexOf('--');
  const flags = args.slice(0, separator);
  if (separator < 0 || separator === args.length - 1 ||
      !(flags.length === 0 || (flags.length === 2 && flags[0] === '--kind' && ['go', 'playwright'].includes(flags[1])))) {
    throw new Error('Usage: node scripts/ci-flake-guard.mjs [--kind playwright|go] -- <command...>');
  }
  const quarantine = JSON.parse(readFileSync(new URL('./ci-quarantine.json', import.meta.url), 'utf8'));
  let module;
  try { module = readFileSync('go.mod', 'utf8').match(/^module\s+(\S+)/m)?.[1]; } catch { /* non-Go cwd */ }
  return runGuard({ command: args.slice(separator + 1), kind: flags[1], env, quarantine, module });
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().then(code => { process.exitCode = code; }).catch(error => {
    console.error(`ci-flake-guard: ${error.message}`); process.exitCode = 2;
  });
}
