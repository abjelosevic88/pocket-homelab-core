#!/usr/bin/env python3
"""Exercise real Core and Money binaries against disposable databases only."""
import contextlib, http.cookiejar, json, os, pathlib, socket, subprocess, tempfile, time, urllib.request, urllib.error
root=pathlib.Path(__file__).resolve().parents[1]

def port():
 with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
def api(base,path,body=None,token='',method=None,client=None):
 data=None if body is None else json.dumps(body).encode()
 req=urllib.request.Request(base+path,data=data,method=method or ('POST' if data is not None else 'GET'),headers={'Content-Type':'application/json',**({'Authorization':'Bearer '+token} if token else {})})
 try:
  with (client or urllib.request).urlopen(req,timeout=10) as res:return res.status,json.loads(res.read() or 'null')
 except urllib.error.HTTPError as err:return err.code,json.loads(err.read() or 'null')
def await_ok(fn):
 deadline=time.monotonic()+30
 while time.monotonic()<deadline:
  try:
   result=fn()
   if result:return result
  except (OSError,urllib.error.URLError):pass
  time.sleep(.1)
 raise AssertionError('timed out waiting for contract condition')
with tempfile.TemporaryDirectory(prefix='pocket-contract-') as tmp:
 tmp=pathlib.Path(tmp);procs=[];logs=[]
 def launch(name,p,extra={}):
  data=tmp/name;data.mkdir(exist_ok=True)
  env={'PATH':os.environ['PATH'],'HOME':str(tmp),'HOST':'127.0.0.1','PORT':str(p),'DATA_DIR':str(data),'BASE_URL':f'http://127.0.0.1:{p}','AUTH_MODE':'local','FX_PROVIDER':'none',**extra}
  log=open(tmp/(name+'.log'),'ab');logs.append(log)
  proc=subprocess.Popen([str(tmp/(name+'-bin'))],env=env,stdout=log,stderr=log);procs.append(proc);return proc
 try:
  for name in ('core','money'):
   subprocess.run(['go','build','-o',str(tmp/(name+'-bin')),'./cmd/pocket-'+name],cwd=root.parent/('pocket-homelab-'+name),check=True)
  cp,mp=port(),port();cb=f'http://127.0.0.1:{cp}';mb=f'http://127.0.0.1:{mp}'
  core=launch('core',cp)
  await_ok(lambda:api(cb,'/healthz')[0]==200)
  browser=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
  # OpenerDirector has open(), whereas api uses urlopen().
  browser.urlopen=browser.open
  code,_=api(cb,'/api/v1/auth/setup',{'email':'owner@example.com','password':'test-password-123'},client=browser);assert code==200,('setup',code)
  _,settings=api(cb,'/api/v1/settings',client=browser)
  money=launch('money',mp,{'CORE_URL':cb,'CORE_REGISTRATION_KEY':settings['registration_key'],'CORE_APP_URL':mb})
  await_ok(lambda:api(mb,'/healthz')[0]==200)
  await_ok(lambda:any(a['slug']=='money' for a in api(cb,'/api/v1/apps',client=browser)[1]))
  _,pair=api(cb,'/api/v1/pair',{},client=browser)
  code,profile=api(cb,'/api/v1/pair/claim',{'code':pair['code'],'name':'contract test'});assert code==201,('pair',code)
  device=profile['token']
  code,account=api(mb,'/api/v1/accounts',{'name':'Test account','currency':'EUR'},device);assert code==201,('account',code)
  code,garage=api(cb,'/api/v1/apps/register',{'slug':'garage','name':'Garage test','url':'http://example.com','uses':[{'app':'money','scopes':['transactions:write']}]},settings['registration_key']);assert code==201
  code,grant=api(cb,'/api/v1/links/token',{'user':'owner@example.com','app':'money','scopes':['transactions:write']},garage['token']);assert code==200,('grant',code)
  payload={'account_id':account['id'],'amount':1200,'currency':'EUR','kind':'expense','description':'Contract fuel','external_id':'fuel:42'}
  code,first=api(mb,'/api/v1/transactions',payload,grant['access_token']);assert code==201,('create',code)
  code,second=api(mb,'/api/v1/transactions',payload,grant['access_token']);assert code==201 and first['id']==second['id'],'duplicate write'
  payload['amount']=1201
  assert api(mb,'/api/v1/transactions',payload,grant['access_token'])[0]==409,'conflicting operation accepted'
  # A separate app without grants cannot read the emitted private transaction.
  _,clips=api(cb,'/api/v1/apps/register',{'slug':'clips','name':'Clips test','url':'http://example.com'},settings['registration_key'])
  assert api(cb,'/api/v1/events?topic=money.*',token=clips['token'])[1]==[],'private event exposed'
  await_ok(lambda:len(api(cb,'/api/v1/events?topic=money.transaction_created',client=browser)[1])==1)
  # Revocation must invalidate even the already-issued delegated token.
  _,links=api(cb,'/api/v1/links',client=browser)
  link=next(l for l in links if l['app_slug']=='garage')
  assert api(cb,'/api/v1/links/'+str(link['id']),{'enabled':False},method='PUT',client=browser)[0]==200
  payload['external_id']='fuel:43'
  assert api(mb,'/api/v1/transactions',payload,grant['access_token'])[0]==401,'revoked grant cached'
  # Issuer/subject preserves the app account across a central email change.
  _,before=api(mb,'/api/v1/auth/me',token=device)
  _,people=api(cb,'/api/v1/users',client=browser)
  owner=next(u for u in people if u['email']=='owner@example.com')
  assert api(cb,'/api/v1/users/'+str(owner['id']),{'email':'renamed@example.com'},method='PUT',client=browser)[0]==200
  code,after=api(mb,'/api/v1/auth/me',token=device)
  assert code==200 and before['id']==after['id'] and after['email']=='renamed@example.com','email change split the app account'
  # Exercise the actual OIDC browser dance and invalidate its local session.
  code,member=api(cb,'/api/v1/users',{'email':'member@example.com','password':'member-password-123','role':'member'},client=browser);assert code==201
  member_browser=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
  member_browser.urlopen=member_browser.open
  assert api(cb,'/api/v1/auth/login',{'email':'member@example.com','password':'member-password-123'},client=member_browser)[0]==200
  with member_browser.open(mb+'/api/v1/auth/oidc/start',timeout=10) as response:response.read()
  assert api(mb,'/api/v1/auth/me',client=member_browser)[0]==200,'SSO did not create a local session'
  assert api(cb,'/api/v1/users/'+str(member['id']),{'disabled':True},method='PUT',client=browser)[0]==200
  assert api(mb,'/api/v1/auth/me',client=member_browser)[0]==401,'disabled person retained app session'
  assert api(cb,'/api/v1/users/'+str(member['id']),{'disabled':False},method='PUT',client=browser)[0]==200
  assert api(mb,'/api/v1/auth/me',client=member_browser)[0]==401,'re-enabling resurrected an old session'
  # An app-local API token remains an independent credential during an outage.
  code,_=api(mb,'/api/v1/auth/me',token=device);assert code==200
  code,pt=api(mb,'/api/v1/tokens',{'name':'offline test'},device);assert code==200,('personal token',code)
  local=pt['token']
  core.terminate();core.wait(timeout=10)
  payload['external_id']='offline:1'
  code,offline=api(mb,'/api/v1/transactions',payload,local);assert code==201,('offline write',code)
  money.terminate();money.wait(timeout=10)
  core=launch('core',cp)
  await_ok(lambda:api(cb,'/healthz')[0]==200)
  money=launch('money',mp,{'CORE_URL':cb,'CORE_APP_URL':mb})
  await_ok(lambda:api(mb,'/healthz')[0]==200)
  await_ok(lambda:len(api(cb,'/api/v1/events?topic=money.transaction_created',client=browser)[1])==2)
  print('PASS: real Core/Money enrollment, identity, write replay/conflict, event privacy, live revocation, stable email changes, SSO session invalidation, outage and process-restart recovery')
 finally:
  for proc in reversed(procs):
   if proc.poll() is None:
    proc.terminate()
    try:proc.wait(timeout=10)
    except subprocess.TimeoutExpired:proc.kill();proc.wait()
  for log in logs:log.close()
