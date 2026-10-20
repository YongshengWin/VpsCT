#!/usr/bin/env python3
"""Isolated official-core VLESS Reality -> SS-2022 TCP session check.
Requires Go and a local Docker engine. Creates no host ports and removes its
own containers/network. Documentation IPs are allowed only in this fixture.
"""
from pathlib import Path
import json, subprocess, time, os, tempfile, uuid
root=Path(__file__).resolve().parents[1]
workspace=tempfile.TemporaryDirectory(prefix='vpsct-proxy-inbound-')
base=Path(workspace.name)
env=dict(os.environ,CTLVPS_PROXY_CHECK_CONFIG_PATH=str(base/'entry.json'))
subprocess.run(['go','test','./internal/core','-run','^TestVLESSRealityUsesPinnedHandshakeThroughTransport$','-count=1'],cwd=root,env=env,check=True)
image='vpsct-proxy-inbound-fixture:'+uuid.uuid4().hex[:12]
subprocess.run(['docker','build','-q','-t',image,str(root/'scripts/fixtures/proxy-inbound')],check=True)
entry=json.loads((base/'entry.json').read_text());inbound=entry['inbounds'][0];reality=inbound['tls']['reality'];user=inbound['users'][0]
entry['log']['output']='/tmp/sing-box.log'
entry['route']['default_interface']='eth0'
# Documentation addresses are explicitly allowed only inside this isolated fixture.
entry['route']['rules'].insert(0,{'inbound':['node-1'],'ip_cidr':['203.0.113.25/32'],'action':'route','outbound':'node-1-socks5'})
(base/'entry-runtime.json').write_text(json.dumps(entry))
client={'log':{'level':'warn'},'inbounds':[{'type':'http','tag':'test-http','listen':'127.0.0.1','listen_port':2080}],'outbounds':[{'type':'vless','tag':'front','server':'203.0.113.10','server_port':24443,'uuid':user['uuid'],'flow':user['flow'],'tls':{'enabled':True,'server_name':'handshake.example.test','utls':{'enabled':True,'fingerprint':'chrome'},'reality':{'enabled':True,'public_key':(base/'entry.json.public-key').read_text().strip(),'short_id':reality['short_id'][0]}}}],'route':{'final':'front'}}
landing={'log':{'level':'warn'},'inbounds':[{'type':'shadowsocks','listen':'::','listen_port':8388,'method':'2022-blake3-aes-128-gcm','password':'AAAAAAAAAAAAAAAAAAAAAA=='}],'outbounds':[{'type':'direct','tag':'landing-exit'}],'route':{'final':'landing-exit'}}
(base/'client.json').write_text(json.dumps(client));(base/'landing.json').write_text(json.dumps(landing))
network='vpsct-proxy-inbound-'+uuid.uuid4().hex[:12];names=[]
def run(args):return subprocess.run(args,check=True,capture_output=True,text=True).stdout
try:
 run(['docker','network','create','--internal','--subnet','203.0.113.0/24',network])
 specs=[('target','203.0.113.25',image,None),('landing','203.0.113.12','ghcr.io/sagernet/sing-box:v1.14.1','landing.json'),('entry','203.0.113.10','ghcr.io/sagernet/sing-box:v1.14.1','entry-runtime.json'),('client','203.0.113.11','ghcr.io/sagernet/sing-box:v1.14.1','client.json')]
 for suffix,ip,image,config in specs:
  name=network+'-'+suffix;args=['docker','run','-d','--name',name,'--network',network,'--ip',ip,'--read-only','--tmpfs','/tmp','--cap-drop','ALL']
  if suffix=='entry':args+=['--cap-add','NET_ADMIN']
  if config:args+=['-v',str(base/config)+':/config.json:ro']
  args+=[image]
  if config:args+=['run','-c','/config.json']
  run(args);names.append(name)
 result=None
 for attempt in range(15):
  p=subprocess.run(['docker','exec','-e','http_proxy=http://127.0.0.1:2080',network+'-client','wget','-t','1','-T','4','-qO-','http://203.0.113.25:8080/through-vless'],capture_output=True,text=True)
  if p.returncode==0:
   result=json.loads(p.stdout);break
  time.sleep(.5)
 assert result and result['source']=='203.0.113.12', 'traffic did not exit through the landing'
 print(json.dumps({'result':'passed','path':'VLESS Reality -> SS-2022 -> HTTP','exit_verified':'landing','isolated_network':'internal','production_hosts_touched':False}))
except BaseException:
 for name in names:
  print(name,subprocess.run(['docker','logs','--tail','8',name],capture_output=True,text=True).stderr)
  if name.endswith('-entry'):print(subprocess.run(['docker','exec',name,'tail','-8','/tmp/sing-box.log'],capture_output=True,text=True).stdout)
 raise
finally:
 for name in reversed(names):subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 subprocess.run(['docker','network','rm',network],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 subprocess.run(['docker','image','rm',image],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 workspace.cleanup()
