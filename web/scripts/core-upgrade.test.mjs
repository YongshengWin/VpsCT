import test from 'node:test';
import assert from 'node:assert/strict';
import { coreServerPage } from '../src/lib/core-upgrade.ts';
const statuses = ['upgrade', 'pending', 'offline', 'error', 'ready'];
const fleet = Array.from({length: 503}, (_, i) => ({id: i + 1, name: `server-${String(i + 1).padStart(4, '0')}`, version: i % 2 ? '1.14.1' : '1.12.14', status: statuses[i % 5], message: ''}));

test('503 servers paginate without losing or duplicating a server; failures appear first', () => {
  const first = coreServerPage(fleet, '', 'all', 1);
  assert.equal(first.total, 503);
  assert.equal(first.pages, 63);
  assert.equal(first.rows.length, 8);
  assert.equal(first.rows[0].status, 'error');
  const ids = Array.from({length: first.pages}, (_, i) => coreServerPage(fleet, '', 'all', i + 1).rows).flat().map(s => s.id);
  assert.equal(new Set(ids).size, 503);
  assert.equal(fleet[0].id, 1); // Never reorder the live query cache.
});
test('search and status filters narrow hundreds of servers directly', () => {
  assert.deepEqual(coreServerPage(fleet, ' SERVER-0503 ', 'all', 1).rows.map(s => s.id), [503]);
  assert.ok(coreServerPage(fleet, '1.14.1', 'ready', 1).rows.every(s => s.version === '1.14.1' && s.status === 'ready'));
  assert.equal(coreServerPage(fleet, '', 'attention', 1).total, 403);
  assert.equal(coreServerPage(fleet, 'missing', 'all', 1).total, 0);
});
test('refresh and filtering clamp a now-empty last page', () => {
  assert.equal(coreServerPage(fleet.slice(0, 3), '', 'all', 63).page, 1);
  assert.equal(coreServerPage([], '', 'all', 63).page, 1);
  assert.equal(coreServerPage(fleet, '', 'all', -1).page, 1);
  assert.equal(coreServerPage(fleet, '', 'all', Infinity).page, 1);
});
