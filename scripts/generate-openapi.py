#!/usr/bin/env python3
"""Generate the versioned public API contract from explicit endpoint definitions."""
import json
import pathlib

def obj(properties, required=()):
    return {'type':'object','properties':properties,**({'required':list(required)} if required else {})}
def string(**more):return {'type':'string',**more}
def array(schema):return {'type':'array','items':schema}
def ref(name):return {'$ref':'#/components/schemas/'+name}
id_schema=string(pattern='^[a-f0-9]{32}$')
integer={'type':'integer','format':'int64','minimum':0}
boolean={'type':'boolean'}
time=string(format='date-time')
nullable=lambda s:{'anyOf':[s,{'type':'null'}]}
schemas={
'Error':obj({'code':string(),'message':string(),'requestId':string()},['code','message']),
'Session':obj({'administrator':boolean,'csrfToken':string(),'expiresAt':time},['administrator','csrfToken']),
'Site':obj({'id':id_schema,'slug':string(),'name':string(),'description':string(),'status':string(enum=['DRAFT','ACTIVE','PAUSED','DELETED']),'desiredStatus':string(),'generation':integer,'appliedGeneration':integer,'appliedDeploymentId':nullable(id_schema),'desiredDeploymentId':nullable(id_schema),'url':string(format='uri'),'deploymentCount':integer,'bytes':integer,'createdAt':time,'updatedAt':time}),
'File':obj({'path':string(),'size':integer,'sha256':string()},['path','size','sha256']),
'Warning':obj({'code':string(),'file':string(),'reference':string(),'message':string()}),
'Candidate':obj({'rootDirectory':string(),'entryPath':string()}),
'Metadata':obj({'rootDirectory':string(default=''),'entryPath':string(default=''),'routingMode':string(enum=['static','spa'],default='static'),'message':string(maxLength=1000),'interactionMode':string(enum=['strict','interactive'],default='strict'),'publishAfterReady':{'type':'boolean','default':False},'warningsPolicy':string(enum=['require_ack','allow'],default='require_ack'),'expectedGeneration':integer}),
'Report':obj({'status':string(),'files':array(ref('File')),'warnings':array(ref('Warning')),'candidates':array(ref('Candidate')),'rootDirectory':string(),'entryPath':string(),'routingMode':string(),'inspectionReportHash':string(),'artifactHash':string(),'revision':integer,'expandedBytes':integer}),
'Deployment':obj({'id':id_schema,'siteId':id_schema,'filename':string(),'status':string(enum=['RECEIVING','QUEUED','VALIDATING','EXTRACTING','CHECKING','AWAITING_INPUT','READY','FAILED','CANCELED']),'artifactState':string(enum=['PENDING','AVAILABLE','GC_PENDING','PURGED','CORRUPT']),'metadata':ref('Metadata'),'report':ref('Report'),'files':array(ref('File')),'receivedBytes':integer,'expandedBytes':integer,'fileCount':integer,'pinned':boolean,'errorCode':string(),'errorMessage':string(),'autoPublicationId':nullable(id_schema),'autoPublishErrorCode':string(),'autoPublishErrorMessage':string(),'createdAt':time}),
'Publication':obj({'id':id_schema,'siteId':id_schema,'deploymentId':nullable(id_schema),'action':string(enum=['PUBLISH','ROLLBACK','PAUSE','RESUME','DELETE','RESTORE']),'status':string(enum=['PENDING','APPLYING','APPLIED','FAILED','CONFLICT','RECOVERING','CANCELED']),'targetGeneration':integer,'expectedGeneration':integer,'targetStatus':string(),'errorCode':string(),'errorMessage':string(),'createdAt':time,'appliedAt':nullable(time)}),
'PublicationInput':obj({'deploymentId':id_schema,'expectedGeneration':integer,'action':string(enum=['PUBLISH','ROLLBACK'],default='PUBLISH'),'inspectionReportHash':string(),'acknowledgeWarnings':boolean},['deploymentId','expectedGeneration','inspectionReportHash']),
'SiteOperation':obj({'expectedGeneration':integer,'confirmSlug':string(),'deploymentId':id_schema,'inspectionReportHash':string(),'acknowledgeWarnings':boolean},['expectedGeneration']),
'TokenInput':obj({'name':string(minLength=1,maxLength=100),'siteIds':array(id_schema),'scopes':array(string(enum=['sites:read','deployments:write','publications:write','previews:write','sites:manage','sites:delete'])),'expiresAt':time},['name','siteIds','scopes','expiresAt']),
'Token':obj({'id':id_schema,'name':string(),'prefix':string(),'siteIds':array(id_schema),'scopes':array(string()),'expiresAt':time,'revokedAt':nullable(time),'lastUsedAt':nullable(time)}),
'PreviewGrant':obj({'id':id_schema,'url':string(format='uri',description='One-use secret is in the URL fragment, never in a query parameter.'),'expiresAt':time}),
'SystemStatus':obj({'version':string(),'usedBytes':integer,'reservedBytes':integer,'quotaBytes':integer,'siteCount':integer,'queuedJobs':integer,'diskFreeBytes':integer,'gatewayReady':boolean,'latestVerifiedBackup':nullable(time),'backupConfigured':boolean,'adminOrigin':string(),'contentDomain':string()}),
'Generic':{'type':'object','additionalProperties':True},
}
paths={}
def endpoint(method,path,summary,status=200,response='Generic',body=None,anonymous=False,page=False,description=''):
    params=[]
    for segment in path.split('/'):
        if segment.startswith('{'):params.append({'name':segment[1:-1],'in':'path','required':True,'schema':id_schema})
    if page:params.extend([{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':20}},{'name':'cursor','in':'query','schema':string(description='Opaque, filter-bound pagination cursor')}])
    if method in ('post','patch','delete') and not anonymous:params.extend([{'name':'X-CSRF-Token','in':'header','schema':string(),'description':'Required with the admin session cookie. Origin must equal DROP_ADMIN_ORIGIN.'},{'name':'Idempotency-Key','in':'header','schema':string(maxLength=128),'description':'Supported for upload, entry selection and publication intents. Reusing a key with different content returns 409.'}])
    responses={str(status):{'description':summary}}
    if status!=204:
        schema=ref(response)
        if page:schema=obj({'items':array(schema),'nextCursor':nullable(string())},['items','nextCursor'])
        responses[str(status)]['content']={'application/json':{'schema':schema}}
    responses['default']={'description':'Structured error. 401 authentication; 403 permission/CSRF; 404 unknown; 409 state/idempotency conflict; 413 upload size; 422 validation; 429 queue/quota/rate limit; 503 dependency; 507 disk headroom.','content':{'application/json':{'schema':ref('Error')}}}
    op={'summary':summary,'operationId':method+'_'+path.strip('/').replace('/','_').replace('{','').replace('}',''),'responses':responses}
    if description:op['description']=description
    if params:op['parameters']=params
    if anonymous:op['security']=[]
    if body:op['requestBody']={'required':True,'content':{'application/json':{'schema':ref(body) if isinstance(body,str) else body}}}
    paths.setdefault(path,{})[method]=op
endpoint('get','/setup/status','Check whether administrator setup is complete',anonymous=True)
endpoint('post','/setup','Initialize the single administrator',201,'Session',obj({'key':string(),'password':string(minLength=12,maxLength=256)},['key','password']),True)
endpoint('post','/auth/login','Create administrator session',200,'Session',obj({'password':string()},['password']),True)
endpoint('get','/auth/session','Read current session and CSRF token',response='Session')
endpoint('post','/auth/logout','Invalidate current session',204)
endpoint('post','/auth/password','Change password and invalidate all sessions',204,body=obj({'currentPassword':string(),'password':string(minLength=12,maxLength=256)},['currentPassword','password']))
endpoint('get','/sites','List permitted sites',response='Site',page=True)
paths['/sites']['get']['parameters'] += [{'name':'q','in':'query','schema':string()},{'name':'status','in':'query','schema':string(enum=['DRAFT','ACTIVE','PAUSED','DELETED'])}]
endpoint('post','/sites','Create a site (administrator)',201,'Site',obj({'slug':string(pattern='^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$'),'name':string(minLength=1,maxLength=100),'description':string(maxLength=2000)},['slug','name']))
endpoint('get','/sites/{site}','Get a site',response='Site')
endpoint('patch','/sites/{site}','Edit display metadata',response='Site',body=obj({'name':string(),'description':string()},['name']))
endpoint('get','/sites/{site}/deployments','List deployments',response='Deployment',page=True)
endpoint('post','/sites/{site}/deployments','Stream one HTML or ZIP artifact',202,'Deployment',description='Multipart order is metadata (JSON text) then file. Maximum upload 200 MiB by default. Receipt/READY never changes the public route. Browser folders are zipped in a Web Worker.')
paths['/sites/{site}/deployments']['post']['requestBody']={'required':True,'content':{'multipart/form-data':{'schema':obj({'metadata':string(description='JSON-encoded Metadata; this part MUST come first.'),'file':{'type':'string','format':'binary'}},['metadata','file'])}}}
endpoint('get','/deployments/{deployment}','Get inspection and deployment state',response='Deployment')
endpoint('get','/deployments/{deployment}/files','List inspected files',response='File',page=True)
endpoint('post','/deployments/{deployment}/selection','Choose entry and requeue inspection',202,body=obj({'rootDirectory':string(),'entryPath':string(),'routingMode':string(enum=['static','spa']),'inspectionRevision':integer},['rootDirectory','entryPath','routingMode','inspectionRevision']))
endpoint('post','/deployments/{deployment}/cancel','Cancel processing before READY',202)
endpoint('patch','/deployments/{deployment}/retention','Pin or unpin an available artifact',response='Deployment',body=obj({'pinned':boolean},['pinned']))
endpoint('get','/deployments/{deployment}/export','Export verified content and metadata')
paths['/deployments/{deployment}/export']['get']['responses']['200']['content']={'application/zip':{'schema':{'type':'string','format':'binary'}}}
endpoint('post','/sites/{site}/publications','Accept a publish or rollback intent',202,'Publication','PublicationInput',description='PAUSED or desired PAUSED rejects PUBLISH/ROLLBACK with SITE_PAUSED. Explicit sites:manage resume is required before publication.')
endpoint('get','/sites/{site}/publications','List route operations',response='Publication',page=True)
paths['/sites/{site}/publications']['get']['parameters'].append({'name':'deployment','in':'query','schema':id_schema})
endpoint('get','/publications/{publication}','Poll confirmed route outcome',response='Publication')
for op in ['pause','resume','restore']:endpoint('post','/sites/{site}/'+op,op.title()+' site through publication coordinator',202,'Publication','SiteOperation',description='RESUME requires sites:manage; optional deploymentId selects a same-site READY/AVAILABLE replacement and requires its inspectionReportHash and warning acknowledgement.' if op=='resume' else '')
endpoint('delete','/sites/{site}','Move site to trash (confirmSlug required)',202,'Publication','SiteOperation')
endpoint('post','/sites/{site}/purge','Collect eligible trash artifacts; pins and grace period still apply',202,body='SiteOperation')
endpoint('get','/tokens','List token metadata (administrator)',response='Token',page=True)
endpoint('post','/tokens','Create scoped token; raw secret is returned once',201,body='TokenInput')
endpoint('delete','/tokens/{token}','Revoke token immediately',204)
endpoint('post','/deployments/{deployment}/preview-grants','Create one-hour, one-use preview link',201,'PreviewGrant')
endpoint('delete','/preview-grants/{grant}','Revoke a preview grant and its sessions',204)
endpoint('get','/system/status','Read runtime storage and dependency status',response='SystemStatus')
endpoint('get','/system/audit','Read audit events',page=True)
endpoint('get','/system/backups','Read last 100 backup runs',page=True)
endpoint('get','/system/settings','Read retention settings and revision')
endpoint('patch','/system/settings','Extend retention using compare-and-swap revision',body=obj({'revision':integer,'value':obj({'retainVersions':{'type':'integer','minimum':1,'maximum':100},'trashDays':{'type':'integer','minimum':1,'maximum':365}},['retainVersions','trashDays'])},['revision','value']))
for p in ['/tokens','/system/backups']:
    paths[p]['get'].pop('parameters',None)
doc={'openapi':'3.1.0','info':{'title':'Self-hosted Drop API','version':'0.1.0','description':'Single-administrator static publishing. HTML executes only in the visitor browser. All paths below are on the dedicated admin origin; Caddy content/preview endpoints are separate.'},'servers':[{'url':'/api/v1'}],'security':[{'sessionCookie':[]},{'bearerAuth':[]}],'paths':paths,'components':{'securitySchemes':{'sessionCookie':{'type':'apiKey','in':'cookie','name':'__Host-drop_session','description':'Development uses drop_session_dev; writes also require CSRF and exact Origin.'},'bearerAuth':{'type':'http','scheme':'bearer'}},'schemas':schemas}}
path=pathlib.Path(__file__).resolve().parent.parent/'api/openapi.json'
path.write_text(json.dumps(doc,ensure_ascii=False,indent=2)+'\n')
print('Wrote',len(paths),'API paths')
