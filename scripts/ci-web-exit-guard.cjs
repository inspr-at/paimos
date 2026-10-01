// SPDX-License-Identifier: AGPL-3.0-only
'use strict';

// Preload before repository JavaScript; permit only verified Playwright shutdown.
const exit = process.exit.bind(process);
const kill = process.kill.bind(process);
const reallyExit = process.reallyExit.bind(process);
const { writeSync: write, readFileSync: read, realpathSync: realpath } = require('node:fs');
const { resolve, relative } = require('node:path');
const { createHash } = require('node:crypto');
const Module = require('node:module');
const repository = realpath(resolve(process.env.AEON_CI_REPO_ROOT || resolve(__dirname, '..')));
const web = resolve(repository, 'web');
const manifest = JSON.parse(process.env.PLAYWRIGHT_MANIFEST || '{}');
const entries = new Map(Object.entries(manifest));
const lookup = entries.get.bind(entries);
const call = Function.prototype.call.bind(Function.prototype.call);
const hashPrototype = Object.getPrototypeOf(createHash('sha256'));
const updateHash = hashPrototype.update;
const digestHash = hashPrototype.digest;
const sha256 = bytes => {
  const hash = createHash('sha256');
  call(updateHash, hash, bytes);
  return call(digestHash, hash, 'hex');
};
const packagePath = /^node_modules\/(?:@playwright\/test|playwright|playwright-core)\//;
const testPath = packagePath.test.bind(packagePath);
if (!lookup('node_modules/@playwright/test/cli.js') || [...entries].some(([path, digest]) =>
  !packagePath.test(path) || path.split('/').some(p => p === '.' || p === '..') ||
  !/^[0-9a-f]{64}$/.test(digest))) throw new Error('Invalid sealed Playwright manifest');

function verifiedFile(filename, content) {
  try {
    const path = relative(web, filename);
    const expected = lookup(path);
    return testPath(path) && expected && realpath(filename) === filename &&
      sha256(content === undefined ? read(filename) : content) === expected;
  } catch { return false; }
}

// Verify the source handed to Node's compiler, closing the check/read race.
const compile = Module.prototype._compile;
Object.defineProperty(Module.prototype, '_compile', {
  configurable: false, writable: false,
  value: function (content, filename, ...args) {
    if (testPath(relative(web, filename)) && !verifiedFile(filename, content)) {
      write(2, 'UI check rejected changed Playwright compiler input\n');
      reallyExit(1);
    }
    return call(compile, this, content, filename, ...args);
  },
});

// A private realm retains raw CallSites even if specs change the public Error.
const StackError = require('node:vm').runInNewContext('Error');
Object.defineProperty(StackError, 'prepareStackTrace', {
  configurable: false, writable: false, value: (_error, frames) => frames,
});
Object.defineProperty(StackError, 'stackTraceLimit', {
  configurable: false, writable: false, value: 100,
});
const initialFrame = new StackError().stack[0];
const getFileName = Function.prototype.call.bind(initialFrame.getFileName);
const isEval = Function.prototype.call.bind(initialFrame.isEval);
const startsWith = Function.prototype.call.bind(String.prototype.startsWith);

function checkCaller(operation) {
  const frames = new StackError().stack;
  for (let index = 0; index < frames.length; index++) {
    const frame = frames[index];
    const filename = getFileName(frame);
    if (!isEval(frame) && (filename === __filename ||
        (filename && startsWith(filename, 'node:')) || (filename && verifiedFile(filename)))) continue;
    write(2, `UI check rejected process.${operation} from unverified code\n`);
    reallyExit(1);
  }
}

for (const [name, original] of [['exit', exit], ['kill', kill]]) {
  Object.defineProperty(process, name, {
    configurable: false,
    writable: false,
    value: function (...args) {
      checkCaller(name);
      return original(...args);
    },
  });
}

// Playwright's bundled signal-exit replaces reallyExit during startup
// and restores it during shutdown. Keep every version guarded and reject
// replacements from repository code, without breaking dependency lifecycle hooks.
function guardedReallyExit(original) {
  return function (...args) {
    checkCaller('reallyExit');
    return call(original, process, ...args);
  };
}
let currentReallyExit = guardedReallyExit(reallyExit);
Object.defineProperty(process, 'reallyExit', {
  configurable: false,
  get() { return currentReallyExit; },
  set(value) {
    checkCaller('reallyExit');
    if (typeof value !== 'function') throw new TypeError('reallyExit must remain a function');
    currentReallyExit = guardedReallyExit(value);
  },
});
