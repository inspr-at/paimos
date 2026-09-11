#!/usr/bin/env python3
"""Select deterministic quality checks by their complete repository inputs."""
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
# Paths include transitively called scripts and scanned directories. Release
# hygiene scans all these trees through check-branding-hygiene.sh; narrowing
# it to release files would lose that assertion.
INPUTS = {
    'release_hygiene': ('VERSION', 'README.md', 'Justfile', 'backend/', 'frontend/',
                        'data/', 'docs/', 'scripts/', '.github/', '.paimos/cache/PAI/'),
    'calendar_display': ('scripts/check-calendar-version-display.sh',
                         'scripts/check-calendar-version-display-pin.sh',
                         'frontend/src/brand/calendar-version-display.json',
                         'doctrine', 'doctrine/', '.gitmodules'),
    'release_doc_sync': ('scripts/test-release-doc-sync.sh', 'scripts/release-doc-sync.sh',
                         'scripts/release-version.sh'),
    # The live curl calls are replaced by the test's local fixture.
    'knowledge_freshness': ('scripts/test-knowledge-freshness.sh',
                            'scripts/check-knowledge-freshness.sh', 'VERSION', 'README.md',
                            'docs/', 'backend/handlers/schema.go', '.paimos/cache/PAI/'),
    # Network/SSH functions in _deploy-lib are only sourced, never invoked.
    'deploy_target': ('scripts/test-deploy-target.sh', 'scripts/deploy-target.sh',
                      'scripts/_deploy-lib.sh', 'scripts/release-version.sh'),
    'backend_gate': ('backend/', 'scripts/backend-', 'scripts/test-backend-',
                     'scripts/testdata/backend-gate/', 'scripts/wait-backend-full.sh',
                     'scripts/quality-ci-inputs.py', '.github/workflows/backend-full.yml',
                     'docs/RELEASE.md'),
    # Builds can depend on embedded non-Go assets as well as Go/module files.
    'dev_login': ('backend/', 'Dockerfile', '.dockerignore'),
}
# Both suites consult the current UTC/Vienna date. Keep them on every PR.
ALWAYS = ('release', 'release_version')
# A broad shared envelope deliberately covers transitive scripts, workflows,
# fixtures and build settings. Adding a helper under scripts cannot silently
# escape a gate. Go build tags are source inputs, covered by backend/ below
# for checks that compile code (backend_gate and dev_login).
COMMON = ('scripts/', '.github/', 'backend/go.mod', 'backend/go.sum',
          'go.work', 'go.work.sum', 'Dockerfile', '.dockerignore', 'Justfile',
          'Makefile', 'devenv.nix', 'devenv.yaml', 'devenv.lock',
          'flake.nix', 'flake.lock', '.envrc')


def matches(path, inputs):
    return any(path == item or (item.endswith(('/', '-')) and path.startswith(item))
               for item in inputs)


def select(paths):
    return {name: paths is None or any(matches(path, COMMON + inputs) for path in paths)
            for name, inputs in INPUTS.items()} | {name: True for name in ALWAYS}


def main():
    if len(sys.argv) == 3 and sys.argv[1] == '--files-from' and sys.argv[2] == '-':
        paths = sys.stdin.read().splitlines()
    elif len(sys.argv) == 3:
        try:
            data = subprocess.check_output([
                os.environ.get('GIT_COMMAND', 'git'), '-C', str(ROOT), 'diff',
                '--no-renames', '--name-only', '-z', sys.argv[1], sys.argv[2], '--',
            ])
            paths = data.decode().rstrip('\0').split('\0') if data else []
        except (OSError, subprocess.CalledProcessError, UnicodeError):
            print('quality-ci-inputs: diff unavailable; run every check', file=sys.stderr)
            paths = None
    else:
        raise SystemExit('usage: quality-ci-inputs.py <base> <head> | --files-from -')
    for name, enabled in select(paths).items():
        print(f'{name}={str(enabled).lower()}')


if __name__ == '__main__':
    main()
