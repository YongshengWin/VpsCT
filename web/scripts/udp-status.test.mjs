import test from 'node:test';
import assert from 'node:assert/strict';
import { udpStatusView } from '../src/lib/udp-status.ts';
const now=Date.now();
const report={state:'healthy',updated_at:new Date(now).toISOString(),last_healthy:new Date(now).toISOString(),reaped:3,sweep_millis:10};
test('offline and stale diagnostics never present UDP as healthy',()=>{
 for(const s of [report,{...report,updated_at:'bad'},{...report,updated_at:new Date(now-46000).toISOString()},undefined]) {
  assert.equal(udpStatusView(s,false,now).fresh,false);
 }
 assert.equal(udpStatusView({...report,updated_at:new Date(now-46000).toISOString()},true,now).fresh,false);
});
test('UDP pause and recovery descriptions distinguish TCP availability',()=>{
 assert.match(udpStatusView({...report,state:'paused'},true,now).description,/TCP.*继续运行/);
 assert.equal(udpStatusView({...report,state:'paused'},true,now).warning,true);
 assert.equal(udpStatusView(report,true,now).warning,false);
 assert.equal(udpStatusView(report,true,now).fresh,true);
});

test('guard failures do not claim unrelated proxies are running',()=>{
 const view=udpStatusView({...report,state:'paused',code:'udp_guard_unavailable'},true,now);
 assert.match(view.description,/查看服务器诊断/);
 assert.doesNotMatch(view.description,/TCP.*继续运行/);
});
