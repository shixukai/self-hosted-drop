#!/usr/bin/env python3
"""Exercise a running *local* Drop stack using real PostgreSQL, worker and Caddy.
Creates and removes disposable sites. Never use this against production.
"""
import argparse
import concurrent.futures
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

def publish(s,d,action='PUBLISH'):
    current=request('GET','/sites/'+s['id'])
    body={'deploymentId':d['id'],'expectedGeneration':current['generation'],'inspectionReportHash':d['report']['inspectionReportHash'],'acknowledgeWarnings':True,'action':action}
    key=secrets.token_hex(16)
    p=request('POST','/sites/'+s['id']+'/publications',body,{'Idempotency-Key':key},expected=202)
    same=request('POST','/sites/'+s['id']+'/publications',body,{'Idempotency-Key':key},expected=202)
    assert p['id']==same['id'], 'Publication idempotency failed'
    wait_publish(p)
    request('POST','/sites/'+s['id']+'/publications',body,expected=409)

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
    operation(s,'pause')
    for method,path in [('GET','/'),('HEAD','/report.html'),('GET','/report.html')]:
        assert public(host,path,method,headers={'Range':'bytes=0-9','If-None-Match':etag})[0]==503
    assert public(preview,headers={'Cookie':pcookie})[0]==403
    operation(s,'resume')
    assert public(preview,headers={'Cookie':pcookie})[0]==403
    passed('one-time preview exchange and pause/revocation across all resources')
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
    import datetime
    token=request('POST','/tokens',{'name':'integration-limited','siteIds':[s['id']],'scopes':['sites:read','deployments:write'],'expiresAt':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=1)).isoformat()},expected=201)
    raw=token['token'];assert len(request('GET','/sites',token=raw)['items'])==1
    request('GET','/sites/'+other['id'],token=raw,expected=404)
    request('GET','/system/status',token=raw,expected=403)
    request('POST','/sites/'+s['id']+'/publications',{},token=raw,expected=403)
    request('DELETE','/tokens/'+token['id'],expected=204)
    request('GET','/sites',token=raw,expected=401)
    passed('Token site/scope limits, expiration validation and immediate revocation')
    invalid=wait_deploy(upload(other,zipped({'../escape.html':'escape'}),'unsafe.zip'),wanted='FAILED')
    assert invalid['errorCode']=='UNSAFE_PATH'
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
    print(json.dumps({'passed':len(checks),'checks':checks},ensure_ascii=False),flush=True)
finally:
    for sid in created:
        try:
            s=request('GET','/sites/'+sid)
            if s['status']!='DELETED':operation(s,'delete')
        except Exception as e:
            print('Cleanup deferred for fixture',sid,type(e).__name__,flush=True)
