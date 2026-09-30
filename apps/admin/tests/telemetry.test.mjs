import test from 'node:test'
import assert from 'node:assert/strict'
import {telemetryState} from '../src/connection.mjs'
const now='2026-09-30T00:00:00Z', device={online:true,telemetry:{received_at:now}}
test('fresh signed report is visible only while server and device online',()=>{assert.equal(telemetryState(device,now,true),'fresh');assert.equal(telemetryState(device,now,false),'unknown');assert.equal(telemetryState({...device,online:false},now,true),'offline')})
test('legacy devices do not look like zero-resource devices',()=>assert.equal(telemetryState({online:true},now,true),'missing'))
test('old, future and invalid timestamps cannot look fresh',()=>{assert.equal(telemetryState(device,'2026-09-30T00:01:00Z',true),'stale');assert.equal(telemetryState(device,'2026-09-29T00:00:00Z',true),'unknown');assert.equal(telemetryState(device,'invalid',true),'unknown')})
