// Real Chromium integration against ephemeral HTTPS origins and an empty DB.
// Usage: CTLVPS_TEST_BINARY=... PLAYWRIGHT_MODULE_DIR=... node scripts/security-browser.mjs
import {createRequire} from 'node:module';import fs from 'node:fs';import os from 'node:os';import path from 'node:path';import https from 'node:https';import http from 'node:http';import {spawn,execFileSync} from 'node:child_process';import assert from 'node:assert/strict';
const require=createRequire(import.meta.url);const {chromium}=require(process.env.PLAYWRIGHT_MODULE_DIR ? path.join(process.env.PLAYWRIGHT_MODULE_DIR,'playwright'): 'playwright');
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'ctlvps-browser-'));let child,browser,front;const violations=[];
const listen=s=>new Promise(r=>s.listen(0,'127.0.0.1',()=>r(s.address().port)));
try{
 execFileSync('openssl',['req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',dir+'/tls.key','-out',dir+'/tls.crt','-subj','/CN=panel.fixture.test'],{stdio:'ignore'});
 const reserve=http.createServer();const backPort=await listen(reserve);await new Promise(r=>reserve.close(r));
 front=https.createServer({key:fs.readFileSync(dir+'/tls.key'),cert:fs.readFileSync(dir+'/tls.crt')},(req,res)=>{
  if(!req.headers.host.startsWith('panel.fixture.test:')){res.setHeader('Content-Type','text/html');res.end('<!doctype html><title>Hostile fixture</title><body>Independent origin fixture</body>');return;}
  const proxy=http.request({hostname:'127.0.0.1',port:backPort,path:req.url,method:req.method,headers:req.headers},out=>{res.writeHead(out.statusCode,out.headers);out.pipe(res)});proxy.on('error',()=>{res.statusCode=503;res.end()});req.pipe(proxy);
 });
 const port=await listen(front),origin=`https://panel.fixture.test:${port}`;
 fs.mkdirSync(dir+'/data',{mode:0o700});const token=Buffer.alloc(32,7).toString('base64url');fs.writeFileSync(dir+'/data/setup-token',token,{mode:0o600});
 const log=fs.openSync(dir+'/server.log','w');child=spawn(process.env.CTLVPS_TEST_BINARY??'./bin/ctlvpsd',['--data',dir+'/data','--listen',`127.0.0.1:${backPort}`,'--site-url',origin],{stdio:['ignore',log,log]});
 for(let i=0;i<80;i++){try{const r=await fetch(`http://127.0.0.1:${backPort}/healthz`);if(r.ok)break}catch{}await new Promise(r=>setTimeout(r,100));}
 browser=await chromium.launch({headless:true,...(process.env.CTLVPS_BROWSER_CHANNEL?{channel:process.env.CTLVPS_BROWSER_CHANNEL}:{}),args:['--no-proxy-server','--host-resolver-rules=MAP panel.fixture.test 127.0.0.1, MAP evil.fixture.test 127.0.0.1, MAP evil.other.test 127.0.0.1']});
 const context=await browser.newContext({ignoreHTTPSErrors:true});const page=await context.newPage();page.on('pageerror',e=>violations.push(e.message));
 await page.goto(origin);await page.getByRole('button',{name:'创建管理员并登录'}).waitFor();
 await page.getByLabel('初始化令牌').fill(token);await page.locator('input[autocomplete="username"]').fill('browser-fixture');const passwords=page.locator('input[autocomplete="new-password"]');await passwords.nth(0).fill('fixture-password-123');await passwords.nth(1).fill('fixture-password-123');await page.getByRole('button',{name:'创建管理员并登录'}).click();
 await page.waitForURL(url=>!url.pathname.includes('login'));await page.goto(origin+'/servers');await page.getByRole('button',{name:'添加服务器',exact:true}).first().click();await page.getByPlaceholder('hk-1').fill('safe-browser-fixture');await page.getByRole('button',{name:'保存',exact:true}).click();await page.getByText('safe-browser-fixture',{exact:true}).first().waitFor();
 const cookies=await context.cookies(origin);const session=cookies.find(c=>c.name==='__Host-ctlvps_session');assert(session?.secure&&session.httpOnly&&session.path==='/'&&session.sameSite==='Lax');
 const inspect=()=>page.evaluate(async()=>({me:await (await fetch('/api/v1/auth/me')).json(),servers:await (await fetch('/api/v1/servers')).json()}));const before=await inspect();
 // Same-site subdomains receive SameSite cookies on their requests to panel,
 // so the Origin/CSRF boundary, not just SameSite, must reject these attempts.
 for(const hostname of ['evil.fixture.test','evil.other.test']){
  const evil=await context.newPage();const frameErrors=[];evil.on('console', m=>{if(m.type()==='error')frameErrors.push(m.text())});await evil.goto(`https://${hostname}:${port}`);
  const blocked=await evil.evaluate(async(target)=>{
   const outcomes=[];for(const contentType of ['text/plain','application/x-www-form-urlencoded']){try{const r=await fetch(target+'/api/v1/servers',{method:'POST',credentials:'include',headers:{'Content-Type':contentType},body:'{"name":"attacker-created"}'});outcomes.push(r.status)}catch{outcomes.push('cors-blocked')}}
   try{await fetch(target+'/api/v1/servers',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:'{"name":"attacker-json"}'})}catch{outcomes.push('preflight-blocked')}
   const frame=document.createElement('iframe');frame.src=target;document.body.append(frame);return outcomes;
  },origin);
  assert(blocked.includes('preflight-blocked'));await evil.waitForTimeout(300);assert(frameErrors.some(s=>/frame-ancestors|X-Frame-Options|refused to frame/i.test(s)), 'iframe must be blocked by browser');await evil.close();
 }
 const missing=await page.evaluate(async()=>{const r=await fetch('/api/v1/auth/profile',{method:'PUT',headers:{'Content-Type':'application/json'},body:'{"nickname":"tampered"}'});return r.status});assert.equal(missing,403);
 const csp=await page.evaluate(()=>new Promise(resolve=>{window.__injected=false;const el=document.createElement('script');el.textContent='window.__injected=true';document.body.append(el);setTimeout(()=>resolve(window.__injected),100)}));assert.equal(csp,false);
 const after=await inspect();assert.equal(after.servers.length,before.servers.length);assert.equal(after.me.nickname,before.me.nickname);assert.equal(violations.length,0);
 await page.goto(origin+'/servers/'+after.servers[0].id);await page.getByRole('button',{name:'部署节点',exact:true}).first().click();const dialog=page.getByRole('dialog');await dialog.getByRole('combobox').nth(0).click();await page.getByRole('option',{name:'Trojan',exact:true}).click();await dialog.getByRole('combobox').nth(1).click();await page.getByRole('option',{name:'外部证书',exact:true}).click();await dialog.getByText('外部证书 ID',{exact:true}).locator('..').locator('input').fill('fixture-cert');await dialog.getByRole('button',{name:'部署',exact:true}).click();await dialog.waitFor({state:'hidden'});
 // A manually recovered agent retains failed history without a stale banner.
 const maintenanceURL=`**/api/v1/servers/${after.servers[0].id}/maintenance`;
 const failedJob={id:'fixture-maintenance',role:'agent',action:'update',status:'failed',version:'v0.1.5',message:'fixture download failure',updated_at:new Date().toISOString()};
 const maintenanceState={available:true,version:'v0.1.5',target_version:'v0.1.5',agent_update:{current_sha:'same',latest_sha:'same',outdated:false},jobs:[failedJob]};
 await page.route(maintenanceURL,route=>route.fulfill({json:maintenanceState}));
 await page.reload();await page.getByRole('button',{name:'更多',exact:true}).click();await page.getByText('agent 维护',{exact:true}).click();
 await page.getByText('操作记录',{exact:true}).click();await page.getByText('fixture download failure',{exact:true}).waitFor();
 assert.equal(await page.getByRole('status').filter({hasText:'agent 升级'}).count(),0);
 await page.keyboard.press('Escape');
 maintenanceState.agent_update.current_sha='old';maintenanceState.agent_update.outdated=true;
 await page.reload();await page.getByRole('status').filter({hasText:'agent 升级 · 失败'}).waitFor();
 maintenanceState.agent_update.current_sha='same';maintenanceState.agent_update.outdated=false;maintenanceState.available=false;
 await page.reload();await page.getByRole('status').filter({hasText:'agent 升级 · 失败'}).waitFor();
 maintenanceState.available=true;failedJob.action='uninstall';
 await page.reload();await page.getByRole('status').filter({hasText:'agent 卸载 · 失败'}).waitFor();
 await page.unroute(maintenanceURL);
 console.log('PASS recovered agent hides obsolete update banner, retains history, and preserves offline/outdated/uninstall failures.');
 // Structured apply errors retain online status, useful recovery instructions
 // and details in both themes, without disguising offline or recovered state.
 const serverURL=`**/api/v1/servers/${after.servers[0].id}`;
 const failureServer=await page.evaluate(async id=>(await fetch(`/api/v1/servers/${id}`)).json(),after.servers[0].id);
 failureServer.agent_status='online';
 failureServer.diagnostics={...(failureServer.diagnostics??{}),security_policy:true,security_paused:false};
 failureServer.desired={...(failureServer.desired??{}),in_sync:false,status:'failed',error:''};
 await page.route(serverURL,route=>route.fulfill({json:failureServer}));
 for(const theme of ['light','dark']) {
  await page.evaluate(value=>localStorage.setItem('ctlvps-theme',value),theme);
  await page.setViewportSize({width:390,height:844});
  for(const [code,instruction] of [
   ['ingress_policy_unsupported','防火墙规则暂不兼容'],
   ['proxy_mark_unavailable','路由能力检查未通过'],
   ['proxy_sandbox_unavailable','无法启用所需的进程隔离'],
   ['proxy_cgroup_mismatch','未在要求的资源隔离组内运行'],
   ['proxy_preflight_failed','代理配置预检未通过'],
  ]) {
   failureServer.desired.error=`singbox: [${code}] fixture error`;
   await page.reload();
   const notice=page.getByRole('status').filter({hasText:'节点设置未生效'});
   try { await notice.waitFor(); } catch(error) {
    await page.screenshot({path:'/tmp/ctlvps-apply-diagnostic-failure.png',fullPage:true});
    console.error('Diagnostic fixture:',{url:page.url(),statuses:await page.getByRole('status').allTextContents()});
    throw error;
   }
   assert((await notice.textContent()).includes(instruction),`missing ${code} instruction: ${await notice.textContent()}`);
   await notice.getByText(instruction,{exact:false}).waitFor();
   assert.equal(await page.locator('html').evaluate(el=>el.classList.contains('dark')),theme==='dark');
   await notice.getByText('错误详情',{exact:true}).click();
   await notice.getByText(failureServer.desired.error,{exact:true}).waitFor();
   assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'diagnostic notice overflows mobile viewport');
  }
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:`/tmp/ctlvps-apply-diagnostics-${theme}.png`,fullPage:true,animations:'disabled'});
 }
 failureServer.agent_status='offline';await page.reload();await page.getByText('等待服务器上线',{exact:true}).waitFor();
 failureServer.agent_status='online';failureServer.desired.in_sync=true;await page.reload();
 assert.equal(await page.getByRole('status').filter({hasText:'节点设置未生效'}).count(),0);
 await page.unroute(serverURL);await page.evaluate(()=>localStorage.setItem('ctlvps-theme','system'));
 await page.setViewportSize({width:1280,height:900});
 console.log('PASS apply diagnostics: five stable errors, details, light/dark mobile layout, offline and recovery precedence.');
 // Release smoke: exercise the new network navigation against real empty API state.
 await page.goto(origin+'/servers/'+after.servers[0].id+'?tab=routes');
 await page.getByRole('heading',{name:'网站会看到哪台 VPS 的地址？'}).waitFor();
 await page.getByRole('button',{name:/网卡与流量/}).click();
 await page.getByText('等待多网卡数据',{exact:true}).waitFor();
 await page.getByRole('button',{name:'← 返回节点线路'}).click();
 await page.getByRole('button',{name:/连接已有代理/}).click();
 await page.getByText('还没有接入其他代理',{exact:true}).waitFor();
 await page.getByRole('button',{name:'← 返回节点线路'}).click();
 await page.getByRole('button',{name:/转发固定端口/}).click();
 await page.getByRole('button',{name:'添加转发',exact:true}).click();
 const forwardDialog=page.getByRole('dialog');
 await forwardDialog.getByRole('combobox').first().click();
 await page.getByRole('option',{name:'UDP',exact:true}).click();
 assert.equal(await forwardDialog.getByLabel('UDP 空闲超时（秒）').inputValue(),'30');
 assert.equal(await forwardDialog.getByLabel('最大 TCP 连接数').count(),0);
 await forwardDialog.getByRole('combobox').first().click();
 await page.getByRole('option',{name:'TCP + UDP',exact:true}).click();
 assert.equal(await forwardDialog.getByLabel('最大 TCP 连接数').inputValue(),'128');
 assert.equal(await forwardDialog.getByLabel('最大 UDP 会话数').inputValue(),'128');
 await page.keyboard.press('Escape');
 const udpServer=structuredClone(after.servers[0]);
 udpServer.agent_status='online';udpServer.diagnostics??={};
 udpServer.diagnostics.forward_udp={state:'paused',code:'udp_api_unavailable',updated_at:new Date().toISOString(),last_healthy:new Date().toISOString(),active:{1:12},reaped:400,sweep_millis:40};
 const udpURL=origin+'/api/v1/servers/'+udpServer.id;
 await page.route(udpURL,route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(udpServer)}));
 const udpForwardsURL=origin+'/api/v1/servers/'+udpServer.id+'/forwards';
 const legacyUDP={id:1,server_id:udpServer.id,name:'legacy UDP fixture',enabled:true,retired:false,revision:1,rx_30_days:0,tx_30_days:0,config:{listen_mode:'all',listen_port:25001,network:'udp',target_host:'203.0.113.10',target_port:18080,source_mode:'all',source_cidrs:[],max_tcp_connections:0,max_udp_sessions:4096,udp_idle_seconds:30}};
 await page.route(udpForwardsURL,route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify([legacyUDP])}));
 await page.reload();
 await page.getByText(/旧 UDP 会话上限超过官方内核容量/).waitFor();
 await page.getByText('UDP 转发已暂停',{exact:true}).waitFor();
 await page.getByText(/TCP 与普通代理节点继续运行/).waitFor();
 udpServer.diagnostics.forward_udp.code='udp_guard_unavailable';await page.reload();await page.getByText(/代理运行状态还需查看服务器诊断/).waitFor();
 udpServer.diagnostics.forward_udp.state='healthy';udpServer.diagnostics.forward_udp.code='';
 await page.reload();await page.getByText('UDP 会话管理正常',{exact:true}).waitFor();
 await page.getByText(/本次 agent 运行累计清理 400 个/).waitFor();
 udpServer.agent_status='offline';await page.reload();await page.getByText('UDP 状态待确认',{exact:true}).waitFor();
 await page.unroute(udpURL);await page.unroute(udpForwardsURL);
 await page.setViewportSize({width:390,height:844});
 await page.goto(origin+'/servers/'+after.servers[0].id+'?tab=routes');
 await page.getByRole('heading',{name:'网站会看到哪台 VPS 的地址？'}).waitFor();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'network page overflows mobile viewport');
 await page.screenshot({path:'/tmp/ctlvps-network-mobile.png',fullPage:true,animations:'disabled'});
 assert.equal(violations.length,0);
 console.log('PASS release network navigation, empty inventory/egress, UDP/both forwarding controls and 390px layout.');
 await page.screenshot({path:'/tmp/ctlvps-security-browser.png',fullPage:true});console.log('PASS Chromium: UI setup/create, Secure HttpOnly host cookie, same-site/cross-site attacks, CSRF and CSP; no unauthorized mutation.');
}finally{await browser?.close();child?.kill();await new Promise(r=>front?front.close(r):r());fs.rmSync(dir,{recursive:true,force:true});}
