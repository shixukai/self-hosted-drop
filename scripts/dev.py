#!/usr/bin/env python3
"""Run local app/worker with credentials kept in ignored .local files."""
import os
import pathlib
import signal
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent.parent
local = root / '.local'
env = os.environ.copy()
env.update({
    'DROP_DATABASE_URL': 'postgres://drop:' + (local / 'db.key').read_text().strip() + '@127.0.0.1:54336/drop?sslmode=disable',
    'DROP_DATA_DIR': str(root / '.data'),
    'DROP_WEB_DIR': str(root / 'web/dist'),
    'DROP_ADMIN_ORIGIN': 'http://localhost:18180',
    'DROP_CONTENT_DOMAIN': 'drop.localhost:18080',
    'DROP_CADDY_ADMIN': 'http://127.0.0.1:20193',
    'DROP_CADDY_CONTENT': 'http://127.0.0.1:18080',
    'DROP_CADDY_ROOT': '/data/releases',
    'DROP_INTERNAL_KEY_FILE': str(local / 'internal.key'),
    'DROP_SETUP_KEY_FILE': str(local / 'setup.key'),
    'DROP_DEVELOPMENT_MODE': 'true',
})
if len(sys.argv)>1:
    raise SystemExit(subprocess.call([str(root/'bin/drop'),*sys.argv[1:]],env=env,cwd=root))
(local / 'dev.pid').write_text(str(os.getpid()))
children = [subprocess.Popen([str(root/'bin/drop-worker')],env=env,cwd=root), subprocess.Popen([str(root/'bin/drop')],env=env,cwd=root)]
def stop(*_):
    for child in children:
        child.terminate()
    for child in children:
        child.wait(timeout=12)
    raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
signal.signal(signal.SIGINT,stop)
try:
    children[-1].wait()
finally:
    stop()
