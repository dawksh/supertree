#!/usr/bin/env python3
"""Emit only validated Go failure identifiers, never diagnostic output."""
import json
import re
import sys

# A conservative subset: reject workflow-command escapes, controls and free text.
PACKAGE = re.compile(r'github[.]com/dawksh/supertree(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*\Z', re.ASCII)
TEST = re.compile(r'(?:Test|Example|Benchmark|Fuzz)[A-Za-z0-9_]+(?:/[A-Za-z0-9_-]+)*\Z', re.ASCII)


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
        if not isinstance(package, str) or len(package) > 300 or not PACKAGE.fullmatch(package):
            continue
        test = entry.get('Test')
        if test is None:
            message = f'Go package failed: {package}'
        elif isinstance(test, str) and len(test) <= 300 and TEST.fullmatch(test):
            message = f'Go test failed: {package} {test}'
        else:
            continue
        if message not in emitted:
            print(f'::error::{message}')
            emitted.add(message)
    if failed and not emitted:
        print('::error::Go test failed without safe package or test diagnostics')


if __name__ == '__main__':
    main()
