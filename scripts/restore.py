#!/usr/bin/env python3
"""Restore an encrypted snapshot into a NEW directory and optionally an EMPTY DB.
Does not touch any running app, gateway or existing storage directory.
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile
import urllib.parse

parser=argparse.ArgumentParser()
parser.add_argument('snapshot',help='Explicit restic snapshot ID (not latest)')
parser.add_argument('--target',required=True,type=pathlib.Path,help='New, nonexistent destination directory')
parser.add_argument('--database-url-file',type=pathlib.Path,help='Connection URL of an EMPTY restore database')
args=parser.parse_args()
if not re.fullmatch('[0-9a-f]{8,64}',args.snapshot):parser.error('Use an explicit snapshot ID')
target=args.target.resolve()
if target.exists():parser.error('Target already exists; choose a new directory')
if not os.environ.get('RESTIC_REPOSITORY'):parser.error('Set RESTIC_REPOSITORY and RESTIC_PASSWORD_FILE')
target.parent.mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(prefix='drop-restore-',dir=target.parent) as temporary:
    subprocess.run(['restic','restore',args.snapshot,'--target',temporary,'--verify'],check=True,stdout=subprocess.DEVNULL)
    candidates=list(pathlib.Path(temporary).rglob('backup-manifest.json'))
    if len(candidates)!=1:raise SystemExit('Snapshot must contain exactly one Drop backup manifest')
    source=candidates[0].parent
    manifest=json.loads(candidates[0].read_text())
    if manifest.get('version')!=1:raise SystemExit('Unsupported backup format')
    for did,hash in manifest['artifacts'].items():
        if not re.fullmatch('[0-9a-f]{32}',did):raise SystemExit('Invalid deployment ID')
        release=source/'releases'/did
        report=json.loads((release/'manifest.json').read_text())
        if report['artifactHash']!=hash:raise SystemExit('Manifest differs from backup index')
        for file in report['files']:
            relative=pathlib.PurePosixPath(file['path'])
            if relative.is_absolute() or '..' in relative.parts:raise SystemExit('Unsafe path in backup manifest')
            path=release/'public'/relative
            if path.is_symlink() or not path.is_file():raise SystemExit('Artifact missing or not regular')
            with path.open('rb') as f:digest=hashlib.file_digest(f,'sha256').hexdigest()
            if digest!=file['sha256'] or path.stat().st_size!=file['size']:raise SystemExit('Artifact checksum verification failed')
    if args.database_url_file:
        u=urllib.parse.urlparse(args.database_url_file.read_text().strip())
        env=os.environ.copy()
        env.update(PGHOST=u.hostname or '',PGPORT=str(u.port or 5432),PGUSER=urllib.parse.unquote(u.username or ''),PGPASSWORD=urllib.parse.unquote(u.password or ''),PGDATABASE=u.path.lstrip('/'),PGSSLMODE=urllib.parse.parse_qs(u.query).get('sslmode',['prefer'])[0])
        result=subprocess.run(['psql','-X','-At','-c',"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"],env=env,check=True,capture_output=True,text=True)
        if result.stdout.strip()!='0':raise SystemExit('Restore database is not empty')
        subprocess.run(['pg_restore','--exit-on-error','--single-transaction','--no-owner','--no-acl','--dbname='+env['PGDATABASE'],str(source/'database.dump')],env=env,check=True)
        # In-flight process inputs are intentionally excluded from backups. Expire credentials,
        # cancel incomplete jobs, and keep published immutable versions exactly as snapshotted.
        sql="""BEGIN; DELETE FROM admin_sessions; DELETE FROM preview_sessions; UPDATE preview_grants SET revoked_at=now(); DELETE FROM artifact_pins; DELETE FROM quota_reservations; UPDATE deployments SET status='FAILED',error_code='RESTORED_WITHOUT_UPLOAD',error_message='临时上传未包含在备份中，请重新上传' WHERE status NOT IN('READY','FAILED','CANCELED'); UPDATE jobs SET status='CANCELED' WHERE status IN('PENDING','RUNNING'); UPDATE publications SET status='CANCELED',error_code='RESTORED',error_message='未生效的发布意图在恢复时取消' WHERE status IN('PENDING','APPLYING','RECOVERING'); UPDATE sites SET desired_status=status,desired_deployment_id=applied_deployment_id; UPDATE backup_runs SET status='RESTORED' WHERE status='RUNNING'; COMMIT;"""
        subprocess.run(['psql','-X','--set','ON_ERROR_STOP=1','-c',sql],env=env,check=True,stdout=subprocess.DEVNULL)
    shutil.move(str(source),str(target))
    os.chmod(target,0o700)
print(json.dumps({'restoredTo':str(target),'verifiedArtifacts':len(manifest['artifacts']),'databaseRestored':bool(args.database_url_file),'gatewayApplied':False}))
