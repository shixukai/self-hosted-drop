#!/usr/bin/env python3
"""Upload a static artifact with a scoped DROP_TOKEN and optionally publish it."""
import argparse
import hashlib
import http.client
import json
import os
import pathlib
import secrets
import sys
import time
import urllib.parse

parser=argparse.ArgumentParser()
parser.add_argument('file',type=pathlib.Path)
parser.add_argument('--base',default=os.getenv('DROP_URL','https://drop-admin.douyacn.com'))
parser.add_argument('--site',required=True,help='Site slug (must already exist)')
parser.add_argument('--root',default='');parser.add_argument('--entry',default='')
parser.add_argument('--mode',choices=['static','spa'],default='static')
parser.add_argument('--message',default='');parser.add_argument('--publish',action='store_true')
parser.add_argument('--allow-warnings',action='store_true');parser.add_argument('--idempotency-key')
parser.add_argument('--retry-failed',action='store_true',help='Retry one confirmed FAILED/CANCELED deployment with a fresh generated key')
args=parser.parse_args()
if args.retry_failed and args.idempotency_key is not None:parser.error('--retry-failed cannot be combined with --idempotency-key; choose a fresh explicit key yourself')
token=os.environ.get('DROP_TOKEN','');base=urllib.parse.urlparse(args.base.rstrip('/'))
if not token:parser.error('Set DROP_TOKEN in the environment')
if base.scheme!='https' and not(base.scheme=='http' and base.hostname in ('localhost','127.0.0.1')):parser.error('HTTPS is required outside localhost')
if not args.file.is_file() or args.file.suffix.lower() not in ('.html','.htm','.zip'):parser.error('Provide an existing HTML or ZIP file')
def connection():return (http.client.HTTPSConnection if base.scheme=='https' else http.client.HTTPConnection)(base.hostname,base.port,timeout=300)
def decode(c,r):
 b=r.read();c.close()
 try:value=json.loads(b)
 except ValueError:raise RuntimeError('Server returned a non-JSON response')
 if r.status>=400:raise RuntimeError(value.get('code','REQUEST_FAILED')+': '+value.get('message',''))
 return value
def api(method,path,data=None,key=None):
 c=connection();h={'Authorization':'Bearer '+token}
 if data is not None:h['Content-Type']='application/json';data=json.dumps(data)
 if key:h['Idempotency-Key']=key
 c.request(method,'/api/v1'+path,data,h);return decode(c,c.getresponse())
def send_upload(site,meta,key):
 boundary='drop'+secrets.token_hex(16)
 filename=args.file.name
 if any(c in filename for c in '\r\n"'):raise RuntimeError('INVALID_FILENAME')
 prefix=('--'+boundary+'\r\nContent-Disposition: form-data; name="metadata"\r\n\r\n'+json.dumps(meta)+'\r\n--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+filename+'"\r\nContent-Type: application/octet-stream\r\n\r\n').encode();suffix=('\r\n--'+boundary+'--\r\n').encode()
 c=connection();c.putrequest('POST','/api/v1/sites/'+site['id']+'/deployments');c.putheader('Authorization','Bearer '+token);c.putheader('Content-Type','multipart/form-data; boundary='+boundary);c.putheader('Content-Length',str(len(prefix)+args.file.stat().st_size+len(suffix)));c.putheader('Idempotency-Key',key);c.endheaders();c.send(prefix)
 with args.file.open('rb') as file:
  while chunk:=file.read(1<<20):c.send(chunk)
 c.send(suffix);return decode(c,c.getresponse())
d,p,key=None,None,None
try:
 sites=api('GET','/sites?q='+urllib.parse.quote(args.site)+'&limit=100')['items'];site=next((s for s in sites if s['slug']==args.site),None)
 if site is None:raise RuntimeError('SITE_NOT_FOUND: create the site in the admin UI first')
 meta={'rootDirectory':args.root,'entryPath':args.entry,'routingMode':args.mode,'interactionMode':'strict','message':args.message,'publishAfterReady':False}
 with args.file.open('rb') as file:hash=hashlib.file_digest(file,'sha256').hexdigest()
 key=args.idempotency_key or hashlib.sha256((site['id']+hash+args.file.name+json.dumps(meta,sort_keys=True)).encode()).hexdigest()
 for attempt in range(2 if args.retry_failed else 1):
  d=send_upload(site,meta,key)
  for _ in range(300):
   d=api('GET','/deployments/'+d['id'])
   if d['status'] in ('READY','FAILED','CANCELED','AWAITING_INPUT'):break
   time.sleep(1)
  if d['status']=='READY':break
  if args.retry_failed and attempt==0 and d['status'] in ('FAILED','CANCELED'):
   key=key+'-retry-'+secrets.token_hex(8)
   d=None
   continue
  raise RuntimeError(d.get('errorCode') or ('CANCELED' if d['status']=='CANCELED' else 'PROCESSING_NOT_READY'))
 if args.publish:
  if d['report']['warnings'] and not args.allow_warnings:
   print(json.dumps({'deploymentId':d['id'],'status':'READY','warnings':d['report']['warnings'],'requiresAcknowledgement':True},ensure_ascii=False));sys.exit(4)
  p=api('POST','/sites/'+site['id']+'/publications',{'action':'PUBLISH','deploymentId':d['id'],'expectedGeneration':site['generation'],'inspectionReportHash':d['report']['inspectionReportHash'],'acknowledgeWarnings':args.allow_warnings},key='publish-'+key[:90]+'-'+str(site['generation']))
  for _ in range(300):
   p=api('GET','/publications/'+p['id'])
   if p['status'] in ('APPLIED','FAILED','CONFLICT','CANCELED'):break
   time.sleep(1)
  if p['status']!='APPLIED':raise RuntimeError(p.get('errorCode') or 'PUBLICATION_PENDING')
  print(json.dumps({'deploymentId':d['id'],'publicationId':p['id'],'status':'APPLIED','url':site['url']},ensure_ascii=False))
 else:print(json.dumps({'deploymentId':d['id'],'status':'READY','inspectionReportHash':d['report']['inspectionReportHash'],'warnings':d['report']['warnings']},ensure_ascii=False))
except (OSError,RuntimeError,http.client.HTTPException) as e:
 message=str(e)
 result={'error':message,'deploymentId':d.get('id') if d else None,'publicationId':p.get('id') if p else None,'status':p.get('status') if p else d.get('status') if d else 'UNKNOWN'}
 if key:result['idempotencyKey']=key
 print(json.dumps(result,ensure_ascii=False),file=sys.stderr);sys.exit(5 if 'CONFLICT' in message or 'MISMATCH' in message else 3)
