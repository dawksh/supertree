#!/usr/bin/env python3
"""Prefix every raw log line, including CR-separated lines, to disable commands."""
import os
import sys

at_start = True
while True:
    chunk = os.read(sys.stdin.fileno(), 65536)
    if not chunk:
        break
    output = bytearray()
    for byte in chunk:
        if at_start:
            output.extend(b'[go] ')
        # Legacy runner commands can be recognized inside a line, not just at
        # its start. Break their introducer even across input chunk boundaries.
        if byte == 35:
            output.extend(b'\\x23')
        else:
            output.append(byte)
        at_start = byte in (10, 13)
    sys.stdout.buffer.write(output)
    sys.stdout.buffer.flush()
if not at_start:
    sys.stdout.buffer.write(b'\n')
    sys.stdout.buffer.flush()
