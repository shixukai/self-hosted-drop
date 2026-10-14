#!/usr/bin/env python3
"""Exercise a running *local* Drop stack using real PostgreSQL, worker and Caddy.
Creates and removes disposable sites. Never use this against production.
"""
import argparse
import concurrent.futures
import datetime
import hashlib
import http.client
import io
import json
import pathlib
import secrets
import time
import urllib.parse
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument('--base', default='http://localhost:18180')
parser.add_argument('--content', default='http://localhost:18080')
parser.add_argument('--password-file', required=True)
parser.add_argument('--setup-key-file')
args = parser.parse_args()
base, content = urllib.parse.urlparse(args.base), urllib.parse.urlparse(args.content)
assert base.hostname in ('localhost', '127.0.0.1') and content.hostname in ('localhost', '127.0.0.1'), 'Local test stacks only'
password = pathlib.Path(args.password_file).read_text().strip()
cookie, csrf = '', ''
created = []
created_tokens = []
checks = []

def request(method, path, body=None, headers=None, token=None, expected=200):
    global cookie, csrf
    connection = http.client.HTTPConnection(base.hostname, base.port, timeout=30)
    h = {'Origin': args.base, 'Cookie': cookie, 'X-CSRF-Token': csrf}
    if token is not None:
        h = {'Authorization': 'Bearer '+token}
    if isinstance(body, dict):
        body = json.dumps(body)
        h['Content-Type'] = 'application/json'
    if headers:
        h.update(headers)
    connection.request(method, '/api/v1'+path, body, h)
    response = connection.getresponse()
    data = response.read()
    assert response.status == expected, f'{method} {path}: expected {expected}, got {response.status}, error={data[:300] if response.status>=400 else "unexpected success"}'
    if response.getheader('Set-Cookie'):
        cookie = response.getheader('Set-Cookie').split(';')[0]
    value = json.loads(data) if data and response.getheader('Content-Type','').startswith('application/json') else data
    if isinstance(value, dict) and 'csrfToken' in value:
        csrf = value['csrfToken']
    connection.close()
    return value

def public(host, path='/', method='GET', headers=None, body=None):
    connection = http.client.HTTPConnection(content.hostname, content.port, timeout=15)
    h = {'Host': host}
    h.update(headers or {})
    connection.request(method, path, body, h)
    response = connection.getresponse()
    out = response.status, dict(response.getheaders()), response.read()
    connection.close()
    return out

def site():
    s=request('POST','/sites',{'name':'自动化验收站点','slug':'check-'+secrets.token_hex(5),'description':'Disposable integration fixture'},expected=201)
    created.append(s['id'])
    return s

def upload(s, data, filename='index.html', metadata=None, key=None, token=None, expected=202):
    boundary='drop'+secrets.token_hex(16)
    meta={'interactionMode':'interactive','routingMode':'static','message':'Integration fixture'}
    meta.update(metadata or {})
    body=('--'+boundary+'\r\nContent-Disposition: form-data; name="metadata"\r\n\r\n'+json.dumps(meta)+'\r\n--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+filename+'"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()+data+('\r\n--'+boundary+'--\r\n').encode()
    return request('POST','/sites/'+s['id']+'/deployments',body,{'Content-Type':'multipart/form-data; boundary='+boundary,'Idempotency-Key':key or secrets.token_hex(16)},token=token,expected=expected)

def wait_deploy(d, wanted='READY'):
    for _ in range(180):
        d=request('GET','/deployments/'+d['id'])
        if d['status'] in ('READY','FAILED','CANCELED','AWAITING_INPUT'):
            assert d['status']==wanted, f'deployment: {d["status"]}, {d.get("errorCode")}'
            return d
        time.sleep(.15)
    raise AssertionError('Deployment timed out')

def wait_publish(p):
    for _ in range(180):
        p=request('GET','/publications/'+p['id'])
        if p['status'] in ('APPLIED','FAILED','CONFLICT','CANCELED'):
            assert p['status']=='APPLIED', f'publication: {p["status"]} {p.get("errorCode")}'
            return p
        time.sleep(.15)
    raise AssertionError('Publication timed out')

def publication_body(s,d,action='PUBLISH'):
    current=request('GET','/sites/'+s['id'])
    return {'deploymentId':d['id'],'expectedGeneration':current['generation'],'inspectionReportHash':d['report']['inspectionReportHash'],'acknowledgeWarnings':True,'action':action}

def publish(s,d,action='PUBLISH',token=None):
    body=publication_body(s,d,action)
    key=secrets.token_hex(16)
    p=request('POST','/sites/'+s['id']+'/publications',body,{'Idempotency-Key':key},token=token,expected=202)
    same=request('POST','/sites/'+s['id']+'/publications',body,{'Idempotency-Key':key},token=token,expected=202)
    assert p['id']==same['id'], 'Publication idempotency failed'
    wait_publish(p)
    request('POST','/sites/'+s['id']+'/publications',body,token=token,expected=409)

def wait_auto_result(d, error_code=''):
    for _ in range(180):
        d=request('GET','/deployments/'+d['id'])
        if d['autoPublicationId'] or d['autoPublishErrorCode']:
            assert d['status']=='READY', 'Automatic publication must not alter artifact processing status'
            assert d['autoPublishErrorCode']==error_code, f'Unexpected auto-publication error: {d["autoPublishErrorCode"]}'
            if error_code:
                assert d['autoPublicationId'] is None and d['autoPublishErrorMessage']
            else:
                assert d['autoPublicationId'] and not d['autoPublishErrorMessage']
            events=request('GET','/system/audit?limit=100')['items']
            assert any(event['action']=='AUTO_PUBLISH' and event['resourceId']==d['id'] and event['result']==(error_code or 'ACCEPTED') for event in events), 'Automatic-publication result is missing its audit event'
            return d
        time.sleep(.15)
    raise AssertionError('Auto-publication result timed out')

def operation(s,op):
    current=request('GET','/sites/'+s['id'])
    path='/sites/'+s['id']+('' if op=='delete' else '/'+op)
    p=request('DELETE' if op=='delete' else 'POST',path,{'expectedGeneration':current['generation'],'confirmSlug':s['slug']},expected=202)
    wait_publish(p)

def zipped(files):
    output=io.BytesIO()
    with zipfile.ZipFile(output,'w',zipfile.ZIP_DEFLATED) as z:
        for name, data in files.items():
            z.writestr(name,data)
    return output.getvalue()

def passed(label):
    checks.append(label)
    print('PASS',label,flush=True)

try:
    if not request('GET','/setup/status')['initialized']:
        assert args.setup_key_file, 'Setup key required for a fresh stack'
        request('POST','/setup',{'key':pathlib.Path(args.setup_key_file).read_text().strip(),'password':password},expected=201)
    else:
        request('POST','/auth/login',{'password':password})
    request('POST','/sites',{'name':'blocked','slug':'blocked'},headers={'Origin':'http://evil.localhost'},expected=403)
    request('POST','/sites',{'name':'blocked','slug':'blocked'},headers={'X-CSRF-Token':'wrong'},expected=403)
    request('GET','/system/status',headers={'Host':'unregistered.localhost'},expected=404)
    passed('session, Origin, CSRF and Host isolation')
    s=site(); host=urllib.parse.urlparse(s['url']).netloc
    html=b'<!doctype html><meta charset="utf-8"><h1>version one</h1>'
    key=secrets.token_hex(16)
    d=wait_deploy(upload(s,html,'report.html',key=key))
    duplicate=upload(s,html,'report.html',key=key)
    assert duplicate['id']==d['id']
    assert [item['id'] for item in request('GET','/sites/'+s['id']+'/deployments?limit=100')['items']]==[d['id']], 'Successful upload replay added scratch history'
    upload(s,html.replace(b'one',b'two'),'report.html',key=key,expected=409)
    assert public(host)[0]==404, 'READY must not expose content'
    publish(s,d)
    status,headers,body=public(host)
    assert status==200 and body==html
    etag=headers['Etag'] if 'Etag' in headers else headers['ETag']
    assert etag=='"'+hashlib.sha256(html).hexdigest()+'"'
    assert 'Last-Modified' not in headers and headers['Cache-Control']=='no-cache'
    assert public(host,method='HEAD')[0]==200
    assert public(host,headers={'Range':'bytes=0-9'})[0]==206
    assert public(host,headers={'If-None-Match':etag})[0]==304
    assert public(host,'/report.html.drop-etag')[0]==404
    assert public('unknown.drop.localhost')[0]==404
    passed('upload, content hash, idempotency, publish, HEAD, Range and ETag')
    grant=request('POST','/deployments/'+d['id']+'/preview-grants',{},expected=201)
    u=urllib.parse.urlparse(grant['url']);preview=u.netloc
    assert public(preview)[0]==401
    status,h,b=public(preview,'/_drop/exchange',method='POST',headers={'Content-Type':'application/json','Origin':u.scheme+'://'+u.netloc},body=json.dumps({'token':u.fragment}))
    assert status==200, 'Preview exchange failed'
    pcookie=h['Set-Cookie'].split(';')[0]
    assert public(preview,headers={'Cookie':pcookie})[2]==html
    assert public(preview,'/_drop/exchange',method='POST',headers={'Content-Type':'application/json','Origin':u.scheme+'://'+u.netloc},body=json.dumps({'token':u.fragment}))[0]==401
    pre_pause=request('GET','/sites/'+s['id'])
    operation(s,'pause')
    for method,path in [('GET','/'),('HEAD','/report.html'),('GET','/report.html')]:
        assert public(host,path,method,headers={'Range':'bytes=0-9','If-None-Match':etag})[0]==503
    assert public(preview,headers={'Cookie':pcookie})[0]==403
    paused=request('GET','/sites/'+s['id'])
    publisher=request('POST','/tokens',{'name':'integration-publications-only','siteIds':[s['id']],'scopes':['publications:write'],'expiresAt':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=1)).isoformat()},expected=201)
    created_tokens.append(publisher['id'])
    uploader=request('POST','/tokens',{'name':'integration-auto-publication','siteIds':[s['id']],'scopes':['deployments:write','publications:write'],'expiresAt':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=1)).isoformat()},expected=201)
    created_tokens.append(uploader['id'])
    for action in ('PUBLISH','ROLLBACK'):
        body=publication_body(s,d,action)
        for identity in (None,publisher['token']):
            rejection=request('POST','/sites/'+s['id']+'/publications',body,token=identity,expected=409)
            assert rejection['code']=='SITE_PAUSED', 'Publication bypassed the explicit resume requirement'
    resume_body={'expectedGeneration':paused['generation']}
    request('POST','/sites/'+s['id']+'/resume',resume_body,token=publisher['token'],expected=403)
    request('POST','/sites/'+s['id']+'/publications',{**publication_body(s,d),'action':'RESUME'},token=publisher['token'],expected=422)
    stale_body={**publication_body(s,d),'expectedGeneration':pre_pause['generation']}
    stale=request('POST','/sites/'+s['id']+'/publications',stale_body,token=publisher['token'],expected=409)
    assert stale['code']=='DEPLOYMENT_CONFLICT', 'Generation conflicts must precede pause-state validation'
    # Uploads stay available while paused, but neither a stale pre-pause
    # generation nor a fresh generation can auto-publish and resume the site.
    for generation,error_code in ((pre_pause['generation'],'DEPLOYMENT_CONFLICT'),(paused['generation'],'SITE_PAUSED')):
        auto=wait_deploy(upload(s,html,'report.html',{'publishAfterReady':True,'expectedGeneration':generation},token=uploader['token']))
        auto=wait_auto_result(auto,error_code)
        assert request('GET','/sites/'+s['id'])['generation']==paused['generation']
        assert request('GET','/sites/'+s['id']+'/publications?deployment='+auto['id'])['items']==[]
        assert public(host)[0]==503
    operation(s,'resume')
    assert public(preview,headers={'Cookie':pcookie})[0]==403
    publish(s,auto,token=publisher['token'])
    assert public(host)[2]==html
    current=request('GET','/sites/'+s['id'])
    accepted=wait_deploy(upload(s,html,'report.html',{'publishAfterReady':True,'expectedGeneration':current['generation']},token=uploader['token']))
    accepted=wait_auto_result(accepted)
    linked=request('GET','/publications/'+accepted['autoPublicationId'])
    assert linked['deploymentId']==accepted['id']
    wait_publish(linked)
    assert public(host)[2]==html
    current=request('GET','/sites/'+s['id'])
    pending_auto=wait_deploy(upload(s,zipped({'first/index.html':'first','second/page.html':html}),'auto-selection.zip',{'publishAfterReady':True,'expectedGeneration':current['generation']},token=uploader['token']),'AWAITING_INPUT')
    request('DELETE','/tokens/'+uploader['id'],expected=204)
    request('POST','/deployments/'+pending_auto['id']+'/selection',{'rootDirectory':'second','entryPath':'page.html','routingMode':'static','inspectionRevision':pending_auto['report']['revision']},expected=202)
    revoked=wait_auto_result(wait_deploy(pending_auto),'TOKEN_REVOKED')
    assert request('GET','/sites/'+s['id']+'/publications?deployment='+revoked['id'])['items']==[]
    warning_auto=wait_deploy(upload(s,html+b'<img src="missing.png">','warning-auto.html',{'publishAfterReady':True,'expectedGeneration':current['generation']}))
    assert warning_auto['report']['warnings']
    wait_auto_result(warning_auto,'WARNINGS_NOT_ACKNOWLEDGED')
    assert request('GET','/sites/'+s['id'])['generation']==current['generation']
    assert public(host)[2]==html
    request('DELETE','/tokens/'+publisher['id'],expected=204)
    passed('one-time preview exchange and pause/revocation across all resources')
    passed('paused admin/token publication rejection, upload-only preparation and explicit resume before publish')
    passed('automatic-publication errors for pause, stale generation, revoked token and warnings; linked successful publication')
    d2=wait_deploy(upload(s,html.replace(b'one',b'two'),'report.html'))
    publish(s,d2)
    status,h,b=public(host,headers={'If-None-Match':etag})
    assert status==200 and b.endswith(b'version two</h1>')
    publish(s,d,'ROLLBACK')
    assert public(host)[2]==html
    exported=request('GET','/deployments/'+d['id']+'/export')
    with zipfile.ZipFile(io.BytesIO(exported)) as z:
        assert z.read('content/report.html')==html and 'drop-export.json' in z.namelist()
    passed('same-size version replacement, rollback and verified ZIP export')
    other=site()
    token=request('POST','/tokens',{'name':'integration-limited','siteIds':[s['id']],'scopes':['sites:read','deployments:write'],'expiresAt':'2099-01-01T00:00:00Z'},expected=422)
    token=request('POST','/tokens',{'name':'integration-limited','siteIds':[s['id']],'scopes':['sites:read','deployments:write'],'expiresAt':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=1)).isoformat()},expected=201)
    raw=token['token'];assert len(request('GET','/sites',token=raw)['items'])==1
    request('GET','/sites/'+other['id'],token=raw,expected=404)
    request('GET','/system/status',token=raw,expected=403)
    request('POST','/sites/'+s['id']+'/publications',{},token=raw,expected=403)
    request('DELETE','/tokens/'+token['id'],expected=204)
    request('GET','/sites',token=raw,expected=401)
    passed('Token site/scope limits, expiration validation and immediate revocation')
    failed_payload=zipped({'../escape.html':'escape'})
    failed_key=secrets.token_hex(16)
    invalid=wait_deploy(upload(other,failed_payload,'unsafe.zip',key=failed_key),wanted='FAILED')
    assert invalid['errorCode']=='UNSAFE_PATH'
    before_replay={item['id'] for item in request('GET','/sites/'+other['id']+'/deployments?limit=100')['items']}
    failed_replay=upload(other,failed_payload,'unsafe.zip',key=failed_key)
    assert failed_replay['id']==invalid['id'] and failed_replay['status']=='FAILED', 'Terminal upload replay changed its resource'
    assert {item['id'] for item in request('GET','/sites/'+other['id']+'/deployments?limit=100')['items']}==before_replay, 'Failed upload replay added scratch history'
    retry=wait_deploy(upload(other,failed_payload,'unsafe.zip',key=secrets.token_hex(16)),wanted='FAILED')
    after_retry={item['id'] for item in request('GET','/sites/'+other['id']+'/deployments?limit=100')['items']}
    assert retry['id']!=invalid['id'] and after_retry==before_replay|{retry['id']}, 'Fresh failed retry must create exactly one attempt'
    passed('terminal upload idempotency replay preserves history; explicit new key creates one attempt')
    ambiguous=zipped({'first/index.html':'first','second/page.html':'second'})
    strict=wait_deploy(upload(other,ambiguous,'multi.zip',{'interactionMode':'strict'}),'FAILED')
    assert strict['errorCode']=='ENTRY_SELECTION_REQUIRED'
    selected=wait_deploy(upload(other,ambiguous,'multi.zip'),'AWAITING_INPUT')
    request('POST','/deployments/'+selected['id']+'/selection',{'rootDirectory':'second','entryPath':'page.html','routingMode':'static','inspectionRevision':selected['report']['revision']},expected=202)
    selected=wait_deploy(selected)
    assert len(selected['files'])==1 and selected['report']['entryPath']=='page.html'
    spa=wait_deploy(upload(other,zipped({'index.html':'<h1>SPA fixture</h1>','app.js':'console.log("fixture")'}),'spa.zip',{'routingMode':'spa'}))
    publish(other,spa)
    otherhost=urllib.parse.urlparse(other['url']).netloc
    assert public(otherhost,'/deep/route',headers={'Accept':'text/html'})[0]==200
    assert public(otherhost,'/missing.js',headers={'Accept':'text/html'})[0]==404
    assert public(otherhost,'/deep/route',headers={'Accept':'application/json'})[0]==404
    passed('unsafe archive rejection, strict/interactive entry selection and SPA routing')
    current=request('GET','/sites/'+s['id'])
    body={'expectedGeneration':current['generation']}
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        futures=[pool.submit(request,'POST','/sites/'+s['id']+'/pause',body,expected=202) for _ in range(2)]
        success=[];conflicts=0
        for f in futures:
            try:success.append(f.result())
            except AssertionError as e:
                assert 'got 409' in str(e)
                conflicts+=1
    assert len(success)==1 and conflicts==1
    wait_publish(success[0])
    operation(s,'delete')
    assert public(host)[0]==404
    operation(s,'restore')
    assert public(host)[0]==503
    passed('concurrent generation conflict, trash deletion and restore-paused behavior')
    unpublished=site()
    unpublished_host=urllib.parse.urlparse(unpublished['url']).netloc
    operation(unpublished,'pause')
    current=request('GET','/sites/'+unpublished['id'])
    assert current['status']=='PAUSED' and current['appliedDeploymentId'] is None
    no_version=request('POST','/sites/'+unpublished['id']+'/resume',{'expectedGeneration':current['generation']},expected=409)
    assert no_version['code']=='SITE_NOT_PUBLISHED'
    operation(unpublished,'delete')
    operation(unpublished,'restore')
    replacement=wait_deploy(upload(unpublished,html,'report.html'))
    resume_only=request('POST','/tokens',{'name':'integration-resume-boundary','siteIds':[unpublished['id']],'scopes':['publications:write'],'expiresAt':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=1)).isoformat()},expected=201)
    created_tokens.append(resume_only['id'])
    selected_body=publication_body(unpublished,replacement,'RESUME')
    request('POST','/sites/'+unpublished['id']+'/resume',selected_body,token=resume_only['token'],expected=403)
    publication_refusal=request('POST','/sites/'+unpublished['id']+'/publications',{**selected_body,'action':'PUBLISH'},token=resume_only['token'],expected=409)
    assert publication_refusal['code']=='SITE_PAUSED'
    selected_resume=request('POST','/sites/'+unpublished['id']+'/resume',selected_body,expected=202)
    assert selected_resume['action']=='RESUME' and selected_resume['deploymentId']==replacement['id']
    wait_publish(selected_resume)
    assert public(unpublished_host)[2]==html
    request('DELETE','/tokens/'+resume_only['id'],expected=204)
    passed('paused and restored draft resumes only through explicit management of a selected READY version')
    print(json.dumps({'passed':len(checks),'checks':checks},ensure_ascii=False),flush=True)
finally:
    for tid in created_tokens:
        try:
            request('DELETE','/tokens/'+tid,expected=204)
        except Exception as e:
            print('Cleanup deferred for token',tid,type(e).__name__,flush=True)
    for sid in created:
        try:
            s=request('GET','/sites/'+sid)
            if s['status']!='DELETED':operation(s,'delete')
        except Exception as e:
            print('Cleanup deferred for fixture',sid,type(e).__name__,flush=True)
