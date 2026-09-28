import { test } from 'node:test'; import assert from 'node:assert/strict'
import { connectionStatus, csvCell, formatBytes } from '../src/connection.mjs'
const snapshot = { server_time: '2026-09-28T00:00:10Z', server: { heartbeat_at: '2026-09-28T00:00:05Z', status: 'online' } }
test('fresh successful response is online, regardless of local clock skew', () => assert.equal(connectionStatus(snapshot, 1000, 1001), 'online'))
test('network failure never keeps an online badge', () => assert.equal(connectionStatus(snapshot, 1000, 1001, true), 'unreachable'))
test('stale cached response cannot look online', () => assert.equal(connectionStatus(snapshot, 1000, 21001), 'stale'))
test('stale server heartbeat is degraded', () => assert.equal(connectionStatus({...snapshot,server_time:'2026-09-28T00:00:30Z'},1000,1001),'degraded'))
test('initial state is checking, not fabricated online', () => assert.equal(connectionStatus(null, 0), 'checking'))
test('absent metrics never become zero', () => assert.equal(formatBytes(null), '未采集'))
test('CSV escapes user-controlled formulas and quotes', () => {assert.equal(csvCell('=1+1'),'"\'=1+1"');assert.equal(csvCell('a"b'),'"a""b"')})
