import assert from 'node:assert/strict';
import test from 'node:test';
import { catalogBotKey, catalogRows, hostCategory, hostLabel } from './hostCatalog.ts';

test('catalog identity includes both host and bot without delimiter collisions', () => {
  assert.notEqual(catalogBotKey('server', 'shared'), catalogBotKey('mac', 'shared'));
  assert.notEqual(catalogBotKey('a:b', 'c'), catalogBotKey('a', 'b:c'));
  const nodes = [{ id: 'server' }, { id: 'mac' }];
  const rows = catalogRows(nodes, {
    server: [{ id: 'shared', name: 'Server bot' }, { id: 'archived', status: 'archived' }],
    mac: [{ id: 'shared', name: 'Mac bot' }],
    removed: [{ id: 'private-removed' }],
  });
  assert.equal(rows.length, 2);
  assert.deepEqual(rows.map(row => [row.key, row.node.id, row.bot.name]), [
    [catalogBotKey('server', 'shared'), 'server', 'Server bot'],
    [catalogBotKey('mac', 'shared'), 'mac', 'Mac bot'],
  ]);
});

test('device filters classify actual Mac platforms and new Mac names while the local host stays Server', () => {
  assert.equal(hostCategory({ local: true, os: 'darwin', name: 'Local Mac' }), 'server');
  assert.equal(hostCategory({ local: false, platform: 'darwin', name: 'Billy computer' }), 'mac');
  assert.equal(hostCategory({ local: false, os: 'macOS', name: 'Billy computer' }), 'mac');
  assert.equal(hostCategory({ local: false, name: 'My MacBook' }), 'mac');
  assert.equal(hostCategory({ local: false, os: 'linux', name: 'Mac build server' }), 'server');
  assert.equal(hostLabel({ local: true, name: 'This server' }), 'Server');
  assert.equal(hostLabel({ local: false, name: 'MacBook' }), 'MacBook');
});
