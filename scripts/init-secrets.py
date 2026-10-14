#!/usr/bin/env python3
"""Create missing secrets without displaying or replacing existing values."""
import os
import pathlib
import secrets
import sys
root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else '.secrets')
root.mkdir(mode=0o700, parents=True, exist_ok=True)
for name in ['postgres.key', 'setup.key', 'internal.key', 'worker.key', 'restic.key']:
    path = root / name
    if path.exists():
        continue
    with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as file:
        file.write(secrets.token_hex(32))
print('Secret files are ready in', root)
