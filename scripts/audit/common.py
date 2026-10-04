# SPDX-License-Identifier: AGPL-3.0-only
"""Shared offline contracts and canonical identities for audit tools."""

import hashlib
import json
import re
import subprocess
from pathlib import Path, PurePosixPath

HERE = Path(__file__).resolve().parent
SEVERITIES = ('critical', 'high', 'medium', 'low', 'info')


def load(path):
    with Path(path).open(encoding='utf-8') as stream:
        return json.load(stream)


def dump(data, path=None):
    text = json.dumps(data, ensure_ascii=False, sort_keys=True, indent=2) + '\n'
    if path:
        Path(path).write_text(text, encoding='utf-8')
    else:
        print(text, end='')


def git(repo, *args):
    result = subprocess.run(['git', '--no-optional-locks', '-C', str(repo), *args],
                            capture_output=True, check=False)
    if result.returncode:
        raise ValueError('git snapshot operation failed')
    return result.stdout


def snapshot(repo, ref):
    return git(repo, 'rev-parse', '--verify', '--end-of-options', ref + '^{commit}').decode().strip()


def tracked(repo, sha):
    return sorted(p.decode('utf-8') for p in git(repo, 'ls-tree', '-rz', '--name-only', sha).split(b'\0') if p)


def path_name(location):
    """Ignore line-number churn, but preserve a repo-relative file identity."""
    path = re.sub(r':\d+(?:-\d+)?$', '', location).replace('\\', '/')
    parts = path.split('/')
    if not path or PurePosixPath(path).is_absolute() or any(p in ('', '.', '..') for p in parts):
        raise ValueError('locations must use repository-relative paths')
    return path


def fingerprint(finding, tool=False):
    """AEON-571 identity: files + theme + title; tools also include producer."""
    normalize = lambda s: ' '.join(s.split()).casefold()
    identity = [sorted({path_name(p) for p in finding['locations']}),
                normalize(finding['theme']), normalize(finding['title'])]
    if tool:
        identity.append(normalize(finding.get('tool') or finding['theme']))
    return hashlib.sha256(json.dumps(identity, ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()


def validate(value, schema, root=None, at='$'):
    """Validate the checked-in schema's deliberately small JSON Schema subset."""
    root = root or schema
    if '$ref' in schema:
        target = root
        for part in schema['$ref'].removeprefix('#/').split('/'):
            target = target[part]
        return validate(value, target, root, at)
    kinds = {'object': dict, 'array': list, 'string': str, 'integer': int}
    kind = schema.get('type')
    if kind and (not isinstance(value, kinds[kind]) or kind == 'integer' and isinstance(value, bool)):
        raise ValueError(f'{at}: expected {kind}')
    if 'enum' in schema and value not in schema['enum']:
        raise ValueError(f'{at}: invalid enum value')
    if isinstance(value, dict):
        required = set(schema.get('required', [])) - value.keys()
        if required:
            raise ValueError(f'{at}: missing {", ".join(sorted(required))}')
        props = schema.get('properties', {})
        if schema.get('additionalProperties') is False and value.keys() - props.keys():
            raise ValueError(f'{at}: unknown properties')
        for key in value.keys() & props.keys():
            validate(value[key], props[key], root, f'{at}.{key}')
    if isinstance(value, list):
        if len(value) < schema.get('minItems', 0):
            raise ValueError(f'{at}: too few items')
        if schema.get('uniqueItems') and len({json.dumps(v, sort_keys=True) for v in value}) != len(value):
            raise ValueError(f'{at}: duplicate items')
        for index, item in enumerate(value):
            validate(item, schema.get('items', {}), root, f'{at}[{index}]')
    if isinstance(value, str):
        if len(value.strip()) < schema.get('minLength', 0) or 'pattern' in schema and not re.search(schema['pattern'], value):
            raise ValueError(f'{at}: invalid string')
    if isinstance(value, int) and value < schema.get('minimum', value):
        raise ValueError(f'{at}: below minimum')


def manifest(data):
    validate(data, load(HERE / 'finding.schema.json'))
    for entry in data['coverage']:
        if path_name(entry['file']) != entry['file']:
            raise ValueError('coverage must use plain file paths')
    for finding in data['findings']:
        fingerprint(finding)
    return data


def cli(main):
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as error:
        # Never echo source JSON or git stderr (it can include sensitive data).
        import sys
        print(f'audit: {type(error).__name__}: invalid input or failed file operation', file=sys.stderr)
        raise SystemExit(2) from None
