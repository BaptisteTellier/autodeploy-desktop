#!/usr/bin/env python3
"""
check-i18n.py — Verify EN/FR translation key parity in internal/server/i18n.go

Exits 0 if both language maps have identical key sets.
Exits 1 (with details) if any key is missing from either language.

This mirrors the i18n rigour from autodeploy-web.  Run in CI and locally:
    python3 scripts/check-i18n.py
"""

import re
import sys
from pathlib import Path


def extract_keys(text: str, lang: str) -> set[str]:
    """Extract all translation keys defined for *lang* in the translations map.

    Matches lines like:
        "some.key": "some value",
    inside the block for a given language.  The parser is line-oriented and
    state-machine-based to be robust against minor formatting changes.
    """
    keys: set[str] = set()

    # Find the opening brace of the language block: `"<lang>": {`
    lang_pattern = re.compile(
        r'^\s+"' + re.escape(lang) + r'":\s*\{',
    )
    key_pattern = re.compile(r'^\s+"([^"]+)":\s*"')

    inside = False
    depth = 0

    for line in text.splitlines():
        if not inside:
            if lang_pattern.match(line):
                inside = True
                depth = 1
            continue

        # Track brace depth to know when the language block ends.
        depth += line.count('{') - line.count('}')
        if depth <= 0:
            break

        m = key_pattern.match(line)
        if m:
            keys.add(m.group(1))

    return keys


def main() -> int:
    repo_root = Path(__file__).parent.parent
    i18n_file = repo_root / 'internal' / 'server' / 'i18n.go'

    if not i18n_file.exists():
        print(f'ERROR: {i18n_file} not found', file=sys.stderr)
        return 1

    text = i18n_file.read_text(encoding='utf-8')

    en_keys = extract_keys(text, 'en')
    fr_keys = extract_keys(text, 'fr')

    if not en_keys:
        print('ERROR: Could not extract EN keys from i18n.go', file=sys.stderr)
        return 1
    if not fr_keys:
        print('ERROR: Could not extract FR keys from i18n.go', file=sys.stderr)
        return 1

    missing_fr = en_keys - fr_keys
    missing_en = fr_keys - en_keys

    ok = True

    if missing_fr:
        print(f'FAIL: {len(missing_fr)} key(s) present in EN but missing from FR:')
        for k in sorted(missing_fr):
            print(f'  {k}')
        ok = False

    if missing_en:
        print(f'FAIL: {len(missing_en)} key(s) present in FR but missing from EN:')
        for k in sorted(missing_en):
            print(f'  {k}')
        ok = False

    if ok:
        print(f'OK: EN and FR are in parity ({len(en_keys)} keys each)')
        return 0

    print(
        '\nHint: add missing keys to both "en" and "fr" blocks in '
        'internal/server/i18n.go',
        file=sys.stderr,
    )
    return 1


if __name__ == '__main__':
    sys.exit(main())
