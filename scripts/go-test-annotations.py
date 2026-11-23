#!/usr/bin/env python3
"""Emit only validated Go failure identifiers, never diagnostic output."""
import json
import re
import sys
from pathlib import Path

# A conservative subset: reject workflow-command escapes, controls and free text.
MODULE = 'github.com/dawksh/supertree'
PACKAGE = re.compile(r'github[.]com/dawksh/supertree(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*\Z', re.ASCII)
ROOT = Path(__file__).resolve().parent.parent


def valid_package(package):
    if not isinstance(package, str) or len(package) > 300 or not PACKAGE.fullmatch(package):
        return False
    directory = (ROOT / package[len(MODULE):].lstrip('/')).resolve()
    return directory.is_relative_to(ROOT) and directory.is_dir() and any(directory.glob('*.go'))


def main():
    failed = sys.argv[1] != '0'
    emitted = set()
    for line in sys.stdin:
        try:
            entry = json.loads(line)
        except (ValueError, RecursionError):
            continue
        if not isinstance(entry, dict) or entry.get('Action') != 'fail':
            continue
        package = entry.get('Package')
        if 'Test' in entry or not valid_package(package):
            continue
        message = f'Go package failed: {package}'
        if message not in emitted:
            print(f'::error::{message}')
            emitted.add(message)
    if failed and not emitted:
        print('::error::Go test failed without safe package diagnostics')


if __name__ == '__main__':
    main()
