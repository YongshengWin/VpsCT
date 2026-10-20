import test from 'node:test';
import assert from 'node:assert/strict';
import { configurationState } from '../src/lib/config-status.ts';

const failed = (error) => ({
  agent_status: 'online',
  diagnostics: { security_policy: true },
  desired: { in_sync: false, status: 'failed', error },
});

test('stable failure codes produce actionable descriptions ahead of text heuristics', () => {
  const cases = [
    ['ingress_policy_unsupported', '防火墙'],
    ['proxy_mark_unavailable', '路由能力'],
    ['proxy_sandbox_unavailable', '进程隔离'],
    ['proxy_cgroup_mismatch', '资源隔离组'],
    ['proxy_preflight_failed', '本次预检日志'],
  ];
  for (const [code, instruction] of cases) {
    const state = configurationState(failed(`singbox: [${code}] timeout certificate`));
    assert.equal(state.kind, 'failed');
    assert.ok(state.description.includes(instruction), `${code}: ${state.description}`);
    assert.equal(state.error, `singbox: [${code}] timeout certificate`);
  }
});

test('offline, paused, retrying and synchronized states keep their own precedence', () => {
  const server = failed('[proxy_mark_unavailable] unavailable');
  assert.equal(configurationState({ ...server, agent_status: 'offline' }).kind, 'waiting');
  assert.equal(configurationState({ ...server, diagnostics: { security_paused: true } }).kind, 'blocked');
  assert.equal(configurationState(server, true).kind, 'syncing');
  assert.equal(configurationState({ ...server, desired: { ...server.desired, in_sync: true } }), null);
});

test('old agent errors and unknown codes retain useful fallback descriptions', () => {
  const server = failed('');
  const fromAgent = configurationState({ ...server, agent: { apply_error: '[proxy_sandbox_unavailable] unavailable' } });
  assert.ok(fromAgent.description.includes('进程隔离'));
  assert.ok(configurationState(failed('address already in use')).description.includes('端口被其他程序占用'));
  assert.ok(configurationState(failed('[future_code] timeout')).description.includes('网络暂时不可用'));
});
