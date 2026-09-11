#!/usr/bin/env python3
"""Find race-dependent packages and their transitive (including test) importers."""
import re
import sys
from pathlib import Path

# Tokenize before matching: documentation, fixture strings and ordinary build
# metadata such as debug.ReadBuildInfo are not race-dependent execution.
TOKEN = re.compile(r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\])*"|`[^`]*`|\'(?:\\.|[^\'\\])*\'|[A-Za-z_][A-Za-z_0-9]*|[^\s]', re.S)
RACE_FLAG = re.compile(r'(?:is_?)?race_?(?:enabled|detector_?enabled|build)', re.I)


def inspect(source):
    tokens = TOKEN.findall(source)
    guarded = any(re.match(r'//(?:go:build|\s*\+build)\s', token) and
                  re.search(r'\brace\b', token) for token in tokens)
    tokens = [token for token in tokens if not token.startswith(('//', '/*'))]
    imports = set()
    for i, token in enumerate(tokens):
        if token != 'import':
            continue
        end = i + 1
        if end < len(tokens) and tokens[end] == '(':
            end += 1
            while end < len(tokens) and tokens[end] != ')':
                if tokens[end].startswith(('"', '`')):
                    imports.add(tokens[end][1:-1])
                end += 1
        else:
            # Optional alias (identifier or dot), then one import string.
            if end < len(tokens) and not tokens[end].startswith(('"', '`')):
                end += 1
            if end < len(tokens) and tokens[end].startswith(('"', '`')):
                imports.add(tokens[end][1:-1])
    guarded |= 'runtime/race' in imports
    # Match executable flag references/declarations, never comments or strings.
    guarded |= any(RACE_FLAG.fullmatch(token) for token in tokens)
    return bool(guarded), imports


def exclusions(root):
    root = Path(root)
    module = re.search(r'(?m)^module\s+(\S+)', (root / 'go.mod').read_text())[1]
    dependencies = {}
    guarded = set()
    for path in sorted(root.rglob('*.go')):
        relative = path.relative_to(root)
        # These directories are not module packages in go list ./....
        if any(part in ('vendor', 'testdata') or part.startswith(('.', '_'))
               for part in relative.parts[:-1]):
            continue
        suffix = path.parent.relative_to(root).as_posix()
        package = module + ('/' + suffix if suffix != '.' else '')
        depends_on_race, imports = inspect(path.read_text())
        dependencies.setdefault(package, set()).update(imports)
        if depends_on_race:
            guarded.add(package)
    retained = guarded.copy()
    # Inspect imports in all build variants. This safely over-approximates the
    # graph and includes external test packages without running a Go build.
    while True:
        importers = {package for package, imports in dependencies.items()
                     if imports & retained}
        if importers <= retained:
            break
        retained |= importers
    return module, guarded, retained


def main():
    check = len(sys.argv) == 3 and sys.argv[1] == '--check'
    if not check and len(sys.argv) != 2:
        raise SystemExit('usage: backend-race-exclusions.py [--check] <backend>')
    module, guarded, retained = exclusions(sys.argv[-1])
    if check and guarded:
        raise SystemExit('backend-race-exclusions: audit new race guards in ' +
                         ', '.join(sorted(guarded)))
    for package in sorted(retained):
        print('.' + package[len(module):])


if __name__ == '__main__':
    main()
