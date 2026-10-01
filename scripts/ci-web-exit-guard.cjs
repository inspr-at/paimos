// SPDX-License-Identifier: AGPL-3.0-only
'use strict';

// Preload before repository JavaScript; permit Playwright/node_modules shutdown.
const exit = process.exit.bind(process);
const kill = process.kill.bind(process);
const reallyExit = process.reallyExit.bind(process);
const write = require('node:fs').writeSync;
const { resolve } = require('node:path');
const repository = resolve(process.env.AEON_CI_REPO_ROOT || resolve(__dirname, '..'));
const prepareStackTrace = Error.prepareStackTrace;
// A private realm retains the startup formatter and stack depth, while allowing
// Playwright's source maps to adjust the public Error constructor as usual.
const StackError = require('node:vm').runInNewContext('Error');
Object.defineProperty(StackError, 'prepareStackTrace', {
  configurable: false, writable: false, value: prepareStackTrace,
});
Object.defineProperty(StackError, 'stackTraceLimit', {
  configurable: false, writable: false, value: 100,
});

function checkCaller(operation) {
  const frames = String(new StackError().stack).split('\n');
  if (frames.some(frame => frame.includes(repository + '/') &&
      !frame.includes('/node_modules/') && !frame.includes(__filename))) {
    write(2, `UI check rejected process.${operation} from repository code\n`);
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

// signal-exit (used by npm and Playwright) replaces reallyExit during startup
// and restores it during shutdown. Keep every version guarded and reject
// replacements from repository code, without breaking dependency lifecycle hooks.
function guardedReallyExit(original) {
  return function (...args) {
    checkCaller('reallyExit');
    return original.apply(process, args);
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
