// SPDX-License-Identifier: AGPL-3.0-only
'use strict';

// Preload in the CLI and its workers before spec/source code can forge successful
// reporter output and terminate the process. Playwright's own shutdown is allowed.
const exit = process.exit.bind(process);
const kill = process.kill.bind(process);
const write = require('node:fs').writeSync;
const StackError = Error;
const repositoryCaller = /[\\/]web[\\/](?:tests|src)[\\/]/;

function checkCaller(operation) {
  if (repositoryCaller.test(new StackError().stack)) {
    write(2, `UI check rejected process.${operation} from web/tests or web/src\n`);
    exit(1);
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
