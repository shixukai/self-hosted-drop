#!/usr/bin/env python3
"""Prepare an isolated local PostgreSQL/Caddy pair for native Go development."""
import json
import pathlib
import secrets
import subprocess
import time
root=pathlib.Path(__file__).resolve().parent.parent
local=root/'.local';local.mkdir(mode=0o700,exist_ok=True)
for name in ['db.key','setup.key','internal.key']:
    p=local/name
    if not p.exists():p.write_text(secrets.token_hex(32));p.chmod(0o600)
for name in ['uploads','quarantine','releases','runtime']:(root/'.data'/name).mkdir(parents=True,exist_ok=True)
(local/'postgres.env').write_text('POSTGRES_DB=drop\nPOSTGRES_USER=drop\nPOSTGRES_PASSWORD='+(local/'db.key').read_text().strip()+'\n');(local/'postgres.env').chmod(0o600)
def container(name,command):
    exists=subprocess.run(['docker','inspect',name],capture_output=True,text=True)
    if exists.returncode==0:
        meta=json.loads(exists.stdout)[0]
        if meta['Config']['Labels'].get('project')!='self-hosted-drop':raise SystemExit('Container name is in use by another project')
        subprocess.run(['docker','start',name],check=True,stdout=subprocess.DEVNULL)
    else:subprocess.run(['docker','run','-d','--name',name,'--label','project=self-hosted-drop',*command],check=True,stdout=subprocess.DEVNULL)
container('self-hosted-drop-development-db',['--env-file',str(local/'postgres.env'),'-p','127.0.0.1:54336:5432','-v','self-hosted-drop-development-db:/var/lib/postgresql/data','postgres:16-alpine'])
for _ in range(60):
    if subprocess.run(['docker','exec','self-hosted-drop-development-db','pg_isready','-U','drop'],capture_output=True).returncode==0:break
    time.sleep(1)
else:raise SystemExit('PostgreSQL did not become ready')
container('self-hosted-drop-development-caddy',['-p','127.0.0.1:18080:8080','-p','127.0.0.1:18081:8081','-p','127.0.0.1:20193:2019','-v',str(root/'.data/releases')+':/data/releases:ro','-v',str(root/'deploy/caddy-bootstrap.json')+':/etc/caddy/bootstrap.json:ro','-v',str(root/'deploy/caddy-start.sh')+':/etc/caddy/start.sh:ro','-v','self-hosted-drop-development-caddy:/config','--entrypoint','sh','caddy:2.11.7-alpine','/etc/caddy/start.sh'])
print('Local dependencies ready. Build with make build, then run python3 scripts/dev.py.')
